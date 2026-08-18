# Refactor Plan — Applying the Patterns Yourself

A step-by-step checklist to restructure `crm-service` into the ports-and-adapters
layout described in [PATTERNS.md](PATTERNS.md).

**The rules of this refactor:**

1. **The build must be green after every step.** Never leave it broken overnight.
2. **Commit after every step.** One step = one commit.
3. **Behaviour must not change.** Same gRPC API, same responses, same errors.
   If `grpcurl` output changes, you broke something.
4. **Do not skip ahead.** Each step makes the next one possible.

---

## Step 0 — Safety net

```bash
git init && printf 'srv*\n.DS_Store\n.idea/\n' > .gitignore
git add -A && git commit -m "Working CRUD service before refactor"
```

Also capture the current behaviour so you can compare later. With the stack
running:

```bash
docker compose up -d
grpcurl -plaintext -d '{"name":"Baseline","email":"baseline@example.com"}' localhost:50051 customer.CustomerService/CreateCustomer
```

Note the id. After every step, `GetCustomer` on that id must return the same
thing.

---

## Step 1 — Create the domain package

**Goal:** one package at the centre that imports nothing but the standard library.

### Files to create

**`internal/domain/customer.go`**

```go
package domain

import "time"

// Customer is the domain entity. Moved here from internal/store.
type Customer struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}
```

**`internal/domain/errors.go`**

```go
package domain

import "errors"

var (
	ErrNotFound       = errors.New("customer not found")
	ErrDuplicateEmail = errors.New("email already exists")
)
```

### Then

- Delete `Customer`, `ErrNotFound`, `ErrDuplicateEmail` from
  `internal/store/postgres.go`.
- In `store`, import `domain` and change every `Customer` → `domain.Customer`,
  every `ErrNotFound` → `domain.ErrNotFound`, etc.
- Same in `internal/cache/redis.go`.
- In `cmd/server/main.go`, change `store.ErrNotFound` → `domain.ErrNotFound`
  and `store.Customer` → `domain.Customer`.

### Verify

```bash
go build ./... && go vet ./...
```

```bash
go list -deps ./internal/domain | grep -E "pgx|grpc|redis" && echo "LEAK!" || echo "domain is clean"
```

That second command is the architectural test — **run it after every step.**
`domain` must never depend on infrastructure.

### Gotcha

This is a pure move. If you find yourself changing logic, stop — that belongs
in a later step.

---

## Step 2 — Define the ports (interfaces)

**Goal:** describe what the service *needs*, without saying how.

**`internal/domain/repository.go`**

```go
package domain

import "context"

// CustomerRepository is the port for persistent storage.
// Implemented by internal/adapter/postgres.
type CustomerRepository interface {
	Create(ctx context.Context, c Customer) (Customer, error)
	GetByID(ctx context.Context, id int64) (Customer, error)
	Update(ctx context.Context, c Customer) (Customer, error)
	Delete(ctx context.Context, id int64) error
}

// CustomerCache is the port for caching.
// Implemented by internal/adapter/redis.
type CustomerCache interface {
	Get(ctx context.Context, id int64) (Customer, bool)
	Set(ctx context.Context, c Customer)
	Invalidate(ctx context.Context, id int64)
}
```

### Verify

```bash
go build ./...
```

Nothing uses these yet, so this compiles trivially. That is fine — you are
laying track before running a train on it.

### Note the signature change

Your current store takes `(ctx, name, email)`. The interface takes
`(ctx, Customer)`. That is deliberate: a repository stores *entities*, not loose
parameters. You will adjust the implementation in the next step.

---

## Step 3 — Move the adapters, implement the ports

**Goal:** Postgres and Redis become implementations of the interfaces above.

### Move the files

```bash
mkdir -p internal/adapter/postgres internal/adapter/redis
git mv internal/store/postgres.go internal/adapter/postgres/customer_repository.go
git mv internal/cache/redis.go internal/adapter/redis/customer_cache.go
rmdir internal/store internal/cache
```

### Edit `internal/adapter/postgres/customer_repository.go`

- `package store` → `package postgres`
- `type Store` → `type CustomerRepository`
- `func New` → `func NewCustomerRepository`
- Rename methods to match the interface:
  `CreateCustomer` → `Create`, `GetCustomer` → `GetByID`,
  `UpdateCustomer` → `Update`, `DeleteCustomer` → `Delete`
- Change `Create` and `Update` to take a `domain.Customer` instead of loose
  strings, reading `c.Name` / `c.Email` / `c.ID` inside.

**Add this line at the top of the file** — it is the most useful trick in this
whole refactor:

```go
// Compile-time proof that this type satisfies the port.
var _ domain.CustomerRepository = (*CustomerRepository)(nil)
```

If a method name or signature is wrong, **the build fails here** with a clear
message, instead of somewhere confusing later.

### Edit `internal/adapter/redis/customer_cache.go`

- `package cache` → `package redis`
- ⚠️ **Name collision:** this package is now called `redis` and it imports
  `github.com/redis/go-redis/v9`, also called `redis`. Alias the import:
  ```go
  import redisclient "github.com/redis/go-redis/v9"
  ```
