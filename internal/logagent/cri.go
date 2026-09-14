package logagent

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Line is one physical line of a container log file, in the CRI format the
// container runtime writes:
//
//	2026-09-14T05:38:53.123456789Z stderr F connected to postgres (max_conns=20)
//	└──────────── Time ──────────┘ └Stream┘ │ └──────────── Message ───────────┘
//	                                        Tag: F = full line, P = partial
//
// The runtime splits one long line into several P lines followed by an F, so a
// message is only complete once its F line has arrived.
type Line struct {
	Time    string
	Stream  string
	Partial bool
	Message string
}

// ParseLine splits one CRI log line into its parts.
func ParseLine(raw string) (Line, error) {
	parts := strings.SplitN(raw, " ", 4)
	if len(parts) < 3 {
		return Line{}, fmt.Errorf("not a CRI log line: %q", raw)
	}
	if _, err := time.Parse(time.RFC3339Nano, parts[0]); err != nil {
		return Line{}, fmt.Errorf("bad timestamp %q: %w", parts[0], err)
	}

	l := Line{Time: parts[0], Stream: parts[1]}
	if l.Stream != "stdout" && l.Stream != "stderr" {
		return Line{}, fmt.Errorf("unknown stream %q", l.Stream)
	}
	switch parts[2] {
	case "F":
	case "P":
		l.Partial = true
	default:
		return Line{}, fmt.Errorf("unknown tag %q", parts[2])
	}
	if len(parts) == 4 {
		l.Message = parts[3]
	}
	return l, nil
}

// Source says which container a log file belongs to.
type Source struct {
	Pod       string
	Namespace string
	Container string
}

// ParseFileName reads a Source out of a file name in /var/log/containers. The
// kubelet names them <pod>_<namespace>_<container>-<64-hex-id>.log, and none
// of pod, namespace or container names may contain "_", which is what makes
// the split unambiguous.
func ParseFileName(path string) (Source, bool) {
	name := filepath.Base(path)
	base, ok := strings.CutSuffix(name, ".log")
	if !ok {
		return Source{}, false
	}
	parts := strings.Split(base, "_")
	if len(parts) != 3 {
		return Source{}, false
	}
	dash := strings.LastIndex(parts[2], "-")
	if dash <= 0 || len(parts[2])-dash-1 != 64 {
		return Source{}, false
	}
	return Source{Pod: parts[0], Namespace: parts[1], Container: parts[2][:dash]}, true
}

// GuessLevel picks a level from the message text. None of the services here
// write a structured level - Go's log package, Redis and Postgres all print
// plain text, and Go's logger writes everything to stderr, so the stream says
// nothing either. This is a best-effort keyword match, "info" otherwise.
func GuessLevel(message string) string {
	m := strings.ToLower(message)
	switch {
	case strings.Contains(m, "panic"), strings.Contains(m, "fatal"), strings.Contains(m, "error"):
		return "error"
	case strings.Contains(m, "warn"):
		return "warn"
	default:
		return "info"
	}
}
