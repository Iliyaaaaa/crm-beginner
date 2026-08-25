package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/iliya/crm-service/internal/domain"
	"github.com/iliya/crm-service/internal/ingest"
	logpb "github.com/iliya/crm-service/proto/logpb"
)

// --- fakes ----------------------------------------------------------------

// fakeLogRepo records entries instead of writing to Postgres. Used for tests
// that run a REAL *ingest.Ingester behind the handler, to prove the whole
// glue path - stream to handler to ingester to repository - works together.
type fakeLogRepo struct {
	mu      sync.Mutex
	entries []domain.LogEntry
}

var _ domain.LogRepository = (*fakeLogRepo)(nil)

func (f *fakeLogRepo) BulkInsert(ctx context.Context, entries []domain.LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entries...)
	return nil
}

func (f *fakeLogRepo) snapshot() []domain.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.LogEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

// fakeSubmitter implements ingest.Submitter directly, with no real queue or
// workers behind it. Used to test the handler's error-handling branch in
// isolation, which a real Ingester cannot easily be made to fail on demand.
type fakeSubmitter struct {
	mu       sync.Mutex
	received []domain.LogEntry
	failWith error // when set, every Submit fails with this error
}

func (f *fakeSubmitter) Submit(ctx context.Context, e domain.LogEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	f.received = append(f.received, e)
	return nil
}

// fakeIngestStream is a fake logpb.LogService_IngestLogsServer: it replays a
// fixed list of entries through Recv (or a Recv error if recvErr is set),
// then io.EOF, and records whatever SendAndClose is given. The remaining
// methods only exist to satisfy grpc.ServerStream and are never exercised by
// the handler.
type fakeIngestStream struct {
	entries []*logpb.LogEntry
	recvErr error // returned instead of io.EOF once entries are exhausted
	pos     int
	summary *logpb.IngestSummary
}

func (f *fakeIngestStream) Recv() (*logpb.LogEntry, error) {
	if f.pos < len(f.entries) {
		e := f.entries[f.pos]
		f.pos++
		return e, nil
	}
	if f.recvErr != nil {
		return nil, f.recvErr
	}
	return nil, io.EOF
}

func (f *fakeIngestStream) SendAndClose(s *logpb.IngestSummary) error {
	f.summary = s
	return nil
}

func (f *fakeIngestStream) Context() context.Context     { return context.Background() }
func (f *fakeIngestStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeIngestStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeIngestStream) SetTrailer(metadata.MD)       {}
func (f *fakeIngestStream) SendMsg(m any) error          { return nil }
func (f *fakeIngestStream) RecvMsg(m any) error          { return nil }

func entryAt(msg string, ts time.Time) *logpb.LogEntry {
	return &logpb.LogEntry{
		Level: "info", Message: msg, Service: "test-service",
		Timestamp: ts.Format(time.RFC3339),
	}
}

// --- tests: full glue path, through a real Ingester ------------------------

// TestIngestLogs_AcceptsAllEntries proves the whole chain - fake stream to
// handler to real ingester to fake repository - works end to end with no
// Postgres involved.
func TestIngestLogs_AcceptsAllEntries(t *testing.T) {
	// Arrange
	repo := &fakeLogRepo{}
	ing := ingest.New(repo, ingest.Config{
		Workers: 1, BatchSize: 100, FlushInterval: 20 * time.Millisecond, BufferSize: 100,
	})
	ing.Start(context.Background())
	defer ing.Stop()

	handler := NewLogHandler(ing)
	now := time.Now()
	stream := &fakeIngestStream{entries: []*logpb.LogEntry{
		entryAt("one", now), entryAt("two", now), entryAt("three", now),
	}}

	// Act
	err := handler.IngestLogs(stream)

	// Assert
	if err != nil {
		t.Fatalf("IngestLogs: %v", err)
	}
	if stream.summary == nil || stream.summary.Accepted != 3 {
		t.Fatalf("expected summary.Accepted=3, got %+v", stream.summary)
	}
	time.Sleep(50 * time.Millisecond) // let the flush timer fire
	if got := len(repo.snapshot()); got != 3 {
		t.Fatalf("expected 3 entries reaching the repository, got %d", got)
	}
}

// A malformed timestamp on one entry must not fail the whole stream - it
// falls back to "now" and the entry is still accepted.
func TestIngestLogs_BadTimestampFallsBackToNow(t *testing.T) {
	// Arrange
	repo := &fakeLogRepo{}
	ing := ingest.New(repo, ingest.Config{
		Workers: 1, BatchSize: 100, FlushInterval: 20 * time.Millisecond, BufferSize: 10,
	})
	ing.Start(context.Background())
	defer ing.Stop()

	handler := NewLogHandler(ing)
	stream := &fakeIngestStream{entries: []*logpb.LogEntry{
		{Level: "info", Message: "bad ts", Service: "svc", Timestamp: "not-a-timestamp"},
	}}

	// Act
	err := handler.IngestLogs(stream)

	// Assert
	if err != nil {
		t.Fatalf("IngestLogs: %v", err)
	}
	if stream.summary.Accepted != 1 {
		t.Fatalf("expected the entry to still be accepted, got %d", stream.summary.Accepted)
	}
	time.Sleep(50 * time.Millisecond)
	got := repo.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 entry reaching the repository, got %d", len(got))
	}
	if time.Since(got[0].OccurredAt) > 5*time.Second {
		t.Fatalf("expected OccurredAt to fall back to ~now, got %v", got[0].OccurredAt)
	}
}

// --- tests: handler in isolation, via fakeSubmitter -------------------------

// When Submit fails (e.g. ErrBufferFull), the handler must skip that entry
// and keep processing the rest of the stream, rather than aborting it.
func TestIngestLogs_SkipsEntryWhenSubmitFails(t *testing.T) {
	// Arrange
	sub := &fakeSubmitter{failWith: ingest.ErrBufferFull}
	handler := NewLogHandler(sub)
	now := time.Now()
	stream := &fakeIngestStream{entries: []*logpb.LogEntry{
		entryAt("one", now), entryAt("two", now),
	}}

	// Act
	err := handler.IngestLogs(stream)

	// Assert: the call itself succeeds, but nothing was actually accepted
	if err != nil {
		t.Fatalf("IngestLogs: %v", err)
	}
	if stream.summary.Accepted != 0 {
		t.Fatalf("expected Accepted=0 when every Submit fails, got %d", stream.summary.Accepted)
	}
}

// A real Recv error (not io.EOF) must propagate as the handler's own error,
// ending the RPC with a failure instead of a summary.
func TestIngestLogs_PropagatesRecvError(t *testing.T) {
	// Arrange
	sentinel := errors.New("connection reset")
	sub := &fakeSubmitter{}
	handler := NewLogHandler(sub)
	stream := &fakeIngestStream{
		entries: []*logpb.LogEntry{entryAt("one", time.Now())},
		recvErr: sentinel,
	}

	// Act
	err := handler.IngestLogs(stream)

	// Assert
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the Recv error to propagate, got %v", err)
	}
	if stream.summary != nil {
		t.Fatalf("expected SendAndClose to never be called, got %+v", stream.summary)
	}
}
