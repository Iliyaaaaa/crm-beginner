# Architecture Patterns — Explained Against Your Own Code

The seven things on your list:

1. Repository pattern
2. Service pattern
3. Adapter pattern
4. Validation pattern
5. Dependency injection
6. DDD (Domain-Driven Design)
7. Skeleton

**These are not seven unrelated topics.** They are seven views of one idea, and
together they produce a single architecture. This document explains each one,
shows what your code already does, and shows what "doing it properly" looks
like.

---

# Part 0 — The one idea underneath all seven

Every one of these patterns exists to answer the same question:

> **What does my business logic depend on?**

There are two possible answers.

**Answer A — logic depends on infrastructure.** Your rules about customers are
written inside gRPC handlers that call Postgres directly. To test a rule you
need a database. To change from gRPC to REST you rewrite the rules. To swap
Postgres for something else you touch everything.

**Answer B — infrastructure depends on logic.** Your rules sit in the middle,
knowing nothing about gRPC, Postgres or Redis. Those are plugged in around the
edges. To test a rule you need nothing. To change transport you write a new
edge.

```
        Answer A                            Answer B

   ┌──────────────┐                    ┌──────────────┐
   │ gRPC handler │                    │ gRPC handler │
   │   + rules    │                    └──────┬───────┘
   │   + SQL      │                           │ calls
   └──────────────┘                    ┌──────▼───────┐
                                       │  the rules   │  ← knows nothing
   everything knows                    │  (domain +   │    about the others
   about everything                    │   service)   │
                                       └──────┬───────┘
                                              │ calls an interface
                                       ┌──────▼───────┐
                                       │  Postgres    │
                                       └──────────────┘
```

Answer B is called **Ports and Adapters** (or Hexagonal Architecture, or Clean
Architecture — same idea, different books).

- **Repository** = the port for data
- **Service** = where the rules live
- **Adapter** = the plugs at the edges
- **Validation** = keeping bad data out of the rules
- **Dependency injection** = the mechanism that wires it
- **DDD** = the philosophy that says the rules deserve to be the centre
- **Skeleton** = the folder layout that makes all of it obvious

Your project is currently somewhere between A and B. Let's go pattern by
pattern.

---

# Part 1 — Repository pattern

## What it is

A **repository** is an object that behaves like an in-memory collection of
domain objects, hiding the fact that they actually live in a database.

You ask it `Get(id)`, `Save(customer)`, `FindByEmail(email)`. You never ask it
to "run this SQL." The point is that **the caller cannot tell what storage
technology is behind it.**

## The problem it solves

Without it, SQL leaks into your business logic. That means:

- you cannot test a rule without a live database
- changing a table breaks code in twenty files
- the same query gets written slightly differently in five places

## What your code does today

You already have a repository. This is it:

```go
// internal/store/postgres.go
type Store struct {
	pool *pgxpool.Pool
}

func (s *Store) CreateCustomer(ctx context.Context, name, email string) (Customer, error)
func (s *Store) GetCustomer(ctx context.Context, id int64) (Customer, error)
func (s *Store) UpdateCustomer(ctx context.Context, id int64, name, email string) (Customer, error)
func (s *Store) DeleteCustomer(ctx context.Context, id int64) error
```

**This is genuinely a repository.** All SQL is inside; nothing outside knows
Postgres exists; it returns domain-ish structs (`Customer`), not database rows.
You did this right in Task 2 without knowing the name for it.

## What is missing

**It is a concrete struct, not an interface.** Your handler declares:

```go
type customerServer struct {
	store *store.Store   // ← a specific implementation
	cache *cache.Cache
}
```

That `*store.Store` means the handler is welded to Postgres. You cannot swap in a
fake for testing, or an in-memory version, without changing the handler.

## What proper looks like

Define the interface **where it is used**, not where it is implemented. This is
an important Go idiom and the opposite of Java convention:

