# Advo Cache System - Comprehensive Documentation Report

**Generated:** January 3, 2026
**Total Lines of Code:** 2,823
**Language:** Go 1.21

---

## Executive Summary

Advo Cache is a **WebSocket-based distributed cache service** designed as a "Google Sheets meets Redis" solution. It provides real-time, multi-tenant cache management with password-protected tenant isolation, distributed locking, replication capabilities, and automatic failover. The system is built entirely in Go with zero external database dependencies.

---

## Table of Contents

1. [Directory Structure](#directory-structure)
2. [Core Architecture](#core-architecture)
3. [Component Details](#component-details)
4. [Data Flow Diagrams](#data-flow-diagrams)
5. [Configuration Parameters](#configuration-parameters)
6. [Dependencies](#dependencies)
7. [Docker Deployment](#docker-deployment)
8. [Pitfall Prevention Mechanisms](#pitfall-prevention-mechanisms)
9. [Performance Characteristics](#performance-characteristics)
10. [Security Features](#security-features)
11. [Operational Guide](#operational-guide)
12. [WebSocket Actions Reference](#websocket-actions-reference)
13. [Files Manifest](#files-manifest)

---

## Directory Structure

```
caching_system/
├── main.go                 # Entry point, server startup, mode detection
├── go.mod                  # Module definition (Go 1.21)
├── go.sum                  # Dependency checksums
├── .env                    # Configuration (git-ignored)
├── .env.example            # Configuration template
├── .gitignore              # Git ignore rules
├── Dockerfile              # Multi-stage Docker build
├── docker-compose.yml      # Production deployment config
├── README.md               # Comprehensive documentation
├── advo_cache              # Compiled binary
│
├── cache/                  # LRU cache implementation
│   └── cache.go           # LRU cache with doubly linked list + hash map, TTL, jitter
│
├── config/                 # Configuration management
│   └── config.go          # Viper-based .env loader
│
├── lock/                   # Distributed lock manager
│   └── lock.go            # Lock ownership, TTL, cleanup
│
├── logger/                 # Logging utilities
│   └── logger.go          # Colored terminal output (INFO/WARN/ERROR)
│
├── reader/                 # Embeddable Go client
│   └── reader.go          # Local sync.Map + WebSocket client
│
├── replica/                # Replication system
│   ├── replica.go         # Message types and structures
│   └── client.go          # Replica WebSocket client
│
├── sheet/                  # Multi-tenant sheets
│   ├── sheet.go           # Individual tenant cache + collaborators
│   └── manager.go         # Sheet registry with O(1) lookup
│
└── ws/                     # WebSocket handler
    └── handler.go         # Message routing, all 11 actions
```

---

## Core Architecture

### High-Level Overview

```
┌─────────────────────────────────────────────────────────────┐
│                    ADVO CACHE SYSTEM                        │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────────┐        ┌──────────────────────────┐   │
│  │  LEADER SERVER  │◄──────►│   REPLICA SERVER         │   │
│  │  (Primary)      │        │   (Hot Standby)          │   │
│  └────────┬────────┘        └──────────────────────────┘   │
│           │                                                 │
│     ┌─────┴──────────────────────────┐                     │
│     │                                │                     │
│  ┌──▼────┐  ┌────────┐  ┌────────┐  │                     │
│  │Reader1│  │Reader2 │  │Reader3 │  │                     │
│  │(App A)│  │(App B) │  │(App C) │  │                     │
│  └───────┘  └────────┘  └────────┘  │                     │
│                                      │                     │
│     ┌─────────────────────────────┐  │                     │
│     │   SHEET MANAGER             │  │                     │
│     │  (Multi-Tenant Registry)    │  │                     │
│     │                             │  │                     │
│     │  Sheet A ─► Cache A        │  │                     │
│     │  Sheet B ─► Cache B        │  │                     │
│     │  Sheet C ─► Cache C        │  │                     │
│     └─────────────────────────────┘  │                     │
│                                      │                     │
│     ┌─────────────────────────────┐  │                     │
│     │   LOCK MANAGER              │  │                     │
│     │ (Distributed Locks + TTL)   │  │                     │
│     └─────────────────────────────┘  │                     │
│                                      │                     │
└──────────────────────────────────────┘                     │
```

---

## Component Details

### 1. main.go - Entry Point & Server Initialization

**Location:** `main.go`
**Lines:** 264

**Key Functions:**
- `generateInstanceID()` - Creates unique 8-byte hex ID for the instance
- `main()` - Orchestrates startup, mode detection, and server initialization

**Responsibilities:**
1. Load configuration from `.env` file via Viper
2. Generate unique instance ID and record startup time
3. Initialize sheet manager (multi-tenant registry)
4. Initialize WebSocket handler
5. Start lock cleanup goroutine (runs every 5 seconds)
6. Detect leader/replica mode via `ADVO_ROLE` or `ADVO_LEADER_ADDRESS`
7. Start heartbeat (if leader) or replica client (if replica)
8. Listen on WebSocket port and `/health` endpoint

**Mode Detection Logic:**
- **Leader Mode**: No `ADVO_LEADER_ADDRESS` set → starts heartbeat to replicas
- **Replica Mode**: `ADVO_LEADER_ADDRESS` set → connects to leader, receives replication

---

### 2. config/config.go - Configuration Management

**Location:** `config/config.go`
**Lines:** 144

**Config Structure:**
```go
type Config struct {
    // Server
    Port        string  // Default: ":8081"
    Environment string  // Default: "development"
    LogLevel    string  // Default: "info"

    // Cache
    CacheTTLMinutes      int  // Default: 15
    JitterPercent        int  // Default: 20
    CacheCapacity        int  // Default: 10000 (max entries per sheet)
    TTLCleanupSeconds    int  // Default: 60 (background cleanup interval)

    // Lock
    LockTTLSeconds     int  // Default: 30
    LockCleanupSeconds int  // Default: 5

    // Replication
    HeartbeatIntervalSeconds int  // Default: 5
    HeartbeatTimeoutSeconds  int  // Default: 10
    LeaderAddress            string

    // Role
    Role string  // "leader" or "replica"
}
```

**Key Features:**
- Viper-based `.env` loading with environment variable overrides
- Helper methods convert config values to `time.Duration`
- `IsReplica()` - O(1) role detection via switch statement
- `GetRole()` - Backward compatibility with LeaderAddress-only detection

---

### 3. cache/cache.go - LRU Cache with Capacity Limits

**Location:** `cache/cache.go`
**Lines:** 366

**Core Structure (LRU Pattern: Doubly Linked List + Hash Map):**
```go
type Entry struct {
    Key       string
    Value     interface{}
    ExpiresAt time.Time
}

type lruNode struct {
    entry *Entry
    prev  *lruNode
    next  *lruNode
}

type Cache struct {
    mu       sync.RWMutex
    data     map[string]*lruNode  // key → node for O(1) lookup
    head     *lruNode             // sentinel: most recently used
    tail     *lruNode             // sentinel: least recently used
    length   int
    capacity int                  // max entries (default 10000)
}
```

**O(1) Operations:**

| Operation | Time | Implementation |
|-----------|------|----------------|
| `Get(key)` | O(1) | Hash map lookup + move-to-front (hot data stays cached) |
| `Set(key, value, ttl)` | O(1) | Add to front, evict LRU if at capacity, returns evicted key |
| `Delete(key)` | O(1) | Hash map delete + unlink from list |
| `evictOldest()` | O(1) | Remove tail.prev (internal, called when at capacity) |

**Internal LRU Methods (DRY Pattern):**
- `addToFront(node)` - Insert after head sentinel
- `removeNode(node)` - Unlink from list
- `moveToFront(node)` - Reposition accessed node (hot data protection)
- `evictOldest()` - Remove least recently used entry

**O(n) Bulk Operations:**
- `DeletePrefix(prefix)` - Iterate all keys, match prefix, delete
- `DeleteSuffix(suffix)` - Iterate all keys, match suffix, delete
- `Dump()` - Return all entries as map
- `Count()` - Count entries (O(1) via length field)

**Cache Pitfall Prevention:**
1. **Cache Stampede Prevention**: Uses distributed locks (in `lock` package)
2. **Cache Penetration Prevention**: `SetNull(key, ttl)` stores `"__NULL__"` placeholder
3. **Cache Avalanche Prevention**: `SetWithJitter(key, value, baseTTL, jitterPercent)` adds random jitter

**TTL Mechanism (Background Cleaner):**
- `StartTTLCleaner(interval)` - Runs goroutine with ticker
- Periodic cleanup every 60 seconds (configurable via `ADVO_TTL_CLEANUP_SECONDS`)
- Lazy expiration on `Get()` - expired entries removed when accessed
- No per-key goroutines (efficient for high volume)

**LRU Eviction:**
- When capacity reached, `Set()` calls `evictOldest()` before adding new entry
- Returns evicted key so caller can broadcast invalidation to clients
- Hot data (frequently accessed) stays at front, cold data evicted from tail

---

### 4. lock/lock.go - Distributed Lock Manager

**Location:** `lock/lock.go`
**Lines:** 230

**Lock Structure:**
```go
type Lock struct {
    Key        string    // The resource key being locked
    Owner      string    // Client connection ID (pointer as hex string)
    SheetID    string    // Which tenant sheet (for isolation)
    AcquiredAt time.Time // When acquired (UTC)
    ExpiresAt  time.Time // When lock expires (UTC)
}
```

**Key Methods:**

| Method | Purpose | Behavior |
|--------|---------|----------|
| `TryAcquire(sheetID, key, owner, ttl)` | Non-blocking acquire | Returns error if held by another owner |
| `Release(sheetID, key, owner)` | Release lock | Only owner can release |
| `IsLocked(sheetID, key)` | Check if locked | Returns boolean and lock info |
| `ReleaseAllByOwner(owner)` | Batch release | Called on client disconnect |
| `CleanupExpired()` | Remove expired locks | Called periodically (every 5s) |
| `GetAllBySheet(sheetID)` | Get locks for replication | Returns non-expired locks only |

**Ownership & TTL:**
- **Ownership**: Only the connection that acquired can unlock
- **TTL**: Locks auto-expire (default 30s) to prevent deadlocks
- **Disconnect Cleanup**: All locks for a connection released when it closes
- **Refresh**: Same owner re-acquiring updates TTL

---

### 5. logger/logger.go - Logging Utility

**Location:** `logger/logger.go`
**Lines:** 68

**Features:**
- Three-level logging: INFO, WARNING, ERROR
- ANSI color codes for terminal output:
  - Blue for INFO
  - Yellow for WARN
  - Red for ERROR
- Singleton pattern (global `logger` variable)

---

### 6. reader/reader.go - Embeddable Go Client

**Location:** `reader/reader.go`
**Lines:** 592

**Purpose:** Applications embed this package to connect to Leader cache with O(1) local reads.

**Architecture:**
```
┌──────────────────────────────────────┐
│        Application                   │
│                                      │
│  ┌────────────────────────────────┐  │
│  │  Reader Instance               │  │
│  │  ┌──────────────────────────┐  │  │
│  │  │ Local sync.Map Cache     │  │  │
│  │  │ (O(1) reads)             │  │  │
│  │  └──────────────────────────┘  │  │
│  │  ┌──────────────────────────┐  │  │
│  │  │ WebSocket Client         │  │  │
│  │  │ (Writes + Updates)       │  │  │
│  │  └──────────────────────────┘  │  │
│  └────────────────────────────────┘  │
│                 │                    │
└─────────────────┼────────────────────┘
                  │ WebSocket
        ┌─────────▼──────────┐
        │  LEADER SERVER     │
        └────────────────────┘
```

**Config:**
```go
type Config struct {
    LeaderAddr           string        // "ws://localhost:8081/ws"
    BackupAddr           string        // Optional failover
    Sheet                string        // Tenant identifier
    Password             string        // Sheet password
    MaxReconnectAttempts int           // Default: 3
    ReconnectDelay       time.Duration // Default: 1s
}
```

**Read Operations (O(1), no network):**
- `Get(key)` → Load from local sync.Map
- `GetString(key)` → Type-safe string retrieval
- `GetMap(key)` → Type-safe map retrieval
- `Has(key)` → Check existence
- `IsNull(key)` → Check for null placeholder
- `CacheSize()` → Count entries

**Write Operations (Fire-and-forget to server):**
- `Set(ctx, key, value)` - Update locally, send to server
- `Delete(ctx, key)` - Delete locally, send to server
- `DeletePrefix(ctx, prefix)` - Bulk delete by prefix
- `DeleteSuffix(ctx, suffix)` - Bulk delete by suffix

**Connection Lifecycle:**
1. `New(config)` - Connect to leader, authenticate, receive initial state
2. Listen for broadcasts (replicate/invalidate messages)
3. Auto-reconnect with exponential backoff (3 attempts)
4. Failover to backup server if leader unavailable
5. `Close()` - Disconnect and cleanup

---

### 7. sheet/sheet.go - Tenant-Isolated Cache

**Location:** `sheet/sheet.go`
**Lines:** 164

**Structure:**
```go
type Sheet struct {
    ID            string                           // Sheet identifier
    PasswordHash  []byte                           // bcrypt hash
    *cache.Cache                                   // Embedded cache
    Collaborators map[*websocket.Conn]bool        // Connected clients
    mu            sync.RWMutex                    // Protect collaborators
}
```

**Features:**
- **Embedded Cache**: Inherits all cache methods (Get, Set, Delete, etc.)
- **Multi-Tenant Isolation**: Complete data separation between sheets
- **Password Protection**: bcrypt hashing of sheet password
- **Collaborator Tracking**: Knows who's connected

**Broadcasting (Real-Time Invalidation):**
- `Broadcast(sender, msg)` - Send invalidation to all collaborators except sender
- `BroadcastSetMsg(sender, key, value)` - Send set operation to collaborators

---

### 8. sheet/manager.go - Multi-Tenant Registry

**Location:** `sheet/manager.go`
**Lines:** 101

**Structure:**
```go
type Manager struct {
    sheets sync.Map  // sheetID → *Sheet
}
```

**Key Methods:**

| Method | Purpose | Behavior |
|--------|---------|----------|
| `GetOrCreateSheet(id, password)` | Atomic get-or-create | Returns existing if password matches |
| `GetSheet(id)` | Retrieve sheet | Returns nil if not found |
| `AddSheet(id, sheet)` | Direct add | Used by replica for replication |
| `DeleteSheet(id)` | Remove sheet | Called when last collaborator leaves |
| `SheetCount()` | Count active sheets | O(n) iteration |
| `Range(fn)` | Iterate all sheets | For replication to replicas |

---

### 9. replica/replica.go - Replication Message Types

**Location:** `replica/replica.go`
**Lines:** 91

**Message Types (Leader → Replica):**

| Type | When | Fields |
|------|------|--------|
| `FullStateMsg` | On replica register | `sheets: {sheet1: {k:v}, ...}` |
| `ReplicateMsg` | On SET | `sheet, key, value, jitter` |
| `ReplicateMsg` | On DELETE | `sheet, key` (type="replicate_delete") |
| `ReplicateDeletePrefixMsg` | On prefix delete | `sheet, prefix` |
| `ReplicateDeleteSuffixMsg` | On suffix delete | `sheet, suffix` |
| `ReplicateLockMsg` | On lock acquire | `sheet, key, owner, expires_at` |
| `ReplicateUnlockMsg` | On lock release | `sheet, key` |
| `ReplicateEvictMsg` | On LRU eviction | `sheet, key` |

---

### 10. replica/client.go - Replica Connection & Replication

**Location:** `replica/client.go`
**Lines:** 345

**Client Structure:**
```go
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
```

**Key Features:**
1. **Persistent Connection**: Maintains WebSocket to leader
2. **Full State Sync**: Receives all sheets + data on connect
3. **Real-Time Replication**: Gets all subsequent changes
4. **Heartbeat Monitoring**: Detects leader failure
5. **Automatic Promotion**: Becomes leader on heartbeat timeout

**Message Handling:**

| Type | Handler | Action |
|------|---------|--------|
| `heartbeat` | Update `lastHeartbeat` | Confirms leader is alive |
| `full_state` | `applyFullState()` | Load all sheets from leader |
| `replicate` | `applySet()` | Apply SET operation to sheet |
| `replicate_delete` | `applyDelete()` | Apply DELETE operation |
| `replicate_delete_prefix` | `applyDeletePrefix()` | Apply prefix delete |
| `replicate_delete_suffix` | `applyDeleteSuffix()` | Apply suffix delete |
| `replicate_evict` | `applyEvict()` | Remove LRU-evicted key from sheet |

---

### 11. ws/handler.go - WebSocket Message Router

**Location:** `ws/handler.go`
**Lines:** 670

**Handler Structure:**
```go
type Handler struct {
    manager     *sheet.Manager
    lockManager *lock.Manager
    clients     sync.Map              // conn → *Client
    replicas    sync.Map              // conn → *replica.Replica
    instanceID  string
    startedAt   time.Time
    isLeader    bool
}
```

**Request/Response Types:**
```go
type Request struct {
    Action     string      // 11 actions
    Sheet      string      // open
    Password   string      // open
    Key        string      // get, set, delete, lock, unlock, set_null
    Value      interface{} // set
    Prefix     string      // delete_prefix
    Suffix     string      // delete_suffix
    Jitter     bool        // set (avalanche prevention)
    LockTTL    int         // lock (custom TTL)
    InstanceID string      // register_replica
    StartedAt  string      // register_replica (RFC3339)
}
```

---

## Data Flow Diagrams

### Data Flow 1: Set Operation

```
Reader A                  Leader              Reader B
   │                        │                    │
   ├─ set(user:1, data) ───►│                    │
   │                        │                    │
   │                    ┌───▼────┐               │
   │                    │ Store   │               │
   │                    │ in cache│               │
   │                    └────┬────┘               │
   │                        │                    │
   │                    ┌───▼────────────────┐   │
   │                    │ Broadcast to       │   │
   │                    │ collaborators      │   │
   │                    └───┬───────────────┬┘   │
   │                        │               │    │
   │◄───────────────────────┤───────────────├───►│
   │     response (success)  │     replicate msg │
   │                         │               │    │
   │                         │          ┌────▼───┴──┐
   │                         │          │ Store in  │
   │                         │          │ local map │
   │                         │          └───────────┘
```

### Data Flow 2: Replica Synchronization

```
Leader Server                          Replica Server
     │                                      │
     │◄─────────── register_replica ───────┤
     │                                      │
  ┌──▼──────────┐                          │
  │ Gather all  │                          │
  │ sheet data  │                          │
  └──┬──────────┘                          │
     │                                      │
     ├─────── full_state (all sheets) ────►│
     │                                      │
     │                            ┌─────────▼────────┐
     │                            │ Load all sheets  │
     │                            │ into manager     │
     │                            └──────────────────┘
     │
     │ (ongoing replication)
     │
     ├─ replicate {sheet, key, value} ──► Updates
     ├─ replicate_delete {sheet, key} ──► Deletes
     ├─ heartbeat (every 5s) ───────────► Confirms alive
```

### Data Flow 3: Reader Failover

```
Reader              Leader              Replica
  │                  │                    │
  ├─ connect ───────►│                    │
  │                  │                    │
  ├─ open sheet ────►│                    │
  │                  │                    │
  │◄─── sheet_state ─┤                    │
  │                  │                    │
  │ (Leader fails)   X                    │
  │                                       │
  │ (reconnect attempts fail)             │
  │                                       │
  ├─────────────── connect ──────────────►│
  │                                       │
  │◄─────────── sheet_state ─────────────┤
  │                                       │
  │ Now connected to promoted Replica    │
```

### Data Flow 4: LRU Eviction

```
Reader A                   Leader                    Replica
   │                          │                         │
   ├─ set(new_key, data) ────►│                         │
   │                          │                         │
   │                    ┌─────▼──────────┐              │
   │                    │ Cache at       │              │
   │                    │ capacity?      │              │
   │                    └─────┬──────────┘              │
   │                          │ YES                     │
   │                    ┌─────▼──────────┐              │
   │                    │ evictOldest()  │              │
   │                    │ (remove LRU)   │              │
   │                    └─────┬──────────┘              │
   │                          │                         │
   │                    ┌─────▼──────────────────────┐  │
   │                    │ Broadcast invalidate to    │  │
   │                    │ all collaborators          │  │
   │                    └─────┬─────────────────────┬┘  │
   │                          │                     │   │
   │◄─────────────────────────┤                     │   │
   │    {"type":"invalidate", │                     │   │
   │     "key":"evicted_key"} │                     │   │
   │                          │                     │   │
   │ Reader A removes         │                     │   │
   │ evicted_key from         │   replicate_evict ──┼──►│
   │ local sync.Map           │                     │   │
   │                          │                     │   │
   │                          │                ┌────▼───┴──┐
   │                          │                │ Replica   │
   │                          │                │ removes   │
   │                          │                │ evicted   │
   │                          │                │ key       │
   │                          │                └───────────┘
```

**Key Points:**
- LRU eviction is O(1) - removes tail.prev from doubly linked list
- All collaborators (Readers) receive `invalidate` message
- Replica servers receive `replicate_evict` message
- Hot data (frequently accessed) stays at front, never evicted

---

## Configuration Parameters

| Parameter | Default | Type | Purpose |
|-----------|---------|------|---------|
| `ADVO_PORT` | `:8081` | string | WebSocket server port |
| `ADVO_ENVIRONMENT` | `development` | string | Environment mode |
| `ADVO_ROLE` | `leader` | string | Explicit role (leader/replica) |
| `ADVO_CACHE_TTL_MINUTES` | `15` | int | **Entry lifespan** - how long entries live before expiring |
| `ADVO_JITTER_PERCENT` | `20` | int | TTL jitter for avalanche prevention (adds 0-20% random delay) |
| `ADVO_CACHE_CAPACITY` | `10000` | int | Max entries per sheet before LRU eviction |
| `ADVO_TTL_CLEANUP_SECONDS` | `60` | int | **Cleanup frequency** - how often background cleaner runs (NOT expiration time) |
| `ADVO_LOCK_TTL_SECONDS` | `30` | int | Distributed lock auto-expiration |
| `ADVO_LOCK_CLEANUP_SECONDS` | `5` | int | Lock cleanup interval |
| `ADVO_HEARTBEAT_INTERVAL_SECONDS` | `5` | int | Leader heartbeat frequency |
| `ADVO_HEARTBEAT_TIMEOUT_SECONDS` | `10` | int | Replica timeout for leader failure |
| `ADVO_LEADER_ADDRESS` | (empty) | string | Leader URL for replica mode |
| `ADVO_LOG_LEVEL` | `info` | string | Log verbosity |

---

## Dependencies

```go
require (
    github.com/gorilla/websocket v1.5.1
    github.com/spf13/viper v1.18.2
    golang.org/x/crypto v0.21.0  // For bcrypt password hashing
)
```

---

## Docker Deployment

**Dockerfile:** Multi-stage build (Alpine-based)
- Builder stage: Compiles binary with `CGO_ENABLED=0`
- Runtime stage: Minimal Alpine image with just binary

**docker-compose.yml:**
- Leader: NUC (100.117.95.87:8081)
- Replica: Digital Ocean (100.86.64.73:8081)
- Uses `host` network mode for VPN/Tailscale connectivity
- Auto-restart policy: `unless-stopped`

---

## Pitfall Prevention Mechanisms

### 1. Cache Stampede (Thundering Herd)

**Problem:** Multiple requests hit DB when cache expires.

**Solution:** Distributed locking
```
1. Request comes in, cache miss
2. Multiple readers try lock on same key
3. First reader gets lock, others get "lock held" error
4. Lock holder fetches from DB, updates cache, releases lock
5. Other readers wait briefly, then read from cache (hit)
```

### 2. Cache Penetration

**Problem:** Repeated requests for non-existent keys.

**Solution:** Null placeholder
- `set_null` stores `"__NULL__"` for non-existent keys
- Client checks `IsNull()` before hitting DB
- Prevents repeated database misses

### 3. Cache Avalanche

**Problem:** All keys expire simultaneously.

**Solution:** Jitter on TTL
- `SetWithJitter()` adds 0-20% random delay to TTL
- Spreads expirations over time
- Prevents simultaneous mass expiration

---

## Performance Characteristics

### Server Cache (LRU with doubly linked list + hash map)

| Operation | Complexity | Notes |
|-----------|------------|-------|
| Read (Get) | O(1) | Hash map lookup + move-to-front |
| Write (Set) | O(1) | Add to front + evict if at capacity |
| Delete | O(1) | Hash map delete + unlink from list |
| LRU Eviction | O(1) | Remove tail.prev, broadcast + replicate |
| Prefix delete | O(n) | Must iterate all keys |
| Suffix delete | O(n) | Must iterate all keys |
| Dump | O(n) | Full map iteration |
| Count | O(1) | Tracked via length field |

### Other Components (still use sync.Map)

| Operation | Complexity | Notes |
|-----------|------------|-------|
| Lock acquire | O(1) | sync.Map lookup |
| Sheet lookup | O(1) | sync.Map lookup (sheet/manager.go) |
| Reader local read | O(1) | sync.Map lookup (reader/reader.go) |
| Broadcast | O(c) | c = number of collaborators |

**Network Latency:**
- Writes: Network latency (request + response)
- Reads (Reader): Zero latency (local sync.Map)
- Replication: One-way fire-and-forget

---

## Security Features

1. **Multi-Tenant Isolation**
   - Each sheet completely separate
   - Wrong password closes connection
   - No cross-tenant data access possible

2. **Password Protection**
   - bcrypt hashing with default cost (10)
   - Validated on each open attempt
   - First-opener sets password

3. **Connection-Based Ownership**
   - Locks identified by connection pointer
   - Only owner can unlock
   - Locks auto-released on disconnect

---

## Operational Guide

### Starting a Leader
```bash
export ADVO_PORT=:8081
export ADVO_ROLE=leader
export ADVO_ENVIRONMENT=production
go run main.go
```

### Starting a Replica
```bash
export ADVO_PORT=:8082
export ADVO_ROLE=replica
export ADVO_LEADER_ADDRESS=ws://10.0.1.100:8081/ws
export ADVO_ENVIRONMENT=production
go run main.go
```

### Health Check
```bash
curl http://localhost:8081/health
# {"status":"ok"}
```

### Testing with wscat
```bash
npm install -g wscat
wscat -c ws://localhost:8081/ws

# Send: {"action":"open","sheet":"test","password":"secret"}
# Receive: {"success":true,"sheet":"test"}
# Then: {"type":"sheet_state","data":{}}
```

---

## WebSocket Actions Reference

| # | Action | First? | Guards | Send To | Broadcast | Replicate |
|---|--------|--------|--------|---------|-----------|-----------|
| 1 | `open` | Yes | None | Requester | - | - |
| 2 | `get` | No | Sheet | Requester | - | - |
| 3 | `set` | No | Sheet | Requester | Collaborators | Yes |
| 4 | `delete` | No | Sheet | Requester | Collaborators | Yes |
| 5 | `delete_prefix` | No | Sheet | Requester | Collaborators | Yes |
| 6 | `delete_suffix` | No | Sheet | Requester | Collaborators | Yes |
| 7 | `dump` | No | Sheet | Requester | - | - |
| 8 | `lock` | No | Sheet | Requester | - | Yes |
| 9 | `unlock` | No | Sheet | Requester | - | Yes |
| 10 | `set_null` | No | Sheet | Requester | Collaborators | Yes |
| 11 | `register_replica` | Yes | None | Replica | - | Yes (full_state) |

---

## Files Manifest

| File Path | Lines | Purpose |
|-----------|-------|---------|
| `main.go` | 264 | Entry point, mode detection, startup |
| `config/config.go` | 158 | Viper-based configuration (with LRU params) |
| `cache/cache.go` | 366 | LRU cache (doubly linked list + hash map) |
| `lock/lock.go` | 230 | Distributed lock manager |
| `logger/logger.go` | 68 | ANSI-colored logging |
| `reader/reader.go` | 592 | Embeddable Go client (local sync.Map cache) |
| `replica/replica.go` | 98 | Replication message types (incl. ReplicateEvictMsg) |
| `replica/client.go` | 362 | Replica WebSocket client (handles replicate_evict) |
| `sheet/sheet.go` | 164 | Tenant-isolated cache instance |
| `sheet/manager.go` | 101 | Multi-tenant sheet registry (sync.Map) |
| `ws/handler.go` | 695 | WebSocket message router (LRU eviction broadcast) |
| **Total** | **~3,100** | **Complete caching system** |

---

## Conclusion

Advo Cache is a well-architected distributed caching system that provides:

- **Real-time synchronization** via WebSocket connections
- **Multi-tenant isolation** with password-protected sheets
- **Automatic failover** from leader to replica
- **Built-in pitfall prevention** for stampede, penetration, and avalanche scenarios
- **O(1) read performance** through local sync.Map caching in readers
- **Zero external dependencies** - pure in-memory Go implementation

The system is production-ready with Docker deployment configurations and comprehensive logging for operational visibility.
