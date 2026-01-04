// Package ws handles WebSocket connections and message routing
package ws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/CrimsonCoder42/advo_cache/config"
	"github.com/CrimsonCoder42/advo_cache/lock"
	"github.com/CrimsonCoder42/advo_cache/logger"
	"github.com/CrimsonCoder42/advo_cache/replica"
	"github.com/CrimsonCoder42/advo_cache/sheet"
	"github.com/gorilla/websocket"
)

// =============================================================================
// MESSAGE TYPES
// =============================================================================

// Request from client
type Request struct {
	Action     string      `json:"action"` // open, get, set, delete, delete_prefix, delete_suffix, dump, lock, unlock, set_null, register_replica
	Sheet      string      `json:"sheet,omitempty"`       // for "open" action
	Password   string      `json:"password,omitempty"`    // for "open" action
	Key        string      `json:"key,omitempty"`
	Value      interface{} `json:"value,omitempty"`
	TTL        int         `json:"ttl,omitempty"`
	Prefix     string      `json:"prefix,omitempty"`
	Suffix     string      `json:"suffix,omitempty"`
	Jitter     bool        `json:"jitter,omitempty"`
	InstanceID string      `json:"instance_id,omitempty"` // for "register_replica"
	StartedAt  string      `json:"started_at,omitempty"`  // for "register_replica" (RFC3339)
	LockTTL    int         `json:"lock_ttl,omitempty"`    // seconds, for "lock" action
}

// Response to client
type Response struct {
	Success bool        `json:"success"`
	Sheet   string      `json:"sheet,omitempty"` // confirms which sheet
	Key     string      `json:"key,omitempty"`
	Value   interface{} `json:"value,omitempty"`
	Deleted int         `json:"deleted,omitempty"`
	Error   string      `json:"error,omitempty"`
	Count   int         `json:"count,omitempty"`
}

// =============================================================================
// CLIENT - tracks connection and its sheet
// =============================================================================

// Client represents a connected WebSocket client
type Client struct {
	Conn  *websocket.Conn
	Sheet *sheet.Sheet // nil until "open" succeeds
}

// =============================================================================
// WEBSOCKET UPGRADER
// =============================================================================

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins
	},
}

// =============================================================================
// HANDLER
// =============================================================================

// Handler handles WebSocket connections
type Handler struct {
	manager     *sheet.Manager
	lockManager *lock.Manager // distributed lock manager
	clients     sync.Map      // conn → *Client
	replicas    sync.Map      // conn → *replica.Replica
	instanceID  string        // unique ID for this instance
	startedAt   time.Time     // when this instance started (UTC)
	isLeader    bool          // true if we're the leader (first to start)
}

// NewHandler creates a new WebSocket handler
func NewHandler(m *sheet.Manager, instanceID string, startedAt time.Time) *Handler {
	return &Handler{
		manager:     m,
		lockManager: lock.NewManager(),
		instanceID:  instanceID,
		startedAt:   startedAt,
		isLeader:    true, // assume leader until told otherwise
	}
}

// GetLockManager returns the lock manager (for cleanup goroutine)
func (h *Handler) GetLockManager() *lock.Manager {
	return h.lockManager
}

// ServeWS handles WebSocket requests
func (h *Handler) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Upgrade error: " + err.Error())
		return
	}

	// Create client (no sheet yet)
	client := &Client{Conn: conn, Sheet: nil}
	h.clients.Store(conn, client)
	logger.Info("Client connected (no sheet yet)")

	// Cleanup on disconnect
	defer h.onDisconnect(client)

	// Message loop
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return // disconnect
		}

		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			h.sendError(conn, "invalid JSON")
			continue
		}

		h.handleAction(client, req)
	}
}