```go
// internal/domain/repository.go  — the PORT
package domain

type CustomerRepository interface {
	Create(ctx context.Context, c Customer) (Customer, error)
	GetByID(ctx context.Context, id int64) (Customer, error)
	Update(ctx context.Context, c Customer) (Customer, error)
	Delete(ctx context.Context, id int64) error
}
```

Then Postgres becomes one implementation of it:

```go
// internal/adapter/postgres/customer_repository.go  — the ADAPTER
package postgres

type CustomerRepository struct{ pool *pgxpool.Pool }

func (r *CustomerRepository) GetByID(ctx context.Context, id int64) (domain.Customer, error) {
	// the SQL you already wrote
}
```

Notice: the interface lives in `domain`, which imports nothing. The Postgres
package imports `domain`, not the other way round. **The dependency arrow points
inward.** That is the whole trick.

Now a test can do this, with no database at all:

```go
type fakeRepo struct{ customers map[int64]domain.Customer }

func (f *fakeRepo) GetByID(ctx context.Context, id int64) (domain.Customer, error) {
	c, ok := f.customers[id]
	if !ok {
		return domain.Customer{}, domain.ErrNotFound
	}
	return c, nil
}
```

## Naming

Rename `Store` → `CustomerRepository`. "Store" is vague; "Repository" tells the
next reader exactly which pattern they are looking at. Method names lose the
redundant noun too: `store.GetCustomer` becomes `repo.GetByID`, because the
repository is already about customers.

## When it is overkill

If you will genuinely never have a second implementation and never write a unit
test, the interface is ceremony. But **"I need a fake for tests" is almost
always a real second implementation**, so in practice it earns its place.

---

# Part 2 — Service pattern

## What it is

A **service** (also "application service" or "use case") holds the business
logic for one operation. It orchestrates: validate, call repositories, apply
rules, coordinate side effects.

It sits **between** transport (gRPC) and data (repository).

## The problem it solves

Without it, business logic ends up in your transport handlers. Then:

- the same rule has to be duplicated if you add a REST API or a CLI
- you cannot test a rule without constructing gRPC request objects
- handlers become long and mix three concerns

## What your code does today

**You do not have a service layer.** Your gRPC handler is doing four jobs at
once:

```go
func (s *customerServer) GetCustomer(ctx context.Context, req *pb.GetCustomerRequest) (*pb.GetCustomerResponse, error) {
	// JOB 1: caching strategy  ← business/application logic
	if c, hit := s.cache.Get(ctx, req.GetId()); hit {
		return customerToProto(c), nil
	}

	// JOB 2: data access orchestration  ← application logic
	c, err := s.store.GetCustomer(ctx, req.GetId())

	// JOB 3: error translation  ← transport concern
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, ...)
	...
	}

	s.cache.Set(ctx, c)

	// JOB 4: protobuf mapping  ← transport concern
	return customerToProto(c), nil
}
```

Jobs 1 and 2 are application logic — they would be identical if you exposed
this over REST. Jobs 3 and 4 are gRPC-specific. They are tangled together.

**The cache-aside logic is the clearest symptom.** "Check cache, fall back to
database, populate cache" is a business decision about performance. It has
nothing to do with gRPC, yet it lives in a gRPC handler.

## What proper looks like

```go
// internal/service/customer.go
package service

type CustomerService struct {
	repo  domain.CustomerRepository
	cache domain.CustomerCache
}

func NewCustomerService(repo domain.CustomerRepository, cache domain.CustomerCache) *CustomerService {
	return &CustomerService{repo: repo, cache: cache}
}

func (s *CustomerService) GetCustomer(ctx context.Context, id int64) (domain.Customer, error) {
	if c, hit := s.cache.Get(ctx, id); hit {
		return c, nil
	}

	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return domain.Customer{}, err   // domain error, NOT a gRPC status
	}

	s.cache.Set(ctx, c)
	return c, nil
}
```

And the handler shrinks to pure translation:

```go
// internal/adapter/grpc/customer_handler.go
func (h *CustomerHandler) GetCustomer(ctx context.Context, req *pb.GetCustomerRequest) (*pb.GetCustomerResponse, error) {
	c, err := h.svc.GetCustomer(ctx, req.GetId())
	if err != nil {
		return nil, toGRPCError(err)     // one shared translator
	}
	return toProto(c), nil
}
```

