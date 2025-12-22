// Package lock provides distributed lock management with ownership and TTL
// Locks are DATA, not mutex primitives - stored and replicated like cache entries
package lock

import (
	"errors"
	"sync"
	"time"

	"github.com/CrimsonCoder42/advo_cache/logger"
)

// Errors
var (
	ErrLockHeld     = errors.New("lock held by another client")
	ErrNotOwner     = errors.New("not lock owner")
	ErrLockNotFound = errors.New("lock not found")
)

// Lock represents a held lock with ownership
type Lock struct {
	Key        string    `json:"key"`
	Owner      string    `json:"owner"`       // client connection ID
	SheetID    string    `json:"sheet_id"`    // which sheet (for isolation)
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// IsExpired checks if lock has timed out
func (l *Lock) IsExpired() bool {
	return time.Now().After(l.ExpiresAt)
}

// Manager handles distributed locks per sheet
// Uses composite key: "sheetID:key" for O(1) lookup
type Manager struct {
	locks sync.Map // compositeKey → *Lock
}

// NewManager creates a new lock manager
func NewManager() *Manager {
	logger.Info("LockManager initialized")
	return &Manager{}
}

// compositeKey creates unique key from sheet+key
func compositeKey(sheetID, key string) string {
	return sheetID + ":" + key
}

// TryAcquire attempts to acquire a lock (non-blocking)
// Returns lock if acquired, error if held by someone else
func (m *Manager) TryAcquire(sheetID, key, owner string, ttl time.Duration) (*Lock, error) {
	ck := compositeKey(sheetID, key)
	now := time.Now()

	// Check if lock exists
	if existing, ok := m.locks.Load(ck); ok {
		existingLock := existing.(*Lock)

		// If expired, we can take it
		if existingLock.IsExpired() {
			// Fall through to acquire
		} else if existingLock.Owner == owner {
			// Same owner - refresh TTL
			existingLock.ExpiresAt = now.Add(ttl)
			return existingLock, nil
		} else {
			// Held by someone else
			return nil, ErrLockHeld
		}
	}

	// Create new lock
	lock := &Lock{
		Key:        key,
		Owner:      owner,
		SheetID:    sheetID,
		AcquiredAt: now,
		ExpiresAt:  now.Add(ttl),
	}

	// Atomic store - race safe
	actual, loaded := m.locks.LoadOrStore(ck, lock)
	if loaded {
		// Someone else got it first
		existingLock := actual.(*Lock)
		if existingLock.Owner != owner && !existingLock.IsExpired() {
			return nil, ErrLockHeld
		}
		// Either ours or expired - overwrite
		m.locks.Store(ck, lock)
	}

	return lock, nil
}

// Release removes a lock if caller is the owner
func (m *Manager) Release(sheetID, key, owner string) error {
	ck := compositeKey(sheetID, key)

	existing, ok := m.locks.Load(ck)
	if !ok {
		return ErrLockNotFound
	}

	lock := existing.(*Lock)
	if lock.Owner != owner {
		return ErrNotOwner
	}

	m.locks.Delete(ck)
	return nil
}

// IsLocked checks if a key is locked
func (m *Manager) IsLocked(sheetID, key string) (bool, *Lock) {
	ck := compositeKey(sheetID, key)

	existing, ok := m.locks.Load(ck)
	if !ok {
		return false, nil
	}

	lock := existing.(*Lock)
	if lock.IsExpired() {
		return false, nil
	}

	return true, lock
}

// ReleaseAllByOwner releases all locks held by a specific owner
// Called when client disconnects
func (m *Manager) ReleaseAllByOwner(owner string) int {
	count := 0
	toDelete := make([]string, 0)

	m.locks.Range(func(k, v interface{}) bool {
		lock := v.(*Lock)
		if lock.Owner == owner {
			toDelete = append(toDelete, k.(string))
			count++
		}
		return true
	})

	for _, key := range toDelete {
		m.locks.Delete(key)
	}

	return count
}

// CleanupExpired removes all expired locks
// Should be called periodically (e.g., every 5 seconds)
func (m *Manager) CleanupExpired() int {
	count := 0
	toDelete := make([]string, 0)

	m.locks.Range(func(k, v interface{}) bool {
		lock := v.(*Lock)
		if lock.IsExpired() {
			toDelete = append(toDelete, k.(string))
			count++
		}
		return true
	})

	for _, key := range toDelete {
		m.locks.Delete(key)
	}

	if count > 0 {
		logger.Info("Cleaned up " + itoa(count) + " expired locks")
	}

	return count
}

// Count returns number of active locks
func (m *Manager) Count() int {
	count := 0
	m.locks.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// GetAllBySheet returns all locks for a sheet (for replication)
func (m *Manager) GetAllBySheet(sheetID string) []*Lock {
	result := make([]*Lock, 0)
	prefix := sheetID + ":"

	m.locks.Range(func(k, v interface{}) bool {
		key := k.(string)
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			lock := v.(*Lock)
			if !lock.IsExpired() {
				result = append(result, lock)
			}
		}
		return true
	})

	return result
}

// SetLock directly sets a lock (for replication from leader)
func (m *Manager) SetLock(lock *Lock) {
	ck := compositeKey(lock.SheetID, lock.Key)
	m.locks.Store(ck, lock)
}

// itoa - simple int to string
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
