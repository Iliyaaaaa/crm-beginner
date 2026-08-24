.PHONY: help proto server client tidy build test clean db-start db-stop db-create db-migrate db-shell db-rows db-reset

# `make` with no arguments prints the help below. .DEFAULT_GOAL must come
# before any target definition to take effect.
.DEFAULT_GOAL := help

# Prints every target that has a `## description` comment after its name.
# The awk script scans this file for lines shaped `target: ## text` and lays
# them out in two columns - so adding a new target with a ## comment makes it
# show up here automatically, with nothing else to update.
help: ## Show this help
	@echo "crm-service - available commands"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} \
		/^# =+ .* =+$$/ { gsub(/^# =+ | =+$$/, ""); printf "\n  \033[1m%s\033[0m\n", $$0; next } \
		/^[a-zA-Z_-]+:.*?## / { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""

# Homebrew keeps postgresql@17 out of the default PATH.
PG_BIN       := /opt/homebrew/opt/postgresql@17/bin
DATABASE_URL ?= postgres://localhost:5432/crm?sslmode=disable

# ===== Protobuf =====

# Regenerate Go code from every .proto contract.
# Run this every time you edit proto/customerpb/customer.proto or proto/logpb/log.proto
proto: ## Regenerate Go code from all .proto files
	PATH="$(PATH):$(shell go env GOPATH)/bin" protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/customerpb/customer.proto proto/logpb/log.proto

# ===== Build & test =====

server: ## Run the gRPC server locally
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/server

client: ## Run the test client against a running server
	go run ./cmd/client

tidy: ## Sync go.mod with the imports in the code
	go mod tidy

build: ## Compile everything
	go build ./...

test: ## Run all tests with the race detector
	go test ./... -race

clean: ## Delete all generated .pb.go files
	rm -f proto/customerpb/*.pb.go proto/logpb/*.pb.go

# ===== Database =====

db-start: ## Start the local Homebrew Postgres
	brew services start postgresql@17

db-stop: ## Stop the local Homebrew Postgres
	brew services stop postgresql@17

db-create: ## Create the crm database
	$(PG_BIN)/createdb crm || echo "database 'crm' already exists"

# Applies every migration file in db/migrations/ in order. Filenames are
# zero-padded (0001_, 0002_, ...) specifically so shell globbing sorts them
# correctly - this is why migrations must never be renamed once committed.
db-migrate: ## Apply every migration in db/migrations, in order
	for f in db/migrations/*.sql; do \
		echo "applying $$f"; \
		$(PG_BIN)/psql -d crm -f $$f; \
	done

# Open an interactive SQL prompt. \d customers describes the table, \q quits.
db-shell: ## Open an interactive psql prompt
	$(PG_BIN)/psql -d crm

# Show every stored customer.
db-rows: ## Print every customer row
	$(PG_BIN)/psql -d crm -c "SELECT id, name, email, created_at FROM customers ORDER BY id;"

# Drop and recreate the table. Destroys all data.
db-reset: ## DESTRUCTIVE: drop the customers table and re-migrate
	$(PG_BIN)/psql -d crm -c "DROP TABLE IF EXISTS customers;"
	$(MAKE) db-migrate