- `type Cache` → `type CustomerCache`
- Add the same assertion:
  ```go
  var _ domain.CustomerCache = (*CustomerCache)(nil)
  ```

### Update `cmd/server/main.go`

```go
import (
	"github.com/iliya/crm-service/internal/adapter/postgres"
	redisadapter "github.com/iliya/crm-service/internal/adapter/redis"
	"github.com/iliya/crm-service/internal/domain"
)
```

and change the wiring to use the new constructors.

### Verify

```bash
go build ./... && go vet ./... && go list -deps ./internal/domain | grep -E "pgx|grpc|redis" && echo LEAK || echo clean
```

Then restart the stack and re-run your baseline `GetCustomer` — output must be
identical.

### Gotcha

Your nil-safe cache methods (`if c == nil`) still work, but a **nil interface
value is not the same as a nil pointer.** If you assign a nil `*CustomerCache`
to a `domain.CustomerCache` interface, the interface is *not* nil and the nil
check inside still runs — which is what you want. Keep the checks.

---

## Step 4 — Create the service layer

**Goal:** move business logic out of the gRPC handlers. This is the biggest
step and the one that matters most.

**`internal/service/customer.go`**

```go
package service

import (
	"context"
	"strings"

	"github.com/iliya/crm-service/internal/domain"
)

type CustomerService struct {
	repo  domain.CustomerRepository
	cache domain.CustomerCache
}

func NewCustomerService(repo domain.CustomerRepository, cache domain.CustomerCache) *CustomerService {
	return &CustomerService{repo: repo, cache: cache}
}

func (s *CustomerService) Create(ctx context.Context, name, email string) (domain.Customer, error)
func (s *CustomerService) GetByID(ctx context.Context, id int64) (domain.Customer, error)
func (s *CustomerService) Update(ctx context.Context, id int64, name, email string) (domain.Customer, error)
func (s *CustomerService) Delete(ctx context.Context, id int64) error
```

### What moves into these methods

**From `GetCustomer`:** the entire cache-aside block — check cache, fall back to
repo, populate cache.

**From `CreateCustomer` / `UpdateCustomer`:** the validation `if` statements.
But they return a **domain error**, not a gRPC status:

```go
if strings.TrimSpace(name) == "" {
	return domain.Customer{}, domain.ErrInvalidName
}
```

Add those to `internal/domain/errors.go`:

```go
ErrInvalidName  = errors.New("name must not be empty")
ErrInvalidEmail = errors.New("email must not be empty")
```

**From `Update` / `Delete`:** the `cache.Invalidate` calls.

### The service must not contain

- any `status.Error` or `codes.` reference
- any `pb.` type
- any SQL

If you catch yourself importing `grpc` here, the logic belongs elsewhere.

### Update the handlers to call it

For now leave the handlers in `cmd/server/main.go`; just have them call
`s.svc.GetByID(...)` and keep their error `switch`. Step 5 moves them.

### Verify

```bash
go build ./... && go vet ./...
```

```bash
go list -deps ./internal/service | grep -E "grpc|pgx|redis" && echo LEAK || echo "service is clean"
```

Restart the stack, re-run your baseline calls, confirm identical output —
including the error cases (`NotFound`, `AlreadyExists`, `InvalidArgument`).

---

## Step 5 — Move handlers into an adapter, extract error translation

**Goal:** `main.go` becomes pure wiring; handlers become three lines each.

### Files to create

**`internal/adapter/grpc/errors.go`**

```go
package grpc

func toGRPCError(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrDuplicateEmail):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrInvalidName), errors.Is(err, domain.ErrInvalidEmail):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		log.Printf("unexpected error: %v", err)
		return status.Error(codes.Internal, "internal error")
	}
}
```

**`internal/adapter/grpc/mapper.go`** — move `customerToProto` here.

**`internal/adapter/grpc/customer_handler.go`**

```go
package grpc

type CustomerHandler struct {
	pb.UnimplementedCustomerServiceServer
	svc *service.CustomerService
}

func NewCustomerHandler(svc *service.CustomerService) *CustomerHandler {
	return &CustomerHandler{svc: svc}
}

func (h *CustomerHandler) GetCustomer(ctx context.Context, req *pb.GetCustomerRequest) (*pb.GetCustomerResponse, error) {
	c, err := h.svc.GetByID(ctx, req.GetId())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return toProto(c), nil
}
```

Each of the four handlers should now be about that long.

### `cmd/server/main.go` becomes the composition root

```go
func main() {
	// ...signal context, config...

	pool := // connect postgres
	repo  := postgres.NewCustomerRepository(pool)
	cache := redisadapter.NewCustomerCache(rdb)
	svc   := service.NewCustomerService(repo, cache)
	handler := grpcadapter.NewCustomerHandler(svc)

	pb.RegisterCustomerServiceServer(grpcServer, handler)
	// ...serve + graceful shutdown...
}
```

