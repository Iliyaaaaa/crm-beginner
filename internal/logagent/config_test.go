package logagent

import (
	"os"
	"testing"
)

// unsetEnv removes key for one test; t.Setenv first registers the restore.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	os.Unsetenv(key)
}

func TestLoadConfig_Defaults(t *testing.T) {
	// Arrange
	for _, k := range []string{"INGEST_ADDR", "LOG_DIR", "LOG_NAMESPACE", "SELF_CONTAINER",
		"POSITIONS_FILE", "BATCH_SIZE", "FLUSH_INTERVAL", "POLL_INTERVAL", "LOG_EXCLUDE"} {
		unsetEnv(t, k)
	}

	// Act
	cfg, err := LoadConfig()

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IngestAddr != defaultIngestAddr || cfg.Namespace != defaultNamespace || cfg.BatchSize != defaultBatchSize {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if cfg.Exclude == nil || !cfg.Exclude.MatchString("ingester worker 3: flushed 12 entries (full, attempt 1)") {
		t.Fatal("expected the default exclude pattern to drop the pipeline's own flush lines")
	}
}

// An empty LOG_NAMESPACE is a deliberate "collect everything", not "use the
// default".
func TestLoadConfig_EmptyNamespaceMeansEveryNamespace(t *testing.T) {
	// Arrange
	t.Setenv("LOG_NAMESPACE", "")

	// Act
	cfg, err := LoadConfig()

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Namespace != "" {
		t.Fatalf("expected empty namespace, got %q", cfg.Namespace)
	}
}

func TestLoadConfig_RejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"BATCH_SIZE":     "0",
		"FLUSH_INTERVAL": "soon",
		"LOG_EXCLUDE":    "([unclosed",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			// Arrange
			t.Setenv(key, value)

			// Act
			_, err := LoadConfig()

			// Assert
			if err == nil {
				t.Fatalf("expected an error for %s=%q", key, value)
			}
		})
	}
}