// onDisconnect cleans up when client leaves
func (h *Handler) onDisconnect(client *Client) {
	// Check if this was a replica
	if _, isReplica := h.replicas.Load(client.Conn); isReplica {
		h.replicas.Delete(client.Conn)
		client.Conn.Close()
		logger.Info("Replica disconnected")
		return
	}

	// Release all locks held by this client
	ownerID := fmt.Sprintf("%p", client.Conn)
	released := h.lockManager.ReleaseAllByOwner(ownerID)
	if released > 0 {
		logger.Info("Released " + itoa(released) + " locks on disconnect")
	}

	// Remove from clients map
	h.clients.Delete(client.Conn)

	// Remove from sheet collaborators
	if client.Sheet != nil {
		remaining := client.Sheet.RemoveCollaborator(client.Conn)
		if remaining == 0 {
			// Last one out, delete the sheet
			h.manager.DeleteSheet(client.Sheet.ID)
		}
	}

	client.Conn.Close()
	logger.Info("Client disconnected")
}

// =============================================================================
// ACTION ROUTING
// =============================================================================

func (h *Handler) handleAction(client *Client, req Request) {
	// "open" is the only action allowed without a sheet
	if req.Action == "open" {
		h.handleOpen(client, req)
		return
	}

	// "register_replica" - another advo_cache instance connecting as backup
	if req.Action == "register_replica" {
		h.handleRegisterReplica(client, req)
		return
	}

	// Guard: all other actions require a sheet
	if client.Sheet == nil {
		logger.Warning("Action rejected - no sheet open")
		h.sendError(client.Conn, "must open sheet first")
		return
	}

	// Route to handler (uses switch per guidelines)
	switch req.Action {
	case "get":
		h.handleGet(client, req)
	case "set":
		h.handleSet(client, req)
	case "delete":
		h.handleDelete(client, req)
	case "delete_prefix":
		h.handleDeletePrefix(client, req)
	case "delete_suffix":
		h.handleDeleteSuffix(client, req)
	case "dump":
		h.handleDump(client)
	case "lock":
		h.handleLock(client, req)
	case "unlock":
		h.handleUnlock(client, req)
	case "set_null":
		h.handleSetNull(client, req)
	default:
		h.sendError(client.Conn, "unknown action: "+req.Action)
	}
}

// =============================================================================
// OPEN - Join or create a sheet
// =============================================================================

func (h *Handler) handleOpen(client *Client, req Request) {
	// Already in a sheet? Reject
	if client.Sheet != nil {
		h.sendError(client.Conn, "already in sheet: "+client.Sheet.ID)
		return
	}

	// Validate inputs
	if req.Sheet == "" || req.Password == "" {
		h.sendError(client.Conn, "sheet and password required")
		return
	}

	// Get or create sheet
	s, err := h.manager.GetOrCreateSheet(req.Sheet, req.Password)
	if err != nil {
		h.sendError(client.Conn, "invalid password")
		client.Conn.Close()
		return
	}

	// Join the sheet
	client.Sheet = s
	s.AddCollaborator(client.Conn)

	// Send success response
	h.sendResponse(client.Conn, Response{
		Success: true,
		Sheet:   req.Sheet,
	})

	// Send current sheet state to the new collaborator
	h.sendSheetState(client.Conn, s)
}

// sendSheetState sends current sheet data to a client (for Reader package support)
func (h *Handler) sendSheetState(conn *websocket.Conn, s *sheet.Sheet) {
	msg := replica.SheetStateMsg{
		Type: "sheet_state",
		Data: s.Dump(),
	}
	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
}

// =============================================================================
// ACTION HANDLERS - All scoped to client's sheet
// =============================================================================

func (h *Handler) handleGet(client *Client, req Request) {
	value, ok := client.Sheet.Get(req.Key)
	if !ok {
		h.sendError(client.Conn, "key not found")
		return
	}
	h.sendResponse(client.Conn, Response{
		Success: true,
		Key:     req.Key,
		Value:   value,
	})
}

