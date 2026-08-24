-- Logs table: the destination for the batched log-ingestion pipeline.
--
-- No indexes beyond the primary key on purpose. This table is write-heavy -
-- every index added here is extra work on every insert. Add one only once a
-- real query pattern (e.g. "logs for service X in the last hour") justifies
-- the cost.
CREATE TABLE IF NOT EXISTS logs (
    id          BIGSERIAL   PRIMARY KEY,
    level       TEXT        NOT NULL,
    message     TEXT        NOT NULL,
    service     TEXT        NOT NULL,

    -- occurred_at is the client-supplied event time (LogEntry.timestamp).
    -- created_at is when the row was actually inserted. These two diverge on
    -- purpose during a backlog: if the database is down for 10 seconds and a
    -- batch of logs flushes late, created_at clusters around the flush time
    -- while occurred_at preserves when each log line actually happened.
    -- DEFAULT now() on both is a safety net, not the expected path - the
    -- client should always send its own timestamp.
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
