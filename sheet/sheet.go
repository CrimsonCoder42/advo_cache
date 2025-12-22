// Package sheet provides tenant-isolated cache instances
package sheet

import (
	"encoding/json"
	"sync"

	"github.com/CrimsonCoder42/advo_cache/cache"
	"github.com/CrimsonCoder42/advo_cache/logger"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

// Sheet - isolated cache + collaborators (like a Google Sheet)
type Sheet struct {
	ID            string
	PasswordHash  []byte
	*cache.Cache                             // EMBED - reuse all cache methods
	Collaborators map[*websocket.Conn]bool   // who's in this sheet
	mu            sync.RWMutex               // protect Collaborators map
}

// Broadcast message structure (for invalidations)
type Broadcast struct {
	Type   string `json:"type"`
	Key    string `json:"key,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

// BroadcastSet message structure (for set operations)
type BroadcastSet struct {
	Type  string      `json:"type"`  // "replicate"
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

// NewSheet creates a new isolated sheet with password
func NewSheet(id, password string) (*Sheet, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		logger.Error("Failed to hash password for sheet: " + id)
		return nil, err
	}

	return &Sheet{
		ID:            id,
		PasswordHash:  hash,
		Cache:         cache.NewSilent(), // silent version - no log on create
		Collaborators: make(map[*websocket.Conn]bool),
	}, nil
}

// NewReplicaSheet creates sheet without password (for replica receiving replication)
// Replica sheets can accept client connections after promotion
func NewReplicaSheet(id string) *Sheet {
	logger.Info("Replica sheet created: " + id)
	return &Sheet{
		ID:            id,
		PasswordHash:  nil, // No password for replica sheets
		Cache:         cache.NewSilent(),
		Collaborators: make(map[*websocket.Conn]bool),
	}
}

// ValidatePassword checks if password matches
// For replica sheets (nil hash), accepts any password and sets it as the new hash
func (s *Sheet) ValidatePassword(password string) bool {
	// Replica sheet - no password set yet, accept and set it
	if s.PasswordHash == nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return false
		}
		s.PasswordHash = hash
		logger.Info("Password set for promoted replica sheet: " + s.ID)
		return true
	}
	// Normal validation
	err := bcrypt.CompareHashAndPassword(s.PasswordHash, []byte(password))
	return err == nil
}

// AddCollaborator adds a connection to this sheet
func (s *Sheet) AddCollaborator(conn *websocket.Conn) {
	s.mu.Lock()
	s.Collaborators[conn] = true
	count := len(s.Collaborators)
	s.mu.Unlock()
	logger.Info("Client joined sheet: " + s.ID + " (collaborators: " + itoa(count) + ")")
}

// RemoveCollaborator removes a connection, returns remaining count
func (s *Sheet) RemoveCollaborator(conn *websocket.Conn) int {
	s.mu.Lock()
	delete(s.Collaborators, conn)
	count := len(s.Collaborators)
	s.mu.Unlock()
	logger.Info("Client left sheet: " + s.ID + " (collaborators: " + itoa(count) + ")")
	return count
}

// CollaboratorCount returns number of active collaborators
func (s *Sheet) CollaboratorCount() int {
	s.mu.RLock()
	count := len(s.Collaborators)
	s.mu.RUnlock()
	return count
}

// Broadcast sends message to all collaborators except sender
func (s *Sheet) Broadcast(sender *websocket.Conn, msg Broadcast) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for conn := range s.Collaborators {
		if conn != sender {
			conn.WriteMessage(websocket.TextMessage, data)
		}
	}
}

// BroadcastSetMsg sends a set operation to all collaborators except sender
func (s *Sheet) BroadcastSetMsg(sender *websocket.Conn, key string, value interface{}) {
	msg := BroadcastSet{
		Type:  "replicate",
		Key:   key,
		Value: value,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for conn := range s.Collaborators {
		if conn != sender {
			conn.WriteMessage(websocket.TextMessage, data)
		}
	}
}

// itoa - simple int to string (avoid fmt import)
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
