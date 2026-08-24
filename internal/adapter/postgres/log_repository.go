package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iliya/crm-service/internal/domain"
)

// Compile-time proof that this type satisfies the port.
var _ domain.LogRepository = (*LogRepository)(nil)

// LogRepository holds its own connection pool, separate from
// CustomerRepository's. Simpler than sharing one pool across both - the cost
// is a second small pool of connections, which is a fine trade for a service
// this size. If that ever matters, both repositories can be changed to accept
// an already-open *pgxpool.Pool instead of opening their own.
type LogRepository struct {
	pool *pgxpool.Pool
}

// NewLogRepository opens the pool and verifies the database is reachable,
// same as NewCustomerRepository.
func NewLogRepository(ctx context.Context, dsn string) (*LogRepository, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &LogRepository{pool: pool}, nil
}

// Close releases every connection in the pool.
func (r *LogRepository) Close() { r.pool.Close() }

// BulkInsert writes every entry in one round trip using Postgres's binary
// COPY protocol - the fastest bulk-load path pgx offers, and the whole reason
// task 2's batching exists: one CopyFrom for 500 rows instead of 500
// individual INSERTs.
//
// created_at is deliberately not in the column list: the table's
// DEFAULT now() fills it in, exactly like the BIGSERIAL id.
func (r *LogRepository) BulkInsert(ctx context.Context, entries []domain.LogEntry) error {
	if len(entries) == 0 {
		return nil
	}

	rows := make([][]any, len(entries))
	for i, e := range entries {
		rows[i] = []any{e.Level, e.Message, e.Service, e.OccurredAt}
	}

	_, err := r.pool.CopyFrom(
		ctx,
		pgx.Identifier{"logs"},
		[]string{"level", "message", "service", "occurred_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return fmt.Errorf("bulk insert logs: %w", err)
	}
	return nil
}
