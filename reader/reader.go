// Package reader provides an embeddable cache client for Go applications.
// It connects to an advo_cache server via WebSocket and provides O(1) local reads
// with write forwarding to the server.
//
// Architecture:
//   - Reads: Local sync.Map (nanoseconds, no network)
//   - Writes: Fire-and-forget to server, local cache updated immediately
//   - Updates: Server broadcasts changes, Reader applies them automatically
//
// Example usage:
//
//	r, err := reader.New(reader.Config{
//	    LeaderAddr: "ws://localhost:8081/ws",
//	    BackupAddr: "ws://localhost:8082/ws", // optional
//	    Sheet:      "tenant-123",
//	    Password:   "secret",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer r.Close()
//
//	// Read (local, O(1))
//	if val, ok := r.Get("user:1"); ok {
//	    fmt.Println(val)
//	}
//
//	// Write (forwards to server)
//	r.Set(ctx, "user:1", map[string]string{"name": "John"})
package reader

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// =============================================================================
// ERRORS
// =============================================================================

var (
	ErrNotConnected = errors.New("not connected to server")
	ErrAuthFailed   = errors.New("authentication failed")
	ErrClosed       = errors.New("reader is closed")
)

// =============================================================================
// CONFIG
// =============================================================================

// Config for creating a new Reader
type Config struct {
	LeaderAddr string // WebSocket URL of leader (e.g., "ws://localhost:8081/ws")
	BackupAddr string // WebSocket URL of backup (optional, for failover)
	Sheet      string // Sheet/tenant name to join
	Password   string // Password for the sheet

	// Reconnect settings (optional, defaults applied)
	MaxReconnectAttempts int           // Max attempts before failover (default: 3)
	ReconnectDelay       time.Duration // Initial delay between attempts (default: 1s)
}

// =============================================================================
// MESSAGE TYPES (for parsing server messages)
// =============================================================================

type baseMsg struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type sheetStateMsg struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

type replicateMsg struct {
	Type  string      `json:"type"`
	Key   string      `json:"key"`
	Value interface{} `json:"value,omitempty"`
}

