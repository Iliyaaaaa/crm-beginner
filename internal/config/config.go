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
	"strconv"
	"strings"
	"time"
)

const (
	defaultDatabaseURL = "postgres://localhost:5432/crm?sslmode=disable"
	defaultGRPCAddr    = ":50051"
	defaultStartupTO   = 10 * time.Second
	defaultCacheTTL    = 5 * time.Minute

	// defaultDBMaxConns caps the shared Postgres pool. Left at pgx's own
	// default (max(4, NumCPU)) this becomes the write-throughput ceiling under
	// load. 80 was measured under load testing to give the best throughput
	// without hitting Postgres's own max_connections=100 ceiling - back then
	// as two separate pools of 40 each (2x40=80 actual connections); now that
	// both repositories share ONE pool (see postgres.NewPool), that same
	// budget of 80 has to live on this single value, leaving 20 connections
	// of headroom under Postgres's ceiling for psql, migrations, etc.
	defaultDBMaxConns = 80

	// Log ingestion pipeline defaults - see internal/ingest.Config for what
	// each one actually controls.
	defaultLogWorkers       = 4
	defaultLogBatchSize     = 500
	defaultLogFlushInterval = 2 * time.Second
	defaultLogBufferSize    = 10000
)

// Config holds every value the service reads from its environment.
type Config struct {
	// DatabaseURL is the Postgres DSN. Required - the service cannot run
	// without a database.
	DatabaseURL string

	// DBMaxConns caps the shared Postgres connection pool (see
	// postgres.NewPool). Both CustomerRepository and LogRepository draw from
	// this one pool, so this is the total connection budget for the whole
	// service, not a per-repository limit.
	DBMaxConns int

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

	// LogWorkers, LogBatchSize, LogFlushInterval and LogBufferSize configure
	// the log ingestion pipeline (internal/ingest). See that package's
	// Config type for what each one controls.
	LogWorkers       int
	LogBatchSize     int
	LogFlushInterval time.Duration
	LogBufferSize    int
}

// CacheEnabled reports whether a Redis URL was configured.
func (c Config) CacheEnabled() bool { return c.RedisURL != "" }

// Load reads configuration from the environment, applies defaults, and
// validates the result.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:      getEnv("DATABASE_URL", defaultDatabaseURL),
		DBMaxConns:       defaultDBMaxConns,
		RedisURL:         strings.TrimSpace(os.Getenv("REDIS_URL")),
		GRPCAddr:         getEnv("GRPC_ADDR", defaultGRPCAddr),
		StartupTimeout:   defaultStartupTO,
		CacheTTL:         defaultCacheTTL,
		LogWorkers:       defaultLogWorkers,
		LogBatchSize:     defaultLogBatchSize,
		LogFlushInterval: defaultLogFlushInterval,
		LogBufferSize:    defaultLogBufferSize,
	}

	var err error
	if cfg.DBMaxConns, err = getEnvInt("DB_MAX_CONNS", defaultDBMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.StartupTimeout, err = getEnvDuration("STARTUP_TIMEOUT", defaultStartupTO); err != nil {
		return Config{}, err
	}
	if cfg.CacheTTL, err = getEnvDuration("CACHE_TTL", defaultCacheTTL); err != nil {
		return Config{}, err
	}
	if cfg.LogWorkers, err = getEnvInt("LOG_WORKERS", defaultLogWorkers); err != nil {
		return Config{}, err
	}
	if cfg.LogBatchSize, err = getEnvInt("LOG_BATCH_SIZE", defaultLogBatchSize); err != nil {
		return Config{}, err
	}
	if cfg.LogFlushInterval, err = getEnvDuration("LOG_FLUSH_INTERVAL", defaultLogFlushInterval); err != nil {
		return Config{}, err
	}
	if cfg.LogBufferSize, err = getEnvInt("LOG_BUFFER_SIZE", defaultLogBufferSize); err != nil {
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
	if c.DBMaxConns <= 0 {
		return fmt.Errorf("DB_MAX_CONNS must be positive, got %d", c.DBMaxConns)
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
	if c.LogWorkers <= 0 {
		return fmt.Errorf("LOG_WORKERS must be positive, got %d", c.LogWorkers)
	}
	if c.LogBatchSize <= 0 {
		return fmt.Errorf("LOG_BATCH_SIZE must be positive, got %d", c.LogBatchSize)
	}
	if c.LogFlushInterval <= 0 {
		return fmt.Errorf("LOG_FLUSH_INTERVAL must be positive, got %s", c.LogFlushInterval)
	}
	if c.LogBufferSize <= 0 {
		return fmt.Errorf("LOG_BUFFER_SIZE must be positive, got %d", c.LogBufferSize)
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

// getEnvInt parses a plain integer, e.g. "500".
func getEnvInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q: %w", key, raw, err)
	}
	return n, nil
}
