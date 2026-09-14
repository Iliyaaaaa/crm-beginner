package logagent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	logpb "github.com/iliya/crm-service/proto/logpb"
)

// Entry is one complete log message ready to ship, plus where it ended in its
// file - the position that may be saved once the entry has been delivered.
type Entry struct {
	Log    *logpb.LogEntry
	File   string
	Inode  uint64
	Offset int64
}

// Tailer follows every wanted log file in a directory. It is a plain polling
// loop: one pass over the directory, then a pause. No inotify and no goroutine
// per file, so there is nothing to leak when pods come and go.
type Tailer struct {
	cfg       Config
	positions *Positions
	files     map[string]*fileState
}

// fileState is what the tailer remembers about one file between passes.
type fileState struct {
	inode   uint64
	offset  int64  // how far has been READ - ahead of what has been shipped
	pending string // P (partial) chunks still waiting for their F line
}

// NewTailer returns a Tailer that resumes from positions.
func NewTailer(cfg Config, positions *Positions) *Tailer {
	return &Tailer{cfg: cfg, positions: positions, files: map[string]*fileState{}}
}

// Run polls until ctx is cancelled, then returns ctx.Err().
func (t *Tailer) Run(ctx context.Context, out chan<- Entry) error {
	ticker := time.NewTicker(t.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := t.Poll(ctx, out); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Poll makes one pass over the directory and sends every new complete message.
// A file that cannot be read is logged and retried on the next pass; Poll only
// returns an error when ctx is cancelled while it is waiting to send.
//
// Sending blocks while the shipper is busy retrying, and that is intended: the
// tailer simply stops reading, so during an outage unread lines wait on the
// node's disk instead of piling up in memory.
func (t *Tailer) Poll(ctx context.Context, out chan<- Entry) error {
	paths, err := filepath.Glob(filepath.Join(t.cfg.LogDir, "*.log"))
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		src, ok := ParseFileName(path)
		if !ok || !t.wanted(src) {
			continue
		}
		seen[path] = true
		if err := t.readFile(ctx, path, src, out); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("log-agent: skipping %s this pass: %v", filepath.Base(path), err)
		}
	}

	for path := range t.files {
		if !seen[path] {
			delete(t.files, path)
		}
	}
	t.positions.Retain(seen)
	return nil
}

func (t *Tailer) wanted(src Source) bool {
	if src.Container == t.cfg.SelfContainer {
		return false
	}
	return t.cfg.Namespace == "" || src.Namespace == t.cfg.Namespace
}

// readFile sends every complete message written to path since the last pass.
func (t *Tailer) readFile(ctx context.Context, path string, src Source, out chan<- Entry) error {
	f, err := os.Open(path) // follows the symlink into /var/log/pods
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	inode := inodeOf(info)

	st := t.files[path]
	if st == nil {
		st = &fileState{inode: inode}
		// Resume from the last SHIPPED position - but only if it belongs to
		// this same file. A different inode means the log was rotated.
		if pos, ok := t.positions.Get(path); ok && pos.Inode == inode {
			st.offset = pos.Offset
		}
		t.files[path] = st
	}

	// Rotated (same path, different file) or truncated (shorter than what was
	// already read): start again from the top. Anything written to the old
	// file after the last pass and before the rotation is not read.
	if st.inode != inode || info.Size() < st.offset {
		st.inode, st.offset, st.pending = inode, 0, ""
	}
	if info.Size() == st.offset {
		return nil
	}
	if _, err := f.Seek(st.offset, io.SeekStart); err != nil {
		return err
	}

	r := bufio.NewReader(f)
	for {
		raw, err := r.ReadString('\n')
		if errors.Is(err, io.EOF) {
			// No newline yet: the runtime is still writing this line. Leave
			// it - and the offset - for the next pass.
			return nil
		}
		if err != nil {
			return err
		}
		st.offset += int64(len(raw))

		line, err := ParseLine(strings.TrimSuffix(raw, "\n"))
		if err != nil {
			log.Printf("log-agent: %s: %v", filepath.Base(path), err)
			continue
		}
		if line.Partial {
			st.pending += line.Message
			continue
		}
		message := st.pending + line.Message
		st.pending = ""

		if t.cfg.Exclude != nil && t.cfg.Exclude.MatchString(message) {
			continue
		}

		entry := Entry{
			Log: &logpb.LogEntry{
				Level:     GuessLevel(message),
				Message:   message,
				Service:   src.Container,
				Timestamp: line.Time,
			},
			File:   path,
			Inode:  inode,
			Offset: st.offset,
		}
		select {
		case out <- entry:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func inodeOf(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
