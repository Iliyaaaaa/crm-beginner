package main

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/iliya/crm-service/internal/adapter/postgres"
	redisadapter "github.com/iliya/crm-service/internal/adapter/redis"
	"github.com/iliya/crm-service/internal/domain"
	pb "github.com/iliya/crm-service/proto/customerpb"
)

const defaultDSN = "postgres://localhost:5432/crm?sslmode=disable"

// customerServer implements the generated CustomerServiceServer interface.
//
// Both dependencies are now INTERFACES from the domain package, not concrete
// adapter types. That is dependency inversion: this handler no longer knows
// that Postgres or Redis exist, so a test can hand it a fake instead.
type customerServer struct {
	pb.UnimplementedCustomerServiceServer
	repo domain.CustomerRepository
	// cache may be nil, meaning caching is disabled. Every cache method is
	// nil-safe, so no handler needs to check.
	cache domain.CustomerCache
}

func (s *customerServer) CreateCustomer(
	ctx context.Context,
	req *pb.CreateCustomerRequest,
) (*pb.CreateCustomerResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name must not be empty")
	}
	if req.GetEmail() == "" {
		return nil, status.Error(codes.InvalidArgument, "email must not be empty")
	}

	// ctx is passed straight through to the query. If the client's deadline
	// expires or it hangs up, Postgres is told to abandon the statement
	// instead of finishing work nobody is waiting for.
	c, err := s.repo.Create(ctx, domain.Customer{
		Name:  req.GetName(),
		Email: req.GetEmail(),
	})
	switch {
	case errors.Is(err, domain.ErrDuplicateEmail):
		return nil, status.Errorf(codes.AlreadyExists, "email %q is already registered", req.GetEmail())
	case err != nil:
		// Log the real cause, but do not leak database internals to the caller.
		log.Printf("CreateCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not create customer")
	}

	log.Printf("created customer id=%d name=%q email=%q", c.ID, c.Name, c.Email)

	return &pb.CreateCustomerResponse{
		Id:      c.ID,
		Message: "customer created",
	}, nil
}

func (s *customerServer) GetCustomer(
	ctx context.Context,
	req *pb.GetCustomerRequest,
) (*pb.GetCustomerResponse, error) {
	// Cache-aside, step 1: ask the cache first.
	if c, hit := s.cache.Get(ctx, req.GetId()); hit {
		log.Printf("fetched customer id=%d (cache HIT)", c.ID)
		return customerToProto(c), nil
	}

	// Step 2: on a miss, go to the real source of truth.
	c, err := s.repo.GetByID(ctx, req.GetId())
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// Deliberately NOT cached. Caching "this does not exist" is possible
		// (negative caching) but then creating that customer would have to
		// invalidate the negative entry too - more invalidation paths to get
		// wrong, for little benefit here.
		return nil, status.Errorf(codes.NotFound, "customer %d not found", req.GetId())
	case err != nil:
		log.Printf("GetCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not fetch customer")
	}

	// Step 3: populate the cache so the next read is a hit.
	s.cache.Set(ctx, c)

	log.Printf("fetched customer id=%d (cache MISS -> db)", c.ID)

	return customerToProto(c), nil
}

func (s *customerServer) UpdateCustomer(
	ctx context.Context,
	req *pb.UpdateCustomerRequest,
) (*pb.GetCustomerResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name must not be empty")
	}
	if req.GetEmail() == "" {
		return nil, status.Error(codes.InvalidArgument, "email must not be empty")
	}

	c, err := s.repo.Update(ctx, domain.Customer{
		ID:    req.GetId(),
		Name:  req.GetName(),
		Email: req.GetEmail(),
	})
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, "customer %d not found", req.GetId())
	case errors.Is(err, domain.ErrDuplicateEmail):
		return nil, status.Errorf(codes.AlreadyExists, "email %q is already registered", req.GetEmail())
	case err != nil:
		log.Printf("UpdateCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not update customer")
	}

	// Invalidate AFTER the write succeeds. Doing it before would leave a window
	// where a concurrent reader could re-populate the cache with the old row
	// just before the update lands.
	s.cache.Invalidate(ctx, c.ID)

	log.Printf("updated customer id=%d name=%q email=%q (cache invalidated)", c.ID, c.Name, c.Email)

	return customerToProto(c), nil
}

func (s *customerServer) DeleteCustomer(
	ctx context.Context,
	req *pb.DeleteCustomerRequest,
) (*pb.DeleteCustomerResponse, error) {
	err := s.repo.Delete(ctx, req.GetId())
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, "customer %d not found", req.GetId())
	case err != nil:
		log.Printf("DeleteCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not delete customer")
	}

	s.cache.Invalidate(ctx, req.GetId())

	log.Printf("deleted customer id=%d (cache invalidated)", req.GetId())

	return &pb.DeleteCustomerResponse{Message: "customer deleted"}, nil
}

// customerToProto converts a domain.Customer into the wire message shared by
// GetCustomer and UpdateCustomer, so the two handlers don't repeat this
// field-by-field mapping.
func customerToProto(c domain.Customer) *pb.GetCustomerResponse {
	return &pb.GetCustomerResponse{
		Id:        c.ID,
		Name:      c.Name,
		Email:     c.Email,
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
}

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

	// --- composition root: build the adapters, then inject them ------------

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

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterCustomerServiceServer(s, &customerServer{repo: repo, cache: rc})
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
		// RPCs to finish. The deferred st.Close() then runs, releasing the pool.
		s.GracefulStop()
		log.Println("stopped cleanly")
	}
}
