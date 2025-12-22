// Package replica - client.go handles connecting TO leader as a replica
package replica

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/CrimsonCoder42/advo_cache/config"
	"github.com/CrimsonCoder42/advo_cache/logger"
	"github.com/CrimsonCoder42/advo_cache/sheet"
	"github.com/gorilla/websocket"
)

// Client connects to leader and receives replication
type Client struct {
	conn          *websocket.Conn
	manager       *sheet.Manager
	instanceID    string
	startedAt     time.Time
	leaderAddr    string
	done          chan struct{}
	lastHeartbeat time.Time
	mu            sync.Mutex
	isPromoted    bool
}

// NewClient creates replica client
func NewClient(manager *sheet.Manager, instanceID string, startedAt time.Time, leaderAddr string) *Client {
	return &Client{
		manager:       manager,
		instanceID:    instanceID,
		startedAt:     startedAt,
		leaderAddr:    leaderAddr,
		done:          make(chan struct{}),
		lastHeartbeat: time.Now(), // Initialize to now to avoid immediate timeout
	}
}

// Connect to leader and start receiving replication
func (c *Client) Connect() error {
	conn, _, err := websocket.DefaultDialer.Dial(c.leaderAddr, nil)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.lastHeartbeat = time.Now()
	c.mu.Unlock()

	// Register as replica
	msg := RegisterReplicaRequest{
		Action:     "register_replica",
		InstanceID: c.instanceID,
		StartedAt:  c.startedAt.Format(time.RFC3339),
	}
	data, _ := json.Marshal(msg)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		conn.Close()
		return err
	}

	logger.Info("Connected to leader: " + c.leaderAddr)

	// Start listening for replication messages
	go c.listen()

	// Start heartbeat monitor
	go c.monitorHeartbeat()

	return nil
}

// listen for messages from leader
func (c *Client) listen() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()

		if conn == nil {
			return
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			logger.Error("Leader connection lost: " + err.Error())
			c.handleLeaderDisconnect()
			return
		}

		c.handleMessage(data)
	}
}

// handleMessage processes incoming replication messages
func (c *Client) handleMessage(data []byte) {
	var base struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return
	}

	// Use switch for O(1) routing
	switch base.Type {
	case "heartbeat":
		c.mu.Lock()
		c.lastHeartbeat = time.Now()
		c.mu.Unlock()
		logger.Info("Heartbeat received from leader")

	case "success":
		// Registration acknowledged
		logger.Info("Replica registration acknowledged by leader")

	case "full_state":
		var msg FullStateMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			logger.Error("Failed to parse full_state: " + err.Error())
			return
		}
		c.applyFullState(msg)

	case "replicate":
		var msg ReplicateMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			logger.Error("Failed to parse replicate: " + err.Error())
			return
		}
		c.applySet(msg)

	case "replicate_delete":
		var msg ReplicateMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			logger.Error("Failed to parse replicate_delete: " + err.Error())
			return
		}
		c.applyDelete(msg)

	case "replicate_delete_prefix":
		var msg ReplicateDeletePrefixMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			logger.Error("Failed to parse replicate_delete_prefix: " + err.Error())
			return
		}
		c.applyDeletePrefix(msg)

	case "replicate_delete_suffix":
		var msg ReplicateDeleteSuffixMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			logger.Error("Failed to parse replicate_delete_suffix: " + err.Error())
			return
		}
		c.applyDeleteSuffix(msg)

	case "replicate_lock":
		// Lock replication - log but don't apply (locks are connection-specific)
		logger.Info("Lock replication received (ignored - locks are connection-specific)")

	case "replicate_unlock":
		// Unlock replication - log but don't apply
		logger.Info("Unlock replication received (ignored)")

	default:
		logger.Warning("Unknown message type from leader: " + base.Type)
	}
}