Read top to bottom, that is your architecture in five lines.

### Gotcha

Your package is named `grpc` and you import `google.golang.org/grpc`. Alias one
of them, e.g. `grpclib "google.golang.org/grpc"`, or name your package
`grpcadapter`. Pick one and be consistent.

### Verify

```bash
go build ./... && go vet ./... && gofmt -l .
```

Full `grpcurl` pass over all four operations plus all four error cases.

---

## Step 6 — Email value object

**Goal:** make invalid emails unrepresentable, and fix normalisation.

**`internal/domain/email.go`**

```go
package domain

type Email struct {
	value string // unexported: the ONLY way to get one is NewEmail
}

func NewEmail(raw string) (Email, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return Email{}, ErrInvalidEmail
	}
	if !strings.Contains(raw, "@") {   // start simple; refine later
		return Email{}, ErrInvalidEmail
	}
	return Email{value: raw}, nil
}

func (e Email) String() string { return e.value }
```

### Where to use it

Start narrow: use it **inside the service** to validate and normalise, then
store `email.String()` in `domain.Customer`. Changing `Customer.Email` to type
`Email` touches the repository scanning and JSON caching too, so leave that for
later if it gets messy.

### Why this is worth doing

It fixes a real latent bug: `" Ali@Example.COM "` and `"ali@example.com"` are
currently different values, so your `UNIQUE` constraint lets both exist.
Normalising in one place fixes it everywhere.

### Verify

```bash
grpcurl -plaintext -d '{"name":"A","email":"  TEST@Example.COM  "}' localhost:50051 customer.CustomerService/CreateCustomer
```

Then `make db-rows` — the stored email should be `test@example.com`, trimmed and
lowercased. Creating the same address in different casing should now be
rejected with `AlreadyExists`.

---

## Step 7 — The test that proves it worked

**This step is the whole point.** If you can write this, the architecture is
correct.

**`internal/service/customer_test.go`**

```go
package service

// fakeRepo implements domain.CustomerRepository with a map. No database.
type fakeRepo struct {
	customers map[int64]domain.Customer
	nextID    int64
}

func (f *fakeRepo) GetByID(ctx context.Context, id int64) (domain.Customer, error) {
	c, ok := f.customers[id]
	if !ok {
		return domain.Customer{}, domain.ErrNotFound
	}
	return c, nil
}
// ...Create, Update, Delete...

func TestGetByID_NotFound(t *testing.T) {
	svc := NewCustomerService(&fakeRepo{customers: map[int64]domain.Customer{}}, nil)

	_, err := svc.GetByID(context.Background(), 999)

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
```

### Run it

```bash
go test ./... -v
```

**No Docker. No Postgres. No Redis. No gRPC server.** Milliseconds, not seconds.

Note `NewCustomerService(repo, nil)` — passing `nil` for the cache works because
of your nil-safe methods. That is the payoff of that design decision.

### Tests worth writing

| Test | Proves |
|---|---|
| `GetByID` returns `ErrNotFound` for a missing id | error propagation |
| `Create` rejects an empty name | validation lives in the service |
| `GetByID` hits the cache on the second call | cache-aside logic is correct |
| `Update` invalidates the cache | no stale data |

For the cache tests, write a `fakeCache` that counts calls — then assert the
repository was called *once* across two `GetByID` calls.

---

## Definition of done

- [ ] `go build ./... && go vet ./... && gofmt -l .` all clean
- [ ] `go list -deps ./internal/domain | grep -E "pgx|grpc|redis"` finds nothing
- [ ] `go test ./...` passes with no infrastructure running
- [ ] `cmd/server/main.go` contains no business logic — only wiring
- [ ] Every handler is under ~8 lines
- [ ] All four `grpcurl` operations behave exactly as they did at Step 0
- [ ] All four error cases return the same codes as at Step 0

---

## Rough effort

| Step | Difficulty | Notes |
|---|---|---|
| 1. domain package | easy | pure move, mechanical |
| 2. interfaces | easy | just typing declarations |
| 3. adapters | medium | renames + the package-name collision |
| 4. service layer | **hardest** | real thinking about what moves where |
| 5. grpc adapter | medium | mostly moving, plus the error switch |
| 6. value object | easy | small and self-contained |
| 7. tests | medium | new skill, but the most valuable one |

Steps 1–3 are mechanical. **Step 4 is where you actually learn something** —
deciding what is business logic and what is transport is the judgement this
whole exercise is teaching.

---

## If you get stuck

The compiler is your guide. Do the move, run `go build ./...`, and fix what it
reports. Go's error messages for this kind of refactor are unusually clear.

The two errors you will most likely hit:

**`cannot use x (variable of type *T) as domain.CustomerRepository value: missing method GetByID`**
→ Your method name or signature does not match the interface. The
`var _ domain.CustomerRepository = ...` assertion catches this at the source.

**`import cycle not allowed`**
→ Something in `domain` is importing `service` or an adapter. The dependency
arrow must always point inward. Delete the import and move the type instead.

Paste either error and I will point at the cause.
