.PHONY: proto server client tidy build clean db-start db-stop db-create db-migrate db-shell db-reset psql-path

# Homebrew keeps postgresql@17 out of the default PATH.
PG_BIN       := /opt/homebrew/opt/postgresql@17/bin
DATABASE_URL ?= postgres://localhost:5432/crm?sslmode=disable

# ---------------------------------------------------------------- protobuf --

# Regenerate Go code from the .proto contract.
# Run this every time you edit proto/customerpb/customer.proto
proto:
	PATH="$(PATH):$(shell go env GOPATH)/bin" protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/customerpb/customer.proto

# ------------------------------------------------------------------- build --

server:
	DATABASE_URL="$(DATABASE_URL)" go run ./cmd/server

client:
	go run ./cmd/client

tidy:
	go mod tidy

build:
	go build ./...

clean:
	rm -f proto/customerpb/*.pb.go

# ---------------------------------------------------------------- database --

db-start:
	brew services start postgresql@17

db-stop:
	brew services stop postgresql@17

db-create:
	$(PG_BIN)/createdb crm || echo "database 'crm' already exists"

db-migrate:
	$(PG_BIN)/psql -d crm -f db/migrations/0001_create_customers.sql

# Open an interactive SQL prompt. \d customers describes the table, \q quits.
db-shell:
	$(PG_BIN)/psql -d crm

# Show every stored customer.
db-rows:
	$(PG_BIN)/psql -d crm -c "SELECT id, name, email, created_at FROM customers ORDER BY id;"

# Drop and recreate the table. Destroys all data.
db-reset:
	$(PG_BIN)/psql -d crm -c "DROP TABLE IF EXISTS customers;"
	$(MAKE) db-migrate