func (h *Handler) handleSet(client *Client, req Request) {
	// Set returns evicted key if capacity was exceeded (LRU eviction)
	var evictedKey string
	if req.Jitter {
		evictedKey = client.Sheet.SetWithJitter(req.Key, req.Value, config.AppConfig.CacheTTL(), config.AppConfig.JitterPercent)
	} else {
		evictedKey = client.Sheet.Set(req.Key, req.Value, config.AppConfig.CacheTTL())
	}
	h.sendResponse(client.Conn, Response{
		Success: true,
		Key:     req.Key,
	})

	// Broadcast to collaborators in this sheet (so all Readers stay in sync)
	client.Sheet.BroadcastSetMsg(client.Conn, req.Key, req.Value)

	// Replicate to followers (backup servers)
	h.replicateSet(client.Sheet.ID, req.Key, req.Value, req.Jitter)

	// If LRU eviction occurred, notify collaborators AND replicate to followers
	if evictedKey != "" {
		// 1. Broadcast to collaborators (Readers) so they remove from local cache
		client.Sheet.Broadcast(client.Conn, sheet.Broadcast{
			Type: "invalidate",
			Key:  evictedKey,
		})
		// 2. Replicate to backup servers
		h.replicateEvict(client.Sheet.ID, evictedKey)
	}
}

func (h *Handler) handleDelete(client *Client, req Request) {
	client.Sheet.Delete(req.Key)
	h.sendResponse(client.Conn, Response{
		Success: true,
		Key:     req.Key,
		Deleted: 1,
	})

	// Broadcast to THIS sheet's collaborators only
	client.Sheet.Broadcast(client.Conn, sheet.Broadcast{
		Type: "invalidate",
		Key:  req.Key,
	})

	// Replicate to followers
	h.replicateDelete(client.Sheet.ID, req.Key)
}

func (h *Handler) handleDeletePrefix(client *Client, req Request) {
	count := client.Sheet.DeletePrefix(req.Prefix)
	h.sendResponse(client.Conn, Response{
		Success: true,
		Deleted: count,
	})

	client.Sheet.Broadcast(client.Conn, sheet.Broadcast{
		Type:   "invalidate",
		Prefix: req.Prefix,
	})

	// Replicate to followers
	h.replicateDeletePrefix(client.Sheet.ID, req.Prefix)
}

func (h *Handler) handleDeleteSuffix(client *Client, req Request) {
	count := client.Sheet.DeleteSuffix(req.Suffix)
	h.sendResponse(client.Conn, Response{
		Success: true,
		Deleted: count,
	})

	client.Sheet.Broadcast(client.Conn, sheet.Broadcast{
		Type:   "invalidate",
		Suffix: req.Suffix,
	})

	// Replicate to followers
	h.replicateDeleteSuffix(client.Sheet.ID, req.Suffix)
}

func (h *Handler) handleDump(client *Client) {
	data := client.Sheet.Dump()
	h.sendResponse(client.Conn, Response{
		Success: true,
		Value:   data,
		Count:   len(data),
	})
}

// =============================================================================
// DISTRIBUTED LOCKING - With ownership and TTL
// =============================================================================

func (h *Handler) handleLock(client *Client, req Request) {
	// Generate owner ID from connection pointer
	ownerID := fmt.Sprintf("%p", client.Conn)

	// Determine TTL (use custom or default)
	ttl := config.AppConfig.LockTTL()
	if req.LockTTL > 0 {
		ttl = time.Duration(req.LockTTL) * time.Second
	}

	// Try to acquire (non-blocking)
	acquiredLock, err := h.lockManager.TryAcquire(client.Sheet.ID, req.Key, ownerID, ttl)
	if err != nil {
		// Lock held by someone else
		locked, existingLock := h.lockManager.IsLocked(client.Sheet.ID, req.Key)
		if locked && existingLock != nil {
			h.sendResponse(client.Conn, Response{
				Success: false,
				Error:   "lock held by another client",
				Key:     req.Key,
			})
		} else {
			h.sendError(client.Conn, err.Error())
		}
		return
	}

	h.sendResponse(client.Conn, Response{
		Success: true,
		Key:     req.Key,
	})

	// Replicate to followers
	h.replicateLock(client.Sheet.ID, req.Key, ownerID, acquiredLock.ExpiresAt)
}

