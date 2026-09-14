package grpc

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// The point of the recovery interceptor: a panicking handler must not take
// down the process, and the client must get a clean Internal status.
func TestRecoveryUnaryInterceptor_TurnsPanicIntoInternal(t *testing.T) {
	// Arrange
	panicking := func(ctx context.Context, req any) (any, error) {
		panic("boom")
	}

	// Act
	resp, err := RecoveryUnaryInterceptor(
		context.Background(),
		nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/Panic"},
		panicking,
	)

	// Assert
	if err == nil {
		t.Fatal("expected an error after a panic, got nil")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected codes.Internal, got %s", got)
	}
	if resp != nil {
		t.Fatalf("expected nil response after a panic, got %v", resp)
	}
}

// A handler that returns normally must pass straight through untouched.
func TestRecoveryUnaryInterceptor_PassesThroughNormalCalls(t *testing.T) {
	// Arrange: a handler that succeeds normally
	okHandler := func(ctx context.Context, req any) (any, error) { return "result", nil }

	// Act
	resp, err := RecoveryUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/OK"}, okHandler)

	// Assert
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "result" {
		t.Fatalf("expected response to pass through, got %v", resp)
	}

	// Arrange: a handler that fails normally, with no panic - the interceptor
	// must not swallow or rewrite an ordinary error.
	sentinel := errors.New("ordinary failure")
	failHandler := func(ctx context.Context, req any) (any, error) { return nil, sentinel }

	// Act
	_, err = RecoveryUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/Fail"}, failHandler)

	// Assert
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the handler's own error, got %v", err)
	}
}

func TestRecoveryStreamInterceptor_TurnsPanicIntoInternal(t *testing.T) {
	// Arrange
	panicking := func(srv any, ss grpclib.ServerStream) error {
		panic("boom")
	}

	// Act
	err := RecoveryStreamInterceptor(nil, nil,
		&grpclib.StreamServerInfo{FullMethod: "/test/PanicStream"}, panicking)

	// Assert
	if err == nil {
		t.Fatal("expected an error after a panic, got nil")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected codes.Internal, got %s", got)
	}
}

// The logging interceptor must be transparent: it observes, never alters.
func TestLoggingUnaryInterceptor_IsTransparent(t *testing.T) {
	// Arrange
	sentinel := errors.New("handler failed")
	handler := func(ctx context.Context, req any) (any, error) { return "value", sentinel }

	// Act
	resp, err := LoggingUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/Log"}, handler)

	// Assert
	if resp != "value" {
		t.Fatalf("response altered: got %v", resp)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error altered: got %v", err)
	}
}

// captureLog redirects the standard logger into a buffer for one test, and
// restores whatever it was writing to before.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// A successful probe call must leave no log line - on the cluster these were
// 90 of every 90 lines.
func TestLoggingUnaryInterceptor_SkipsSuccessfulHealthChecks(t *testing.T) {
	// Arrange
	buf := captureLog(t)
	ok := func(ctx context.Context, req any) (any, error) { return "SERVING", nil }

	// Act
	_, _ = LoggingUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: healthpb.Health_Check_FullMethodName}, ok)

	// Assert
	if buf.Len() != 0 {
		t.Fatalf("expected no log output for a successful health check, got %q", buf.String())
	}
}

// A failing health check is exactly the line worth seeing, so it must still be
// logged.
func TestLoggingUnaryInterceptor_LogsFailingHealthChecks(t *testing.T) {
	// Arrange
	buf := captureLog(t)
	failing := func(ctx context.Context, req any) (any, error) {
		return nil, status.Error(codes.Unavailable, "not ready")
	}

	// Act
	_, _ = LoggingUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: healthpb.Health_Check_FullMethodName}, failing)

	// Assert
	if !strings.Contains(buf.String(), "Unavailable") {
		t.Fatalf("expected the failing health check to be logged, got %q", buf.String())
	}
}

// Skipping health checks must not silence ordinary methods.
func TestLoggingUnaryInterceptor_LogsOrdinaryCalls(t *testing.T) {
	// Arrange
	buf := captureLog(t)
	ok := func(ctx context.Context, req any) (any, error) { return "value", nil }
	method := "/customer.CustomerService/GetCustomer"

	// Act
	_, _ = LoggingUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: method}, ok)

	// Assert
	if !strings.Contains(buf.String(), method) {
		t.Fatalf("expected %s to be logged, got %q", method, buf.String())
	}
}
