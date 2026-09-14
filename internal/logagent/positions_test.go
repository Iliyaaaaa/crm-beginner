package logagent

import (
	"path/filepath"
	"testing"
)

func newPositions(t *testing.T) *Positions {
	t.Helper()
	p, err := LoadPositions(filepath.Join(t.TempDir(), "state", "positions.json"))
	if err != nil {
		t.Fatalf("load positions: %v", err)
	}
	return p
}

// The very first start on a node has no positions file yet.
func TestPositions_MissingFileStartsEmpty(t *testing.T) {
	// Arrange / Act
	p := newPositions(t)

	// Assert
	if _, ok := p.Get("anything.log"); ok {
		t.Fatal("expected no positions on a first start")
	}
}

func TestPositions_SaveThenLoad(t *testing.T) {
	// Arrange
	p := newPositions(t)
	p.Set("a.log", Position{Inode: 7, Offset: 1234})

	// Act
	if err := p.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := LoadPositions(p.path)

	// Assert
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, ok := reloaded.Get("a.log"); !ok || got != (Position{Inode: 7, Offset: 1234}) {
		t.Fatalf("position did not survive a save and reload: %+v, %v", got, ok)
	}
}

func TestPositions_RetainForgetsGoneFiles(t *testing.T) {
	// Arrange
	p := newPositions(t)
	p.Set("alive.log", Position{Offset: 1})
	p.Set("gone.log", Position{Offset: 2})

	// Act
	p.Retain(map[string]bool{"alive.log": true})

	// Assert
	if _, ok := p.Get("gone.log"); ok {
		t.Fatal("expected the gone file to be forgotten")
	}
	if _, ok := p.Get("alive.log"); !ok {
		t.Fatal("expected the live file to be kept")
	}
}