func (h *Handler) handleUnlock(client *Client, req Request) {
	// Generate owner ID from connection pointer
	ownerID := fmt.Sprintf("%p", client.Conn)

	// Try to release (only owner can release)
	err := h.lockManager.Release(client.Sheet.ID, req.Key, ownerID)
	if err != nil {
		switch err {
		case lock.ErrNotOwner:
			h.sendError(client.Conn, "not lock owner")
		case lock.ErrLockNotFound:
			h.sendError(client.Conn, "lock not found")
		default:
			h.sendError(client.Conn, err.Error())
		}
		return
	}

	h.sendResponse(client.Conn, Response{
		Success: true,
		Key:     req.Key,
	})

	// Replicate unlock to followers
	h.replicateUnlock(client.Sheet.ID, req.Key)
}

// =============================================================================
// PENETRATION PREVENTION - Null Placeholder
// =============================================================================

func (h *Handler) handleSetNull(client *Client, req Request) {
	// SetNull returns evicted key if capacity was exceeded (LRU eviction)
	evictedKey := client.Sheet.SetNull(req.Key, config.AppConfig.CacheTTL())
	h.sendResponse(client.Conn, Response{
		Success: true,
		Key:     req.Key,
	})

	// Replicate to followers (set_null is just a special set)
	h.replicateSet(client.Sheet.ID, req.Key, "__NULL__", false)

	// If LRU eviction occurred, notify collaborators AND replicate to followers
	if evictedKey != "" {
		// 1. Broadcast to collaborators (Readers) so they remove from local cache
		client.Sheet.Broadcast(client.Conn, sheet.Broadcast{
			Type: "invalidate",
			Key:  evictedKey,
		})
		// 2. Replicate to backup servers
		h.replicateEvict(client.Sheet.ID, evictedKey)
	}
}

// =============================================================================
// REPLICATION - Leader/Follower support
// =============================================================================

func (h *Handler) handleRegisterReplica(client *Client, req Request) {
	// Parse started_at timestamp
	startedAt, err := time.Parse(time.RFC3339, req.StartedAt)
	if err != nil {
		h.sendError(client.Conn, "invalid started_at timestamp (use RFC3339)")
		return
	}

	// Validate inputs
	if req.InstanceID == "" {
		h.sendError(client.Conn, "instance_id required")
		return
	}

	// Create replica
	rep := replica.New(client.Conn, req.InstanceID, startedAt)
	h.replicas.Store(client.Conn, rep)

	// Remove from clients map (it's a replica, not a regular client)
	h.clients.Delete(client.Conn)

	logger.Info("Replica registered: " + req.InstanceID + " (started: " + req.StartedAt + ")")

	// Send full state dump to replica
	h.sendFullState(client.Conn)

	// Send success response
	h.sendResponse(client.Conn, Response{
		Success: true,
	})
}

// sendFullState sends all sheets and their data to a replica
func (h *Handler) sendFullState(conn *websocket.Conn) {
	sheets := make(map[string]map[string]interface{})

	// Iterate all sheets and dump their data
	h.manager.Range(func(sheetID string, s *sheet.Sheet) bool {
		sheets[sheetID] = s.Dump()
		return true
	})

	msg := replica.FullStateMsg{
		Type:   "full_state",
		Sheets: sheets,
	}

	data, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, data)
	logger.Info("Sent full state to replica (" + itoa(len(sheets)) + " sheets)")
}

// replicateSet sends a set operation to all replicas
func (h *Handler) replicateSet(sheetID, key string, value interface{}, jitter bool) {
	msg := replica.ReplicateMsg{
		Type:   "replicate",
		Sheet:  sheetID,
		Key:    key,
		Value:  value,
		Jitter: jitter,
	}
	h.broadcastToReplicas(msg)
}

// replicateDelete sends a delete operation to all replicas
func (h *Handler) replicateDelete(sheetID, key string) {
	msg := replica.ReplicateMsg{
		Type:  "replicate_delete",
		Sheet: sheetID,
		Key:   key,
	}
	h.broadcastToReplicas(msg)
}

