// Package cache provides in-memory caching with sync.Map
// Flat KVPs like a spreadsheet - no nesting
// O(1) lookups via string key hashing
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

// Cache - sync.Map with composite string keys
type Cache struct {
	data sync.Map
	// NOTE: locks field removed - use lock.Manager for distributed locking
}

// New creates a new cache instance (with log)
func New() *Cache {
	logger.Info("Cache initialized")
	return &Cache{}
}

// NewSilent creates a new cache instance (no log - for sheet embedding)
func NewSilent() *Cache {
	return &Cache{}
}

// =============================================================================
// CORE OPERATIONS - O(1)
// =============================================================================

// Get retrieves value by key - O(1)
func (c *Cache) Get(key string) (interface{}, bool) {
	return c.data.Load(key)
}

// Set stores value with TTL - O(1) store + TTL goroutine
func (c *Cache) Set(key string, value interface{}, ttl time.Duration) {
	c.data.Store(key, value)
	go func() {
		time.Sleep(ttl)
		c.data.Delete(key)
	}()
}

// Delete removes key - O(1)
func (c *Cache) Delete(key string) {
	c.data.Delete(key)
}

// =============================================================================
// BULK OPERATIONS - O(n) where n = total keys
// =============================================================================

// DeletePrefix removes all keys starting with prefix
func (c *Cache) DeletePrefix(prefix string) int {
	count := 0
	c.data.Range(func(k, _ interface{}) bool {
		key := k.(string)
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			c.data.Delete(k)
			count++
		}
		return true
	})
	return count
}

// DeleteSuffix removes all keys ending with suffix
func (c *Cache) DeleteSuffix(suffix string) int {
	count := 0
	c.data.Range(func(k, _ interface{}) bool {
		key := k.(string)
		if len(key) >= len(suffix) && key[len(key)-len(suffix):] == suffix {
			c.data.Delete(k)
			count++
		}
		return true
	})
	return count
}

// =============================================================================
// DEBUG
// =============================================================================

// Dump returns all keys and values - for debugging only
func (c *Cache) Dump() map[string]interface{} {
	result := make(map[string]interface{})
	c.data.Range(func(k, v interface{}) bool {
		result[k.(string)] = v
		return true
	})
	return result
}

// Count returns number of entries
func (c *Cache) Count() int {
	count := 0
	c.data.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// =============================================================================
// STAMPEDE PREVENTION - Per-key locking
// =============================================================================
// NOTE: Local mutex-based locking has been replaced with distributed locking
// via the lock.Manager package. See ws/handler.go handleLock/handleUnlock.
// The lock.Manager provides:
//   - Ownership tracking (only owner can unlock)
//   - TTL/expiration (prevents deadlocks)
//   - Cleanup on disconnect
//   - Replication to followers

// =============================================================================
// PENETRATION PREVENTION - Null placeholder
// =============================================================================

// SetNull stores placeholder for non-existent key
// Prevents repeated DB misses for keys that don't exist
func (c *Cache) SetNull(key string, ttl time.Duration) {
	c.Set(key, NullPlaceholder, ttl)
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
func (c *Cache) SetWithJitter(key string, value interface{}, baseTTL time.Duration, jitterPercent int) {
	// Add 0-jitterPercent% jitter to spread out expirations
	jitterMax := int64(baseTTL) * int64(jitterPercent) / 100
	jitter := time.Duration(rand.Int63n(jitterMax + 1))
	ttl := baseTTL + jitter
	c.Set(key, value, ttl)
}
