# Log Ingestion Pipeline — Roadmap

Three tasks that together build one thing:

1. Logging over **client streaming** instead of unary
2. A **worker pool** that batches and bulk-inserts (500 rows or 2 seconds)
3. Survive a **10-second database outage** with no crash and no lost logs

---

# Part 1 — The idea in one picture

```
   many clients                one connection each
        │                              │
        ▼                              ▼
   ┌─────────────────────────────────────────┐
   │  IngestLogs handler (client streaming)  │   receives log entries in a loop
   └──────────────────┬──────────────────────┘
                      │  pushes into
                      ▼
              ┌───────────────┐
              │    channel    │   a queue with a fixed size (the buffer)
              │  (buffered)   │
              └───────┬───────┘
                      │  N workers read from it
        ┌─────────────┼─────────────┐
        ▼             ▼             ▼
    ┌────────┐   ┌────────┐   ┌────────┐
    │worker 1│   │worker 2│   │worker 3│   each collects its own batch
    │ batch  │   │ batch  │   │ batch  │
    └───┬────┘   └───┬────┘   └───┬────┘
        │            │            │       flush when 500 rows OR 2 seconds
        └────────────┼────────────┘
                     ▼
              ┌─────────────┐
              │  Postgres   │   ONE bulk insert per batch
              └─────────────┘
```

Everything else in this document is detail on those five boxes.

---

# Part 2 — Why each piece exists

## Why client streaming instead of unary?

Unary = one request, one response, one round trip. For logs that is wasteful:
sending 10,000 logs means 10,000 round trips, each with its own headers and
acknowledgement.

Client streaming = **open the connection once, send thousands of messages, get
one response at the end**. This is precisely the case client streaming was
designed for.

| | Unary | Client streaming |
|---|---|---|
| Connections | one per log | one per client, reused |
| Round trips | 10,000 | 1 |
| Response | one per log | one summary at the end |

## Why a worker pool and batching?

Streaming fixes the *network*. It does not fix the *database*.

If you `INSERT` once per log, Postgres still does 10,000 separate statements.
One bulk insert of 10,000 rows is a single statement. The difference is
typically 10–100x.

**Batching by size alone is not enough.** If traffic is slow, 4 logs would sit
in memory forever waiting for a 500th that never comes. So you flush on
whichever comes first:

- the batch reaches **500 rows**, or
- **2 seconds** pass since the last flush

That "whichever comes first" is the heart of task 2, and in Go it is a `select`
over a channel and a `time.Ticker`.

## Why does the outage test matter?

A log pipeline that crashes when the database hiccups is worse than no
pipeline. Three properties you are being asked to demonstrate:

1. **No crash** — a failing insert must not kill the process
2. **No loss** — the batch stays in memory and is retried
3. **No stall after recovery** — once the database is back, the backlog drains

The mechanism is: retry the flush with a delay, keep the batch until it
succeeds, and let the buffer absorb incoming logs meanwhile.

---

# Part 3 — Concepts you need first

Read this section before writing code. Each is short.

## Channels as a queue

```go
ch := make(chan LogEntry, 10000)   // buffered: holds 10,000 before blocking
ch <- entry                         // producer (the handler) pushes
entry := <-ch                       // consumer (a worker) pulls
```

A **buffered** channel is a queue with a fixed size. When it is full, the sender
**blocks** until there is room.

That blocking is a feature, not a bug: it is **backpressure**. During a database
outage the buffer fills, the handler slows down, and the client feels it -
which is far better than silently dropping logs.

## Worker pool

A worker pool is just N goroutines reading from the same channel:

```go
for i := 0; i < workers; i++ {
    go worker(ch)      // each one loops forever pulling from ch
}
```

Go distributes messages between them automatically. No locks, no coordination.

**Design choice:** give each worker its **own** batch and its **own** ticker.
Then no two workers touch the same slice, so no mutex is needed anywhere. Up to
N inserts can run in parallel.

## Flush on size OR time

This is the pattern that does the real work:

```go
ticker := time.NewTicker(2 * time.Second)
var batch []LogEntry

for {
    select {
    case entry := <-ch:
        batch = append(batch, entry)
        if len(batch) >= 500 { flush(batch); batch = nil }

    case <-ticker.C:
        if len(batch) > 0 { flush(batch); batch = nil }

    case <-ctx.Done():
        flush(batch)        // drain before exiting
        return
    }
}
```

