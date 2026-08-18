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
	"github.com/iliya/crm-service/internal/service"
	pb "github.com/iliya/crm-service/proto/customerpb"
)

const defaultDSN = "postgres://localhost:5432/crm?sslmode=disable"

// customerServer implements the generated CustomerServiceServer interface.
//
// It now holds a *service.CustomerService instead of the repository and cache
// directly - all the cache-aside logic and validation moved there. This
// handler's only job is decode -> delegate -> encode.
type customerServer struct {
	pb.UnimplementedCustomerServiceServer
	svc *service.CustomerService
}

func (s *customerServer) CreateCustomer(
	ctx context.Context,
	req *pb.CreateCustomerRequest,
) (*pb.CreateCustomerResponse, error) {
	c, err := s.svc.Create(ctx, req.GetName(), req.GetEmail())
	switch {
	case errors.Is(err, domain.ErrInvalidName), errors.Is(err, domain.ErrInvalidEmail):
		return nil, status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrDuplicateEmail):
		return nil, status.Errorf(codes.AlreadyExists, "email %q is already registered", req.GetEmail())
	case err != nil:
		// Log the real cause, but do not leak database internals to the caller.
		log.Printf("CreateCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not create customer")
	}

	return &pb.CreateCustomerResponse{
		Id:      c.ID,
		Message: "customer created",
	}, nil
}

func (s *customerServer) GetCustomer(
	ctx context.Context,
	req *pb.GetCustomerRequest,
) (*pb.GetCustomerResponse, error) {
	c, err := s.svc.GetByID(ctx, req.GetId())
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, "customer %d not found", req.GetId())
	case err != nil:
		log.Printf("GetCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not fetch customer")
	}

	return customerToProto(c), nil
}

func (s *customerServer) UpdateCustomer(
	ctx context.Context,
	req *pb.UpdateCustomerRequest,
) (*pb.GetCustomerResponse, error) {
	c, err := s.svc.Update(ctx, req.GetId(), req.GetName(), req.GetEmail())
	switch {
	case errors.Is(err, domain.ErrInvalidName), errors.Is(err, domain.ErrInvalidEmail):
		return nil, status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, "customer %d not found", req.GetId())
	case errors.Is(err, domain.ErrDuplicateEmail):
		return nil, status.Errorf(codes.AlreadyExists, "email %q is already registered", req.GetEmail())
	case err != nil:
		log.Printf("UpdateCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not update customer")
	}

	return customerToProto(c), nil
}

func (s *customerServer) DeleteCustomer(
	ctx context.Context,
	req *pb.DeleteCustomerRequest,
) (*pb.DeleteCustomerResponse, error) {
	err := s.svc.Delete(ctx, req.GetId())
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, "customer %d not found", req.GetId())
	case err != nil:
		log.Printf("DeleteCustomer: %v", err)
		return nil, status.Error(codes.Internal, "could not delete customer")
	}

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

	svc := service.NewCustomerService(repo, rc)

	s := grpc.NewServer()
	pb.RegisterCustomerServiceServer(s, &customerServer{svc: svc})
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
