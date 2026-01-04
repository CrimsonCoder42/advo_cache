# Advo Cache

A WebSocket-based distributed cache service with multi-tenant isolation. Think of it as **Google Sheets meets Redis** - real-time collaboration with password-protected tenant data.

---

## Table of Contents

1. [Features](#features)
2. [Quick Start](#quick-start)
3. [Architecture](#architecture)
4. [Production Deployment](#production-deployment)
5. [Reader Package](#reader-package)
6. [WebSocket API Reference](#websocket-api-reference)
7. [Multi-Tenant Sheets](#multi-tenant-sheets)
8. [Distributed Locking](#distributed-locking)
9. [Cache Pitfall Prevention](#cache-pitfall-prevention)
10. [Real-Time Broadcasts](#real-time-broadcasts)
11. [Replication](#replication)
12. [Configuration](#configuration)
13. [Docker Deployment](#docker-deployment)
14. [File Structure](#file-structure)

---

## Features

- **O(1) Operations** - Server uses LRU cache (doubly linked list + hash map), Reader uses sync.Map for local reads
- **LRU Eviction** - Server cache has capacity limits (default 10K entries per sheet), hot data stays cached longer
- **Multi-Tenant Isolation** - Password-protected sheets with complete data separation
- **Distributed Locking** - Ownership tracking + TTL auto-expiration
- **Cache Pitfall Prevention** - Built-in stampede, penetration, and avalanche protection
- **Real-Time Broadcasts** - Instant SET, DELETE, and LRU eviction propagation to all collaborators
- **Leader/Follower Replication** - Hot standby for high availability
- **Embeddable Reader Package** - Import directly into Go applications for O(1) local reads via sync.Map
- **Configurable** - Viper-based .env configuration with environment overrides

---

## Quick Start

```bash
# Clone the repository
git clone <repo-url>
cd advo_cache

# Configure environment
cp .env.example .env

# Run the server
go run main.go

# Connect via WebSocket
# ws://localhost:8081/ws
```

### Test with wscat

```bash
# Install wscat
npm install -g wscat

# Connect
wscat -c ws://localhost:8081/ws

# Open a sheet (required first)
{"action": "open", "sheet": "my-tenant", "password": "secret123"}

# Set a value
{"action": "set", "key": "user:1", "value": {"name": "John", "email": "john@example.com"}}

# Get the value
{"action": "get", "key": "user:1"}
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                      PRODUCTION DEPLOYMENT                                   │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│   ┌──────────────────────────────────────────────────────────────────────┐  │
│   │                         LEADER SERVER                                 │  │
│   │                      (e.g., 10.0.1.100:8081)                         │  │
│   │                                                                       │  │
│   │  ┌─────────────┐    ┌─────────────┐    ┌─────────────────────────┐   │  │
│   │  │ WS Handler  │───▶│Sheet Manager│───▶│   Multi-Tenant Sheets   │   │  │
│   │  └──────┬──────┘    └─────────────┘    │  (Isolated Caches)      │   │  │
│   │         │                               └─────────────────────────┘   │  │
│   │         │                                                             │  │
│   │         ├─── Broadcasts to Collaborators (Readers)                    │  │
│   │         │    - SET: {"type":"replicate","key":"...","value":{...}}    │  │
│   │         │    - DELETE: {"type":"invalidate","key":"..."}              │  │
│   │         │                                                             │  │
│   │         └─── Replicates to Backup (Replica Server)                    │  │
│   │                                                                       │  │
│   └──────────────────────────────────────────────────────────────────────┘  │
│                              │                                               │
│         ┌────────────────────┼────────────────────┐                         │
│         │                    │                    │                         │
│         ▼                    ▼                    ▼                         │
│   ┌───────────┐        ┌───────────┐        ┌───────────┐                  │
│   │  Reader 1 │        │  Reader 2 │        │  Reader 3 │                  │
│   │ (App A)   │        │ (App B)   │        │ (App C)   │                  │
│   │10.0.2.10  │        │10.0.2.11  │        │10.0.2.12  │                  │
│   │           │        │           │        │           │                  │
│   │ sync.Map  │        │ sync.Map  │        │ sync.Map  │                  │
│   │ (local)   │        │ (local)   │        │ (local)   │                  │
│   └───────────┘        └───────────┘        └───────────┘                  │
│         │                    │                    │                         │
│         └────────────────────┼────────────────────┘                         │
│                              │                                               │
│                    All connect to LEADER                                     │
│                    via WebSocket                                             │
│                                                                              │
│   ┌──────────────────────────────────────────────────────────────────────┐  │
│   │                       REPLICA SERVER (Hot Standby)                    │  │
│   │                      (e.g., 10.0.1.101:8082)                         │  │
│   │                                                                       │  │
│   │  Receives full state on connect, then all changes in real-time       │  │
│   │  Ready to take over if Leader goes down                              │  │
│   └──────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### Data Flow

1. **Reader calls `Set()`** → Sends to Leader via WebSocket
2. **Leader stores value** → Updates its Sheet cache
3. **Leader broadcasts to ALL collaborators** → `{"type":"replicate","key":"...","value":{...}}`
4. **All Readers receive broadcast** → Update their local sync.Map
5. **All Readers now have same data** → O(1) local reads

### Component Responsibilities

| Component | Location | Purpose |
|-----------|----------|---------|
| Leader | Standalone server | Central authority, broadcasts changes |
| Replica | Standalone server | Hot standby, receives replication stream |
| Reader | Embedded in app | Local cache + WebSocket client |

---

## Production Deployment

### Mode Detection

The server mode is determined by `ADVO_ROLE` (or falls back to `ADVO_LEADER_ADDRESS`):

| Mode | Configuration | Behavior |
|------|---------------|----------|
| **Leader** | `ADVO_ROLE=leader` | Accepts clients, sends heartbeat to replicas |
| **Replica** | `ADVO_ROLE=replica` + `ADVO_LEADER_ADDRESS` | Connects to leader, receives replication, promotes on leader failure |

### Three-Server Example

**Server 1 - Leader (10.0.1.100)**
```bash
# .env
ADVO_PORT=:8081
ADVO_ENVIRONMENT=production
ADVO_ROLE=leader
```

On startup you'll see:
```
MODE: LEADER
Heartbeat started (interval: 5s)
```

**Server 2 - Replica (10.0.1.101)**
```bash
# .env
ADVO_PORT=:8082
ADVO_ENVIRONMENT=production
ADVO_ROLE=replica
ADVO_LEADER_ADDRESS=ws://10.0.1.100:8081/ws
```

On startup you'll see:
```
MODE: REPLICA
Leader: ws://10.0.1.100:8081/ws
Connected to leader: ws://10.0.1.100:8081/ws
Applied full state from leader (X sheets)
```

**App Servers - Readers (10.0.2.x)**
```go
import "github.com/CrimsonCoder42/advo_cache/reader"

r, _ := reader.New(reader.Config{
    LeaderAddr: "ws://10.0.1.100:8081/ws",
    BackupAddr: "ws://10.0.1.101:8082/ws", // failover
    Sheet:      "tenant-abc",
    Password:   "secret",
})

// Reads are LOCAL - nanoseconds, no network
val, ok := r.Get("user:123")

// Writes forward to Leader, then broadcast back
r.Set(ctx, "user:123", map[string]any{"name": "John"})
```

### Failover Flow

```
NORMAL OPERATION:
┌─────────────────┐    heartbeat     ┌─────────────────┐
│     LEADER      │ ──────────────▶  │     REPLICA     │
│  10.0.1.100     │   replication    │   10.0.1.101    │
└────────▲────────┘                  └────────▲────────┘
         │                                    │
         │                                    │
    Readers connect to Leader                 │
    with BackupAddr = Replica                 │
                                              │
LEADER DIES:                                  │
                                              │
1. Replica detects heartbeat timeout (10s)    │
2. Replica PROMOTES TO LEADER                 │
3. Readers detect connection lost             │
4. Readers failover to BackupAddr ────────────┘
5. All Readers now connected to promoted Replica
```

---

## Reader Package

The Reader package is an **embeddable Go client** that apps import directly. It provides:

- **O(1) local reads** via sync.Map (no network call)
- **Write forwarding** to Leader server
- **Automatic sync** via Leader broadcasts
- **Failover** to backup server if Leader disconnects

### Installation

```go
import "github.com/CrimsonCoder42/advo_cache/reader"
```

### Usage

```go
package main

import (
    "context"
    "log"

    "github.com/CrimsonCoder42/advo_cache/reader"
)

func main() {
    // Connect to Leader
    r, err := reader.New(reader.Config{
        LeaderAddr: "ws://localhost:8081/ws",
        BackupAddr: "ws://localhost:8082/ws", // optional failover
        Sheet:      "tenant-123",
        Password:   "secret",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer r.Close()

    ctx := context.Background()

    // Write - forwards to Leader, updates local immediately
    r.Set(ctx, "user:1", map[string]any{
        "name":  "John",
        "email": "john@example.com",
    })

    // Read - local sync.Map, O(1), NO network call
    if val, ok := r.Get("user:1"); ok {
        log.Printf("Got: %v", val)
    }

    // Check if key exists
    if r.Has("user:1") {
        log.Println("Key exists")
    }

    // Check for null placeholder (cache penetration prevention)
    if r.IsNull("user:nonexistent") {
        log.Println("Key marked as non-existent in DB")
    }

    // Delete operations
    r.Delete(ctx, "user:1")
    r.DeletePrefix(ctx, "user:")
    r.DeleteSuffix(ctx, ":metadata")

    // Connection status
    log.Printf("Connected: %v", r.IsConnected())
    log.Printf("Server: %s", r.CurrentServer())
    log.Printf("Cache size: %d", r.CacheSize())
}
```

### Reader API

| Method | Description |
|--------|-------------|
| `New(Config)` | Connect to Leader, authenticate, load initial state |
| `Get(key)` | O(1) local read from sync.Map |
| `GetString(key)` | Get as string type |
| `GetMap(key)` | Get as map[string]interface{} type |
| `Has(key)` | Check if key exists locally |
| `IsNull(key)` | Check if key is null placeholder |
| `Set(ctx, key, value)` | Forward write to Leader |
| `Delete(ctx, key)` | Forward delete to Leader |
| `DeletePrefix(ctx, prefix)` | Forward prefix delete to Leader |
| `DeleteSuffix(ctx, suffix)` | Forward suffix delete to Leader |
| `Close()` | Disconnect and cleanup |
| `IsConnected()` | Check connection status |
| `CurrentServer()` | Get current server address |
| `CacheSize()` | Count of entries in local cache |

### Config Options

```go
type Config struct {
    LeaderAddr           string        // Required: "ws://leader:8081/ws"
    BackupAddr           string        // Optional: failover target
    Sheet                string        // Required: tenant identifier
    Password             string        // Required: sheet password
    MaxReconnectAttempts int           // Default: 3
    ReconnectDelay       time.Duration // Default: 1s
}
```

### How Reader Stays Synced

1. On connect: Reader sends `{"action":"open","sheet":"...","password":"..."}`
2. Leader responds with `{"type":"sheet_state","data":{...}}` (full dump)
3. Reader populates local sync.Map
4. Leader broadcasts changes:
   - SET: `{"type":"replicate","key":"...","value":{...}}`
   - DELETE: `{"type":"invalidate","key":"..."}`
5. Reader updates local cache on each broadcast
6. All Readers stay synchronized automatically

---

## WebSocket API Reference

All messages are JSON. Connect to `ws://localhost:8081/ws`.

### open (Required First)

Authenticate and join a tenant sheet.

```json
// Request
{"action": "open", "sheet": "tenant-abc", "password": "secret"}

// Response (success)
{"success": true, "sheet": "tenant-abc"}

// Then receives current state
{"type": "sheet_state", "data": {"key1": "val1", "key2": {...}}}

// Error (wrong password closes connection)
{"success": false, "error": "invalid password"}
```

### get

Retrieve a cached value.

```json
// Request
{"action": "get", "key": "user:123"}

// Response
{"success": true, "key": "user:123", "value": {"name": "John"}}

// Not found
{"success": false, "error": "key not found"}
```

### set

Store a value (auto-expires based on TTL config).

```json
// Request
{"action": "set", "key": "user:123", "value": {"name": "John"}}

// With jitter (prevents avalanche)
{"action": "set", "key": "user:123", "value": {"name": "John"}, "jitter": true}

// Response
{"success": true, "key": "user:123"}
```

**Broadcast:** All other collaborators receive:
```json
{"type": "replicate", "key": "user:123", "value": {"name": "John"}}
```

### delete

Remove a key (broadcasts invalidation to collaborators).

```json
// Request
{"action": "delete", "key": "user:123"}

// Response
{"success": true, "key": "user:123", "deleted": 1}
```

**Broadcast:** All other collaborators receive:
```json
{"type": "invalidate", "key": "user:123"}
```

### delete_prefix

Bulk delete all keys starting with prefix.

```json
// Request
{"action": "delete_prefix", "prefix": "user:"}

// Response
{"success": true, "deleted": 15}
```

**Broadcast:**
```json
{"type": "invalidate", "prefix": "user:"}
```

### delete_suffix

Bulk delete all keys ending with suffix.

```json
// Request
{"action": "delete_suffix", "suffix": ":metadata"}

// Response
{"success": true, "deleted": 8}
```

**Broadcast:**
```json
{"type": "invalidate", "suffix": ":metadata"}
```

### dump

Debug: Get all cached data (O(n), use sparingly).

```json
// Request
{"action": "dump"}

// Response
{"success": true, "value": {"key1": "val1", "key2": "val2"}, "count": 2}
```

### lock

Acquire a distributed lock (non-blocking).

```json
// Request
{"action": "lock", "key": "user:123"}

// With custom TTL
{"action": "lock", "key": "user:123", "lock_ttl": 60}

// Success
{"success": true, "key": "user:123"}

// Already held
{"success": false, "error": "lock held by another client", "key": "user:123"}
```

### unlock

Release a lock (only owner can unlock).

```json
// Request
{"action": "unlock", "key": "user:123"}

// Success
{"success": true, "key": "user:123"}

// Not owner
{"success": false, "error": "not lock owner"}
```

### set_null

Store null placeholder (prevents cache penetration).

```json
// Request
{"action": "set_null", "key": "user:nonexistent"}

// Response
{"success": true, "key": "user:nonexistent"}
```

### register_replica

Register as a follower instance (for replication).

```json
// Request
{
  "action": "register_replica",
  "instance_id": "replica-001",
  "started_at": "2024-01-01T00:00:00Z"
}

// Response
{"success": true}

// Then receives full state
{"type": "full_state", "sheets": {"sheet1": {...}, "sheet2": {...}}}
```

---

## Multi-Tenant Sheets

Sheets provide complete data isolation between tenants.

### How It Works

1. **First opener creates the sheet** - Password is bcrypt hashed
2. **Subsequent openers must know password** - Wrong password = connection closed
3. **All data is scoped to the sheet** - No cross-tenant access possible
4. **Sheet auto-deletes** - When last collaborator disconnects

### Example

```
Tenant A opens "tenant-a" with password "secretA"
Tenant B opens "tenant-b" with password "secretB"

Tenant A sets {"action": "set", "key": "data", "value": "A's data"}
Tenant B sets {"action": "set", "key": "data", "value": "B's data"}

Each tenant only sees their own "data" key - complete isolation.
```

---

## Distributed Locking

Prevents cache stampede (thundering herd) when cache expires.

### Features

- **Ownership** - Only the lock owner can unlock
- **TTL** - Locks auto-expire (default 30s) to prevent deadlocks
- **Non-blocking** - TryAcquire fails immediately if held
- **Disconnect cleanup** - Client's locks released when connection closes
- **Replication** - Locks replicated to follower instances

### Usage Pattern

```
1. Try to acquire lock
2. If success: fetch from DB, populate cache, unlock
3. If failure: wait briefly, retry get from cache
```

```json
// Acquire lock
{"action": "lock", "key": "expensive:query:123"}

// ... fetch from database, set in cache ...

// Release lock
{"action": "unlock", "key": "expensive:query:123"}
```

---

## Cache Pitfall Prevention

### Cache Stampede (Thundering Herd)

**Problem:** When cache expires, many requests hit DB simultaneously.

**Solution:** Distributed locking - only one client fetches, others wait.

### Cache Penetration

**Problem:** Repeated requests for keys that don't exist in DB.

**Solution:** Store `__NULL__` placeholder to stop repeated DB misses.

```json
// DB lookup returns nothing? Store null placeholder
{"action": "set_null", "key": "user:nonexistent"}

// On GET, check if value is "__NULL__" and return 404
```

### Cache Avalanche

**Problem:** Mass expiration causes all keys to expire at once.

**Solution:** Add jitter (random delay) to TTL to spread expirations.

```json
// 20% jitter: 15 min TTL becomes 15-18 min
{"action": "set", "key": "data", "value": {...}, "jitter": true}
```

---

## Real-Time Broadcasts

When data changes, all collaborators in the same sheet receive updates instantly.

### SET Broadcasts

When any client calls SET, all other collaborators receive:

```json
{"type": "replicate", "key": "user:123", "value": {"name": "John"}}
```

### DELETE Broadcasts (Invalidation)

```json
// Single key deleted
{"type": "invalidate", "key": "user:123"}

// Prefix deleted
{"type": "invalidate", "prefix": "user:"}

// Suffix deleted
{"type": "invalidate", "suffix": ":metadata"}
```

### LRU Eviction Broadcasts

When the server cache reaches capacity (default 10,000 entries per sheet), the least recently used entry is evicted. All collaborators receive an invalidation:

```json
// LRU eviction (capacity exceeded, oldest entry removed)
{"type": "invalidate", "key": "evicted-key"}
```

This ensures Reader clients remove stale keys from their local sync.Map cache.

**Note:** Sender does not receive their own broadcast.

### Use Case

```
Reader A updates database and calls Set("user:123", data)
Leader broadcasts {"type":"replicate",...} to all collaborators
Reader B and Reader C receive broadcast immediately
All Readers now have identical cache state
```

---

## Replication

Leader/Follower model for high availability.

### Setup

**Leader** (default):
```env
ADVO_PORT=:8081
# No ADVO_LEADER_ADDRESS
```

**Replica**:
```env
ADVO_PORT=:8082
ADVO_LEADER_ADDRESS=ws://leader-host:8081/ws
```

### Replication Messages

When replica connects:
```json
{"type": "full_state", "sheets": {"sheet1": {...}, "sheet2": {...}}}
```

Ongoing replication:
```json
{"type": "replicate", "sheet": "tenant-a", "key": "user:1", "value": {...}}
{"type": "replicate_delete", "sheet": "tenant-a", "key": "user:1"}
{"type": "replicate_delete_prefix", "sheet": "tenant-a", "prefix": "user:"}
{"type": "replicate_delete_suffix", "sheet": "tenant-a", "suffix": ":meta"}
{"type": "replicate_lock", "sheet": "tenant-a", "key": "user:1", "owner": "..."}
{"type": "replicate_unlock", "sheet": "tenant-a", "key": "user:1"}
```

---

## Configuration

All configuration is via environment variables or `.env` file.

| Variable | Default | Description |
|----------|---------|-------------|
| `ADVO_PORT` | `:8081` | WebSocket server port |
| `ADVO_ENVIRONMENT` | `development` | Environment (development/staging/production) |
| `ADVO_ROLE` | `leader` | Explicit role: `leader` or `replica` |
| `ADVO_CACHE_TTL_MINUTES` | `15` | **Entry lifespan** - how long entries live before expiring |
| `ADVO_JITTER_PERCENT` | `20` | TTL jitter for avalanche prevention (adds 0-20% random delay) |
| `ADVO_CACHE_CAPACITY` | `10000` | Max entries per sheet before LRU eviction |
| `ADVO_TTL_CLEANUP_SECONDS` | `60` | **Cleanup frequency** - how often background cleaner checks for expired entries (NOT the expiration time) |
| `ADVO_LOCK_TTL_SECONDS` | `30` | Distributed lock auto-expiration |
| `ADVO_LOCK_CLEANUP_SECONDS` | `5` | Lock cleanup interval |
| `ADVO_HEARTBEAT_INTERVAL_SECONDS` | `5` | Leader heartbeat frequency |
| `ADVO_HEARTBEAT_TIMEOUT_SECONDS` | `10` | Replica timeout for leader |
| `ADVO_LEADER_ADDRESS` | (empty) | Set to leader URL for replica mode |
| `ADVO_LOG_LEVEL` | `info` | Logging verbosity |

### Important: TTL vs Cleanup Interval

**These are NOT the same thing:**

| Setting | What It Controls | Example |
|---------|------------------|---------|
| `ADVO_CACHE_TTL_MINUTES=15` | Entry lifespan | Entry added at 10:00 expires at 10:15 |
| `ADVO_TTL_CLEANUP_SECONDS=60` | How often we check | Cleaner runs every 60s to remove expired entries |

The cleanup interval (60s) is just the frequency of the background garbage collector. Entries still live for the full TTL (15 minutes by default). Expired entries are also removed immediately on access (lazy expiration).

### Example .env

```env
ADVO_PORT=:8081
ADVO_ENVIRONMENT=production
ADVO_CACHE_TTL_MINUTES=30
ADVO_CACHE_CAPACITY=50000
ADVO_LOCK_TTL_SECONDS=60
```

---

## Docker Deployment

### Build

```bash
docker build -t advo-cache .
```

### Run Single Instance

```bash
docker run -p 8081:8081 advo-cache
```

### Run Leader + Replica (Local)

```bash
# Terminal 1 - Leader on :8081
ADVO_PORT=:8081 ADVO_ROLE=leader go run main.go

# Terminal 2 - Replica on :8082
ADVO_PORT=:8082 ADVO_ROLE=replica ADVO_LEADER_ADDRESS=ws://localhost:8081/ws go run main.go
```

### Production Deployment (Tailscale/VPN)

The docker-compose.yml is configured for production deployment across a VPN network:

```yaml
# Deploy to NUC (100.117.95.87):
docker-compose up advo-cache-leader -d

# Deploy to Digital Ocean (100.86.64.73):
docker-compose up advo-cache-replica -d
```

### docker-compose.yml

```yaml
services:
  # Leader Instance - Deploy to NUC (100.117.95.87)
  advo-cache-leader:
    image: nostro37/advo-cache:latest
    container_name: advo-cache-leader
    network_mode: "host"
    environment:
      - ADVO_PORT=:8081
      - ADVO_ENVIRONMENT=production
      - ADVO_ROLE=leader
      - ADVO_CACHE_TTL_MINUTES=15
      - ADVO_HEARTBEAT_INTERVAL_SECONDS=5
      - ADVO_HEARTBEAT_TIMEOUT_SECONDS=10
    restart: unless-stopped

  # Replica Instance - Deploy to Digital Ocean (100.86.64.73)
  advo-cache-replica:
    image: nostro37/advo-cache:latest
    container_name: advo-cache-replica
    network_mode: "host"
    environment:
      - ADVO_PORT=:8081
      - ADVO_ENVIRONMENT=production
      - ADVO_ROLE=replica
      - ADVO_LEADER_ADDRESS=ws://100.117.95.87:8081/ws
      - ADVO_HEARTBEAT_INTERVAL_SECONDS=5
      - ADVO_HEARTBEAT_TIMEOUT_SECONDS=10
    restart: unless-stopped
```

**Key Points:**
- Uses `network_mode: "host"` for direct VPN/Tailscale access
- Uses `image:` (pulls from Docker Hub) instead of `build:`
- `ADVO_ROLE` explicitly declares leader or replica mode

---

## File Structure

```
advo_cache/
├── main.go                 # Entry point, HTTP server, startup
├── go.mod                  # Go module definition
├── go.sum                  # Dependency checksums
├── .env                    # Configuration (git-ignored)
├── .env.example            # Configuration template
├── .gitignore              # Git ignore rules
├── Dockerfile              # Container build
├── docker-compose.yml      # Multi-instance orchestration
├── README.md               # This file
│
├── cache/
│   └── cache.go            # LRU cache (doubly linked list + hash map, capacity limits, TTL, jitter)
│
├── config/
│   └── config.go           # Viper configuration loader
│
├── lock/
│   └── lock.go             # Distributed lock manager (ownership, TTL)
│
├── logger/
│   └── logger.go           # Colored, leveled logging
│
├── reader/
│   └── reader.go           # Embeddable Go client package
│
├── replica/
│   ├── client.go           # Replica WebSocket client (connects to leader)
│   └── replica.go          # Replication message types
│
├── sheet/
│   ├── manager.go          # Sheet registry (create/get/delete)
│   └── sheet.go            # Tenant sheet (cache + collaborators + broadcast)
│
└── ws/
    └── handler.go          # WebSocket handler (all 11 actions)
```

### Component Details

| Component | Purpose |
|-----------|---------|
| `main.go` | Server startup, routes, lock cleanup goroutine |
| `cache/cache.go` | LRU cache with O(1) operations, capacity limits, TTL cleanup, jitter, null placeholders |
| `config/config.go` | Load .env via Viper, provide AppConfig struct |
| `lock/lock.go` | Distributed locks with ownership, TTL, disconnect cleanup |
| `logger/logger.go` | ANSI-colored terminal output (INFO/WARN/ERROR) |
| `reader/reader.go` | Embeddable client: local sync.Map + WebSocket client |
| `replica/client.go` | Replica client: connects to leader, applies replication, monitors heartbeat |
| `replica/replica.go` | Replication message struct definitions |
| `sheet/manager.go` | Multi-tenant sheet registry, O(1) lookup by ID |
| `sheet/sheet.go` | Single tenant: embedded cache + collaborator list + broadcast methods |
| `ws/handler.go` | WebSocket upgrade, message routing, all 11 action handlers, replication |

---

## Message Types Summary

### Client → Server (Actions)

| Action | Description |
|--------|-------------|
| `open` | Authenticate and join a sheet |
| `get` | Retrieve a cached value |
| `set` | Store a value with TTL |
| `delete` | Remove a key |
| `delete_prefix` | Remove all keys with prefix |
| `delete_suffix` | Remove all keys with suffix |
| `dump` | Get all data (debug) |
| `lock` | Acquire distributed lock |
| `unlock` | Release distributed lock |
| `set_null` | Store null placeholder |
| `register_replica` | Register as backup server |

### Server → Client (Broadcasts)

| Type | When | Purpose |
|------|------|---------|
| `sheet_state` | On open | Full current state |
| `replicate` | On any SET | Sync new/updated value |
| `invalidate` | On DELETE or LRU eviction | Clear key from local cache |

### Leader → Replica (Replication)

| Type | Purpose |
|------|---------|
| `heartbeat` | Keep-alive signal (every 5s) |
| `full_state` | Initial sync of all sheets |
| `replicate` | New/updated value |
| `replicate_delete` | Single key deleted |
| `replicate_delete_prefix` | Prefix deleted |
| `replicate_delete_suffix` | Suffix deleted |
| `replicate_lock` | Lock acquired |
| `replicate_unlock` | Lock released |
| `replicate_evict` | LRU eviction (capacity exceeded) |

---

## License

MIT
