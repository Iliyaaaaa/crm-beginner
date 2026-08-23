# Runbook — How to Run the Project

Copy-pasteable commands for starting the service and doing every CRUD
operation. Two ways to run it; pick one.

All commands assume you are in the project directory:

```bash
cd ~/Desktop/programming/GO/crm-service
```

---

# Option A — Native (Homebrew Postgres + `go run`)

Use this for day-to-day development. Faster startup, easier debugging.

## Start it

**Terminal 1** — make sure the database is running, then start the server:

```bash
brew services start postgresql@17
```

```bash
make server
```

You should see:

```
connected to postgres
gRPC server listening on :50051
```

Leave this running. Every request will log a line here.

## Stop it

In Terminal 1, press `Ctrl+C`. You'll see:

```
shutdown signal received, draining...
stopped cleanly
```

The database keeps running in the background. To stop it too:

```bash
brew services stop postgresql@17
```

---

# Option B — Docker (app + database in containers)

Use this to run the whole stack the way it would run in production.

## Start it

Docker Desktop must be running first (whale icon in the menu bar).
Port 5432 must be free, so stop the native Postgres:

```bash
brew services stop postgresql@17
```

```bash
docker compose up -d
```

Check both containers are up and Postgres is healthy:

```bash
docker compose ps
```

Watch the server's logs (the equivalent of Terminal 1 above):

```bash
docker compose logs -f server
```

Press `Ctrl+C` to stop following the logs — this does **not** stop the server.

## Stop it

```bash
docker compose down
```

Containers are destroyed; the database data survives in the `pgdata` volume.

```bash
docker compose down -v
```

⚠️ Same, but **also deletes the volume — your data is gone**.

## After changing Go code

The image must be rebuilt:

```bash
docker compose up --build -d
```

---

> ⚠️ **You cannot run both options at once.** They both want ports 5432 and
> 50051. Also note they use **different databases** — the native Postgres and
> the Docker volume hold separate data.

---

# CRUD operations

Open a **second terminal**. These work identically for Option A or B.

Every command has the same shape:

```
grpcurl -plaintext -d '<json>' localhost:50051 customer.CustomerService/<Method>
```

## CREATE

```bash
grpcurl -plaintext -d '{"name":"Sara Ahmadi","email":"sara@example.com"}' localhost:50051 customer.CustomerService/CreateCustomer
```

```json
{ "id": "7", "message": "customer created" }
```

**Note the returned `id`** — you need it for the next three commands.

## READ

```bash
grpcurl -plaintext -d '{"id":7}' localhost:50051 customer.CustomerService/GetCustomer
```

```json
{
  "id": "7",
  "name": "Sara Ahmadi",
  "email": "sara@example.com",
  "createdAt": "2026-08-11T09:51:41+03:30",
  "updatedAt": "2026-08-11T09:51:41+03:30"
}
```

## UPDATE

Both `name` and `email` are required — this is a full replace, not a partial
patch.

```bash
grpcurl -plaintext -d '{"id":7,"name":"Sara A. Ahmadi","email":"sara.new@example.com"}' localhost:50051 customer.CustomerService/UpdateCustomer
```

`updatedAt` changes; `createdAt` stays the same.

## DELETE

```bash
grpcurl -plaintext -d '{"id":7}' localhost:50051 customer.CustomerService/DeleteCustomer
```

```json
{ "message": "customer deleted" }
```

## LIST — not implemented

There is no `ListCustomers` RPC yet. To see all customers, query the database
directly (see below). Adding this RPC is the natural next feature.

---

# Inspecting the database

## Native

```bash
make db-rows
```

```bash
make db-shell
```

(In the shell: `\d customers` describes the table, `\q` quits.)

## Docker

```bash
docker compose exec postgres psql -U crm -d crm -c "SELECT * FROM customers;"
```

```bash
docker compose exec postgres psql -U crm -d crm
```

---

# Exploring the API

List every service the server exposes:

```bash
grpcurl -plaintext localhost:50051 list
```

