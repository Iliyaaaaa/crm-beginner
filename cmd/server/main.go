// cmd/server is the entry point. It does four things and nothing else: catch
// shutdown signals, load config, build the app, run it.
//
// All wiring lives in internal/app; all business logic in internal/service.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/iliya/crm-service/internal/app"
	"github.com/iliya/crm-service/internal/config"
)

func main() {
	// signal.NotifyContext cancels ctx on Ctrl+C or SIGTERM. Everything below
	// hangs off this one context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	a, err := app.New(ctx, cfg)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer a.Close()

	if err := a.Run(ctx); err != nil {
		log.Fatalf("%v", err)
	}
}
