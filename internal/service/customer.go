// Package service holds the application logic for customers: validation,
// caching strategy, and orchestration between the repository and the cache.
//
// This package must never import gRPC, protobuf, pgx, or redis. It knows only
// domain types and the two port interfaces. That is what lets it be tested
// with fakes and reused behind a different transport (REST, CLI, ...) with no
// changes.
package service

import (
	"context"
	"log"
	"strings"

	"github.com/iliya/crm-service/internal/domain"
)

type CustomerService struct {
	repo  domain.CustomerRepository
	cache domain.CustomerCache
}

// NewCustomerService wires the service to its two dependencies. cache may be
// nil - every domain.CustomerCache method used here must tolerate that ,
// which is true of the redis adapter's nil-receiver checks.
func NewCustomerService(repo domain.CustomerRepository, cache domain.CustomerCache) *CustomerService {
	return &CustomerService{repo: repo, cache: cache}
}

// Create validates input and inserts a new customer.
func (s *CustomerService) Create(ctx context.Context, name, email string) (domain.Customer, error) {
	if strings.TrimSpace(name) == "" {
		return domain.Customer{}, domain.ErrInvalidName
	}
	if strings.TrimSpace(email) == "" {
		return domain.Customer{}, domain.ErrInvalidEmail
	}

	c, err := s.repo.Create(ctx, domain.Customer{Name: name, Email: email})
	if err != nil {
		return domain.Customer{}, err
	}

	log.Printf("created customer id=%d name=%q email=%q", c.ID, c.Name, c.Email)
	return c, nil
}

// GetByID implements cache-aside: check the cache first, fall back to the
// repository on a miss, then populate the cache for next time.
func (s *CustomerService) GetByID(ctx context.Context, id int64) (domain.Customer, error) {
	if c, hit := s.cache.Get(ctx, id); hit {
		log.Printf("fetched customer id=%d (cache HIT)", c.ID)
		return c, nil
	}

	c, err := s.repo.GetByID(ctx, id)
	if err != nil {
		// Deliberately not cached. Caching "this does not exist" (negative
		// caching) is possible, but then Create would have to invalidate the
		// negative entry too - more invalidation paths to get wrong, for
		// little benefit at this scale.
		return domain.Customer{}, err
	}

	s.cache.Set(ctx, c)
	log.Printf("fetched customer id=%d (cache MISS -> db)", c.ID)
	return c, nil
}

// Update validates input, replaces name and email, and invalidates the cache.
// This is a full replace (like HTTP PUT), not a partial patch.
func (s *CustomerService) Update(ctx context.Context, id int64, name, email string) (domain.Customer, error) {
	if strings.TrimSpace(name) == "" {
		return domain.Customer{}, domain.ErrInvalidName
	}
	if strings.TrimSpace(email) == "" {
		return domain.Customer{}, domain.ErrInvalidEmail
	}

	c, err := s.repo.Update(ctx, domain.Customer{ID: id, Name: name, Email: email})
	if err != nil {
		return domain.Customer{}, err
	}

	// Invalidate AFTER the write succeeds. Invalidating first would leave a
	// window where a concurrent reader could re-populate the cache with the
	// old row just before this update lands.
	s.cache.Invalidate(ctx, c.ID)

	log.Printf("updated customer id=%d name=%q email=%q (cache invalidated)", c.ID, c.Name, c.Email)
	return c, nil
}

// Delete removes a customer and invalidates the cache.
func (s *CustomerService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	s.cache.Invalidate(ctx, id)
	log.Printf("deleted customer id=%d (cache invalidated)", id)
	return nil
}