List the methods of our service:

```bash
grpcurl -plaintext localhost:50051 list customer.CustomerService
```

Show the full contract — every method, every message field:

```bash
grpcurl -plaintext localhost:50051 describe customer.CustomerService
```

Show the shape of one message, so you know what JSON to send:

```bash
grpcurl -plaintext localhost:50051 describe customer.CreateCustomerRequest
```

This works because of `reflection.Register(s)` in `../cmd/server/main.go`. It is
how grpcurl knows your API without you giving it the `.proto` file.

---

# The built-in test client

Instead of typing grpcurl commands one at a time, this runs all four operations
plus the error cases in one go:

```bash
make client
```

Useful as a quick smoke test after changing something.

---

# Error responses you should expect

| Command | Result |
|---|---|
| `{"name":"","email":"x@y.com"}` on Create | `InvalidArgument: name must not be empty` |
| `{"name":"X","email":""}` on Create | `InvalidArgument: email must not be empty` |
| Create with an email that already exists | `AlreadyExists: email "..." is already registered` |
| `{"id":9999}` on Get / Update / Delete | `NotFound: customer 9999 not found` |

Try one:

```bash
grpcurl -plaintext -d '{"id":9999}' localhost:50051 customer.CustomerService/GetCustomer
```

`grpcurl` exits with code 69 on a gRPC error. That is normal, not a bug.

---

# Development commands

| Command | Does |
|---|---|
| `make proto` | regenerate Go code after editing `log.proto` |
| `make build` | compile everything |
| `make tidy` | sync `../go.mod` with your imports |
| `go vet ./...` | catch suspicious code |
| `gofmt -l .` | list badly formatted files (prints nothing if clean) |

**After editing `log.proto` you must run `make proto`.** Nothing is
automatic. This is the most common thing to forget.

---

# Database maintenance

| Command | Does |
|---|---|
| `make db-create` | create the `crm` database (first-time setup) |
| `make db-migrate` | apply the schema files in `../db/migrations` |
| `make db-reset` | ⚠️ drop the table and recreate it — **destroys all data** |

Setting up from scratch on a new machine:

```bash
brew services start postgresql@17 && make db-create && make db-migrate
```

---

# Troubleshooting

### `address already in use`

Something is already on the port. Find it:

```bash
lsof -nP -iTCP:50051 -sTCP:LISTEN
```

Then `kill <PID>`. Note `go run` starts a *child* process — killing the wrapper
does not always kill the child.

### `connection refused` from grpcurl

The server isn't running. Check Terminal 1, or `docker compose ps`.

### `database: ping database: ... connection refused`

Postgres isn't running.

```bash
brew services start postgresql@17
```

Or for Docker, check `docker compose ps` shows postgres as `healthy`.

### `relation "customers" does not exist`

The schema was never applied:

```bash
make db-migrate
```

### Changed the `.proto` but nothing happened

```bash
make proto
```

### Docker: `Cannot connect to the Docker daemon`

Docker Desktop isn't running. Open it from Applications and wait for the whale
icon to stop animating.

---

# Quick reference card

```bash
# --- native ---
brew services start postgresql@17     # start database
make server                           # start service          (terminal 1)
make db-rows                          # see all rows

# --- docker ---
brew services stop postgresql@17      # free port 5432 first
docker compose up -d                  # start everything
docker compose logs -f server         # watch logs
docker compose down                   # stop (data survives)

# --- CRUD (terminal 2) ---
grpcurl -plaintext -d '{"name":"N","email":"E"}' localhost:50051 customer.CustomerService/CreateCustomer
grpcurl -plaintext -d '{"id":1}'                 localhost:50051 customer.CustomerService/GetCustomer
grpcurl -plaintext -d '{"id":1,"name":"N","email":"E"}' localhost:50051 customer.CustomerService/UpdateCustomer
grpcurl -plaintext -d '{"id":1}'                 localhost:50051 customer.CustomerService/DeleteCustomer

# --- after changing log.proto ---
make proto
```