// applyFullState loads all sheets from leader
func (c *Client) applyFullState(msg FullStateMsg) {
	count := 0
	for sheetID, data := range msg.Sheets {
		// Get or create sheet
		s := c.manager.GetSheet(sheetID)
		if s == nil {
			// Create sheet without password validation for replica
			s = sheet.NewReplicaSheet(sheetID)
			c.manager.AddSheet(sheetID, s)
		}
		// Load all data
		for key, value := range data {
			s.Set(key, value, config.AppConfig.CacheTTL())
		}
		count++
	}
	logger.Info("Applied full state from leader (" + itoa(count) + " sheets)")
}

// applySet applies a set operation from leader
func (c *Client) applySet(msg ReplicateMsg) {
	s := c.manager.GetSheet(msg.Sheet)
	if s == nil {
		// Sheet doesn't exist yet - create it
		s = sheet.NewReplicaSheet(msg.Sheet)
		c.manager.AddSheet(msg.Sheet, s)
	}

	if msg.Jitter {
		s.SetWithJitter(msg.Key, msg.Value, config.AppConfig.CacheTTL(), config.AppConfig.JitterPercent)
	} else {
		s.Set(msg.Key, msg.Value, config.AppConfig.CacheTTL())
	}
}

// applyDelete applies a delete operation from leader
func (c *Client) applyDelete(msg ReplicateMsg) {
	s := c.manager.GetSheet(msg.Sheet)
	if s != nil {
		s.Delete(msg.Key)
	}
}

// applyDeletePrefix applies a prefix delete from leader
func (c *Client) applyDeletePrefix(msg ReplicateDeletePrefixMsg) {
	s := c.manager.GetSheet(msg.Sheet)
	if s != nil {
		s.DeletePrefix(msg.Prefix)
	}
}

// applyDeleteSuffix applies a suffix delete from leader
func (c *Client) applyDeleteSuffix(msg ReplicateDeleteSuffixMsg) {
	s := c.manager.GetSheet(msg.Sheet)
	if s != nil {
		s.DeleteSuffix(msg.Suffix)
	}
}

// monitorHeartbeat watches for leader heartbeat timeout
func (c *Client) monitorHeartbeat() {
	ticker := time.NewTicker(config.AppConfig.HeartbeatInterval())
	defer ticker.Stop()

	timeout := config.AppConfig.HeartbeatTimeout()

	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			lastBeat := c.lastHeartbeat
			promoted := c.isPromoted
			c.mu.Unlock()

			if promoted {
				return
			}

			if time.Since(lastBeat) > timeout {
				logger.Warning("Leader heartbeat timeout (" + timeout.String() + ") - promoting to leader")
				c.promoteToLeader()
				return
			}
		}
	}
}

// promoteToLeader transitions this replica to leader mode
func (c *Client) promoteToLeader() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isPromoted {
		return // Already promoted
	}
	c.isPromoted = true

	// Close connection to old leader
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	// Replica is now the leader - it already accepts client connections!
	logger.Info("═══════════════════════════════════════════════════════════════")
	logger.Info("  PROMOTED TO LEADER - now accepting clients")
	logger.Info("═══════════════════════════════════════════════════════════════")
}

// handleLeaderDisconnect attempts reconnection before promoting
func (c *Client) handleLeaderDisconnect() {
	c.mu.Lock()
	if c.isPromoted {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	// Try to reconnect
	for i := 0; i < 3; i++ {
		logger.Info("Attempting to reconnect to leader (attempt " + itoa(i+1) + "/3)...")
		time.Sleep(time.Second * time.Duration(i+1))

		if err := c.Connect(); err == nil {
			logger.Info("Reconnected to leader successfully")
			return
		}
	}

	// Give up - promote to leader
	logger.Warning("Failed to reconnect to leader after 3 attempts")
	c.promoteToLeader()
}

// Close shuts down the replica client
func (c *Client) Close() {
	close(c.done)
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.mu.Unlock()
}

// IsPromoted returns true if this replica has been promoted to leader
func (c *Client) IsPromoted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isPromoted
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