**Three lines.** That is what a transport handler should look like: decode,
delegate, encode.

## The payoff

Add a REST API tomorrow and you write a new handler that calls the same
`CustomerService`. The caching, the rules, the error semantics — all reused, none
duplicated.

## When it is overkill

For a pure CRUD pass-through with no logic at all, a service layer can be an
empty tunnel that just forwards calls. Some teams accept that for consistency;
others skip it until there is actual logic. **Your service already has real
logic** (caching strategy, validation rules), so it is justified here.

---

# Part 3 — Adapter pattern

## What it is

An **adapter** converts between two representations that cannot talk directly.

Classic definition: it makes an incompatible interface compatible. In practice
in backend work, an adapter is the code at the edge of your system that
translates the outside world into your domain's language, and back.

## Two kinds, and you have both

**Driving adapters (inbound)** — something outside calls you.
Your gRPC handler is a driving adapter: it turns protobuf messages into calls
on your logic.

**Driven adapters (outbound)** — you call something outside.
Your Postgres repository and Redis cache are driven adapters: they turn your
logic's requests into SQL and Redis commands.

```
    ┌──────────┐        ┌───────────────┐        ┌──────────────┐
    │  gRPC    │───────▶│   your logic  │───────▶│  Postgres    │
    │ handler  │        │  (domain +    │        │  repository  │
    │(inbound  │        │   service)    │        │  (outbound   │
    │ adapter) │        └───────────────┘        │   adapter)   │
    └──────────┘                │                └──────────────┘
                                │                ┌──────────────┐
                                └───────────────▶│    Redis     │
                                                 │    cache     │
                                                 └──────────────┘
```

## What your code does today

You already have a small, textbook adapter:

```go
// cmd/server/main.go
func customerToProto(c store.Customer) *pb.GetCustomerResponse {
	return &pb.GetCustomerResponse{
		Id:        c.ID,
		Name:      c.Name,
		Email:     c.Email,
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
}
```

**This function is the adapter pattern in miniature.** It converts your internal
type into the wire type. Note what it does that matters: `time.Time` becomes an
RFC 3339 *string*, because protobuf has no native Go time type. That conversion
is exactly the "incompatible interfaces" the pattern is named for.

You also, deliberately, kept `store.Customer` separate from
`pb.GetCustomerResponse` — meaning you already refused to let the wire format
be your internal model. That decision is what makes the adapter necessary and
correct.

Your store is an adapter too:

```go
err := s.pool.QueryRow(ctx, q, id).Scan(&c.ID, &c.Name, &c.Email, &c.CreatedAt, &c.UpdatedAt)
```

`Scan` adapts database columns into Go struct fields.

## What proper looks like

Nothing structural is wrong here — you just move the adapters to the edges and
name them:

```
internal/adapter/grpc/mapper.go       toProto / fromProto
internal/adapter/postgres/mapper.go   rowToCustomer
internal/adapter/redis/mapper.go      JSON encode / decode
```

And add the missing one — a single error adapter, replacing the `switch`
repeated in all four handlers:

```go
// internal/adapter/grpc/errors.go
func toGRPCError(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrDuplicateEmail):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.As(err, &domain.ValidationError{}):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		log.Printf("unexpected: %v", err)
		return status.Error(codes.Internal, "internal error")
	}
}
```

That function **is** the adapter between your domain's error vocabulary and
gRPC's. You wrote this logic four times inline; the pattern says write it once.

---

# Part 4 — Validation pattern

## What it is

Deciding **where** data gets checked, and **what** guarantees the rest of your
code can rely on.

There is no single "validation pattern" — there are layers, and the pattern is
knowing which check belongs in which layer.

## The four layers

| Layer | Checks | Example |
|---|---|---|
| **Transport** | is the request well-formed? | field present, parseable |
| **Application** | are the inputs acceptable? | name non-empty, email looks like an email |
| **Domain** | is this a valid business object? | a Customer *cannot exist* without a valid email |
| **Database** | final integrity guarantee | `NOT NULL`, `UNIQUE`, foreign keys |

