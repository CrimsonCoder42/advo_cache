// Package cache provides in-memory LRU caching with TTL support
// O(1) operations via doubly linked list + hash map pattern
// Thread-safe with sync.RWMutex
package cache

import (
	"math/rand"
	"sync"
	"time"

	"github.com/CrimsonCoder42/advo_cache/logger"
)

// NullPlaceholder - stored for keys that don't exist in DB
// Prevents cache penetration (repeated DB misses)
const NullPlaceholder = "__NULL__"

// Default capacity if not specified
const DefaultCapacity = 10000

// Default TTL cleanup interval (60 seconds)
const DefaultTTLCleanupInterval = 60 * time.Second

// =============================================================================
// DATA STRUCTURES - LRU Pattern (Doubly Linked List + Map)
// =============================================================================

// Entry wraps a cached value with LRU metadata
type Entry struct {
	Key       string
	Value     interface{}
	ExpiresAt time.Time
}

// lruNode is a doubly linked list node for LRU ordering
type lruNode struct {
	entry *Entry
	prev  *lruNode
	next  *lruNode
}

// Cache - thread-safe LRU cache with TTL support
// Uses doubly linked list + map for O(1) operations
type Cache struct {
	mu       sync.RWMutex
	data     map[string]*lruNode // key -> node for O(1) lookup
	head     *lruNode            // sentinel: most recently used
	tail     *lruNode            // sentinel: least recently used
	length   int                 // current number of entries
	capacity int                 // max entries (0 = unlimited)
}

// =============================================================================
// CONSTRUCTORS
// =============================================================================

// New creates a new cache instance with default capacity (with log)
func New() *Cache {
	logger.Info("Cache initialized with default capacity: " + itoa(DefaultCapacity))
	return NewWithCapacity(DefaultCapacity)
}

// NewSilent creates a new cache instance with default capacity (no log - for sheet embedding)
func NewSilent() *Cache {
	return NewWithCapacity(DefaultCapacity)
}

// NewWithCapacity creates a cache with specified max capacity
// capacity=0 means unlimited (not recommended for production)
// Automatically starts the TTL cleanup goroutine
func NewWithCapacity(capacity int) *Cache {
	c := &Cache{
		data:     make(map[string]*lruNode),
		capacity: capacity,
	}
	// Initialize sentinel nodes (head and tail never hold data)
	c.head = &lruNode{}
	c.tail = &lruNode{}
	c.head.next = c.tail
	c.tail.prev = c.head

	// Auto-start TTL cleanup goroutine
	c.StartTTLCleaner(DefaultTTLCleanupInterval)

	return c
}

// =============================================================================
// INTERNAL LRU OPERATIONS - DRY (Don't Repeat Yourself)
// =============================================================================

// addToFront inserts node right after head sentinel - O(1)
// Called after lock is held
func (c *Cache) addToFront(node *lruNode) {
	node.prev = c.head
	node.next = c.head.next
	c.head.next.prev = node
	c.head.next = node
	c.length++
}

// removeNode unlinks node from list - O(1)
// Called after lock is held
// Does NOT delete from map (caller must do that)
func (c *Cache) removeNode(node *lruNode) {
	node.prev.next = node.next
	node.next.prev = node.prev
	c.length--
}

// moveToFront moves existing node to front (most recently used) - O(1)
// Called after lock is held
func (c *Cache) moveToFront(node *lruNode) {
	// Unlink from current position (don't decrement length)
	node.prev.next = node.next
	node.next.prev = node.prev
	// Re-add at front (don't increment length)
	node.prev = c.head
	node.next = c.head.next
	c.head.next.prev = node
	c.head.next = node
}

// evictOldest removes the least recently used entry - O(1)
// Called after lock is held
// Returns evicted key (empty string if cache was empty)
func (c *Cache) evictOldest() string {
	oldest := c.tail.prev
	if oldest == c.head {
		return "" // Empty cache
	}

	key := oldest.entry.Key
	c.removeNode(oldest)
	delete(c.data, key)

	return key
}

// =============================================================================
// CORE OPERATIONS - O(1)
// =============================================================================

