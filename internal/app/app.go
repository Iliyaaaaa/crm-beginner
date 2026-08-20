// Package app is the composition root: the single place that knows how every
// component is constructed and wired to the next.
//
// This is manual dependency injection - no framework. Read New() top to bottom
// and you have a complete picture of the architecture: adapters at the bottom,
// service in the middle, transport on top, each handed its dependencies rather
// than reaching for them.
//
// Keeping this here rather than in main() means the wiring is a named,
// importable construct: an integration test can call app.New with a test
// config and get a fully assembled service.
package app

import (
	"context"
	"fmt"
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	grpcadapter "github.com/iliya/crm-service/internal/adapter/grpc"
	"github.com/iliya/crm-service/internal/adapter/postgres"
	redisadapter "github.com/iliya/crm-service/internal/adapter/redis"
	"github.com/iliya/crm-service/internal/config"
	"github.com/iliya/crm-service/internal/service"
	pb "github.com/iliya/crm-service/proto/customerpb"
)

// App owns the assembled dependency graph and the resources that need closing.
type App struct {
	cfg  config.Config
	repo *postgres.CustomerRepository
	// Deliberately the concrete type, not domain.CustomerCache: a nil pointer
	// stored in an interface makes the interface itself non-nil, so a
	// `cache == nil` check would not fire. This works only because the
	// adapter's methods each check their own nil receiver.
	cache    *redisadapter.CustomerCache
	grpcSrv  *grpc.Server
	listener net.Listener
}

// New builds the dependency graph. On any failure it closes whatever it has
// already opened, so a partially constructed App never leaks a connection.
func New(ctx context.Context, cfg config.Config) (*App, error) {
	a := &App{cfg: cfg}

	// Bound the startup connections so a wedged dependency fails fast.
	startCtx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancel()

	// --- outbound adapters -------------------------------------------------

	repo, err := postgres.NewCustomerRepository(startCtx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	a.repo = repo
	log.Println("connected to postgres")

	if cfg.CacheEnabled() {
		cache, err := redisadapter.NewCustomerCache(startCtx, cfg.RedisURL, cfg.CacheTTL)
		if err != nil {
			a.Close() // release the pool we just opened
			return nil, fmt.Errorf("redis: %w", err)
		}
		a.cache = cache
		log.Printf("connected to redis (caching enabled, ttl=%s)", cfg.CacheTTL)
	} else {
		log.Println("REDIS_URL not set, caching disabled")
	}

	// --- service, then inbound adapter ------------------------------------

	svc := service.NewCustomerService(a.repo, a.cache)
	handler := grpcadapter.NewCustomerHandler(svc)

	// Interceptors are middleware. ChainUnaryInterceptor runs them in the
	// order listed, outermost first, so Recovery wraps Logging - a panic
	// anywhere inside, including in the logging interceptor itself, is caught.
	a.grpcSrv = grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcadapter.RecoveryUnaryInterceptor,
			grpcadapter.LoggingUnaryInterceptor,
		),
		grpc.ChainStreamInterceptor(
			grpcadapter.RecoveryStreamInterceptor,
			grpcadapter.LoggingStreamInterceptor,
		),
	)
	pb.RegisterCustomerServiceServer(a.grpcSrv, handler)
	reflection.Register(a.grpcSrv)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("listen on %s: %w", cfg.GRPCAddr, err)
	}
	a.listener = lis

	return a, nil
}

// Run serves until ctx is cancelled or the server fails. On cancellation it
// drains in-flight RPCs before returning.
func (a *App) Run(ctx context.Context) error {
	// Serve blocks, so it runs in its own goroutine and reports failures back
	// over a buffered channel - buffered so the goroutine can exit even if
	// nobody is left reading.
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("gRPC server listening on %s", a.cfg.GRPCAddr)
		serveErr <- a.grpcSrv.Serve(a.listener)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		log.Println("shutdown signal received, draining...")
		// GracefulStop stops accepting new connections and waits for in-flight
		// RPCs to finish. It also closes the listener.
		a.grpcSrv.GracefulStop()
		log.Println("stopped cleanly")
		return nil
	}
}

// Close releases resources in reverse construction order. Safe to call on a
// partially built App - every field is nil-checked.
func (a *App) Close() {
	if a.cache != nil {
		if err := a.cache.Close(); err != nil {
			log.Printf("closing redis: %v", err)
		}
	}
	if a.repo != nil {
		a.repo.Close()
	}
}
