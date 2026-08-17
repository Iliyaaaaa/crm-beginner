// Package store is the data-access layer: it owns the database connection and
// all SQL. It deliberately knows nothing about gRPC, so it returns plain Go
// errors that the service layer translates into status codes.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors. Callers compare with errors.Is rather than inspecting
// driver-specific error types, which keeps Postgres details out of the handlers.
var (
	ErrNotFound       = errors.New("customer not found")
	ErrDuplicateEmail = errors.New("email already exists")
)

// uniqueViolation is the SQLSTATE code Postgres returns when a UNIQUE
// constraint is violated. See https://www.postgresql.org/docs/current/errcodes-appendix.html
const uniqueViolation = "23505"

// Customer is one row of the customers table.
type Customer struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Store holds the connection pool. A pool is safe for concurrent use by many
// goroutines, which is why the server no longer needs its own mutex.
type Store struct {
	pool *pgxpool.Pool
}

// New opens the pool and verifies the database is actually reachable.
// pgxpool.New alone does not connect, so without the Ping a bad DSN would
// only surface on the first request.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection in the pool.
func (s *Store) Close() { s.pool.Close() }

// CreateCustomer inserts a row and returns it, including the id and timestamp
// the database generated.
func (s *Store) CreateCustomer(ctx context.Context, name, email string) (Customer, error) {
	// $1 and $2 are bind parameters. Never build SQL by concatenating strings:
	// the driver sends these values separately from the query text, which is
	// what makes SQL injection impossible.
	const q = `
		INSERT INTO customers (name, email)
		VALUES ($1, $2)
		RETURNING id, name, email, created_at, updated_at`

	var c Customer
	err := s.pool.QueryRow(ctx, q, name, email).
		Scan(&c.ID, &c.Name, &c.Email, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		// errors.As unwraps the chain looking for a *pgconn.PgError, which is
		// where Postgres puts the SQLSTATE code.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Customer{}, ErrDuplicateEmail
		}
		return Customer{}, fmt.Errorf("insert customer: %w", err)
	}
	return c, nil
}

// GetCustomer looks up one customer by id.
func (s *Store) GetCustomer(ctx context.Context, id int64) (Customer, error) {
	const q = `SELECT id, name, email, created_at, updated_at FROM customers WHERE id = $1`

	var c Customer
	err := s.pool.QueryRow(ctx, q, id).
		Scan(&c.ID, &c.Name, &c.Email, &c.CreatedAt, &c.UpdatedAt)
	// pgx reports "no rows" as an error rather than an empty result, so this
	// is the direct replacement for the map lookup's `ok` boolean.
	if errors.Is(err, pgx.ErrNoRows) {
		return Customer{}, ErrNotFound
	}
	if err != nil {
		return Customer{}, fmt.Errorf("select customer: %w", err)
	}
	return c, nil
}

// UpdateCustomer replaces name and email for an existing row and returns it
// as it now stands. It is a full replace (like HTTP PUT), not a partial patch:
// proto3 cannot tell "field not sent" apart from "field sent as empty string"
// for a plain string, so a partial-update API would need a separate mechanism
// (a field mask) to be unambiguous. Requiring both fields sidesteps that.
func (s *Store) UpdateCustomer(ctx context.Context, id int64, name, email string) (Customer, error) {
	const q = `
		UPDATE customers
		SET name = $1, email = $2, updated_at = now()
		WHERE id = $3
		RETURNING id, name, email, created_at, updated_at`

	var c Customer
	err := s.pool.QueryRow(ctx, q, name, email, id).
		Scan(&c.ID, &c.Name, &c.Email, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// UPDATE ... WHERE id = $3 matched zero rows: no such customer.
		// Indistinguishable, from here, from "existed but nothing changed" -
		// there is no such state, since name/email are required on every call.
		return Customer{}, ErrNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return Customer{}, ErrDuplicateEmail
		}
		return Customer{}, fmt.Errorf("update customer: %w", err)
	}
	return c, nil
}

// DeleteCustomer removes a row by id. This is a hard delete: the row is gone,
// not flagged. A real CRM usually wants a soft delete (a deleted_at column,
// filtered out of normal queries) so records can be restored - left out here
// to keep this change focused on the CRUD mechanics.
func (s *Store) DeleteCustomer(ctx context.Context, id int64) error {
	const q = `DELETE FROM customers WHERE id = $1`

	// Exec, not QueryRow: DELETE returns no rows, only a command tag telling
	// us how many rows it touched.
	tag, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("delete customer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