// Get retrieves value by key - O(1) with move-to-front
// Returns (value, true) if found and not expired
// Returns (nil, false) if not found or expired (lazy expiration)
func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	node, ok := c.data[key]
	if !ok {
		return nil, false
	}

	// Lazy TTL expiration - check if expired
	if time.Now().After(node.entry.ExpiresAt) {
		c.removeNode(node)
		delete(c.data, key)
		return nil, false
	}

	// Move to front (hot data stays cached longer)
	c.moveToFront(node)

	return node.entry.Value, true
}

// Set stores value with TTL - O(1)
// Returns evicted key if capacity was exceeded (empty string otherwise)
// Use returned key for replication to followers
func (c *Cache) Set(key string, value interface{}, ttl time.Duration) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := &Entry{
		Key:       key,
		Value:     value,
		ExpiresAt: time.Now().Add(ttl),
	}

	// Check if key already exists (update case)
	if node, ok := c.data[key]; ok {
		node.entry = entry
		c.moveToFront(node)
		return "" // No eviction on update
	}

	// Evict oldest if at capacity
	var evictedKey string
	if c.capacity > 0 && c.length >= c.capacity {
		evictedKey = c.evictOldest()
	}

	// Add new entry at front
	node := &lruNode{entry: entry}
	c.addToFront(node)
	c.data[key] = node

	return evictedKey
}

// Delete removes key - O(1)
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	node, ok := c.data[key]
	if !ok {
		return
	}

	c.removeNode(node)
	delete(c.data, key)
}

// =============================================================================
// BULK OPERATIONS - O(n) where n = total keys
// =============================================================================

// DeletePrefix removes all keys starting with prefix
func (c *Cache) DeletePrefix(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	for key, node := range c.data {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			c.removeNode(node)
			delete(c.data, key)
			count++
		}
	}
	return count
}

// DeleteSuffix removes all keys ending with suffix
func (c *Cache) DeleteSuffix(suffix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	for key, node := range c.data {
		if len(key) >= len(suffix) && key[len(key)-len(suffix):] == suffix {
			c.removeNode(node)
			delete(c.data, key)
			count++
		}
	}
	return count
}

// =============================================================================
// DEBUG / UTILITY
// =============================================================================

// Dump returns all keys and values - for debugging only
func (c *Cache) Dump() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]interface{})
	for key, node := range c.data {
		result[key] = node.entry.Value
	}
	return result
}

// Count returns number of entries - O(1)
func (c *Cache) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.length
}

// Capacity returns max capacity
func (c *Cache) Capacity() int {
	return c.capacity
}

// =============================================================================
// TTL CLEANUP - Background goroutine
// =============================================================================

// StartTTLCleaner runs periodic cleanup of expired entries
// Call once on startup, runs forever in background
func (c *Cache) StartTTLCleaner(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			c.cleanupExpired()
		}
	}()
}

// cleanupExpired removes all expired entries
func (c *Cache) cleanupExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	count := 0

	for key, node := range c.data {
		if now.After(node.entry.ExpiresAt) {
			c.removeNode(node)
			delete(c.data, key)
			count++
		}
	}

	if count > 0 {
		logger.Info("TTL cleanup: removed " + itoa(count) + " expired entries")
	}
}

// =============================================================================
// PENETRATION PREVENTION - Null placeholder
// =============================================================================

// SetNull stores placeholder for non-existent key
// Prevents repeated DB misses for keys that don't exist
func (c *Cache) SetNull(key string, ttl time.Duration) string {
	return c.Set(key, NullPlaceholder, ttl)
}

// IsNull checks if value is null placeholder
func (c *Cache) IsNull(value interface{}) bool {
	str, ok := value.(string)
	return ok && str == NullPlaceholder
}

// =============================================================================
// AVALANCHE PREVENTION - Jittered TTL
// =============================================================================

// SetWithJitter adds random jitter to TTL to prevent mass expiration
// Spreads out expirations to avoid thundering herd after cache restart
func (c *Cache) SetWithJitter(key string, value interface{}, baseTTL time.Duration, jitterPercent int) string {
	// Add 0-jitterPercent% jitter to spread out expirations
	jitterMax := int64(baseTTL) * int64(jitterPercent) / 100
	jitter := time.Duration(rand.Int63n(jitterMax + 1))
	ttl := baseTTL + jitter
	return c.Set(key, value, ttl)
}

// =============================================================================
// HELPER
// =============================================================================

// itoa - simple int to string (avoid fmt import for performance)
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
