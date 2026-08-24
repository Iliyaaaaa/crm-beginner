package ingest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iliya/crm-service/internal/domain"
)

// fakeRepo records every batch it is handed instead of writing to Postgres.
// Workers call BulkInsert from several goroutines at once, so unlike the
// service-layer fakes this one genuinely needs a mutex.
type fakeRepo struct {
	mu      sync.Mutex
	batches [][]domain.LogEntry
}

var _ domain.LogRepository = (*fakeRepo)(nil)

func (f *fakeRepo) BulkInsert(ctx context.Context, entries []domain.LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Copy: the ingester reuses its batch slice, so storing the slice header
	// alone would let later writes mutate what we recorded.
	cp := make([]domain.LogEntry, len(entries))
	copy(cp, entries)
	f.batches = append(f.batches, cp)
	return nil
}

func (f *fakeRepo) totalEntries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func (f *fakeRepo) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

func entry(msg string) domain.LogEntry {
	return domain.LogEntry{
		Level: "info", Message: msg, Service: "test", OccurredAt: time.Now(),
	}
}

// Filling a batch to BatchSize must trigger a flush without waiting for the
// timer. One worker so the batch cannot be split across workers.
func TestFlushesWhenBatchIsFull(t *testing.T) {
	// Arrange
	repo := &fakeRepo{}
	ing := New(repo, Config{
		Workers: 1, BatchSize: 5,
		FlushInterval: time.Hour, // long, so only "batch full" can fire
		BufferSize:    100,
	})
	ing.Start(context.Background())

	// Act
	for n := 0; n < 5; n++ {
		if err := ing.Submit(context.Background(), entry("m")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	ing.Stop() // waits for the worker to finish

	// Assert
	if got := repo.batchCount(); got != 1 {
		t.Fatalf("expected exactly 1 batch, got %d", got)
	}
	if got := repo.totalEntries(); got != 5 {
		t.Fatalf("expected 5 entries, got %d", got)
	}
}

// A partial batch must still reach the database once the interval elapses -
// otherwise low-traffic logs would sit in memory forever.
func TestFlushesOnTimer(t *testing.T) {
	// Arrange
	repo := &fakeRepo{}
	ing := New(repo, Config{
		Workers: 1, BatchSize: 1000, // high, so only the timer can fire
		FlushInterval: 50 * time.Millisecond,
		BufferSize:    100,
	})
	ing.Start(context.Background())

	// Act
	for n := 0; n < 3; n++ {
		if err := ing.Submit(context.Background(), entry("m")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	time.Sleep(150 * time.Millisecond) // let at least one tick pass

	// Assert
	if got := repo.totalEntries(); got != 3 {
		t.Fatalf("expected 3 entries flushed by the timer, got %d", got)
	}
	ing.Stop()
}

// Stop must drain: entries still sitting in a partial batch have to be written
// before the process exits, not thrown away.
func TestStopDrainsRemainingEntries(t *testing.T) {
	// Arrange
	repo := &fakeRepo{}
	ing := New(repo, Config{
		Workers: 1, BatchSize: 1000, FlushInterval: time.Hour, // neither can fire
		BufferSize: 100,
	})
	ing.Start(context.Background())

	// Act
	for n := 0; n < 7; n++ {
		if err := ing.Submit(context.Background(), entry("m")); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	ing.Stop()

	// Assert
	if got := repo.totalEntries(); got != 7 {
		t.Fatalf("expected all 7 entries drained on Stop, got %d", got)
	}
}

// A full buffer must be reported, never silently swallowed.
func TestSubmitReturnsErrBufferFullWhenQueueIsFull(t *testing.T) {
	// Arrange: no workers started, so nothing ever drains the queue.
	repo := &fakeRepo{}
	ing := New(repo, Config{Workers: 1, BatchSize: 10, FlushInterval: time.Hour, BufferSize: 2})

	// Act: two fit in the buffer, the third cannot.
	_ = ing.Submit(context.Background(), entry("1"))
	_ = ing.Submit(context.Background(), entry("2"))
	err := ing.Submit(context.Background(), entry("3"))

	// Assert
	if err != ErrBufferFull {
		t.Fatalf("expected ErrBufferFull, got %v", err)
	}
}

// Nothing may be lost when many goroutines submit at once through several
// workers - the case a mutex bug would show up in.
func TestNoEntriesLostUnderConcurrency(t *testing.T) {
	// Arrange
	repo := &fakeRepo{}
	ing := New(repo, Config{
		Workers: 4, BatchSize: 50,
		FlushInterval: 20 * time.Millisecond,
		BufferSize:    5000,
	})
	ing.Start(context.Background())

	// Act: 20 goroutines x 100 entries = 2000 total
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if err := ing.Submit(context.Background(), entry("m")); err != nil {
					t.Errorf("Submit: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	ing.Stop()

	// Assert
	if got := repo.totalEntries(); got != 2000 {
		t.Fatalf("expected 2000 entries, got %d", got)
	}
}

// Stop is called from both App.Close and shutdown paths; calling it twice must
// not panic on a double close(ch).
func TestStopIsIdempotent(t *testing.T) {
	// Arrange
	ing := New(&fakeRepo{}, Config{Workers: 1, BatchSize: 10, FlushInterval: time.Hour, BufferSize: 10})
	ing.Start(context.Background())

	// Act + Assert: the second call must be a no-op, not a panic.
	ing.Stop()
	ing.Stop()
}
