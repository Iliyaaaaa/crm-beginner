package grpc

import (
	"errors"
	"log"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iliya/crm-service/internal/domain"
)

// toGRPCError translates a domain error into a gRPC status. This is the
// single place that mapping happens - previously it was duplicated as an
// inline switch in all four handlers.
//
// Messages here are intentionally generic (the domain error's own text, e.g.
// "customer not found") rather than echoing back the request's id or email as
// the old inline switches did. gRPC clients are expected to branch on Code,
// never parse Message, so this loses nothing that matters to a correct
// caller while removing the duplication.
func toGRPCError(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrDuplicateEmail):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrInvalidName), errors.Is(err, domain.ErrInvalidEmail):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		// Log the real cause, but do not leak internal details to the caller.
		log.Printf("unexpected error: %v", err)
		return status.Error(codes.Internal, "internal error")
	}
}
