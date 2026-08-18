// Package config loads all runtime configuration from the environment in one
// place, so no other package ever calls os.Getenv.
//
// Centralising this matters for two reasons. First, every setting the service
// accepts is discoverable by reading one struct rather than grepping for
// os.Getenv. Second, Load validates once at startup, so the process fails
// immediately on bad configuration instead of at the first request that
// happens to touch it.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultDatabaseURL = "postgres://localhost:5432/crm?sslmode=disable"
	defaultGRPCAddr    = ":50051"
	defaultStartupTO   = 10 * time.Second
	defaultCacheTTL    = 5 * time.Minute
)

// Config holds every value the service reads from its environment.
type Config struct {
	// DatabaseURL is the Postgres DSN. Required - the service cannot run
	// without a database.
	DatabaseURL string

	// RedisURL is optional. Empty means caching is disabled, which is a
	// supported mode: a cache is an optimisation, not a dependency.
	RedisURL string

	// GRPCAddr is the listen address, e.g. ":50051".
	GRPCAddr string

	// StartupTimeout bounds the initial connection attempts so a wedged
	// dependency makes the process fail fast rather than hang forever.
	StartupTimeout time.Duration

	// CacheTTL is how long a cached customer stays valid. Acts as a safety net
	// for missed invalidations: a stale entry can never outlive this window.
	CacheTTL time.Duration
}

// CacheEnabled reports whether a Redis URL was configured.
func (c Config) CacheEnabled() bool { return c.RedisURL != "" }

// Load reads configuration from the environment, applies defaults, and
// validates the result.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:    getEnv("DATABASE_URL", defaultDatabaseURL),
		RedisURL:       strings.TrimSpace(os.Getenv("REDIS_URL")),
		GRPCAddr:       getEnv("GRPC_ADDR", defaultGRPCAddr),
		StartupTimeout: defaultStartupTO,
		CacheTTL:       defaultCacheTTL,
	}

	var err error
	if cfg.StartupTimeout, err = getEnvDuration("STARTUP_TIMEOUT", defaultStartupTO); err != nil {
		return Config{}, err
	}
	if cfg.CacheTTL, err = getEnvDuration("CACHE_TTL", defaultCacheTTL); err != nil {
		return Config{}, err
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("DATABASE_URL must not be empty")
	}
	if strings.TrimSpace(c.GRPCAddr) == "" {
		return fmt.Errorf("GRPC_ADDR must not be empty")
	}
	if c.StartupTimeout <= 0 {
		return fmt.Errorf("STARTUP_TIMEOUT must be positive, got %s", c.StartupTimeout)
	}
	if c.CacheTTL <= 0 {
		return fmt.Errorf("CACHE_TTL must be positive, got %s", c.CacheTTL)
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// getEnvDuration parses a Go duration string such as "30s" or "5m".
func getEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", key, raw, err)
	}
	return d, nil
}
