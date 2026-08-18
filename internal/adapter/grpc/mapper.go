package grpc

import (
	"time"

	"github.com/iliya/crm-service/internal/domain"
	pb "github.com/iliya/crm-service/proto/customerpb"
)

// toProto converts a domain.Customer into the wire message shared by
// GetCustomer and UpdateCustomer, so neither handler repeats this
// field-by-field mapping.
func toProto(c domain.Customer) *pb.GetCustomerResponse {
	return &pb.GetCustomerResponse{
		Id:        c.ID,
		Name:      c.Name,
		Email:     c.Email,
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.Format(time.RFC3339),
	}
}