type invalidateMsg struct {
	Type   string `json:"type"`
	Key    string `json:"key,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

// =============================================================================
// READER
// =============================================================================

// Reader is an embeddable cache client that connects to an advo_cache server.
// It provides O(1) local reads and forwards writes to the server.
type Reader struct {
	config Config

	conn      *websocket.Conn
	cache     sync.Map // key → value (local cache)
	mu        sync.RWMutex
	connected bool
	closed    bool
	done      chan struct{}
	wg        sync.WaitGroup

	// Current connection target
	currentAddr string
}

// New creates a new Reader and connects to the server.
// It returns after successfully authenticating and receiving the initial state.
func New(cfg Config) (*Reader, error) {
	// Apply defaults
	if cfg.MaxReconnectAttempts == 0 {
		cfg.MaxReconnectAttempts = 3
	}
	if cfg.ReconnectDelay == 0 {
		cfg.ReconnectDelay = time.Second
	}

	r := &Reader{
		config: cfg,
		done:   make(chan struct{}),
	}

	// Connect to leader
	if err := r.connectTo(cfg.LeaderAddr); err != nil {
		// Try backup if available
		if cfg.BackupAddr != "" {
			if err := r.connectTo(cfg.BackupAddr); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	return r, nil
}

// =============================================================================
// READ OPERATIONS - Local sync.Map, O(1)
// =============================================================================

// Get retrieves a value from the local cache.
// Returns (value, true) if found, (nil, false) if not.
// This is an O(1) operation with no network call.
func (r *Reader) Get(key string) (interface{}, bool) {
	return r.cache.Load(key)
}

// GetString retrieves a string value from the cache.
// Returns ("", false) if not found or not a string.
func (r *Reader) GetString(key string) (string, bool) {
	val, ok := r.cache.Load(key)
	if !ok {
		return "", false
	}
	str, ok := val.(string)
	return str, ok
}

// GetMap retrieves a map value from the cache.
// Returns (nil, false) if not found or not a map.
func (r *Reader) GetMap(key string) (map[string]interface{}, bool) {
	val, ok := r.cache.Load(key)
	if !ok {
		return nil, false
	}
	m, ok := val.(map[string]interface{})
	return m, ok
}

// Has checks if a key exists in the local cache.
func (r *Reader) Has(key string) bool {
	_, ok := r.cache.Load(key)
	return ok
}

// IsNull checks if a key is a null placeholder (cache penetration prevention).
func (r *Reader) IsNull(key string) bool {
	val, ok := r.cache.Load(key)
	if !ok {
		return false
	}
	str, ok := val.(string)
	return ok && str == "__NULL__"
}

// =============================================================================
// WRITE OPERATIONS - Fire-and-forget to server
// =============================================================================

// Set stores a value in the cache.
// The write is sent to the server and the local cache is updated immediately.
// This is fire-and-forget - errors are returned but the operation continues.
func (r *Reader) Set(ctx context.Context, key string, value interface{}) error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return ErrClosed
	}
	if !r.connected {
		r.mu.RUnlock()
		return ErrNotConnected
	}
	r.mu.RUnlock()

	// Update local cache immediately
	r.cache.Store(key, value)

	// Send to server (fire-and-forget)
	msg := map[string]interface{}{
		"action": "set",
		"key":    key,
		"value":  value,
	}
	return r.send(msg)
}

// Delete removes a key from the cache.
// The delete is sent to the server and the local cache is updated immediately.
func (r *Reader) Delete(ctx context.Context, key string) error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return ErrClosed
	}
	if !r.connected {
		r.mu.RUnlock()
		return ErrNotConnected
	}
	r.mu.RUnlock()

	// Update local cache immediately
	r.cache.Delete(key)

	// Send to server
	msg := map[string]interface{}{
		"action": "delete",
		"key":    key,
	}
	return r.send(msg)
}

// DeletePrefix removes all keys starting with the given prefix.
func (r *Reader) DeletePrefix(ctx context.Context, prefix string) error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return ErrClosed
	}
	if !r.connected {
		r.mu.RUnlock()
		return ErrNotConnected
	}
	r.mu.RUnlock()

	// Update local cache immediately
	r.cache.Range(func(k, _ interface{}) bool {
		if key, ok := k.(string); ok && strings.HasPrefix(key, prefix) {
			r.cache.Delete(key)
		}
		return true
	})

	// Send to server
	msg := map[string]interface{}{
		"action": "delete_prefix",
		"prefix": prefix,
	}
	return r.send(msg)
}

// DeleteSuffix removes all keys ending with the given suffix.
func (r *Reader) DeleteSuffix(ctx context.Context, suffix string) error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return ErrClosed
	}
	if !r.connected {
		r.mu.RUnlock()
		return ErrNotConnected
	}
	r.mu.RUnlock()

	// Update local cache immediately
	r.cache.Range(func(k, _ interface{}) bool {
		if key, ok := k.(string); ok && strings.HasSuffix(key, suffix) {
			r.cache.Delete(key)
		}
		return true
	})

	// Send to server
	msg := map[string]interface{}{
		"action": "delete_suffix",
		"suffix": suffix,
	}
	return r.send(msg)
}

// =============================================================================
// LIFECYCLE
// =============================================================================

// Close disconnects from the server and releases resources.
func (r *Reader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.connected = false
	close(r.done)
	conn := r.conn
	r.mu.Unlock()

	if conn != nil {
		conn.Close()
	}

	r.wg.Wait()
	return nil
}

// IsConnected returns true if connected to a server.
func (r *Reader) IsConnected() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connected
}

// CurrentServer returns the address of the currently connected server.
func (r *Reader) CurrentServer() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.currentAddr
}

// CacheSize returns the number of entries in the local cache.
func (r *Reader) CacheSize() int {
	count := 0
	r.cache.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// =============================================================================
// CONNECTION MANAGEMENT
// =============================================================================

func (r *Reader) connectTo(addr string) error {
	// Dial WebSocket
	conn, _, err := websocket.DefaultDialer.Dial(addr, nil)
	if err != nil {
		return err
	}

	// Send open action
	openMsg := map[string]string{
		"action":   "open",
		"sheet":    r.config.Sheet,
		"password": r.config.Password,
	}
	data, _ := json.Marshal(openMsg)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		conn.Close()
		return err
	}

	// Wait for response + sheet_state
	authenticated := false
	stateReceived := false

	for !authenticated || !stateReceived {
		_, data, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return err
		}

		var base baseMsg
		json.Unmarshal(data, &base)

		// Check for auth response
		if !authenticated {
			if base.Success {
				authenticated = true
			} else if base.Error != "" {
				conn.Close()
				return ErrAuthFailed
			}
			continue
		}

		// Check for sheet_state
		if base.Type == "sheet_state" {
			var state sheetStateMsg
			json.Unmarshal(data, &state)
			r.loadState(state.Data)
			stateReceived = true
		}
	}

	// Connected successfully
	r.mu.Lock()
	r.conn = conn
	r.connected = true
	r.currentAddr = addr
	r.mu.Unlock()

	// Start listener goroutine
	r.wg.Add(1)
	go r.listen()

	return nil
}

func (r *Reader) loadState(data map[string]interface{}) {
	// Clear existing cache
	r.cache.Range(func(key, _ interface{}) bool {
		r.cache.Delete(key)
		return true
	})

	// Load new state
	for key, value := range data {
		r.cache.Store(key, value)
	}
}

func (r *Reader) listen() {
	defer r.wg.Done()

	for {
		select {
		case <-r.done:
			return
		default:
		}

		r.mu.RLock()
		conn := r.conn
		r.mu.RUnlock()

		if conn == nil {
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			r.handleDisconnect()
			return
		}

		r.handleMessage(data)
	}
}

func (r *Reader) handleMessage(data []byte) {
	var base baseMsg
	json.Unmarshal(data, &base)

	switch base.Type {
	case "sheet_state":
		// Full state reload (happens on reconnect)
		var state sheetStateMsg
		json.Unmarshal(data, &state)
		r.loadState(state.Data)

	case "replicate":
		// Set from another client
		var msg replicateMsg
		json.Unmarshal(data, &msg)
		r.cache.Store(msg.Key, msg.Value)

	case "invalidate":
		// Delete from another client
		var msg invalidateMsg
		json.Unmarshal(data, &msg)

		if msg.Key != "" {
			r.cache.Delete(msg.Key)
		} else if msg.Prefix != "" {
			r.cache.Range(func(k, _ interface{}) bool {
				if key, ok := k.(string); ok && strings.HasPrefix(key, msg.Prefix) {
					r.cache.Delete(key)
				}
				return true
			})
		} else if msg.Suffix != "" {
			r.cache.Range(func(k, _ interface{}) bool {
				if key, ok := k.(string); ok && strings.HasSuffix(key, msg.Suffix) {
					r.cache.Delete(key)
				}
				return true
			})
		}
	}
}

// =============================================================================
// FAILOVER
// =============================================================================

func (r *Reader) handleDisconnect() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.connected = false
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.mu.Unlock()

	// Try reconnect to current server
	for i := 0; i < r.config.MaxReconnectAttempts; i++ {
		select {
		case <-r.done:
			return
		case <-time.After(r.config.ReconnectDelay * time.Duration(i+1)):
		}

		if err := r.connectTo(r.currentAddr); err == nil {
			return
		}
	}

	// Failover to backup if available
	if r.config.BackupAddr != "" && r.currentAddr != r.config.BackupAddr {
		for i := 0; i < r.config.MaxReconnectAttempts; i++ {
			select {
			case <-r.done:
				return
			case <-time.After(r.config.ReconnectDelay * time.Duration(i+1)):
			}

			if err := r.connectTo(r.config.BackupAddr); err == nil {
				return
			}
		}
	}

	// Try leader again if we failed over to backup
	if r.config.LeaderAddr != "" && r.currentAddr != r.config.LeaderAddr {
		for i := 0; i < r.config.MaxReconnectAttempts; i++ {
			select {
			case <-r.done:
				return
			case <-time.After(r.config.ReconnectDelay * time.Duration(i+1)):
			}

			if err := r.connectTo(r.config.LeaderAddr); err == nil {
				return
			}
		}
	}
}

func (r *Reader) send(msg interface{}) error {
	r.mu.RLock()
	conn := r.conn
	r.mu.RUnlock()

	if conn == nil {
		return ErrNotConnected
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return conn.WriteMessage(websocket.TextMessage, data)
}
