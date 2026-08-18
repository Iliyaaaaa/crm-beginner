// Package redis is the outbound adapter for caching customer records.
//
// Like the postgres adapter it knows nothing about gRPC. Unlike it, this one is
// OPTIONAL: every method is safe to call on a nil *CustomerCache, which is what
// lets the service run with caching switched off (no REDIS_URL configured)
// without the callers needing any special-case code.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	// This package is itself named "redis", so the client library has to be
	// aliased or the two names collide.
	redisclient "github.com/redis/go-redis/v9"

	"github.com/iliya/crm-service/internal/domain"
)

// Compile-time proof that this type satisfies the port.
var _ domain.CustomerCache = (*CustomerCache)(nil)

// CustomerCache wraps a Redis client. A nil *CustomerCache is valid and behaves
// as "no cache".
type CustomerCache struct {
	rdb *redisclient.Client

	// ttl is how long a cached customer stays valid before Redis evicts it.
	//
	// This is the safety net for the invalidation bugs you WILL eventually
	// write: even if some code path forgets to invalidate, a stale entry
	// disappears on its own within this window. Shorter = fresher data but
	// fewer cache hits. Configured via CACHE_TTL.
	ttl time.Duration
}

// NewCustomerCache parses a redis:// URL, connects, and verifies the server
// responds.
func NewCustomerCache(ctx context.Context, url string, ttl time.Duration) (*CustomerCache, error) {
	opt, err := redisclient.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redisclient.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &CustomerCache{rdb: rdb, ttl: ttl}, nil
}

// Close releases the connection pool.
func (c *CustomerCache) Close() error {
	if c == nil {
		return nil
	}
	return c.rdb.Close()
}

// key builds the Redis key for one customer. Namespacing keys with a prefix
// ("customer:42") is convention: one Redis server is often shared by several
// applications, and the prefix keeps their key spaces from colliding.
func key(id int64) string {
	return fmt.Sprintf("customer:%d", id)
}

// Get returns a cached customer. The bool reports whether it was a cache HIT.
//
// A cache miss and a cache failure are deliberately indistinguishable to the
// caller: both return false, and the caller simply falls through to the
// database. Redis being down must never turn into a failed request.
func (c *CustomerCache) Get(ctx context.Context, id int64) (domain.Customer, bool) {
	if c == nil {
		return domain.Customer{}, false
	}

	data, err := c.rdb.Get(ctx, key(id)).Bytes()
	if err != nil {
		// redisclient.Nil is the "key does not exist" sentinel - an ordinary
		// miss, not a problem. Anything else is a real Redis failure, and is
		// also treated as a miss.
		if !errors.Is(err, redisclient.Nil) {
			return domain.Customer{}, false
		}
		return domain.Customer{}, false
	}

	var cust domain.Customer
	if err := json.Unmarshal(data, &cust); err != nil {
		// Corrupt or outdated entry (e.g. the struct changed shape between
		// deploys). Treat as a miss; the fresh value will overwrite it.
		return domain.Customer{}, false
	}
	return cust, true
}

// Set stores a customer with the package TTL. Errors are ignored on purpose:
// failing to populate a cache is not a reason to fail the user's request.
func (c *CustomerCache) Set(ctx context.Context, cust domain.Customer) {
	if c == nil {
		return
	}
	data, err := json.Marshal(cust)
	if err != nil {
		return
	}
	c.rdb.Set(ctx, key(cust.ID), data, c.ttl)
}

// Invalidate removes a customer from the cache. This MUST be called after every
// write - update and delete - or readers keep serving the old value until the
// TTL expires.
//
// Note it deletes rather than overwrites. Deleting is safer: if two concurrent
// updates each wrote their own value here, the cache could end up holding the
// older one. Deleting forces the next reader to re-read the database, which is
// the actual source of truth.
func (c *CustomerCache) Invalidate(ctx context.Context, id int64) {
	if c == nil {
		return
	}
	c.rdb.Del(ctx, key(id))
}
