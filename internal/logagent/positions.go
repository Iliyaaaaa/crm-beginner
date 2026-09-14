package logagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Position is how far into one file the agent has safely shipped. Inode says
// WHICH file the offset belongs to: when the kubelet rotates a log, the same
// path suddenly names a new, shorter file, and an offset into the old one would
// be meaningless.
type Position struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

// Positions keeps shipped offsets and persists them to disk, so a restarted
// agent resumes where it stopped instead of shipping every file from the start.
// The tailer reads them and the shipper writes them from another goroutine,
// hence the mutex.
type Positions struct {
	path string
	mu   sync.Mutex
	m    map[string]Position
}

// LoadPositions reads the positions file. A missing file is not an error: it
// is simply the agent's first start on this node.
func LoadPositions(path string) (*Positions, error) {
	p := &Positions{path: path, m: map[string]Position{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read positions: %w", err)
	}
	if err := json.Unmarshal(data, &p.m); err != nil {
		return nil, fmt.Errorf("parse positions %s: %w", path, err)
	}
	return p, nil
}

// Get returns the saved position for file.
func (p *Positions) Get(file string) (Position, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos, ok := p.m[file]
	return pos, ok
}

// Set records a position in memory; Save writes it out.
func (p *Positions) Set(file string, pos Position) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[file] = pos
}

// Retain forgets every file not in keep. Pods that are gone take their log
// files with them, and their offsets would otherwise pile up forever.
func (p *Positions) Retain(keep map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for file := range p.m {
		if !keep[file] {
			delete(p.m, file)
		}
	}
}

// Save writes the positions to disk. It writes a temporary file and renames it
// over the real one: a rename is atomic, so a crash mid-write can never leave a
// half-written, unreadable positions file behind.
func (p *Positions) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := json.Marshal(p.m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}
