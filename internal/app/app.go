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

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	grpcadapter "github.com/iliya/crm-service/internal/adapter/grpc"
	"github.com/iliya/crm-service/internal/adapter/postgres"
	redisadapter "github.com/iliya/crm-service/internal/adapter/redis"
	"github.com/iliya/crm-service/internal/config"
	"github.com/iliya/crm-service/internal/ingest"
	"github.com/iliya/crm-service/internal/service"
	pb "github.com/iliya/crm-service/proto/customerpb"
	logpb "github.com/iliya/crm-service/proto/logpb"
)

// App owns the assembled dependency graph and the resources that need closing.
type App struct {
	cfg config.Config
	// pool is the ONE Postgres connection pool for the whole service. repo and
	// logRepo both draw from it - see postgres.NewPool for why sharing one
	// pool (instead of each repository opening its own) matters.
	pool *pgxpool.Pool
	repo *postgres.CustomerRepository
	// Deliberately the concrete type, not domain.CustomerCache: a nil pointer
	// stored in an interface makes the interface itself non-nil, so a
	// `cache == nil` check would not fire. This works only because the
	// adapter's methods each check their own nil receiver.
	cache    *redisadapter.CustomerCache
	logRepo  *postgres.LogRepository
	ingester *ingest.Ingester
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

	pool, err := postgres.NewPool(startCtx, cfg.DatabaseURL, int32(cfg.DBMaxConns))
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	a.pool = pool
	a.repo = postgres.NewCustomerRepository(a.pool)
	log.Printf("connected to postgres (max_conns=%d)", cfg.DBMaxConns)

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

	a.logRepo = postgres.NewLogRepository(a.pool)

	// The ingester's lifetime is the WHOLE app, not just startup - it must
	// keep running after New returns, so it gets the long-lived ctx, not
	// startCtx (which is cancelled the moment New returns via its own defer).
	a.ingester = ingest.New(a.logRepo, ingest.Config{
		Workers:       cfg.LogWorkers,
		BatchSize:     cfg.LogBatchSize,
		FlushInterval: cfg.LogFlushInterval,
		BufferSize:    cfg.LogBufferSize,
	})
	a.ingester.Start(ctx)

	// --- service, then inbound adapter ------------------------------------

	svc := service.NewCustomerService(a.repo, a.cache)
	handler := grpcadapter.NewCustomerHandler(svc)
	logHandler := grpcadapter.NewLogHandler(a.ingester)

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
	logpb.RegisterLogServiceServer(a.grpcSrv, logHandler)
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
//
// The ingester is stopped FIRST, before the pool closes. Stop drains any
// in-flight batch, and that drain still needs a live connection to write to -
// closing the pool first would make the final flush fail every time.
func (a *App) Close() {
	if a.ingester != nil {
		a.ingester.Stop()
	}
	if a.cache != nil {
		if err := a.cache.Close(); err != nil {
			log.Printf("closing redis: %v", err)
		}
	}
	// repo and logRepo both point at this one pool and own none of it
	// themselves (see postgres.NewPool), so closing it here is the only
	// close either of them needs.
	if a.pool != nil {
		a.pool.Close()
	}
}