**The deepest layer is the only one that cannot be bypassed.** That is why the
`UNIQUE` constraint matters even though you also check in Go — as you saw with
the duplicate-email case, application-level checks are a lie under concurrency.

## What your code does today

Validation is inline, in the transport layer, duplicated:

```go
// in CreateCustomer
if req.GetName() == "" {
	return nil, status.Error(codes.InvalidArgument, "name must not be empty")
}
if req.GetEmail() == "" {
	return nil, status.Error(codes.InvalidArgument, "email must not be empty")
}

// in UpdateCustomer — the SAME four lines again
```

Three problems:

1. **Duplicated** between Create and Update.
2. **In the wrong layer** — it returns a gRPC status, so a future REST endpoint
   would need its own copy.
3. **Weak** — `email` only has to be non-empty. `"x"` passes.

## What proper looks like — three escalating options

### Option 1: a validation method on the request

Simplest improvement. Keeps rules in one place:

```go
func validateCustomerInput(name, email string) error {
	if strings.TrimSpace(name) == "" {
		return domain.NewValidationError("name", "must not be empty")
	}
	if !strings.Contains(email, "@") {
		return domain.NewValidationError("email", "must be a valid email address")
	}
	return nil
}
```

Called by the **service**, not the handler — so every transport gets it free.

### Option 2: value objects (this is the DDD answer)

Make invalid states **unrepresentable**. Instead of passing `string` around,
create a type that can only exist if valid:

```go
// internal/domain/email.go
type Email struct {
	value string
}

func NewEmail(raw string) (Email, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if !emailRegex.MatchString(raw) {
		return Email{}, NewValidationError("email", "invalid format")
	}
	return Email{value: raw}, nil
}

func (e Email) String() string { return e.value }
```

Because `value` is unexported, **the only way to obtain an `Email` is through
`NewEmail`**, which validates. Now this signature is self-documenting and
impossible to misuse:

```go
func (s *CustomerService) Create(ctx context.Context, name Name, email Email) (Customer, error)
```

You literally cannot pass an invalid email to it. There is no check inside
because there is nothing left to check. This is the single most powerful idea
in this whole document.

Notice the constructor also **normalises** (trim, lowercase) — so
`"  Ali@Example.COM "` and `"ali@example.com"` become the same value, and your
`UNIQUE` constraint starts working the way users expect.

### Option 3: a validation library

For large request structs, tag-based validation (`go-playground/validator`)
reduces boilerplate:

```go
type CreateCustomerInput struct {
	Name  string `validate:"required,min=2,max=100"`
	Email string `validate:"required,email"`
}
```

Convenient, but the rules become strings in tags rather than compiler-enforced
types. Good for large forms; weaker than value objects for core domain concepts.

## Recommendation for your project

Option 1 now (cheap, removes the duplication), Option 2 for `Email`
specifically (it is your uniqueness key, so normalisation genuinely matters).

---

# Part 5 — Dependency injection

## What it is

**Giving an object its dependencies from outside, instead of letting it create
them.**

That is the entire concept. In Go it needs no framework — it is just passing
arguments to a constructor.

```go
// NOT dependency injection — the service creates its own dependency
func NewCustomerService() *CustomerService {
	pool, _ := pgxpool.New(ctx, "postgres://...")   // welded to Postgres forever
	return &CustomerService{pool: pool}
}

// Dependency injection — the dependency is handed in
func NewCustomerService(repo domain.CustomerRepository) *CustomerService {
	return &CustomerService{repo: repo}
}
```

## What your code does today

**You already do constructor injection.** This line is DI:

```go
// cmd/server/main.go
pb.RegisterCustomerServiceServer(s, &customerServer{store: st, cache: rc})
```

The handler does not create the store or the cache. `main` creates them and
hands them in. That is the pattern, correctly applied.

## What is missing: depending on abstractions

You inject **concrete types**:

