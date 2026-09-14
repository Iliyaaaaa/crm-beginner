// Command log-agent is the node-level log collector: run one per node, as a
// Kubernetes DaemonSet. See internal/logagent for how it works.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/iliya/crm-service/internal/logagent"
	logpb "github.com/iliya/crm-service/proto/logpb"
)

func main() {
	// SIGTERM is how Kubernetes asks a pod to stop; cancelling ctx lets the
	// agent send what it holds and save its positions before exiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := logagent.LoadConfig()
	if err != nil {
		log.Fatalf("log-agent: config: %v", err)
	}

	conn, err := grpc.NewClient(cfg.IngestAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Same reason as cmd/client: skip the resolver's DNS TXT lookup for a
		// service config nobody publishes.
		grpc.WithDisableServiceConfig(),
	)
	if err != nil {
		log.Fatalf("log-agent: grpc client: %v", err)
	}
	defer conn.Close()

	log.Printf("log-agent: collecting %s (namespace=%q) into %s", cfg.LogDir, cfg.Namespace, cfg.IngestAddr)
	if err := logagent.Run(ctx, cfg, logpb.NewLogServiceClient(conn)); err != nil {
		log.Fatalf("log-agent: %v", err)
	}
	log.Println("log-agent: stopped")
}
