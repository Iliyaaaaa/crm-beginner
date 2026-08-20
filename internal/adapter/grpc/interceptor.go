package grpc

import (
	"context"
	"log"
	"runtime/debug"
	"time"

	// This package is itself named "grpc", so the library has to be aliased.
	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Interceptors are gRPC's middleware. Each one wraps the next, so a request
// passes through them on the way in and the response passes back out through
// them in reverse - the same onion shape as HTTP middleware.
//
// gRPC keeps unary and streaming interceptors separate because their
// signatures differ: a unary handler returns a response, a stream handler
// returns only an error. That means each concern is written twice, once per
// shape. It is a little repetitive and it is how the library works.

// LoggingUnaryInterceptor logs one line per completed call: method, resulting
// status code, and how long it took.
//
// Having this here is why the service layer no longer logs "created customer
// id=..." per operation - one interceptor covers every method, including ones
// added later, and cannot be forgotten.
func LoggingUnaryInterceptor(
	ctx context.Context,
	req any,
	info *grpclib.UnaryServerInfo,
	handler grpclib.UnaryHandler,
) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)

	// status.Code maps a nil error to OK, so this reports success and failure
	// in the same line.
	log.Printf("%s %s %s", info.FullMethod, status.Code(err), time.Since(start).Round(time.Microsecond))
	return resp, err
}

// LoggingStreamInterceptor is the streaming counterpart. The duration covers
// the whole stream, from open to close, not a single message.
func LoggingStreamInterceptor(
	srv any,
	ss grpclib.ServerStream,
	info *grpclib.StreamServerInfo,
	handler grpclib.StreamHandler,
) error {
	start := time.Now()
	err := handler(srv, ss)

	log.Printf("%s %s %s (stream)", info.FullMethod, status.Code(err), time.Since(start).Round(time.Microsecond))
	return err
}

// RecoveryUnaryInterceptor turns a panic into an Internal error instead of
// letting it kill the process.
//
// Without this, a nil map write or an out-of-range index in any handler takes
// down the whole server for every client. With it, the one request fails and
// everything else keeps serving.
//
// Note the NAMED return values: the deferred closure has to be able to assign
// to err, which is only possible if the return is named.
func RecoveryUnaryInterceptor(
	ctx context.Context,
	req any,
	info *grpclib.UnaryServerInfo,
	handler grpclib.UnaryHandler,
) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			// Log the panic and stack for the operator; tell the client
			// nothing beyond "internal error".
			log.Printf("PANIC in %s: %v\n%s", info.FullMethod, r, debug.Stack())
			resp = nil
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}

// RecoveryStreamInterceptor is the streaming counterpart.
func RecoveryStreamInterceptor(
	srv any,
	ss grpclib.ServerStream,
	info *grpclib.StreamServerInfo,
	handler grpclib.StreamHandler,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in %s: %v\n%s", info.FullMethod, r, debug.Stack())
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(srv, ss)
}