```go
type customerServer struct {
	store *store.Store   // concrete
	cache *cache.Cache   // concrete
}
```

DI gives its full benefit only when you depend on an **interface**:

```go
type CustomerService struct {
	repo  domain.CustomerRepository   // interface
	cache domain.CustomerCache        // interface
}
```

This is the **Dependency Inversion Principle** (the D in SOLID), and it is a
different thing from dependency injection despite the similar name:

- **Injection** = who supplies the dependency (outside, not inside)
- **Inversion** = what type the dependency is (an abstraction, not a concrete)

You have injection. You do not yet have inversion.

## The composition root

There should be exactly **one** place in the program that knows how everything
is wired together. That place is `main()`, and it is called the **composition
root**.

```go
func main() {
	// 1. build the outermost adapters
	pool := mustConnectPostgres(dsn)
	rdb  := mustConnectRedis(redisURL)

	// 2. wrap them as domain ports
	repo  := postgres.NewCustomerRepository(pool)
	cache := redisadapter.NewCustomerCache(rdb)

	// 3. inject into the service
	svc := service.NewCustomerService(repo, cache)

	// 4. inject the service into the transport adapter
	handler := grpcadapter.NewCustomerHandler(svc)

	// 5. serve
	pb.RegisterCustomerServiceServer(grpcServer, handler)
}
```

Read that top to bottom: it is a picture of your entire architecture in fifteen
lines. **Nothing else in the program should call `pgxpool.New` or
`redis.NewClient`.** That is the rule that makes the whole thing hold together.

## Do you need a DI framework?

**No.** Go's community strongly prefers manual wiring — it is explicit,
debuggable, and compiler-checked. `google/wire` exists for very large graphs
(it generates the wiring code at compile time), but for a service this size,
manual wiring in `main` is the right answer and will stay right for a long time.

---

# Part 6 — DDD (Domain-Driven Design)

## What it is

DDD is a **philosophy**, not a folder structure: the business domain is the most
important part of your software, so it should be modelled explicitly and kept
free of technical noise.

It comes from Eric Evans' 2003 book. It has two halves, and most people only
learn the first.

## Strategic DDD (the big half — and the one your senior probably cares about)

**Ubiquitous language.** Developers and domain experts use the *same words*. If
sales people say "lead" and "opportunity" mean different things, your code must
not have a single `Deal` type covering both. Your `.proto` file is where this
language becomes concrete.

**Bounded contexts.** The same word means different things in different parts of
a business. "Customer" in Sales = a prospect with a pipeline stage. "Customer"
in Billing = an entity with a payment method and tax ID. **These should be two
different types in two different services**, not one shared "Customer" object
that grows fields forever.

This is directly relevant to you: `CRM_ANALYSIS.md` proposes splitting into
Customer / Sales / Activity / Billing services. **Those are bounded contexts.**
DDD is the theory behind why that split is the right one — you draw service
boundaries where the *language* changes, not where the tables happen to sit.

## Tactical DDD (the building blocks)

| Block | Definition | In your project |
|---|---|---|
| **Entity** | has identity that persists through change | `Customer` — id 6 is still customer 6 after a rename |
| **Value object** | no identity, defined only by its values, immutable | `Email`, `Money`, `DateRange` — you have none yet |
| **Aggregate** | a cluster of objects treated as one unit, with a root | `Customer` is a trivial one-entity aggregate |
| **Aggregate root** | the only entry point to the cluster | `Customer` |
| **Repository** | collection-like access to aggregates | your `store` — one repository *per aggregate root* |
| **Domain service** | logic that does not belong to any single entity | e.g. "transfer accounts between owners" |
| **Domain event** | something meaningful happened | `CustomerCreated`, `OpportunityWon` |

## What your code does today

You have an **anemic domain model** — the standard term for this:

```go
type Customer struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
}
```

A bag of public fields with no behaviour and no invariants. Any code anywhere
can set `Email = "not-an-email"` and nothing stops it. All the actual rules live
outside, in handlers.

