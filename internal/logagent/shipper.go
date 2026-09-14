package logagent

import (
	"context"
	"errors"
	"io"
	"log"
	"time"

	logpb "github.com/iliya/crm-service/proto/logpb"
)

const (
	sendTimeout         = 10 * time.Second
	initialRetryBackoff = 100 * time.Millisecond
	maxRetryBackoff     = 5 * time.Second
	reportInterval      = time.Minute
)

// Shipper batches entries and delivers each batch as one IngestLogs call.
//
// One client stream PER BATCH, not one stream kept open forever: the
// IngestSummary that closes a stream is the server's receipt, and only after
// that receipt is it safe to save the batch's file positions. A single endless
// stream would never hand one back.
//
// Delivery is at-least-once. If a batch reaches the server but the receipt is
// lost, the batch is sent again, and those lines are stored twice. Losing lines
// is the worse failure, so that is the trade taken.
type Shipper struct {
	client     logpb.LogServiceClient
	positions  *Positions
	batchSize  int
	flushEvery time.Duration
	shipped    int // since the last report; only Run's goroutine touches it
}

// NewShipper returns a Shipper that sends through client and saves positions.
func NewShipper(client logpb.LogServiceClient, positions *Positions, batchSize int, flushEvery time.Duration) *Shipper {
	return &Shipper{client: client, positions: positions, batchSize: batchSize, flushEvery: flushEvery}
}

// Run ships until in is closed. Once ctx is cancelled it stops flushing and
// only collects, so that everything still in the channel goes out together in
// one final send after in is closed.
func (s *Shipper) Run(ctx context.Context, in <-chan Entry) {
	flush := time.NewTicker(s.flushEvery)
	defer flush.Stop()
	report := time.NewTicker(reportInterval)
	defer report.Stop()

	batch := make([]Entry, 0, s.batchSize)
	for {
		select {
		case e, ok := <-in:
			if !ok {
				s.final(batch)
				return
			}
			batch = append(batch, e)
			if len(batch) >= s.batchSize && ctx.Err() == nil {
				s.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-flush.C:
			if len(batch) > 0 && ctx.Err() == nil {
				s.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-report.C:
			if s.shipped > 0 {
				log.Printf("log-agent: shipped %d entries in the last %s", s.shipped, reportInterval)
				s.shipped = 0
			}
		}
	}
}

// flush delivers batch, retrying with backoff until it succeeds or ctx is
// cancelled. A batch abandoned at shutdown is not lost: its positions were
// never saved, so its lines are read again on the next start.
func (s *Shipper) flush(ctx context.Context, batch []Entry) {
	backoff := initialRetryBackoff
	for attempt := 1; ; attempt++ {
		accepted, err := s.send(ctx, batch)
		if err == nil {
			s.commit(batch, accepted)
			return
		}
		log.Printf("log-agent: shipping %d entries failed (attempt %d): %v - retrying in %s",
			len(batch), attempt, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, maxRetryBackoff)
	}
}

// final is the shutdown send: one attempt, on a fresh context, because ctx is
// already cancelled by the time it runs.
func (s *Shipper) final(batch []Entry) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	accepted, err := s.send(ctx, batch)
	if err != nil {
		log.Printf("log-agent: last %d entries not shipped: %v - they are read again on the next start", len(batch), err)
		return
	}
	s.commit(batch, accepted)
}

// send streams one batch and returns how many entries the server accepted.
func (s *Shipper) send(ctx context.Context, batch []Entry) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	stream, err := s.client.IngestLogs(ctx)
	if err != nil {
		return 0, err
	}
	for _, e := range batch {
		if err := stream.Send(e.Log); err != nil {
			if errors.Is(err, io.EOF) {
				// io.EOF from Send only says the stream has ended; the reason
				// is whatever CloseAndRecv reports.
				if _, rerr := stream.CloseAndRecv(); rerr != nil {
					return 0, rerr
				}
				return 0, errors.New("stream closed before the whole batch was sent")
			}
			return 0, err
		}
	}
	summary, err := stream.CloseAndRecv()
	if err != nil {
		return 0, err
	}
	return summary.GetAccepted(), nil
}

// commit saves the positions for a delivered batch.
func (s *Shipper) commit(batch []Entry, accepted int64) {
	if dropped := int64(len(batch)) - accepted; dropped > 0 {
		log.Printf("log-agent: server accepted %d of %d entries; %d were dropped because its buffer was full",
			accepted, len(batch), dropped)
	}
	// Entries arrive in file order, so the last one for each file wins.
	for _, e := range batch {
		s.positions.Set(e.File, Position{Inode: e.Inode, Offset: e.Offset})
	}
	if err := s.positions.Save(); err != nil {
		log.Printf("log-agent: saving positions: %v", err)
	}
	s.shipped += len(batch)
}
