package logagent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// testID stands in for the 64-hex container id in log file names.
const testID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func logPath(dir, pod, namespace, container string) string {
	return filepath.Join(dir, pod+"_"+namespace+"_"+container+"-"+testID+".log")
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func testConfig(dir string) Config {
	return Config{
		LogDir:        dir,
		Namespace:     "crm",
		SelfContainer: "log-agent",
		PollInterval:  time.Second,
		Exclude:       regexp.MustCompile(defaultExclude),
	}
}

// poll runs one pass and returns everything it sent.
func poll(t *testing.T, tl *Tailer) []Entry {
	t.Helper()
	out := make(chan Entry, 100)
	if err := tl.Poll(context.Background(), out); err != nil {
		t.Fatalf("poll: %v", err)
	}
	close(out)
	var got []Entry
	for e := range out {
		got = append(got, e)
	}
	return got
}

func TestTailer_ReadsCompleteLines(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := logPath(dir, "crm-server-abc", "crm", "crm-server")
	appendTo(t, path, "2026-09-14T05:38:53.1Z stderr F connected to postgres\n"+
		"2026-09-14T05:38:54.2Z stderr F ERROR: something broke\n")

	// Act
	got := poll(t, NewTailer(testConfig(dir), newPositions(t)))

	// Assert
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	first := got[0].Log
	if first.Message != "connected to postgres" || first.Service != "crm-server" ||
		first.Timestamp != "2026-09-14T05:38:53.1Z" || first.Level != "info" {
		t.Fatalf("first entry wrong: %+v", first)
	}
	if got[1].Log.Level != "error" {
		t.Fatalf("expected the ERROR line to be level error, got %q", got[1].Log.Level)
	}
	info, _ := os.Stat(path)
	if got[1].Offset != info.Size() {
		t.Fatalf("last entry offset = %d, want the file size %d", got[1].Offset, info.Size())
	}
}

// The runtime splits long lines into P chunks ending in an F chunk.
func TestTailer_JoinsPartialLines(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := logPath(dir, "p", "crm", "app")
	appendTo(t, path, "2026-09-14T05:38:53Z stdout P hel\n"+
		"2026-09-14T05:38:53Z stdout P lo \n"+
		"2026-09-14T05:38:53Z stdout F world\n")

	// Act
	got := poll(t, NewTailer(testConfig(dir), newPositions(t)))

	// Assert
	if len(got) != 1 || got[0].Log.Message != "hello world" {
		t.Fatalf("expected one joined entry %q, got %+v", "hello world", got)
	}
}

// A line without its newline is still being written and must wait.
func TestTailer_WaitsForAHalfWrittenLine(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := logPath(dir, "p", "crm", "app")
	tl := NewTailer(testConfig(dir), newPositions(t))
	appendTo(t, path, "2026-09-14T05:38:53Z stdout F still writ")

	// Act
	before := poll(t, tl)
	appendTo(t, path, "ing\n")
	after := poll(t, tl)

	// Assert
	if len(before) != 0 {
		t.Fatalf("expected nothing before the newline, got %+v", before)
	}
	if len(after) != 1 || after[0].Log.Message != "still writing" {
		t.Fatalf("expected the completed line once, got %+v", after)
	}
}

func TestTailer_NextPollReadsOnlyNewLines(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := logPath(dir, "p", "crm", "app")
	tl := NewTailer(testConfig(dir), newPositions(t))
	appendTo(t, path, "2026-09-14T05:38:53Z stdout F one\n")
	poll(t, tl)

	// Act
	appendTo(t, path, "2026-09-14T05:38:54Z stdout F two\n")
	got := poll(t, tl)

	// Assert
	if len(got) != 1 || got[0].Log.Message != "two" {
		t.Fatalf("expected only the new line, got %+v", got)
	}
}

// A file shorter than what was already read has been truncated or replaced.
func TestTailer_StartsOverWhenTheFileShrinks(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := logPath(dir, "p", "crm", "app")
	tl := NewTailer(testConfig(dir), newPositions(t))
	appendTo(t, path, "2026-09-14T05:38:53Z stdout F a fairly long first line\n"+
		"2026-09-14T05:38:54Z stdout F and a fairly long second line\n")
	poll(t, tl)

	// Act
	if err := os.WriteFile(path, []byte("2026-09-14T05:39:00Z stdout F new\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got := poll(t, tl)

	// Assert
	if len(got) != 1 || got[0].Log.Message != "new" {
		t.Fatalf("expected the new file's only line, got %+v", got)
	}
}

// A restarted agent must not ship what it already shipped.
func TestTailer_ResumesFromASavedPosition(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := logPath(dir, "p", "crm", "app")
	first := "2026-09-14T05:38:53Z stdout F already shipped\n"
	appendTo(t, path, first+"2026-09-14T05:38:54Z stdout F not yet\n")
	info, _ := os.Stat(path)
	positions := newPositions(t)
	positions.Set(path, Position{Inode: inodeOf(info), Offset: int64(len(first))})

	// Act
	got := poll(t, NewTailer(testConfig(dir), positions))

	// Assert
	if len(got) != 1 || got[0].Log.Message != "not yet" {
		t.Fatalf("expected to resume after the saved position, got %+v", got)
	}
}

// The loop guard: no own lines, no pipeline self-talk, no other namespaces.
func TestTailer_SkipsWhatMustNotBeShipped(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	appendTo(t, logPath(dir, "crm-server-a", "crm", "crm-server"),
		"2026-09-14T05:38:53Z stderr F ingester worker 1: flushed 5 entries (full, attempt 1)\n"+
			"2026-09-14T05:38:53Z stderr F /log.LogService/IngestLogs OK 1.2ms (stream)\n"+
			"2026-09-14T05:38:54Z stderr F /customer.CustomerService/GetCustomer OK 3ms\n")
	appendTo(t, logPath(dir, "log-agent-x", "crm", "log-agent"),
		"2026-09-14T05:38:53Z stderr F log-agent: shipped 3 entries in the last 1m0s\n")
	appendTo(t, logPath(dir, "coredns-y", "kube-system", "coredns"),
		"2026-09-14T05:38:53Z stdout F [INFO] plugin/reload\n")
	appendTo(t, filepath.Join(dir, "random.log"), "not a container log\n")

	// Act
	got := poll(t, NewTailer(testConfig(dir), newPositions(t)))

	// Assert
	if len(got) != 1 || got[0].Log.Message != "/customer.CustomerService/GetCustomer OK 3ms" {
		t.Fatalf("expected only the ordinary crm-server line, got %+v", got)
	}
}