// replicateDeletePrefix sends a delete_prefix operation to all replicas
func (h *Handler) replicateDeletePrefix(sheetID, prefix string) {
	msg := replica.ReplicateDeletePrefixMsg{
		Type:   "replicate_delete_prefix",
		Sheet:  sheetID,
		Prefix: prefix,
	}
	h.broadcastToReplicas(msg)
}

// replicateDeleteSuffix sends a delete_suffix operation to all replicas
func (h *Handler) replicateDeleteSuffix(sheetID, suffix string) {
	msg := replica.ReplicateDeleteSuffixMsg{
		Type:   "replicate_delete_suffix",
		Sheet:  sheetID,
		Suffix: suffix,
	}
	h.broadcastToReplicas(msg)
}

// replicateLock sends a lock acquisition to all replicas
func (h *Handler) replicateLock(sheetID, key, owner string, expiresAt time.Time) {
	msg := replica.ReplicateLockMsg{
		Type:      "replicate_lock",
		Sheet:     sheetID,
		Key:       key,
		Owner:     owner,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	}
	h.broadcastToReplicas(msg)
}

// replicateUnlock sends a lock release to all replicas
func (h *Handler) replicateUnlock(sheetID, key string) {
	msg := replica.ReplicateUnlockMsg{
		Type:  "replicate_unlock",
		Sheet: sheetID,
		Key:   key,
	}
	h.broadcastToReplicas(msg)
}

// replicateEvict sends an LRU eviction to all replicas
func (h *Handler) replicateEvict(sheetID, key string) {
	msg := replica.ReplicateEvictMsg{
		Type:  "replicate_evict",
		Sheet: sheetID,
		Key:   key,
	}
	h.broadcastToReplicas(msg)
	logger.Info("LRU eviction replicated: " + sheetID + "/" + key)
}

// broadcastToReplicas sends a message to all connected replicas
func (h *Handler) broadcastToReplicas(msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	h.replicas.Range(func(key, value interface{}) bool {
		conn := key.(*websocket.Conn)
		conn.WriteMessage(websocket.TextMessage, data)
		return true
	})
}

// ReplicaCount returns number of connected replicas
func (h *Handler) ReplicaCount() int {
	count := 0
	h.replicas.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// =============================================================================
// HEARTBEAT - Keep replicas informed that leader is alive
// =============================================================================

// StartHeartbeat begins sending periodic heartbeat to all replicas
func (h *Handler) StartHeartbeat() {
	ticker := time.NewTicker(config.AppConfig.HeartbeatInterval())
	go func() {
		for range ticker.C {
			h.broadcastHeartbeat()
		}
	}()
	logger.Info("Heartbeat started (interval: " + config.AppConfig.HeartbeatInterval().String() + ")")
}

// broadcastHeartbeat sends heartbeat to all connected replicas
func (h *Handler) broadcastHeartbeat() {
	msg := map[string]string{"type": "heartbeat"}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	count := 0
	h.replicas.Range(func(key, _ interface{}) bool {
		conn := key.(*websocket.Conn)
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			logger.Warning("Failed to send heartbeat to replica")
		} else {
			count++
		}
		return true
	})

	if count > 0 {
		logger.Info("Heartbeat sent to " + itoa(count) + " replica(s)")
	}
}

// itoa - simple int to string (avoid fmt import for this)
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

// =============================================================================
// RESPONSE HELPERS
// =============================================================================

func (h *Handler) sendResponse(conn *websocket.Conn, resp Response) {
	data, _ := json.Marshal(resp)
	conn.WriteMessage(websocket.TextMessage, data)
}

func (h *Handler) sendError(conn *websocket.Conn, msg string) {
	h.sendResponse(conn, Response{
		Success: false,
		Error:   msg,
	})
}

// ClientCount returns total connected clients
func (h *Handler) ClientCount() int {
	count := 0
	h.clients.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// Stats returns handler statistics
func (h *Handler) Stats() string {
	return fmt.Sprintf("Clients: %d, Sheets: %d, Replicas: %d", h.ClientCount(), h.manager.SheetCount(), h.ReplicaCount())
}
