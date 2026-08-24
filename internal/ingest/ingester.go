// Package ingest is the log pipeline: it accepts log entries one at a time and
// writes them to the repository in batches.
//
// It knows only domain.LogRepository - no gRPC, no SQL, no Redis. That is what
// lets it be tested with a fake repository and no infrastructure at all.
package ingest

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/iliya/crm-service/internal/domain"
)

// ErrBufferFull is returned by Submit when the queue has no room. The caller
// decides what to do about it - the pipeline never silently drops an entry.
var ErrBufferFull = errors.New("log buffer is full")

// Config controls the batching behaviour.
type Config struct {
	Workers       int           // how many goroutines drain the queue
	BatchSize     int           // flush once a batch reaches this many entries
	FlushInterval time.Duration // ...or once this long has passed
	BufferSize    int           // how many entries the queue can hold
}

// Ingester owns the queue and the workers that drain it.
type Ingester struct {
	repo domain.LogRepository
	cfg  Config

	// ch is the queue. A buffered channel IS the worker-pool queue in Go -
	// producers send, workers receive, and the runtime distributes entries
	// between whichever workers are free. No locks needed anywhere.
	ch chan domain.LogEntry

	// wg lets Stop wait until every worker has finished its final flush.
	wg sync.WaitGroup

	// stopOnce guards against Stop being called twice, which would panic on
	// the second close(ch).
	stopOnce sync.Once
}

func New(repo domain.LogRepository, cfg Config) *Ingester {
	// Defensive defaults so a zero-valued Config cannot produce a pipeline
	// that silently never flushes.
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 2 * time.Second
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 10000
	}

	return &Ingester{
		repo: repo,
		cfg:  cfg,
		ch:   make(chan domain.LogEntry, cfg.BufferSize),
	}
}

// Start launches the worker goroutines. Call it once, before any Submit.
func (i *Ingester) Start(ctx context.Context) {
	for n := 0; n < i.cfg.Workers; n++ {
		i.wg.Add(1)
		go func(id int) {
			defer i.wg.Done()
			i.worker(ctx, id)
		}(n)
	}
	log.Printf("ingester started: %d workers, batch=%d, flush=%s, buffer=%d",
		i.cfg.Workers, i.cfg.BatchSize, i.cfg.FlushInterval, i.cfg.BufferSize)
}

// Submit queues one entry. It never blocks: if the buffer is full it returns
// ErrBufferFull immediately rather than stalling the caller's request.
//
// The default case is what makes this non-blocking. Removing it would turn a
// full buffer into backpressure (the caller waits) instead - a legitimate
// alternative, but one that lets a slow database stall every inbound stream.
func (i *Ingester) Submit(ctx context.Context, e domain.LogEntry) error {
	select {
	case i.ch <- e:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrBufferFull
	}
}

// worker is one consumer: it accumulates entries into a batch and flushes on
// whichever comes first - the batch filling up, or the interval elapsing.
//
// Each worker owns its own batch and its own ticker, so no two workers ever
// touch the same slice. That is why there is no mutex in this file.
func (i *Ingester) worker(ctx context.Context, id int) {
	ticker := time.NewTicker(i.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]domain.LogEntry, 0, i.cfg.BatchSize)

	for {
		select {
		case e, ok := <-i.ch:
			if !ok {
				// Channel closed by Stop: flush whatever is left and exit.
				// This is the drain that stops shutdown from losing entries.
				i.flush(batch, id, "shutdown")
				return
			}
			batch = append(batch, e)
			if len(batch) >= i.cfg.BatchSize {
				batch = i.flush(batch, id, "full")
			}

		case <-ticker.C:
			batch = i.flush(batch, id, "timer")

		case <-ctx.Done():
			i.flush(batch, id, "cancelled")
			return
		}
	}
}

// flush writes the batch and returns a fresh empty one. On failure it logs and
// drops the batch - step 8 replaces that with retry-and-keep, which is what
// makes a database outage survivable.
func (i *Ingester) flush(batch []domain.LogEntry, workerID int, reason string) []domain.LogEntry {
	if len(batch) == 0 {
		return batch
	}

	// Deliberately NOT the worker's ctx: during shutdown that context is
	// already cancelled, and the final drain still needs to reach the
	// database. A short independent timeout bounds it instead.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := i.repo.BulkInsert(ctx, batch); err != nil {
		log.Printf("ingester worker %d: flush of %d entries failed (%s): %v",
			workerID, len(batch), reason, err)
	} else {
		log.Printf("ingester worker %d: flushed %d entries (%s)", workerID, len(batch), reason)
	}

	return make([]domain.LogEntry, 0, i.cfg.BatchSize)
}

// Stop closes the queue and waits for every worker to drain and exit.
// Safe to call more than once.
func (i *Ingester) Stop() {
	i.stopOnce.Do(func() {
		close(i.ch)
		i.wg.Wait()
		log.Println("ingester stopped, all workers drained")
	})
}
