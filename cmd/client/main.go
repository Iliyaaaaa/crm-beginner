package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/iliya/crm-service/proto/customerpb"
)

func main() {
	conn, err := grpc.NewClient(
		"localhost:50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("failed to create client: %v", err)
	}
	defer conn.Close()

	client := pb.NewCustomerServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Unique per run, so the script can be re-run against a database that
	// already holds rows from previous runs.
	email := fmt.Sprintf("ali+%d@example.com", time.Now().UnixNano())

	// --- 1. Create a customer ---------------------------------------------
	created, err := client.CreateCustomer(ctx, &pb.CreateCustomerRequest{
		Name:  "Ali Rezaei",
		Email: email,
	})
	if err != nil {
		log.Fatalf("CreateCustomer failed: %v", err)
	}
	log.Printf("created: id=%d message=%q", created.GetId(), created.GetMessage())

	// --- 2. Read it back --------------------------------------------------
	got, err := client.GetCustomer(ctx, &pb.GetCustomerRequest{Id: created.GetId()})
	if err != nil {
		log.Fatalf("GetCustomer failed: %v", err)
	}
	log.Printf("fetched: id=%d name=%q email=%q created_at=%s",
		got.GetId(), got.GetName(), got.GetEmail(), got.GetCreatedAt())

	// --- 3. An id that does not exist -> NotFound -------------------------
	if _, err := client.GetCustomer(ctx, &pb.GetCustomerRequest{Id: 999999}); err != nil {
		st, _ := status.FromError(err)
		log.Printf("as expected, id=999999 -> %s: %s", st.Code(), st.Message())
		if st.Code() != codes.NotFound {
			log.Fatalf("expected NotFound, got %s", st.Code())
		}
	}

	// --- 4. Validation failure -> InvalidArgument -------------------------
	if _, err := client.CreateCustomer(ctx, &pb.CreateCustomerRequest{Name: ""}); err != nil {
		st, _ := status.FromError(err)
		log.Printf("as expected, empty name -> %s: %s", st.Code(), st.Message())
	}

	// --- 5. Re-using an email -> AlreadyExists ----------------------------
	//
	// This one is enforced by the UNIQUE constraint in Postgres, not by Go
	// code. The store turns SQLSTATE 23505 into ErrDuplicateEmail and the
	// handler maps that to codes.AlreadyExists.
	if _, err := client.CreateCustomer(ctx, &pb.CreateCustomerRequest{
		Name:  "Impostor",
		Email: email,
	}); err != nil {
		st, _ := status.FromError(err)
		log.Printf("as expected, duplicate email -> %s: %s", st.Code(), st.Message())
	}

	// --- 6. Update the customer --------------------------------------------
	newEmail := fmt.Sprintf("ali.updated+%d@example.com", time.Now().UnixNano())
	updated, err := client.UpdateCustomer(ctx, &pb.UpdateCustomerRequest{
		Id:    created.GetId(),
		Name:  "Ali A. Rezaei",
		Email: newEmail,
	})
	if err != nil {
		log.Fatalf("UpdateCustomer failed: %v", err)
	}
	log.Printf("updated: id=%d name=%q email=%q updated_at=%s",
		updated.GetId(), updated.GetName(), updated.GetEmail(), updated.GetUpdatedAt())

	// --- 7. Updating an id that does not exist -> NotFound ------------------
	if _, err := client.UpdateCustomer(ctx, &pb.UpdateCustomerRequest{
		Id: 999999, Name: "Nobody", Email: "nobody@example.com",
	}); err != nil {
		st, _ := status.FromError(err)
		log.Printf("as expected, update missing id -> %s: %s", st.Code(), st.Message())
	}

	// --- 8. Delete the customer ----------------------------------------------
	del, err := client.DeleteCustomer(ctx, &pb.DeleteCustomerRequest{Id: created.GetId()})
	if err != nil {
		log.Fatalf("DeleteCustomer failed: %v", err)
	}
	log.Printf("deleted: %s", del.GetMessage())

	// --- 9. Fetching the now-deleted customer -> NotFound -------------------
	if _, err := client.GetCustomer(ctx, &pb.GetCustomerRequest{Id: created.GetId()}); err != nil {
		st, _ := status.FromError(err)
		log.Printf("as expected, get after delete -> %s: %s", st.Code(), st.Message())
		if st.Code() != codes.NotFound {
			log.Fatalf("expected NotFound, got %s", st.Code())
		}
	}

	// --- 10. Deleting it again -> NotFound (delete is not idempotent here) --
	if _, err := client.DeleteCustomer(ctx, &pb.DeleteCustomerRequest{Id: created.GetId()}); err != nil {
		st, _ := status.FromError(err)
		log.Printf("as expected, delete again -> %s: %s", st.Code(), st.Message())
	}
}
