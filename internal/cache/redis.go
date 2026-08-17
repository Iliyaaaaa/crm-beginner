// Package cache is a thin Redis-backed cache for customer records.
//
// Like the store package, it knows nothing about gRPC. Unlike the store, it is
// OPTIONAL: every method is safe to call on a nil *Cache, which is what lets
// the service run with caching switched off (no REDIS_URL configured) without
// the handlers needing any special-case code.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/iliya/crm-service/internal/domain"
)

// ttl is how long a cached customer stays valid before Redis evicts it.
//
// This is the safety net for the invalidation bugs you WILL eventually write:
// even if some code path forgets to invalidate, a stale entry disappears on its
// own within this window. Shorter = fresher data but fewer cache hits.
const ttl = 5 * time.Minute

// Cache wraps a Redis client. A nil *Cache is valid and behaves as "no cache".
type Cache struct {
	rdb *redis.Client
}

// New parses a redis:// URL, connects, and verifies the server responds.
func New(ctx context.Context, url string) (*Cache, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Cache{rdb: rdb}, nil
}

// Close releases the connection pool.
func (c *Cache) Close() error {
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
func (c *Cache) Get(ctx context.Context, id int64) (domain.Customer, bool) {
	if c == nil {
		return domain.Customer{}, false
	}

	data, err := c.rdb.Get(ctx, key(id)).Bytes()
	if err != nil {
		// redis.Nil is the "key does not exist" sentinel - an ordinary miss,
		// not a problem. Anything else is a real Redis failure, and is also
		// treated as a miss.
		if !errors.Is(err, redis.Nil) {
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
func (c *Cache) Set(ctx context.Context, cust domain.Customer) {
	if c == nil {
		return
	}
	data, err := json.Marshal(cust)
	if err != nil {
		return
	}
	c.rdb.Set(ctx, key(cust.ID), data, ttl)
}

// Invalidate removes a customer from the cache. This MUST be called after every
// write - update and delete - or readers keep serving the old value until the
// TTL expires.
//
// Note it deletes rather than overwrites. Deleting is safer: if two concurrent
// updates each wrote their own value here, the cache could end up holding the
// older one. Deleting forces the next reader to re-read the database, which is
// the actual source of truth.
func (c *Cache) Invalidate(ctx context.Context, id int64) {
	if c == nil {
		return
	}
	c.rdb.Del(ctx, key(id))
}
