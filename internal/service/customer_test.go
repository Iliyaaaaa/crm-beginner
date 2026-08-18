package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iliya/crm-service/internal/domain"
)

// --- fakes ------------------------------------------------------------
//
// Neither fake touches a database or Redis. That absence is the entire point
// of this file: if CustomerService needed real infrastructure to be tested,
// the architecture would have failed at its one job.

type fakeRepo struct {
	customers map[int64]domain.Customer
	nextID    int64
	getCalls  int
}

var _ domain.CustomerRepository = (*fakeRepo)(nil)

func newFakeRepo() *fakeRepo {
	return &fakeRepo{customers: make(map[int64]domain.Customer)}
}

func (f *fakeRepo) Create(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	for _, existing := range f.customers {
		if existing.Email == c.Email {
			return domain.Customer{}, domain.ErrDuplicateEmail
		}
	}
	f.nextID++
	c.ID = f.nextID
	c.CreatedAt = time.Now()
	c.UpdatedAt = c.CreatedAt
	f.customers[c.ID] = c
	return c, nil
}

func (f *fakeRepo) GetByID(ctx context.Context, id int64) (domain.Customer, error) {
	f.getCalls++
	c, ok := f.customers[id]
	if !ok {
		return domain.Customer{}, domain.ErrNotFound
	}
	return c, nil
}

func (f *fakeRepo) Update(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	existing, ok := f.customers[c.ID]
	if !ok {
		return domain.Customer{}, domain.ErrNotFound
	}
	for id, other := range f.customers {
		if id != c.ID && other.Email == c.Email {
			return domain.Customer{}, domain.ErrDuplicateEmail
		}
	}
	existing.Name = c.Name
	existing.Email = c.Email
	existing.UpdatedAt = time.Now()
	f.customers[c.ID] = existing
	return existing, nil
}

func (f *fakeRepo) Delete(ctx context.Context, id int64) error {
	if _, ok := f.customers[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.customers, id)
	return nil
}

// fakeCache checks for a nil receiver on every method, mirroring the real
// redis adapter. That is deliberate: TestNilCache_DoesNotPanic below passes a
// typed-nil *fakeCache to prove the service tolerates a disabled cache -
// which only works because the fake behaves like the real adapter here.
type fakeCache struct {
	data map[int64]domain.Customer
}

var _ domain.CustomerCache = (*fakeCache)(nil)

func newFakeCache() *fakeCache {
	return &fakeCache{data: make(map[int64]domain.Customer)}
}

func (f *fakeCache) Get(ctx context.Context, id int64) (domain.Customer, bool) {
	if f == nil {
		return domain.Customer{}, false
	}
	c, ok := f.data[id]
	return c, ok
}

func (f *fakeCache) Set(ctx context.Context, c domain.Customer) {
	if f == nil {
		return
	}
	f.data[c.ID] = c
}

func (f *fakeCache) Invalidate(ctx context.Context, id int64) {
	if f == nil {
		return
	}
	delete(f.data, id)
}

// --- tests --------------------------------------------------------------

