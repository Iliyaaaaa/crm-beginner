// Package postgres is the outbound adapter for persistent storage: it owns the
// database connection and all SQL. It knows nothing about gRPC, and returns
// domain errors that the transport layer translates into status codes.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iliya/crm-service/internal/domain"
)

// Compile-time proof that this type satisfies the port. If a method name or
// signature drifts from the interface, the build fails HERE with a clear
// message instead of somewhere confusing in main.go.
var _ domain.CustomerRepository = (*CustomerRepository)(nil)

// uniqueViolation is the SQLSTATE code Postgres returns when a UNIQUE
// constraint is violated. See https://www.postgresql.org/docs/current/errcodes-appendix.html
const uniqueViolation = "23505"

// CustomerRepository holds the connection pool. A pool is safe for concurrent
// use by many goroutines, which is why the server needs no mutex of its own.
type CustomerRepository struct {
	pool *pgxpool.Pool
}

// NewCustomerRepository opens the pool and verifies the database is actually
// reachable. pgxpool.New alone does not connect, so without the Ping a bad DSN
// would only surface on the first request.
func NewCustomerRepository(ctx context.Context, dsn string) (*CustomerRepository, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &CustomerRepository{pool: pool}, nil
}

// Close releases every connection in the pool.
func (r *CustomerRepository) Close() { r.pool.Close() }

// Create inserts a row and returns it, including the id and timestamps the
// database generated. c.ID is ignored - BIGSERIAL assigns it.
func (r *CustomerRepository) Create(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	// $1 and $2 are bind parameters. Never build SQL by concatenating strings:
	// the driver sends these values separately from the query text, which is
	// what makes SQL injection impossible.
	const q = `
		INSERT INTO customers (name, email)
		VALUES ($1, $2)
		RETURNING id, name, email, created_at, updated_at`

	var out domain.Customer
	err := r.pool.QueryRow(ctx, q, c.Name, c.Email).
		Scan(&out.ID, &out.Name, &out.Email, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		// errors.As unwraps the chain looking for a *pgconn.PgError, which is
		// where Postgres puts the SQLSTATE code.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return domain.Customer{}, domain.ErrDuplicateEmail
		}
		return domain.Customer{}, fmt.Errorf("insert customer: %w", err)
	}
	return out, nil
}

// GetByID looks up one customer by id.
func (r *CustomerRepository) GetByID(ctx context.Context, id int64) (domain.Customer, error) {
	const q = `SELECT id, name, email, created_at, updated_at FROM customers WHERE id = $1`

	var c domain.Customer
	err := r.pool.QueryRow(ctx, q, id).
		Scan(&c.ID, &c.Name, &c.Email, &c.CreatedAt, &c.UpdatedAt)
	// pgx reports "no rows" as an error rather than an empty result, so this
	// is the direct replacement for the map lookup's `ok` boolean.
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Customer{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Customer{}, fmt.Errorf("select customer: %w", err)
	}
	return c, nil
}

// Update replaces name and email for an existing row and returns it as it now
// stands. It is a full replace (like HTTP PUT), not a partial patch: proto3
// cannot tell "field not sent" apart from "field sent as empty string" for a
// plain string, so a partial-update API would need a field mask to be
// unambiguous. Requiring both fields sidesteps that.
func (r *CustomerRepository) Update(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	const q = `
		UPDATE customers
		SET name = $1, email = $2, updated_at = now()
		WHERE id = $3
		RETURNING id, name, email, created_at, updated_at`

	var out domain.Customer
	err := r.pool.QueryRow(ctx, q, c.Name, c.Email, c.ID).
		Scan(&out.ID, &out.Name, &out.Email, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// UPDATE ... WHERE id = $3 matched zero rows: no such customer.
		return domain.Customer{}, domain.ErrNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return domain.Customer{}, domain.ErrDuplicateEmail
		}
		return domain.Customer{}, fmt.Errorf("update customer: %w", err)
	}
	return out, nil
}

// Delete removes a row by id. This is a hard delete: the row is gone, not
// flagged. A real CRM usually wants a soft delete (a deleted_at column,
// filtered out of normal queries) so records can be restored.
func (r *CustomerRepository) Delete(ctx context.Context, id int64) error {
	const q = `DELETE FROM customers WHERE id = $1`

	// Exec, not QueryRow: DELETE returns no rows, only a command tag telling
	// us how many rows it touched.
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete customer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
