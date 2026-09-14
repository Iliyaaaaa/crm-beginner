// Package logagent is the node-level log collector: it reads the log files the
// container runtime writes for every pod on a node, and ships the lines into
// crm-server's LogService.IngestLogs.
//
// It never talks to the pods themselves. Anything a container writes to stdout
// or stderr is already on the node's disk under /var/log/containers, so
// collecting logs needs no change to any application - a vacuum cleaner that
// sweeps the node, rather than a sidecar inside every pod.
package logagent

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultIngestAddr    = "crm-server:50051"
	defaultLogDir        = "/var/log/containers"
	defaultNamespace     = "crm"
	defaultSelfContainer = "log-agent"
	defaultPositionsFile = "/var/lib/crm-log-agent/positions.json"
	defaultBatchSize     = 500
	defaultFlushInterval = time.Second
	defaultPollInterval  = time.Second

	// defaultExclude drops the lines the log pipeline prints about ITSELF.
	// Every batch this agent ships makes crm-server log the IngestLogs stream
	// and an ingester flush. Collecting those lines would ship them, which logs
	// another stream and another flush, forever - a loop that keeps the logs
	// table growing even when nothing else is happening.
	defaultExclude = `/log\.LogService/IngestLogs |ingester worker \d+: flushed |IngestLogs: dropping one entry`
)

// Config holds every setting the agent reads from its environment.
type Config struct {
	// IngestAddr is where LogService listens, e.g. "crm-server:50051".
	IngestAddr string

	// LogDir is the directory of container log files on the node.
	LogDir string

	// Namespace limits collection to one namespace; "" collects every one.
	Namespace string

	// SelfContainer is the agent's own container name. Its lines are skipped:
	// the agent logs about shipping, and shipping those lines would loop.
	SelfContainer string

	// PositionsFile records how far each file has been shipped, so a restarted
	// agent resumes instead of shipping everything again.
	PositionsFile string

	// BatchSize and FlushInterval decide when a batch is sent: when it is full,
	// or when the interval passes - the same rule as the server's ingester.
	BatchSize     int
	FlushInterval time.Duration

	// PollInterval is how often the directory is scanned for new lines.
	PollInterval time.Duration

	// Exclude drops any message it matches; nil excludes nothing.
	Exclude *regexp.Regexp
}

// LoadConfig reads the environment, applies defaults and validates the result.
func LoadConfig() (Config, error) {
	cfg := Config{
		IngestAddr:    envString("INGEST_ADDR", defaultIngestAddr),
		LogDir:        envString("LOG_DIR", defaultLogDir),
		Namespace:     defaultNamespace,
		SelfContainer: envString("SELF_CONTAINER", defaultSelfContainer),
		PositionsFile: envString("POSITIONS_FILE", defaultPositionsFile),
	}

	// LookupEnv rather than a default-on-empty helper: LOG_NAMESPACE="" is a
	// real choice meaning "every namespace", and must not fall back to "crm".
	if v, ok := os.LookupEnv("LOG_NAMESPACE"); ok {
		cfg.Namespace = strings.TrimSpace(v)
	}

	var err error
	if cfg.BatchSize, err = envInt("BATCH_SIZE", defaultBatchSize); err != nil {
		return Config{}, err
	}
	if cfg.FlushInterval, err = envDuration("FLUSH_INTERVAL", defaultFlushInterval); err != nil {
		return Config{}, err
	}
	if cfg.PollInterval, err = envDuration("POLL_INTERVAL", defaultPollInterval); err != nil {
		return Config{}, err
	}

	exclude := defaultExclude
	if v, ok := os.LookupEnv("LOG_EXCLUDE"); ok {
		exclude = v // "" deliberately excludes nothing
	}
	if exclude != "" {
		if cfg.Exclude, err = regexp.Compile(exclude); err != nil {
			return Config{}, fmt.Errorf("LOG_EXCLUDE: %w", err)
		}
	}

	switch {
	case cfg.BatchSize <= 0:
		return Config{}, fmt.Errorf("BATCH_SIZE must be positive, got %d", cfg.BatchSize)
	case cfg.FlushInterval <= 0:
		return Config{}, fmt.Errorf("FLUSH_INTERVAL must be positive, got %s", cfg.FlushInterval)
	case cfg.PollInterval <= 0:
		return Config{}, fmt.Errorf("POLL_INTERVAL must be positive, got %s", cfg.PollInterval)
	}
	return cfg, nil
}

func envString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
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

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
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