func TestGetByID_NotFound(t *testing.T) {
	svc := NewCustomerService(newFakeRepo(), newFakeCache())

	_, err := svc.GetByID(context.Background(), 999)

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreate_RejectsEmptyName(t *testing.T) {
	svc := NewCustomerService(newFakeRepo(), newFakeCache())

	_, err := svc.Create(context.Background(), "", "valid@example.com")

	if !errors.Is(err, domain.ErrInvalidName) {
		t.Fatalf("expected ErrInvalidName, got %v", err)
	}
}

func TestCreate_RejectsInvalidEmail(t *testing.T) {
	svc := NewCustomerService(newFakeRepo(), newFakeCache())

	_, err := svc.Create(context.Background(), "Valid Name", "not-an-email")

	if !errors.Is(err, domain.ErrInvalidEmail) {
		t.Fatalf("expected ErrInvalidEmail, got %v", err)
	}
}

func TestCreate_NormalisesEmail(t *testing.T) {
	svc := NewCustomerService(newFakeRepo(), newFakeCache())

	c, err := svc.Create(context.Background(), "Ali", "  Ali@Example.COM  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Email != "ali@example.com" {
		t.Fatalf("expected normalised email %q, got %q", "ali@example.com", c.Email)
	}
}

func TestCreate_DuplicateEmail(t *testing.T) {
	svc := NewCustomerService(newFakeRepo(), newFakeCache())
	ctx := context.Background()

	if _, err := svc.Create(ctx, "First", "dup@example.com"); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Different casing on purpose: proves normalisation happens before the
	// uniqueness check, not just on display.
	_, err := svc.Create(ctx, "Second", "DUP@Example.com")
	if !errors.Is(err, domain.ErrDuplicateEmail) {
		t.Fatalf("expected ErrDuplicateEmail, got %v", err)
	}
}

// TestGetByID_CachesOnMiss is the test that actually proves the cache-aside
// logic works: the repository must be hit exactly once across two reads.
func TestGetByID_CachesOnMiss(t *testing.T) {
	repo := newFakeRepo()
	svc := NewCustomerService(repo, newFakeCache())
	ctx := context.Background()

	created, err := svc.Create(ctx, "Ali", "ali@example.com")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := svc.GetByID(ctx, created.ID); err != nil {
		t.Fatalf("first GetByID: %v", err)
	}
	if repo.getCalls != 1 {
		t.Fatalf("expected 1 repo call after a cache miss, got %d", repo.getCalls)
	}

	if _, err := svc.GetByID(ctx, created.ID); err != nil {
		t.Fatalf("second GetByID: %v", err)
	}
	if repo.getCalls != 1 {
		t.Fatalf("expected repo calls to stay at 1 on a cache hit, got %d", repo.getCalls)
	}
}

// TestUpdate_InvalidatesCache proves an update can never leave a stale entry
// behind - the bug class that made Invalidate mandatory in the first place.
func TestUpdate_InvalidatesCache(t *testing.T) {
	repo := newFakeRepo()
	cache := newFakeCache()
	svc := NewCustomerService(repo, cache)
	ctx := context.Background()

	created, err := svc.Create(ctx, "Ali", "ali@example.com")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := svc.GetByID(ctx, created.ID); err != nil { // warm the cache
		t.Fatalf("warm cache: %v", err)
	}
	if _, ok := cache.data[created.ID]; !ok {
		t.Fatalf("setup: expected cache to be warmed")
	}

	if _, err := svc.Update(ctx, created.ID, "Ali Renamed", "ali@example.com"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, ok := cache.data[created.ID]; ok {
		t.Fatalf("expected cache entry to be gone after Update")
	}
}

func TestDelete_NotFound(t *testing.T) {
	svc := NewCustomerService(newFakeRepo(), newFakeCache())

	err := svc.Delete(context.Background(), 999)

	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestNilCache_DoesNotPanic mirrors main.go: a nil *redisadapter.CustomerCache
// gets boxed into the domain.CustomerCache interface when REDIS_URL is unset.
// That is a NON-nil interface holding a nil pointer, which is why the service
// can call methods on it safely - as long as, like the real adapter, every
// method checks its own receiver for nil before touching any field.
//
// Passing a bare `nil` instead of a typed *fakeCache(nil) would be a
// different, unrelated case: calling a method on a truly nil interface value
// panics immediately, because there is no concrete type to dispatch to.
func TestNilCache_DoesNotPanic(t *testing.T) {
	var nilCache *fakeCache
	svc := NewCustomerService(newFakeRepo(), nilCache)
	ctx := context.Background()

	created, err := svc.Create(ctx, "Ali", "ali@example.com")
	if err != nil {
		t.Fatalf("Create with nil cache: %v", err)
	}

	got, err := svc.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID with nil cache: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("expected id %d, got %d", created.ID, got.ID)
	}
}
