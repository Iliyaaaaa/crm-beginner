// Package grpc is the inbound adapter: it translates protobuf messages into
// calls on the service layer, and domain errors back into gRPC status codes.
// It contains no business logic - decode, delegate, encode.
package grpc

import (
	"context"

	"github.com/iliya/crm-service/internal/service"
	pb "github.com/iliya/crm-service/proto/customerpb"
)

// CustomerHandler implements the generated CustomerServiceServer interface.
type CustomerHandler struct {
	pb.UnimplementedCustomerServiceServer
	svc *service.CustomerService
}

func NewCustomerHandler(svc *service.CustomerService) *CustomerHandler {
	return &CustomerHandler{svc: svc}
}

func (h *CustomerHandler) CreateCustomer(
	ctx context.Context,
	req *pb.CreateCustomerRequest,
) (*pb.CreateCustomerResponse, error) {
	c, err := h.svc.Create(ctx, req.GetName(), req.GetEmail())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return &pb.CreateCustomerResponse{Id: c.ID, Message: "customer created"}, nil
}

func (h *CustomerHandler) GetCustomer(
	ctx context.Context,
	req *pb.GetCustomerRequest,
) (*pb.GetCustomerResponse, error) {
	c, err := h.svc.GetByID(ctx, req.GetId())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return toProto(c), nil
}

func (h *CustomerHandler) UpdateCustomer(
	ctx context.Context,
	req *pb.UpdateCustomerRequest,
) (*pb.GetCustomerResponse, error) {
	c, err := h.svc.Update(ctx, req.GetId(), req.GetName(), req.GetEmail())
	if err != nil {
		return nil, toGRPCError(err)
	}
	return toProto(c), nil
}

func (h *CustomerHandler) DeleteCustomer(
	ctx context.Context,
	req *pb.DeleteCustomerRequest,
) (*pb.DeleteCustomerResponse, error) {
	if err := h.svc.Delete(ctx, req.GetId()); err != nil {
		return nil, toGRPCError(err)
	}
	return &pb.DeleteCustomerResponse{Message: "customer deleted"}, nil
}
