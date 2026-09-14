package app

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// startHealthServer serves only the health service on a free local port, which
// is all stopGracefully needs - no database or cache involved.
func startHealthServer(t *testing.T) (*grpc.Server, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	return srv, lis.Addr().String()
}

// With nothing open, the drain must finish on its own and never need forcing.
func TestStopGracefully_NoOpenStreams_DrainsWithoutForcing(t *testing.T) {
	// Arrange
	srv, _ := startHealthServer(t)

	// Act
	forced := stopGracefully(srv, 2*time.Second)

	// Assert
	if forced {
		t.Fatal("expected a graceful stop with nothing open, but it was forced")
	}
}

// A health Watch stream never ends by itself, so a plain GracefulStop would
// block forever - in Kubernetes, until SIGKILL. This is the bug found on the
// live cluster: the pod took 31s to die and the ingester never drained.
func TestStopGracefully_OpenWatchStream_ForcesStopAfterTimeout(t *testing.T) {
	// Arrange: a client holding a Watch stream open
	srv, addr := startHealthServer(t)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer conn.Close()
	stream, err := healthpb.NewHealthClient(conn).Watch(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	// Receive the first update so the stream is definitely open server-side.
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("first watch update: %v", err)
	}

	// Act
	start := time.Now()
	forced := stopGracefully(srv, 200*time.Millisecond)
	elapsed := time.Since(start)

	// Assert
	if !forced {
		t.Fatal("expected the open Watch stream to force a stop")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("stop took %s, want roughly the 200ms timeout", elapsed)
	}
}
