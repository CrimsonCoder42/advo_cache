// Package sheet provides tenant-isolated cache instances
package sheet

import (
	"errors"
	"sync"

	"github.com/CrimsonCoder42/advo_cache/logger"
)

// ErrInvalidPassword returned when password doesn't match existing sheet
var ErrInvalidPassword = errors.New("invalid password")

// Manager holds all sheets - O(1) lookup by ID
type Manager struct {
	sheets sync.Map // sheetID → *Sheet
}

// NewManager creates a new sheet manager
func NewManager() *Manager {
	logger.Info("SheetManager initialized")
	return &Manager{}
}

// GetOrCreateSheet returns existing sheet (if password matches) or creates new one
// Uses sync.Map.LoadOrStore for atomic operation - prevents race conditions
func (m *Manager) GetOrCreateSheet(id, password string) (*Sheet, error) {
	// Validate inputs
	if id == "" || password == "" {
		return nil, errors.New("sheet ID and password required")
	}

	// Try to load existing sheet first
	if existing, ok := m.sheets.Load(id); ok {
		sheet := existing.(*Sheet)
		if !sheet.ValidatePassword(password) {
			logger.Warning("Invalid password for sheet: " + id)
			return nil, ErrInvalidPassword
		}
		return sheet, nil
	}

	// Create new sheet
	newSheet, err := NewSheet(id, password)
	if err != nil {
		return nil, err
	}

	// Atomic store - if another goroutine created it first, use theirs
	actual, loaded := m.sheets.LoadOrStore(id, newSheet)
	if loaded {
		// Another goroutine created it first, validate password against theirs
		existingSheet := actual.(*Sheet)
		if !existingSheet.ValidatePassword(password) {
			logger.Warning("Invalid password for sheet: " + id)
			return nil, ErrInvalidPassword
		}
		return existingSheet, nil
	}

	// We created the sheet
	logger.Info("Sheet created: " + id)
	return newSheet, nil
}

// GetSheet returns sheet by ID (nil if not found)
func (m *Manager) GetSheet(id string) *Sheet {
	if sheet, ok := m.sheets.Load(id); ok {
		return sheet.(*Sheet)
	}
	return nil
}

// AddSheet adds a sheet directly (used by replica to receive replication)
func (m *Manager) AddSheet(id string, s *Sheet) {
	m.sheets.Store(id, s)
}

// DeleteSheet removes a sheet from the manager
func (m *Manager) DeleteSheet(id string) {
	m.sheets.Delete(id)
	logger.Info("Sheet deleted: " + id)
}

// SheetCount returns number of active sheets
func (m *Manager) SheetCount() int {
	count := 0
	m.sheets.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// Range iterates over all sheets, calling fn for each
func (m *Manager) Range(fn func(id string, s *Sheet) bool) {
	m.sheets.Range(func(key, value interface{}) bool {
		return fn(key.(string), value.(*Sheet))
	})
}
