// Package config provides Viper-based configuration management
// Loads from .env file with environment variable override support
package config

import (
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	// Server settings
	Port        string `mapstructure:"ADVO_PORT"`
	Environment string `mapstructure:"ADVO_ENVIRONMENT"`
	LogLevel    string `mapstructure:"ADVO_LOG_LEVEL"`

	// Cache settings
	CacheTTLMinutes      int `mapstructure:"ADVO_CACHE_TTL_MINUTES"`
	JitterPercent        int `mapstructure:"ADVO_JITTER_PERCENT"`
	CacheCapacity        int `mapstructure:"ADVO_CACHE_CAPACITY"`
	TTLCleanupSeconds    int `mapstructure:"ADVO_TTL_CLEANUP_SECONDS"`

	// Lock settings
	LockTTLSeconds     int `mapstructure:"ADVO_LOCK_TTL_SECONDS"`
	LockCleanupSeconds int `mapstructure:"ADVO_LOCK_CLEANUP_SECONDS"`

	// Replication settings
	HeartbeatIntervalSeconds int    `mapstructure:"ADVO_HEARTBEAT_INTERVAL_SECONDS"`
	HeartbeatTimeoutSeconds  int    `mapstructure:"ADVO_HEARTBEAT_TIMEOUT_SECONDS"`
	LeaderAddress            string `mapstructure:"ADVO_LEADER_ADDRESS"`

	// Role setting (explicit declaration: "leader" or "replica")
	Role string `mapstructure:"ADVO_ROLE"`
}

// AppConfig is the global configuration instance
var AppConfig Config

// =============================================================================
// COMPUTED DURATIONS - Convert config values to time.Duration
// =============================================================================

// CacheTTL returns the cache TTL as time.Duration
func (c *Config) CacheTTL() time.Duration {
	return time.Duration(c.CacheTTLMinutes) * time.Minute
}

// GetCacheCapacity returns the max cache capacity per sheet
func (c *Config) GetCacheCapacity() int {
	return c.CacheCapacity
}

// TTLCleanupInterval returns the TTL cleanup interval as time.Duration
func (c *Config) TTLCleanupInterval() time.Duration {
	return time.Duration(c.TTLCleanupSeconds) * time.Second
}

// LockTTL returns the lock TTL as time.Duration
func (c *Config) LockTTL() time.Duration {
	return time.Duration(c.LockTTLSeconds) * time.Second
}

// LockCleanupInterval returns the lock cleanup interval as time.Duration
func (c *Config) LockCleanupInterval() time.Duration {
	return time.Duration(c.LockCleanupSeconds) * time.Second
}

// HeartbeatInterval returns the heartbeat interval as time.Duration
func (c *Config) HeartbeatInterval() time.Duration {
	return time.Duration(c.HeartbeatIntervalSeconds) * time.Second
}

// HeartbeatTimeout returns the heartbeat timeout as time.Duration
func (c *Config) HeartbeatTimeout() time.Duration {
	return time.Duration(c.HeartbeatTimeoutSeconds) * time.Second
}

// =============================================================================
// CONFIG LOADING
// =============================================================================

// LoadConfig loads configuration from .env file and environment variables
// Environment variables override .env file values
func LoadConfig(path string) error {
	viper.AddConfigPath(path)
	viper.SetConfigName(".env")
	viper.SetConfigType("env")

	// Set defaults (fallback if .env missing)
	viper.SetDefault("ADVO_PORT", ":8081")
	viper.SetDefault("ADVO_ENVIRONMENT", "development")
	viper.SetDefault("ADVO_LOG_LEVEL", "info")
	viper.SetDefault("ADVO_CACHE_TTL_MINUTES", 15)
	viper.SetDefault("ADVO_JITTER_PERCENT", 20)
	viper.SetDefault("ADVO_CACHE_CAPACITY", 10000)
	viper.SetDefault("ADVO_TTL_CLEANUP_SECONDS", 60)
	viper.SetDefault("ADVO_LOCK_TTL_SECONDS", 30)
	viper.SetDefault("ADVO_LOCK_CLEANUP_SECONDS", 5)
	viper.SetDefault("ADVO_HEARTBEAT_INTERVAL_SECONDS", 5)
	viper.SetDefault("ADVO_HEARTBEAT_TIMEOUT_SECONDS", 10)
	viper.SetDefault("ADVO_LEADER_ADDRESS", "")
	viper.SetDefault("ADVO_ROLE", "leader")

	// Enable environment variable override
	viper.AutomaticEnv()

	// Read .env file (non-fatal if missing - defaults will be used)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return err
		}
		// .env not found is OK - use defaults + env vars
	}

	if err := viper.Unmarshal(&AppConfig); err != nil {
		return err
	}

	return nil
}

// IsDevelopment returns true if running in development mode
func (c *Config) IsDevelopment() bool {
	return c.Environment == "development"
}

// IsProduction returns true if running in production mode
func (c *Config) IsProduction() bool {
	return c.Environment == "production"
}

// IsReplica returns true if this instance should run as a replica
// Uses switch for O(1) lookup based on Role, with LeaderAddress fallback
func (c *Config) IsReplica() bool {
	switch c.Role {
	case "replica":
		return true
	case "leader":
		return false
	default:
		// Backward compatibility: empty role uses LeaderAddress detection
		return c.LeaderAddress != ""
	}
}

// GetRole returns the role ("leader" or "replica")
func (c *Config) GetRole() string {
	switch {
	case c.Role != "":
		return c.Role
	case c.LeaderAddress != "":
		return "replica"
	default:
		return "leader"
	}
}
