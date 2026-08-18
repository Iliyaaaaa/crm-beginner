# Docker — Theory Notes

Task 3: what Docker is, what a `Dockerfile` contains, and why containers matter
in a microservice architecture.

Written after containerising `crm-service`, so every example is from this
project and every number was measured on it.

---

## 1. The problem Docker solves

Before containers, deploying a service meant handing someone a binary plus a
list of instructions: install PostgreSQL 17, install these system libraries,
create this user, set these environment variables, put the config file here.

Every step is a chance for the target machine to differ from yours. The result
is the oldest excuse in software:

> "It works on my machine."

The machine *is* the difference. Your laptop has different library versions, a
different OS, different paths, leftover state from something you installed
months ago and forgot.

**Docker's answer: ship the environment with the application.** Instead of
instructions for recreating an environment, you ship the environment itself —
the OS libraries, the binary, the configuration — as one immutable unit. It runs
identically on your Mac, your senior's laptop, and the production server,
because it is *literally the same thing* in all three places.

---

## 2. Image vs container

These two words get confused constantly, and the distinction matters.

| | Image | Container |
|---|---|---|
| What it is | A read-only template | A running instance of an image |
| Analogy | A class | An object |
| Analogy | An executable file on disk | A process |
| Lifetime | Permanent until deleted | Starts, runs, stops, is discarded |
| How many | One image | Many containers from that one image |

You **build** an image once, then **run** many containers from it. In our
project:

```
crm-service-server:latest   ← the image (45.9MB, built once)
crm-service-server-1        ← the container (a running process)
```

When we ran `docker compose down` and then `docker compose up`, the *containers*
were destroyed and recreated. The *image* was untouched — which is why the
second start took seconds instead of rebuilding everything.

**Containers are disposable. This is the point, not a limitation.** Anything
that must survive has to live outside the container — in a volume, or in a
database. We will come back to this.

---

## 3. Container vs virtual machine

A common question, and the answer explains why containers are practical at all.

A **virtual machine** emulates hardware and runs a complete guest operating
system — its own kernel, its own init system, its own everything. Booting one
takes tens of seconds and costs gigabytes.

A **container** shares the host's kernel. It is really just a normal process,
isolated using kernel features (namespaces for what it can see, cgroups for what
it can use). Starting one takes milliseconds and costs megabytes.

```
   Virtual machines                     Containers

  ┌──────┐ ┌──────┐                   ┌──────┐ ┌──────┐
  │ App  │ │ App  │                   │ App  │ │ App  │
  ├──────┤ ├──────┤                   ├──────┤ ├──────┤
  │Guest │ │Guest │                   │ libs │ │ libs │
  │  OS  │ │  OS  │                   └──────┘ └──────┘
  ├──────┴─┴──────┤                   ├───────────────┤
  │   Hypervisor  │                   │ Docker engine │
  ├───────────────┤                   ├───────────────┤
  │    Host OS    │                   │    Host OS    │  ← shared kernel
  ├───────────────┤                   ├───────────────┤
  │   Hardware    │                   │   Hardware    │
  └───────────────┘                   └───────────────┘
```

The trade-off: VMs isolate more strongly (separate kernel), containers are far
lighter. For running many small services, containers win decisively — which is
exactly why containers and microservices grew up together.

> **A macOS footnote.** Containers need a *Linux* kernel. Your Mac does not have
> one, so Docker Desktop quietly runs a small Linux VM and puts your containers
> inside it. This is why Docker Desktop needed that privileged install step, and
> why our image says `GOOS=linux` even though you are on a Mac.

---

## 4. The Dockerfile

A `Dockerfile` is a recipe: a text file of ordered steps that produce an image.
Here is ours, in full, with what each line does.

### Stage one — build

```dockerfile
FROM golang:1-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server
```

| Instruction | Meaning |
|---|---|
| `FROM` | the base image to start from — here, Debian with the Go toolchain installed |
| `AS build` | names this stage, so a later stage can copy from it |
| `WORKDIR` | sets the working directory inside the image (like `cd`, but persistent) |
| `COPY` | copies files from your project into the image |
| `RUN` | executes a command *during the build* |

Two details worth understanding:

**`CGO_ENABLED=0`** disables cgo, producing a **statically linked** binary — one
that carries everything it needs and depends on no system C libraries. That is
what allows the final image to be nearly empty. Without it, the binary would
need `glibc` present at runtime and would crash in a bare Alpine image.

**`GOOS=linux`** cross-compiles for Linux. You are on macOS; the container runs
Linux. Go makes this a single environment variable.

### Stage two — the image that actually ships

```dockerfile
FROM alpine:3.20
COPY --from=build /out/server /server

EXPOSE 50051
ENTRYPOINT ["/server"]
```

`FROM` a second time starts a **new, empty image**. Everything from stage one is
discarded except what we explicitly copy with `--from=build`.

| Instruction | Meaning |
|---|---|
| `EXPOSE` | documents which port the app listens on (documentation only — it does not publish anything) |
| `ENTRYPOINT` | the command run when a container starts |

---

## 5. Multi-stage builds — the measured payoff

