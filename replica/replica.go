// Package replica handles replication between advo_cache instances
package replica

import (
	"time"

	"github.com/gorilla/websocket"
)

// Replica represents a connected replica instance
type Replica struct {
	Conn       *websocket.Conn
	InstanceID string
	StartedAt  time.Time // UTC - used for leader election
}

// ReplicateMsg sent from leader to replicas
type ReplicateMsg struct {
	Type   string      `json:"type"`            // "replicate" or "replicate_delete"
	Sheet  string      `json:"sheet"`           // which sheet
	Key    string      `json:"key"`             // which key
	Value  interface{} `json:"value,omitempty"` // value (for set)
	Jitter bool        `json:"jitter,omitempty"` // was jitter used
}

// FullStateMsg sent when replica first connects
type FullStateMsg struct {
	Type   string                            `json:"type"` // "full_state"
	Sheets map[string]map[string]interface{} `json:"sheets"`
}

// RegisterReplicaRequest from replica to leader
type RegisterReplicaRequest struct {
	Action     string `json:"action"`      // "register_replica"
	InstanceID string `json:"instance_id"` // unique ID for this instance
	StartedAt  string `json:"started_at"`  // RFC3339 UTC timestamp
}

// SheetStateMsg sent to clients when they open a sheet (contains current state)
type SheetStateMsg struct {
	Type string                 `json:"type"` // "sheet_state"
	Data map[string]interface{} `json:"data"`
}

// InvalidateMsg broadcast to collaborators when data is deleted
type InvalidateMsg struct {
	Type   string `json:"type"`             // "invalidate"
	Key    string `json:"key,omitempty"`    // single key deleted
	Prefix string `json:"prefix,omitempty"` // prefix deleted
	Suffix string `json:"suffix,omitempty"` // suffix deleted
}

// ReplicateDeletePrefixMsg sent to replicas for prefix deletions
type ReplicateDeletePrefixMsg struct {
	Type   string `json:"type"`   // "replicate_delete_prefix"
	Sheet  string `json:"sheet"`  // which sheet
	Prefix string `json:"prefix"` // prefix to delete
}

// ReplicateDeleteSuffixMsg sent to replicas for suffix deletions
type ReplicateDeleteSuffixMsg struct {
	Type   string `json:"type"`   // "replicate_delete_suffix"
	Sheet  string `json:"sheet"`  // which sheet
	Suffix string `json:"suffix"` // suffix to delete
}

// ReplicateLockMsg sent to replicas when a lock is acquired
type ReplicateLockMsg struct {
	Type      string `json:"type"`       // "replicate_lock"
	Sheet     string `json:"sheet"`      // which sheet
	Key       string `json:"key"`        // lock key
	Owner     string `json:"owner"`      // owner identifier
	ExpiresAt string `json:"expires_at"` // RFC3339 expiration time
}

// ReplicateUnlockMsg sent to replicas when a lock is released
type ReplicateUnlockMsg struct {
	Type  string `json:"type"`  // "replicate_unlock"
	Sheet string `json:"sheet"` // which sheet
	Key   string `json:"key"`   // lock key
}

// ReplicateEvictMsg sent to replicas when LRU eviction occurs
type ReplicateEvictMsg struct {
	Type  string `json:"type"`  // "replicate_evict"
	Sheet string `json:"sheet"` // which sheet
	Key   string `json:"key"`   // evicted key
}

// New creates a new Replica
func New(conn *websocket.Conn, instanceID string, startedAt time.Time) *Replica {
	return &Replica{
		Conn:       conn,
		InstanceID: instanceID,
		StartedAt:  startedAt,
	}
}
