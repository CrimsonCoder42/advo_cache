// Advo Cache - WebSocket Cache Service
// All backend instances connect via persistent WebSocket
// Real-time, bidirectional, low latency
// Multi-tenant: each tenant gets isolated "sheet"
package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/CrimsonCoder42/advo_cache/config"
	"github.com/CrimsonCoder42/advo_cache/logger"
	"github.com/CrimsonCoder42/advo_cache/replica"
	"github.com/CrimsonCoder42/advo_cache/sheet"
	"github.com/CrimsonCoder42/advo_cache/ws"
)

// generateInstanceID creates a unique identifier for this instance
func generateInstanceID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func main() {
	// Load configuration first (from .env file)
	if err := config.LoadConfig("."); err != nil {
		logger.Error("Failed to load config: " + err.Error())
		os.Exit(1)
	}

	// Record startup time (used for leader election)
	startedAt := time.Now().UTC()
	instanceID := generateInstanceID()

	logger.Info("Instance ID: " + instanceID)
	logger.Info("Started at:  " + startedAt.Format(time.RFC3339))
	logger.Info("Environment: " + config.AppConfig.Environment)

	// Initialize sheet manager (multi-tenant)
	manager := sheet.NewManager()

	// Initialize WebSocket handler
	wsHandler := ws.NewHandler(manager, instanceID, startedAt)

	// Start lock cleanup goroutine (removes expired locks)
	go func() {
		ticker := time.NewTicker(config.AppConfig.LockCleanupInterval())
		for range ticker.C {
			wsHandler.GetLockManager().CleanupExpired()
		}
	}()

	// ==========================================================================
	// MODE DETECTION: Leader or Replica
	// ==========================================================================
	if config.AppConfig.IsReplica() {
		// REPLICA MODE: Connect to leader and receive replication
		logger.Info("───────────────────────────────────────────────────────────────")
		logger.Info("  MODE: REPLICA")
		logger.Info("  Leader: " + config.AppConfig.LeaderAddress)
		logger.Info("───────────────────────────────────────────────────────────────")

		replicaClient := replica.NewClient(
			manager,
			instanceID,
			startedAt,
			config.AppConfig.LeaderAddress,
		)

		// Connect in background with retry
		go func() {
			for {
				if err := replicaClient.Connect(); err != nil {
					logger.Error("Failed to connect to leader: " + err.Error())
					logger.Info("Retrying in 5 seconds...")
					time.Sleep(time.Second * 5)
					continue
				}
				break
			}
		}()
	} else {
		// LEADER MODE: Start heartbeat for replicas
		logger.Info("───────────────────────────────────────────────────────────────")
		logger.Info("  MODE: LEADER")
		logger.Info("───────────────────────────────────────────────────────────────")

		// Start heartbeat to keep replicas informed
		wsHandler.StartHeartbeat()
	}

	// Routes
	http.HandleFunc("/ws", wsHandler.ServeWS)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Start server
	logger.Info("═══════════════════════════════════════════════════════════════")
	logger.Info("  ADVO CACHE - Multi-Tenant WebSocket Cache Service")
	logger.Info("═══════════════════════════════════════════════════════════════")
	logger.Info("")
	logger.Info("Server running on " + config.AppConfig.Port)
	logger.Info("Connect: ws://localhost" + config.AppConfig.Port + "/ws")
	logger.Info("Health:  http://localhost" + config.AppConfig.Port + "/health")
	logger.Info("")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("  HOW IT WORKS")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("Think of it like Google Sheets or a Zoom meeting room:")
	logger.Info("")
	logger.Info("  1. Connect via WebSocket to the server")
	logger.Info("  2. 'Open' a sheet by name + password (creates if new)")
	logger.Info("  3. All servers with same sheet+password share the same data")
	logger.Info("  4. Each tenant's data is completely isolated from others")
	logger.Info("")
	logger.Info("Architecture:")
	logger.Info("  • Sheets are created on first open, password-protected (bcrypt)")
	logger.Info("  • Multiple backend instances can join the same sheet")
	logger.Info("  • All cache data is isolated per-sheet (no cross-tenant access)")
	logger.Info("  • Sheets auto-delete when last collaborator disconnects")
	logger.Info("  • All operations are O(1) using sync.Map for thread safety")
	logger.Info("")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("  STEP 1: OPEN A SHEET (required first)")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("Before any cache operation, you must open a sheet:")
	logger.Info("")
	logger.Info("{\"action\": \"open\", \"sheet\": \"tenant-abc123\", \"password\": \"secret\"}")
	logger.Info("")
	logger.Info("  • sheet:    Unique identifier (typically tenant ID)")
	logger.Info("  • password: Shared secret all tenant servers know")
	logger.Info("")
	logger.Info("Behavior:")
	logger.Info("  • First opener creates the sheet and sets the password")
	logger.Info("  • Subsequent openers with same password join the sheet")
	logger.Info("  • Wrong password = connection immediately closed")
	logger.Info("  • All other actions rejected until sheet is opened")
	logger.Info("")
	logger.Info("Response: {\"success\": true, \"sheet\": \"tenant-abc123\"}")
	logger.Info("")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("  STEP 2: CACHE COMMANDS")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("After opening a sheet, use these commands:")
	logger.Info("")
	logger.Info("GET - Retrieve a value:")
	logger.Info("  Request:  {\"action\": \"get\", \"key\": \"patient:123\"}")
	logger.Info("  Response: {\"success\": true, \"key\": \"patient:123\", \"value\": {...}}")
	logger.Info("  Error:    {\"success\": false, \"error\": \"key not found\"}")
	logger.Info("")
	logger.Info("SET - Store a value (auto-expires based on config TTL):")
	logger.Info("  Request:  {\"action\": \"set\", \"key\": \"patient:123\", \"value\": {\"name\": \"John\"}}")
	logger.Info("  Response: {\"success\": true, \"key\": \"patient:123\"}")
	logger.Info("")
	logger.Info("DELETE - Remove a key (broadcasts invalidation to collaborators):")
	logger.Info("  Request:  {\"action\": \"delete\", \"key\": \"patient:123\"}")
	logger.Info("  Response: {\"success\": true, \"key\": \"patient:123\", \"deleted\": 1}")
	logger.Info("")
	logger.Info("DUMP - Get all keys/values (debug only, O(n)):")
	logger.Info("  Request:  {\"action\": \"dump\"}")
	logger.Info("  Response: {\"success\": true, \"value\": {...}, \"count\": 42}")
	logger.Info("")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("  BULK OPERATIONS")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("Delete multiple keys by pattern (broadcasts invalidation):")
	logger.Info("")
	logger.Info("DELETE_PREFIX - Delete all keys starting with prefix:")
	logger.Info("  Request:  {\"action\": \"delete_prefix\", \"prefix\": \"patient:\"}")
	logger.Info("  Response: {\"success\": true, \"deleted\": 15}")
	logger.Info("  Use case: Invalidate all patient data for a tenant")
	logger.Info("")
	logger.Info("DELETE_SUFFIX - Delete all keys ending with suffix:")
	logger.Info("  Request:  {\"action\": \"delete_suffix\", \"suffix\": \":metadata\"}")
	logger.Info("  Response: {\"success\": true, \"deleted\": 8}")
	logger.Info("  Use case: Invalidate all metadata across entities")
	logger.Info("")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("  PITFALL PREVENTION")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("Built-in protection against common caching problems:")
	logger.Info("")
	logger.Info("CACHE STAMPEDE (thundering herd):")
	logger.Info("  When cache expires, many requests hit DB simultaneously.")
	logger.Info("  Solution: Distributed locking with ownership + TTL.")
	logger.Info("")
	logger.Info("  LOCK (non-blocking, fails if held by another):")
	logger.Info("    {\"action\": \"lock\", \"key\": \"patient:123\"}")
	logger.Info("    {\"action\": \"lock\", \"key\": \"patient:123\", \"lock_ttl\": 60}")
	logger.Info("    Success: {\"success\": true, \"key\": \"patient:123\"}")
	logger.Info("    Failure: {\"success\": false, \"error\": \"lock held by another client\"}")
	logger.Info("")
	logger.Info("  UNLOCK (only owner can release):")
	logger.Info("    {\"action\": \"unlock\", \"key\": \"patient:123\"}")
	logger.Info("    Failure: {\"success\": false, \"error\": \"not lock owner\"}")
	logger.Info("")
	logger.Info("  Lock features:")
	logger.Info("    - Ownership: Only the client that acquired can release")
	logger.Info("    - TTL: Locks auto-expire after 30s (prevents deadlocks)")
	logger.Info("    - Disconnect cleanup: Client's locks released on disconnect")
	logger.Info("    - Distributed: Replicated to follower instances")
	logger.Info("")
	logger.Info("CACHE PENETRATION:")
	logger.Info("  Repeated requests for keys that don't exist in DB.")
	logger.Info("  Solution: Store null placeholder to prevent repeated DB misses.")
	logger.Info("  SET_NULL: {\"action\": \"set_null\", \"key\": \"patient:999\"}")
	logger.Info("  The value '__NULL__' is stored; your app checks for it.")
	logger.Info("")
	logger.Info("CACHE AVALANCHE:")
	logger.Info("  Mass expiration causes all keys to expire at once.")
	logger.Info("  Solution: Add jitter (random delay) to TTL to spread expirations.")
	logger.Info("  SET+JITTER: {\"action\": \"set\", \"key\": \"...\", \"value\": {...}, \"jitter\": true}")
	logger.Info("")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("  BROADCASTS (Real-time Invalidation)")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("When data is deleted, all collaborators in the SAME sheet")
	logger.Info("receive an invalidation broadcast (except the sender).")
	logger.Info("")
	logger.Info("Your backend servers should listen for these messages:")
	logger.Info("")
	logger.Info("Single key invalidation:")
	logger.Info("  {\"type\": \"invalidate\", \"key\": \"patient:123\"}")
	logger.Info("")
	logger.Info("Prefix invalidation:")
	logger.Info("  {\"type\": \"invalidate\", \"prefix\": \"patient:\"}")
	logger.Info("")
	logger.Info("Suffix invalidation:")
	logger.Info("  {\"type\": \"invalidate\", \"suffix\": \":metadata\"}")
	logger.Info("")
	logger.Info("Use case: When one server updates the DB and invalidates cache,")
	logger.Info("          other servers immediately know to clear their local state.")
	logger.Info("")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("  REPLICATION (Hot Standby)")
	logger.Info("───────────────────────────────────────────────────────────────")
	logger.Info("Another advo_cache instance can connect as a replica:")
	logger.Info("")
	logger.Info("REGISTER_REPLICA - Connect as a backup instance:")
	logger.Info("  {\"action\": \"register_replica\", \"instance_id\": \"...\", \"started_at\": \"2024-01-01T00:00:00Z\"}")
	logger.Info("")
	logger.Info("After registering:")
	logger.Info("  1. Replica receives full state dump of all sheets")
	logger.Info("  2. Replica receives all subsequent changes in real-time")
	logger.Info("  3. Replica is ready to take over if leader goes down")
	logger.Info("")
	logger.Info("Replication messages (replica receives):")
	logger.Info("  {\"type\": \"full_state\", \"sheets\": {...}}")
	logger.Info("  {\"type\": \"replicate\", \"sheet\": \"...\", \"key\": \"...\", \"value\": {...}}")
	logger.Info("  {\"type\": \"replicate_delete\", \"sheet\": \"...\", \"key\": \"...\"}")
	logger.Info("")
	logger.Info("═══════════════════════════════════════════════════════════════")
	logger.Info("  Ready for connections...")
	logger.Info("═══════════════════════════════════════════════════════════════")
	if err := http.ListenAndServe(config.AppConfig.Port, nil); err != nil {
		logger.Error("Server error: " + err.Error())
	}
}