This is the single most valuable idea in the Dockerfile, and our project shows
it concretely.

The Go toolchain image is around **800MB**. Our source code, dependencies and
build cache add more on top. But the final image is:

```
crm-service-server:latest   45.9MB
```

Because stage two starts from `alpine:3.20` — a ~5MB Linux distribution — and
receives exactly one file: the compiled binary.

**What is *not* in the shipped image:**

- the Go compiler
- your source code
- `go.mod`, `go.sum`, the module cache
- any build tooling

Why this matters, in order of importance:

1. **Security.** An attacker who breaks into the container finds no compiler, no
   shell tooling to speak of, and no source code to read. Less installed means
   less to exploit.
2. **Speed.** Smaller images push and pull faster. Multiply by every deploy, on
   every service, every day.
3. **Clarity.** The image contains only what runs. Nothing incidental.

---

## 6. Layers and build caching

Each instruction in a Dockerfile creates a **layer** — a saved snapshot of the
filesystem changes that instruction made. Layers stack to form the image, and
Docker caches them.

**The rule: if an instruction and everything before it are unchanged, Docker
reuses the cached layer instead of re-running it.**

This is why our Dockerfile does something that looks redundant:

```dockerfile
COPY go.mod go.sum ./      ← just the dependency manifest
RUN go mod download        ← expensive: downloads every dependency
COPY . .                   ← now the actual source
RUN go build ...
```

Instead of the obvious:

```dockerfile
COPY . .                   ← everything at once
RUN go mod download        ← re-runs on EVERY source edit
RUN go build ...
```

In the first version, editing `main.go` invalidates the cache only from
`COPY . .` onward — `go mod download` is reused. In the second, changing *any*
file invalidates everything, and you re-download all dependencies on every
build.

**The general principle: order your Dockerfile from least-frequently-changed to
most-frequently-changed.** Dependencies change rarely; your code changes
constantly.

### `.dockerignore`

Before building, Docker sends your project directory (the "build context") to
the Docker engine. `.dockerignore` excludes files from that transfer — ours
excludes `.git/`, the documentation, and `db/` (mounted into Postgres directly,
never needed in the app image). Smaller context, faster builds, and no risk of
accidentally baking secrets into an image.

---

## 7. Volumes — where the data lives

Containers are disposable, but a database's data must not be. This tension is
resolved by **volumes**: storage managed by Docker that exists independently of
any container.

```yaml
volumes:
  - pgdata:/var/lib/postgresql/data
```

This says: whatever Postgres writes to `/var/lib/postgresql/data` inside the
container is actually stored in a Docker-managed volume named `pgdata`, on the
host.

**We proved this works.** We created a customer, ran `docker compose down`
(which *destroys both containers entirely*), ran `docker compose up` to build
fresh containers, and the customer was still there.

```
docker compose down     → containers destroyed, volume kept
docker compose down -v  → containers destroyed AND volume deleted (data gone)
```

That `-v` flag is the difference between "restart the system" and "wipe the
database." Worth remembering before you type it.

There is a second kind of mount, and we use that too:

```yaml
- ./db/migrations:/docker-entrypoint-initdb.d:ro
```

This is a **bind mount** — a folder on your Mac exposed inside the container,
read-only (`:ro`). The official Postgres image runs any `.sql` file found in
`/docker-entrypoint-initdb.d/` on first startup, in filename order. That is why
our migrations are named `0001_`, `0002_`: Postgres applies them itself, in
order, with no manual `psql -f` step.

| | Named volume | Bind mount |
|---|---|---|
| Managed by | Docker | You |
| Lives | wherever Docker keeps it | a path you choose |
| Best for | databases, persistent state | source code in dev, config, seed data |

---

## 8. Networking — how services find each other

This is the part that makes microservices practical, and it is worth reading the
compose file carefully.

Docker Compose puts every service on a shared private network and gives each one
a **DNS name equal to its service name**. So our server reaches the database at:

```yaml
DATABASE_URL: postgres://crm:crm@postgres:5432/crm?sslmode=disable
#                                 ^^^^^^^^
#                                 the service name, not localhost
```

Compare with running natively, where it was `localhost:5432`. Inside a
container, `localhost` means *that container itself* — so `localhost:5432` would
try to find Postgres inside the server's own container, and fail. The service
name is how one container addresses another.

**This is service discovery in miniature.** The server does not know Postgres's
IP address, does not care which host it runs on, and does not need to be
rebuilt if Postgres moves. It knows only a name. Scale that idea up and it is
how a real microservice cluster works.

### Publishing ports to the outside

```yaml
ports:
  - "50051:50051"
#    ^host  ^container
```

The private network is invisible from your Mac. `ports` punches a hole: traffic
arriving at port 50051 on your Mac is forwarded to port 50051 inside the
container. That is why your `grpcurl -plaintext localhost:50051` commands kept
working unchanged.

Note that **Postgres does not have to be published at all.** We publish it for
convenience so you can inspect it with `psql`, but if only the server needed it,
removing that `ports` entry would make the database reachable *only* from inside
the Docker network — a meaningful security improvement.

---

## 9. Docker Compose — orchestration