**This is not automatically wrong.** For genuine CRUD, an anemic model is
simpler and perfectly appropriate — plenty of experienced engineers argue DDD is
over-applied. It becomes wrong when business rules multiply and end up scattered
across handlers because there is no home for them.

## What a rich domain model looks like

```go
// internal/domain/customer.go
package domain

type Customer struct {
	id        int64        // unexported: nothing outside can corrupt state
	name      Name
	email     Email
	createdAt time.Time
	updatedAt time.Time
}

// NewCustomer is the only way to create one, so a Customer is always valid.
func NewCustomer(name Name, email Email) Customer {
	now := time.Now()
	return Customer{name: name, email: email, createdAt: now, updatedAt: now}
}

// Rename is behaviour that belongs to the entity, not to a handler.
func (c *Customer) Rename(n Name) {
	c.name = n
	c.updatedAt = time.Now()
}

func (c Customer) ID() int64      { return c.id }
func (c Customer) Name() Name     { return c.name }
func (c Customer) Email() Email   { return c.email }
```

The invariant "a Customer always has a valid name and email" is now enforced by
the **type system**, not by remembering to check.

The cost is real: unexported fields mean you need explicit mapping in the
repository (you cannot just `Scan` into them), and getters add noise. That is
the trade DDD asks you to make.

## Honest advice

For your current 4-endpoint CRUD service, full tactical DDD is **overkill** —
and saying so to your senior, with the reasoning, will impress more than
cargo-culting it.

What is genuinely worth adopting now:

- **the `domain` package** as the dependency-free centre
- **value objects for `Email`** (normalisation matters for your `UNIQUE` key)
- **domain errors** instead of store-specific ones
- **strategic DDD** for the microservice split — this is the part that actually
  matters at your scale

---

# Part 7 — Skeleton

## What it is

"Skeleton" has two meanings and both are probably intended.

### Meaning 1 — the standard project layout

A conventional folder structure so that any engineer opening any service knows
where things are.

Your current layout is already reasonable:

```
cmd/          entry points (one folder per binary)
internal/     private code — compiler-enforced, cannot be imported by other modules
proto/        contracts
db/           migrations
```

The layout that all seven patterns produce:

```
crm-service/
├── cmd/
│   └── server/main.go              composition root — wiring ONLY
│
├── internal/
│   ├── domain/                     ← the centre. imports NOTHING.
│   │   ├── customer.go             entity
│   │   ├── email.go                value object
│   │   ├── errors.go               domain errors
│   │   └── repository.go           port interfaces
│   │
│   ├── service/                    ← business logic
│   │   └── customer.go             use cases, orchestration, caching policy
│   │
│   └── adapter/                    ← everything touching the outside world
│       ├── grpc/                   inbound: handlers, mappers, error translation
│       ├── postgres/               outbound: repository implementation
│       └── redis/                  outbound: cache implementation
│
├── proto/customerpb/
├── db/migrations/
├── Dockerfile
└── docker-compose.yml
```

**The rule that makes this work:** dependencies point inward.
`adapter` → `service` → `domain`. Never the reverse. `domain` imports nothing
but the standard library.

You can verify this mechanically — if `go list -deps ./internal/domain` ever
shows `pgx` or `grpc`, someone broke the architecture.

### Meaning 2 — a reusable service template

Once you have this structure working, you extract it into a **skeleton
repository**: a template with all the plumbing and no business logic, so the
next microservice starts in an hour instead of a week.

A good skeleton contains:

- the folder structure above, with one trivial example entity
- `../Dockerfile` + `../docker-compose.yml`
- `../Makefile` with `proto`, `build`, `test`, `lint`, `run`
- config loading from environment
- structured logging, health checks, graceful shutdown
- database connection + migration setup
- CI pipeline config
- a `README` explaining where to put what

**Your project is roughly 70% of a skeleton already.** Once you finish the
Sales or Activity service, the parts you copied are exactly the parts that
belong in the template.

---

# Part 8 — Putting it together

Here is one operation, `GetCustomer`, in the target architecture. Follow the
types as they cross each boundary:

