package config

import (
	"testing"
	"time"
)

// t.Setenv automatically restores the previous value when the test ends, and
// marks the test as unable to run in parallel - which is correct here, since
// process environment is global state.

func TestLoad_Defaults(t *testing.T) {
	// Arrange: no environment variables set
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")
	t.Setenv("GRPC_ADDR", "")
	t.Setenv("STARTUP_TIMEOUT", "")
	t.Setenv("CACHE_TTL", "")

	// Act
	cfg, err := Load()

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabaseURL != defaultDatabaseURL {
		t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, defaultDatabaseURL)
	}
	if cfg.GRPCAddr != defaultGRPCAddr {
		t.Errorf("GRPCAddr = %q, want %q", cfg.GRPCAddr, defaultGRPCAddr)
	}
	if cfg.StartupTimeout != defaultStartupTO {
		t.Errorf("StartupTimeout = %s, want %s", cfg.StartupTimeout, defaultStartupTO)
	}
	if cfg.CacheTTL != defaultCacheTTL {
		t.Errorf("CacheTTL = %s, want %s", cfg.CacheTTL, defaultCacheTTL)
	}
	if cfg.CacheEnabled() {
		t.Error("CacheEnabled() = true with no REDIS_URL, want false")
	}
	if cfg.LogWorkers != defaultLogWorkers {
		t.Errorf("LogWorkers = %d, want %d", cfg.LogWorkers, defaultLogWorkers)
	}
	if cfg.LogBatchSize != defaultLogBatchSize {
		t.Errorf("LogBatchSize = %d, want %d", cfg.LogBatchSize, defaultLogBatchSize)
	}
	if cfg.LogFlushInterval != defaultLogFlushInterval {
		t.Errorf("LogFlushInterval = %s, want %s", cfg.LogFlushInterval, defaultLogFlushInterval)
	}
	if cfg.LogBufferSize != defaultLogBufferSize {
		t.Errorf("LogBufferSize = %d, want %d", cfg.LogBufferSize, defaultLogBufferSize)
	}
}

func TestLoad_FromEnvironment(t *testing.T) {
	// Arrange
	t.Setenv("DATABASE_URL", "postgres://user:pw@db:5432/app")
	t.Setenv("REDIS_URL", "redis://cache:6379/0")
	t.Setenv("GRPC_ADDR", ":9090")
	t.Setenv("STARTUP_TIMEOUT", "30s")
	t.Setenv("CACHE_TTL", "2m")

	// Act
	cfg, err := Load()

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabaseURL != "postgres://user:pw@db:5432/app" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.GRPCAddr != ":9090" {
		t.Errorf("GRPCAddr = %q, want :9090", cfg.GRPCAddr)
	}
	if cfg.StartupTimeout != 30*time.Second {
		t.Errorf("StartupTimeout = %s, want 30s", cfg.StartupTimeout)
	}
	if cfg.CacheTTL != 2*time.Minute {
		t.Errorf("CacheTTL = %s, want 2m", cfg.CacheTTL)
	}
	if !cfg.CacheEnabled() {
		t.Error("CacheEnabled() = false with REDIS_URL set, want true")
	}
}

// Whitespace-only values must fall back to the default rather than producing
// an address like " " that fails much later at net.Listen.
func TestLoad_WhitespaceFallsBackToDefault(t *testing.T) {
	// Arrange
	t.Setenv("GRPC_ADDR", "   ")

	// Act
	cfg, err := Load()

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr != defaultGRPCAddr {
		t.Errorf("GRPCAddr = %q, want default %q", cfg.GRPCAddr, defaultGRPCAddr)
	}
}

func TestLoad_RejectsBadDuration(t *testing.T) {
	// Arrange
	t.Setenv("CACHE_TTL", "5 minutes") // not a Go duration string

	// Act
	_, err := Load()

	// Assert
	if err == nil {
		t.Fatal("expected an error for an unparseable duration, got nil")
	}
}

func TestLoad_RejectsNonPositiveDuration(t *testing.T) {
	// Arrange
	t.Setenv("STARTUP_TIMEOUT", "0s")

	// Act
	_, err := Load()

	// Assert
	if err == nil {
		t.Fatal("expected an error for a zero timeout, got nil")
	}
}
