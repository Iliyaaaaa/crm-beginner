package domain

import "errors"

var (
	ErrNotFound       = errors.New("customer not found")
	ErrDuplicateEmail = errors.New("email already exists")

	// Validation errors. These are business rules, not transport concerns, so
	// they live here rather than as gRPC status codes. The transport adapter
	// (internal/adapter/grpc) is what turns them into codes.InvalidArgument.
	ErrInvalidName  = errors.New("name must not be empty")
	ErrInvalidEmail = errors.New("email must be a non-empty, valid address")
)
