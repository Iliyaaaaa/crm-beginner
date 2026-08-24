package domain

import "context"

// LogRepository is the port for persisting logs. Implemented by
// internal/adapter/postgres.
//
// One method, taking a slice - not "insert one log". That signature is the
// whole point: it makes batching part of the contract, not an accident of how
// some caller happens to use it.
type LogRepository interface {
	BulkInsert(ctx context.Context, entries []LogEntry) error
}
