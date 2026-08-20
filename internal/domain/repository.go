package domain

import "context"

// CustomerRepository is the port for persistent storage.
// Implemented by internal/adapter/postgres.
type CustomerRepository interface {
	Create(ctx context.Context, c Customer) (Customer, error)
	GetByID(ctx context.Context, id int64) (Customer, error)
	Update(ctx context.Context, c Customer) (Customer, error)
	Delete(ctx context.Context, id int64) error

	// List returns up to limit customers, oldest first.
	//
	// It returns a slice rather than a channel or iterator: with limit capped
	// server-side the result is bounded and small, so the simplicity is worth
	// more than avoiding the intermediate allocation. A genuinely large export
	// would want row-by-row streaming from the database instead.
	List(ctx context.Context, limit int32) ([]Customer, error)
}

// CustomerCache is the port for caching.
// Implemented by internal/adapter/redis.
type CustomerCache interface {
	Get(ctx context.Context, id int64) (Customer, bool)
	Set(ctx context.Context, c Customer)
	Invalidate(ctx context.Context, id int64)
}