A `Dockerfile` describes one image. Real systems are several containers that
must start together, find each other, and share configuration. That is
**Docker Compose**: one YAML file describing the whole stack, started with one
command.

Our stack has two services, and one relationship between them worth studying:

```yaml
depends_on:
  postgres:
    condition: service_healthy
```

**Why `service_healthy` and not just `depends_on: [postgres]`?**

Plain `depends_on` waits for the container to *start*. But Postgres accepts TCP
connections several seconds before it is actually ready to answer queries. A
server starting in that window would fail its initial connection and exit.

So Postgres declares a healthcheck:

```yaml
healthcheck:
  test: ["CMD-SHELL", "pg_isready -U crm -d crm"]
  interval: 2s
  retries: 20
```

Docker runs `pg_isready` every 2 seconds and marks the container healthy only
when it passes. Our server container does not start until then. You can see this
in the startup output:

```
Container crm-service-postgres-1  Started
Container crm-service-postgres-1  Waiting
Container crm-service-postgres-1  Healthy      ← only now
Container crm-service-server-1    Starting
```

This is a real distributed-systems problem — startup ordering and readiness —
solved declaratively in five lines.

---

## 10. Configuration through the environment

Notice something about containerising this project: **not one line of Go code
changed.**

That works because `cmd/server/main.go` already reads its database address from
an environment variable:

```go
dsn := os.Getenv("DATABASE_URL")
if dsn == "" {
    dsn = defaultDSN   // localhost, for running natively
}
```

Compose simply supplies a different value. The same binary runs against
Homebrew Postgres on your Mac and against a Postgres container, with no rebuild.

This is a widely followed principle (often cited as part of the
**Twelve-Factor App** methodology): **configuration comes from the environment,
not from source code.** It exists because the same artifact must run in
development, staging and production, differing only in configuration — and
because connection strings eventually contain passwords, which must never be
committed.

---

## 11. Why containers for microservices

Task 3's actual question. Six reasons, roughly in order of importance:

**1. Independent deployment.** Each service is its own image with its own
version. Deploying the Customer service does not touch the Sales service. With a
monolith, any change means redeploying everything.

**2. Independent technology choices.** One service in Go, another in Python, a
third in Java — each with its own dependencies, none conflicting, because each
lives in its own image. Installing all of that on one host would be a
compatibility nightmare.

**3. Independent scaling.** If the Activity service takes ten times the traffic
of the others (as our CRM analysis predicts it will), run ten containers of it
and one of everything else. You cannot scale part of a monolith.

**4. Environment parity.** The image you tested is byte-for-byte the image that
runs in production. This eliminates an entire category of bug.

**5. Fast, cheap, disposable.** Containers start in milliseconds. Crashed
containers get replaced rather than repaired. This is what makes automated
orchestration (Kubernetes and similar) possible at all.

**6. A declarative local environment.** A new developer runs
`docker compose up` and has the whole system — every service, every database —
running in minutes, with no setup document to follow and no chance of
misfollowing it.

**The honest costs**, since a good answer names them:

- more moving parts to understand and debug
- networking and volumes add real complexity
- image size and build times need active attention
- local development can get slower and heavier
- a distributed system is genuinely harder to reason about than one process

Containers are worth it *when you actually have multiple services*. For a single
small application, they can be more overhead than help.

---

## 12. Command reference

```bash
docker compose up -d          # build if needed, start everything, detached
docker compose up --build -d  # force a rebuild first
docker compose ps             # what is running, and is it healthy
docker compose logs -f server # follow one service's logs
docker compose down           # stop and remove containers (volumes survive)
docker compose down -v        # ...and delete volumes (DESTROYS DATA)
docker compose exec postgres psql -U crm -d crm   # shell into a container
docker images                 # list images and their sizes
docker ps -a                  # list containers, including stopped ones
```

---

## 13. Summary

- A **container** is an isolated process sharing the host kernel; a **VM**
  emulates hardware and runs a whole guest OS. Containers are far lighter.
- An **image** is the read-only template; a **container** is a running instance
  of it. Class and object.
- A **Dockerfile** is the recipe. Each instruction becomes a cached **layer**, so
  order matters: least-changing first.
- **Multi-stage builds** compile in a heavy image and ship a light one. Ours:
  ~800MB toolchain in, **45.9MB** out, containing only the binary.
- Containers are **disposable**; **volumes** hold anything that must survive.
  Verified: our data outlived a full `down`/`up` cycle.
- Containers find each other by **service name** over a private network —
  `postgres`, not `localhost`.
- **Compose** describes a multi-container stack declaratively, including startup
  ordering via **healthchecks**.
- **Configuration comes from the environment**, which is why containerising this
  project required no code changes at all.
- For microservices, containers give **independent deployment, technology
  choice, and scaling** — the properties that make the architecture viable.

---

## References

- [Docker documentation](https://docs.docker.com/)
- [Dockerfile reference](https://docs.docker.com/reference/dockerfile/)
- [Compose file reference](https://docs.docker.com/reference/compose-file/)
- [Multi-stage builds](https://docs.docker.com/build/building/multi-stage/)
- [The Twelve-Factor App](https://12factor.net/) — especially factor III, Config