`select` waits on several channel operations and runs whichever is ready first.
That single construct gives you "500 rows or 2 seconds, whichever comes first".

## Bulk insert in Postgres

Two options with pgx:

**`CopyFrom`** - fastest, uses Postgres's binary COPY protocol:
```go
pool.CopyFrom(ctx, pgx.Identifier{"logs"}, []string{"level","message"}, source)
```

**Multi-row INSERT** - simpler to read, still fast:
```sql
INSERT INTO logs (level, message) VALUES ($1,$2), ($3,$4), ($5,$6), ...
```

Start with `CopyFrom`. It is what it exists for, and pgx makes it a single call.

---

# Part 4 — Step-by-step plan

Each step ends with a working build. Commit after each one.

## Step 1 — The contract

Add to `log.proto` (or a new `log.proto` - your call; a separate file is
cleaner since this is a different concern):

```proto
service LogService {
  rpc IngestLogs(stream LogEntry) returns (IngestSummary);
}
```

Note where `stream` sits: on the **request** side. That is what makes it client
streaming, the mirror of `ListCustomers` where it was on the response side.

`LogEntry` needs roughly: `level`, `message`, `service`, `timestamp`.
`IngestSummary` returns how many were accepted.

Then `make proto`, and read the generated server interface - it will be
`IngestLogs(grpc.ClientStreamingServer[...]) error`. One parameter, no request
argument, because the requests arrive over the stream.

**Verify:** `go build ./...` still passes (the `Unimplemented` stub covers you).

## Step 2 — Migration

`db/migrations/0003_create_logs.sql`:

```sql
CREATE TABLE IF NOT EXISTS logs (
    id         BIGSERIAL   PRIMARY KEY,
    level      TEXT        NOT NULL,
    message    TEXT        NOT NULL,
    service    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Deliberately **no indexes beyond the primary key** at first: every index slows
inserts down, and this table is write-heavy. Add them when you actually query it.

**Verify:** `docker compose down -v && docker compose up -d` re-runs migrations
on a fresh volume. (Or apply by hand with `make db-migrate`.)

## Step 3 — Domain type and port

In `internal/domain`:

```go
type LogEntry struct {
    Level     string
    Message   string
    Service   string
    CreatedAt time.Time
}

type LogRepository interface {
    BulkInsert(ctx context.Context, entries []LogEntry) error
}
```

One method, taking a slice. That signature is the whole point - the port makes
batching explicit rather than accidental.

## Step 4 — Postgres adapter

`internal/adapter/postgres/log_repository.go`, implementing `BulkInsert` with
`pool.CopyFrom` and `pgx.CopyFromSlice`.

Add the compile-time assertion as usual:
```go
var _ domain.LogRepository = (*LogRepository)(nil)
```

**Verify:** write a quick throwaway test or a small `main` that inserts 1,000
rows and time it. Compare against a loop of 1,000 single inserts. Seeing the
difference yourself is worth more than reading that it is faster.

## Step 5 — The ingester (the heart of the task)

New package `internal/ingest`. This owns the channel, the workers, and the
batching. It depends only on `domain.LogRepository`.

Shape:

```go
type Ingester struct {
    repo    domain.LogRepository
    ch      chan domain.LogEntry
    wg      sync.WaitGroup
    // config: workers, batchSize, flushInterval
}

