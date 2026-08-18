// cmd/server is the composition root: the one place in the program that knows
// how every piece is wired together. It builds each adapter, injects it into
// the layer above, and starts serving. It contains no business logic and no
// protobuf handling of its own - both live in internal/service and
// internal/adapter/grpc respectively.
package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	grpcadapter "github.com/iliya/crm-service/internal/adapter/grpc"
	"github.com/iliya/crm-service/internal/adapter/postgres"
	redisadapter "github.com/iliya/crm-service/internal/adapter/redis"
	"github.com/iliya/crm-service/internal/service"
	pb "github.com/iliya/crm-service/proto/customerpb"
)

const defaultDSN = "postgres://localhost:5432/crm?sslmode=disable"

func main() {
	// signal.NotifyContext cancels ctx on Ctrl+C or SIGTERM. Everything below
	// hangs off this one context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = defaultDSN
	}

	// Give the initial connection its own bounded deadline so a wedged
	// database makes the process fail fast instead of hanging at startup.
	dbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// --- composition root: build each adapter, then inject it upward -------

	repo, err := postgres.NewCustomerRepository(dbCtx, dsn)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer repo.Close()
	log.Println("connected to postgres")

	// Redis is optional. With REDIS_URL unset the service runs uncached, which
	// keeps `make server` working without a Redis instance. A cache is an
	// optimisation, not a dependency - the service must be correct without it.
	//
	// Note this stays a concrete *CustomerCache rather than the interface: a
	// nil pointer stored in an interface makes the interface itself non-nil,
	// so `cache == nil` checks would not fire. It works here only because the
	// adapter's methods each check their nil receiver.
	var rc *redisadapter.CustomerCache
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		rc, err = redisadapter.NewCustomerCache(dbCtx, redisURL)
		if err != nil {
			log.Fatalf("redis: %v", err)
		}
		defer rc.Close()
		log.Println("connected to redis (caching enabled)")
	} else {
		log.Println("REDIS_URL not set, caching disabled")
	}

	svc := service.NewCustomerService(repo, rc)
	handler := grpcadapter.NewCustomerHandler(svc)

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterCustomerServiceServer(s, handler)
	reflection.Register(s)

	// Serve blocks, so it runs in its own goroutine and reports failures back
	// over a channel. main then waits for whichever happens first: the server
	// dying, or a shutdown signal.
	serveErr := make(chan error, 1)
	go func() {
		log.Println("gRPC server listening on :50051")
		serveErr <- s.Serve(lis)
	}()

	select {
	case err := <-serveErr:
		log.Fatalf("failed to serve: %v", err)
	case <-ctx.Done():
		log.Println("shutdown signal received, draining...")
		// GracefulStop stops accepting new connections and waits for in-flight
		// RPCs to finish. The deferred repo.Close() then runs, releasing the pool.
		s.GracefulStop()
		log.Println("stopped cleanly")
	}
}
