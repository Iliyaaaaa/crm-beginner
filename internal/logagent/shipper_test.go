package logagent

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	logpb "github.com/iliya/crm-service/proto/logpb"
)

// fakeLogServer is a real LogService over a real gRPC connection, recording
// what it receives and able to fail a scripted number of calls.
type fakeLogServer struct {
	logpb.UnimplementedLogServiceServer
	mu       sync.Mutex
	received []*logpb.LogEntry
	failNext int
}

func (f *fakeLogServer) IngestLogs(stream logpb.LogService_IngestLogsServer) error {
	f.mu.Lock()
	fail := f.failNext > 0
	if fail {
		f.failNext--
	}
	f.mu.Unlock()
	if fail {
		return status.Error(codes.Unavailable, "database is down")
	}

	var accepted int64
	for {
		e, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&logpb.IngestSummary{Accepted: accepted})
		}
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.received = append(f.received, e)
		f.mu.Unlock()
		accepted++
	}
}

func (f *fakeLogServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func startLogServer(t *testing.T, fake *fakeLogServer) logpb.LogServiceClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	logpb.RegisterLogServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return logpb.NewLogServiceClient(conn)
}

func entryAt(message, file string, offset int64) Entry {
	return Entry{
		Log:    &logpb.LogEntry{Level: "info", Message: message, Service: "app", Timestamp: "2026-09-14T05:38:53Z"},
		File:   file,
		Inode:  42,
		Offset: offset,
	}
}

// Full batches go out as they fill, the remainder goes out on close, and the
// saved position is the last delivered one.
func TestShipper_DeliversEntriesAndSavesPositions(t *testing.T) {
	// Arrange
	fake := &fakeLogServer{}
	positions := newPositions(t)
	shipper := NewShipper(startLogServer(t, fake), positions, 2, time.Hour)
	in := make(chan Entry, 10)
	in <- entryAt("one", "a.log", 10)
	in <- entryAt("two", "a.log", 20)
	in <- entryAt("three", "a.log", 30)
	close(in)

	// Act
	shipper.Run(context.Background(), in)

	// Assert
	if got := fake.count(); got != 3 {
		t.Fatalf("expected 3 entries delivered, got %d", got)
	}
	reloaded, err := LoadPositions(positions.path)
	if err != nil {
		t.Fatalf("reload positions: %v", err)
	}
	if pos, ok := reloaded.Get("a.log"); !ok || pos.Offset != 30 {
		t.Fatalf("expected saved offset 30, got %+v (found=%v)", pos, ok)
	}
}

// While the server is down, the same batch is retried - and delivered once.
func TestShipper_RetriesUntilTheServerAccepts(t *testing.T) {
	// Arrange
	fake := &fakeLogServer{failNext: 2}
	shipper := NewShipper(startLogServer(t, fake), newPositions(t), 1, time.Hour)
	in := make(chan Entry, 1)
	in <- entryAt("survives an outage", "a.log", 10)
	close(in)

	// Act
	shipper.Run(context.Background(), in)

	// Assert
	if got := fake.count(); got != 1 {
		t.Fatalf("expected exactly 1 delivery after 2 failures, got %d", got)
	}
}

// Shutting down during an outage must return promptly AND must not save a
// position for lines that never arrived - otherwise they would be lost.
func TestShipper_ShutdownDuringOutageSavesNothing(t *testing.T) {
	// Arrange
	fake := &fakeLogServer{failNext: 1 << 30}
	positions := newPositions(t)
	shipper := NewShipper(startLogServer(t, fake), positions, 1, time.Hour)
	in := make(chan Entry, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		shipper.Run(ctx, in)
		close(done)
	}()
	in <- entryAt("never delivered", "a.log", 10)
	time.Sleep(300 * time.Millisecond) // let it fail and back off a few times

	// Act
	cancel()
	close(in)

	// Assert
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after shutdown during an outage")
	}
	if _, ok := positions.Get("a.log"); ok {
		t.Fatal("a position was saved for an entry that was never delivered")
	}
}
