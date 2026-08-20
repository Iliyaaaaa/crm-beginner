package grpc

import (
	"context"
	"errors"
	"testing"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The point of the recovery interceptor: a panicking handler must not take
// down the process, and the client must get a clean Internal status.
func TestRecoveryUnaryInterceptor_TurnsPanicIntoInternal(t *testing.T) {
	panicking := func(ctx context.Context, req any) (any, error) {
		panic("boom")
	}

	resp, err := RecoveryUnaryInterceptor(
		context.Background(),
		nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/Panic"},
		panicking,
	)

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
	sentinel := errors.New("ordinary failure")

	okHandler := func(ctx context.Context, req any) (any, error) { return "result", nil }
	resp, err := RecoveryUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/OK"}, okHandler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "result" {
		t.Fatalf("expected response to pass through, got %v", resp)
	}

	// An ordinary error must not be swallowed or rewritten by the interceptor.
	failHandler := func(ctx context.Context, req any) (any, error) { return nil, sentinel }
	_, err = RecoveryUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/Fail"}, failHandler)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the handler's own error, got %v", err)
	}
}

func TestRecoveryStreamInterceptor_TurnsPanicIntoInternal(t *testing.T) {
	panicking := func(srv any, ss grpclib.ServerStream) error {
		panic("boom")
	}

	err := RecoveryStreamInterceptor(nil, nil,
		&grpclib.StreamServerInfo{FullMethod: "/test/PanicStream"}, panicking)

	if err == nil {
		t.Fatal("expected an error after a panic, got nil")
	}
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("expected codes.Internal, got %s", got)
	}
}

// The logging interceptor must be transparent: it observes, never alters.
func TestLoggingUnaryInterceptor_IsTransparent(t *testing.T) {
	sentinel := errors.New("handler failed")
	handler := func(ctx context.Context, req any) (any, error) { return "value", sentinel }

	resp, err := LoggingUnaryInterceptor(context.Background(), nil,
		&grpclib.UnaryServerInfo{FullMethod: "/test/Log"}, handler)

	if resp != "value" {
		t.Fatalf("response altered: got %v", resp)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error altered: got %v", err)
	}
}
