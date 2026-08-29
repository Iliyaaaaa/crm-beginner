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

// LogRepository holds the connection pool, shared with CustomerRepository -
// see postgres.NewPool. Because of that, LogRepository does not own or close
// it; whoever called NewPool is responsible for closing it.
type LogRepository struct {
	pool *pgxpool.Pool
}

// NewLogRepository wraps an already-open, already-verified pool. Use
// postgres.NewPool to create one.
func NewLogRepository(pool *pgxpool.Pool) *LogRepository {
	return &LogRepository{pool: pool}
}

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