func New(repo domain.LogRepository, cfg Config) *Ingester
func (i *Ingester) Start(ctx context.Context)   // launches N worker goroutines
func (i *Ingester) Submit(ctx context.Context, e domain.LogEntry) error
func (i *Ingester) Stop()                       // stop accepting, drain, wait
```

- `Submit` pushes onto the channel. Use a `select` with `ctx.Done()` so a
  cancelled request does not block forever on a full buffer.
- Each worker runs the flush-on-size-or-time loop from Part 3.
- `Stop` closes the channel and uses `sync.WaitGroup` to wait for every worker
  to finish its final flush.

**Verify:** unit-test it with a fake `LogRepository` that records batches. No
database needed - the same trick as `internal/service`. Assert that submitting
500 entries produces one batch, and that 10 entries plus a wait produces one
batch after the interval.

## Step 6 — The gRPC handler

`internal/adapter/grpc/log_handler.go`:

```go
func (h *LogHandler) IngestLogs(stream pb.LogService_IngestLogsServer) error {
    var count int64
    for {
        entry, err := stream.Recv()
        if errors.Is(err, io.EOF) {
            // client finished sending - reply with the summary
            return stream.SendAndClose(&pb.IngestSummary{Accepted: count})
        }
        if err != nil {
            return err
        }
        // convert to domain, hand to the ingester
        count++
    }
}
```

Two things to notice:
- **`io.EOF` is not an error here.** It is how the client says "I am done".
  That is the single most important detail in client streaming.
- **`SendAndClose`** sends the one response and ends the RPC.

## Step 7 — Wire it up

In `internal/app/app.go`: build the log repository, build the ingester, start
it, register the handler, and make sure `Close()` calls `ingester.Stop()`
**before** closing the database pool - otherwise the final drain has nothing to
write to.

Add the knobs to `internal/config`: `LOG_WORKERS`, `LOG_BATCH_SIZE`,
`LOG_FLUSH_INTERVAL`, `LOG_BUFFER_SIZE`.

## Step 8 — Resilience (task 3)

Now make the flush survive a dead database. Inside the worker's flush:

```
attempt := 0
for {
    err := repo.BulkInsert(ctx, batch)
    if err == nil { return }        // done

    attempt++
    if attempt > maxAttempts { log it and give up }

    wait = backoff(attempt)          // 100ms, 200ms, 400ms, 800ms... capped
    sleep(wait) or return on ctx.Done()
}
```

Key points:

- **Keep the batch** until the insert succeeds. Never `batch = nil` before a
  successful flush.
- **Exponential backoff** - doubling the wait each time - so a long outage does
  not hammer a struggling database.
- **Cap the wait** (say 5s) so recovery is quick once it comes back.
- Meanwhile incoming logs pile up in the channel. That is the buffer doing its
  job. Size it for your worst expected outage: 10 seconds at 1,000 logs/sec
  needs at least 10,000 slots.

**Honest limitation, worth saying out loud to your senior:** this survives a
*database* outage, not a *process* crash. The buffer is in memory. True
zero-loss needs a disk-backed queue (or Kafka). For a 10-second database blip
with the process alive, in-memory is the right amount of engineering.

## Step 9 — Prove it

The actual demonstration for task 3:

1. Start a client sending logs continuously
2. `docker compose pause postgres`  ← freezes the container, simulating an outage
3. Wait 10 seconds, watch the retries in the logs, confirm **no crash**
4. `docker compose unpause postgres`
5. Confirm the backlog drains
6. Count rows in the database and compare to the number sent - they must match

`pause`/`unpause` is better than `stop`/`start` for this: it freezes the process
without closing connections cleanly, which is a more realistic failure.

---

# Part 5 — Order of difficulty

| Step | Difficulty | Note |
|---|---|---|
| 1. proto | easy | one keyword in a new place |
| 2. migration | easy | copy the pattern from 0001 |
| 3. domain + port | easy | two small declarations |
| 4. bulk insert | medium | `CopyFrom` is new API |
| 5. ingester | **hardest** | channels, goroutines, select, WaitGroup |
| 6. handler | medium | `io.EOF` is the trick |
| 7. wiring | easy | you have done this twice now |
| 8. retry | medium | ordering matters: keep the batch |
| 9. outage test | easy | mostly running commands |

**Step 5 is where the real learning is.** Do not rush it. If you understand the
`select` loop with a channel, a ticker, and `ctx.Done()`, you understand most of
what makes Go good at this kind of work.

---

# Part 6 — Questions worth asking your senior

- Should the pipeline drop logs under extreme load, or block the producer?
  (Dropping protects the service; blocking protects the data. Different systems
  choose differently, and it should be a deliberate decision.)
- Is in-memory buffering acceptable, or does it need to survive a process
  restart?
- Should `IngestLogs` reject invalid entries mid-stream, or accept everything
  and report a count of failures in the summary?
