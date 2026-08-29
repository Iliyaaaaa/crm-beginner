package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens the single connection pool shared by every Postgres-backed
// repository (CustomerRepository, LogRepository, ...). Sharing one pool
// instead of each repository opening its own matters because Postgres
// enforces ONE server-wide max_connections limit across everything talking
// to it - two separate pools of, say, 40 connections each need 80 slots for
// no benefit, since nothing stops either pool from using all 40 at once.
//
// maxConns bounds how many connections THIS pool may hold open at a time.
// Left unset, pgx defaults to max(4, NumCPU) - fine for light traffic, but a
// hard ceiling on write throughput under load. Passing it explicitly here
// makes the limit a deliberate, visible choice instead of whatever the host
// machine happens to have.
func NewPool(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		poolCfg.MaxConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	// pgxpool.NewWithConfig alone does not connect, so without the Ping a bad
	// DSN would only surface on the first request.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