```go
// 1. ADAPTER (inbound) — protobuf in, protobuf out. No logic.
func (h *CustomerHandler) GetCustomer(ctx context.Context, req *pb.GetCustomerRequest) (*pb.GetCustomerResponse, error) {
	c, err := h.svc.GetCustomer(ctx, req.GetId())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return toProto(c), nil
}

// 2. SERVICE — application logic. Knows domain, not gRPC or SQL.
func (s *CustomerService) GetCustomer(ctx context.Context, id int64) (domain.Customer, error) {
	if c, hit := s.cache.Get(ctx, id); hit {
		return c, nil
	}
	c, err := s.repo.GetByID(ctx, id)     // ← an INTERFACE
	if err != nil {
		return domain.Customer{}, err
	}
	s.cache.Set(ctx, c)
	return c, nil
}

// 3. DOMAIN — the port. Defines what is needed, not how.
type CustomerRepository interface {
	GetByID(ctx context.Context, id int64) (Customer, error)
}

// 4. ADAPTER (outbound) — SQL. Knows domain, nothing above it.
func (r *CustomerRepository) GetByID(ctx context.Context, id int64) (domain.Customer, error) {
	// your existing SQL
}
```

Each pattern's contribution:

| Pattern | Where it shows up above |
|---|---|
| Repository | step 3, the interface; step 4, its implementation |
| Service | step 2 |
| Adapter | steps 1 and 4 |
| Validation | inside `domain.NewEmail`, called before this path |
| DI | `h.svc`, `s.repo`, `s.cache` — all injected by `main` |
| DDD | `domain.Customer` as the shared language across all layers |
| Skeleton | the folder each file lives in |

---

# Part 9 — Suggested order to learn/apply

Do not do all seven at once. Each step is independently useful:

| # | Step | Why first |
|---|---|---|
| 1 | Create `../internal/domain` with `Customer` + errors | everything else depends on having a centre |
| 2 | Define `CustomerRepository` interface in `domain` | makes step 3 possible |
| 3 | Move `store` → `adapter/postgres`, implement the interface | repository pattern complete |
| 4 | Create `internal/service`, move caching + rules out of the handler | service pattern; handlers shrink to 3 lines |
| 5 | Extract `toGRPCError` once | removes four duplicated switches |
| 6 | Add an `Email` value object | validation done properly, and fixes normalisation |
| 7 | Write a test with a fake repository | **this is the payoff** — proves it worked |

**Step 7 is the real test of whether you understood any of this.** If you can
write a meaningful test with no database and no gRPC server running, the
architecture is correct. If you cannot, something is still coupled.

---

# Part 10 — What to say to your senior

Two things worth raising, because they show judgement rather than compliance:

**1. "Which of these does the team actually use?"** These patterns have many
variants, and teams have house styles. Ask to see an existing service and
follow its conventions rather than inventing your own.

**2. "How much of this is right for a service this small?"** Full DDD on four
CRUD endpoints is over-engineering, and a good senior knows it. The honest
position — *"repository and service layers earn their place immediately because
they make testing possible; rich domain entities I would add when the rules
justify them"* — is a stronger answer than applying everything uniformly.

---

# Summary

| Pattern | One-line definition | Your status |
|---|---|---|
| **Repository** | collection-like data access that hides storage | ✅ have it, ❌ not an interface |
| **Service** | business logic between transport and data | ❌ missing — logic is in handlers |
| **Adapter** | translates between the outside world and your domain | ✅ `customerToProto`, ❌ error adapter duplicated |
| **Validation** | which layer guarantees what | ⚠️ inline, duplicated, weak |
| **DI** | dependencies handed in from outside | ✅ injected, ❌ concrete not abstract |
| **DDD** | model the business, keep it free of tech | ⚠️ anemic model; strategic DDD already applied in your service split |
| **Skeleton** | the layout, and a reusable template | ⚠️ good foundation, needs domain/service/adapter split |

**The single sentence version:** put your business rules in the middle, make
everything else plug into them from the outside, and hand each piece its
dependencies rather than letting it reach for them.
