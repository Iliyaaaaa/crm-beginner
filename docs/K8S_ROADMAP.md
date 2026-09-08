# Kubernetes Migration — Roadmap

Moving crm-service from `docker compose` to Kubernetes, in 19 steps.

The mental shift that makes all of it make sense:

> **You don't tell Kubernetes what to do. You tell it what you want, and it
> works out how to get there.**
>
> Docker: "start this container" — an action.
> K8s: "I want 3 copies running, always" — a desired state.
>
> K8s then loops forever: *"Do I have 3? No, I have 2. Start one more."*
> That loop is called **reconciliation**, and it is why K8s self-heals.
> You never tell it to restart anything.

---

# Three facts about THIS project that shape the plan

1. **No gRPC health service is registered** in the Go code, so Kubernetes'
   native `grpc:` probes cannot be used yet - they would fail every check and
   put the pod in a restart loop. Steps 15 uses TCP probes instead; Part E
   fixes it properly.

2. **Migrations run through Postgres's `docker-entrypoint-initdb.d`**, which
   in compose is a bind mount from the laptop. There is no laptop in a
   cluster, so Step 8 replaces it with a ConfigMap.

3. **The image is local-only** (no registry), so minikube cannot pull it.
   Step 9 loads it in by hand.

---

# How your docker-compose maps to Kubernetes

| docker-compose | Kubernetes |
| --- | --- |
| `services: server:` | **Deployment** (which creates Pods) |
| `ports: "50051:50051"` | **Service** |
| `environment: CACHE_TTL` | **ConfigMap** |
| `POSTGRES_PASSWORD: crm` | **Secret** |
| `volumes: pgdata:` | **PersistentVolumeClaim** |
| `command: redis-server --save ""` | container `args:` |
| `healthcheck:` | **liveness probe** |
| `depends_on: service_healthy` | **readiness probe** |
| postgres (a stateful database) | **StatefulSet**, not Deployment |

---

# PART A — Foundation

## Step 1 — Get the cluster running

Start minikube with the docker driver.

**Why:** `kubectl` is only a client; it needs a cluster to talk to. Prove the
connection works before debugging anything harder. The docker driver runs the
whole node inside a container, so no hypervisor is needed.

**Verify:** `kubectl get nodes` shows one node, `Ready`.

## Step 2 — Create a namespace

Create a namespace `crm` and make it the default for your current context.

**Why:** A namespace is a folder. Everything lands inside it, so cleanup is one
command instead of hunting objects, and your experiments stay separate from the
Kubernetes system pods.

**Learn:** `kubectl create namespace`,
`kubectl config set-context --current --namespace=crm`

**Verify:** `kubectl get namespace` lists `crm`.

---

# PART B — Dependencies (Redis, then Postgres)

These come first because the app cannot start without them. Redis is first
because it is the simplest object in Kubernetes: no state, no config, no
secrets.

## Step 3 — Deploy Redis

A **Deployment** running `redis:7-alpine`, passing the same flags the compose
file uses (`--save ""`, `--appendonly no`) so it stays a pure in-memory cache.

**Why:** This is the "hello world" of Kubernetes. In compose, `command:` becomes
`args:` in the container spec.

**Key concept:** A Deployment does not run containers directly. It creates a
**ReplicaSet**, which creates **Pods**. `kubectl get all` shows all three.

**Verify:** `kubectl get pods` shows one Redis pod `Running`.

## Step 4 — Expose Redis with a Service

A **ClusterIP** Service named `redis` on port 6379.

**Why:** Pods get a new IP on every restart, so nothing may depend on a pod IP.
A Service is a permanent DNS name in front of whichever pods are alive. Name it
exactly `redis` and the existing `REDIS_URL=redis://redis:6379/0` keeps working
unchanged - the same internal-DNS trick compose already uses.

**Critical concept - labels and selectors:** a Service finds its pods by
matching **labels**, not names. If the Service's `selector` does not exactly
match the pod's `labels`, the Service silently points at nothing. This is the
number-one beginner bug.

**Verify:** `kubectl get endpoints redis` shows a pod IP.
**Empty means the selector is wrong.**

## Step 5 — Create the Postgres Secret

A **Secret** holding the Postgres user, password and database name.

**Why:** The compose file has `POSTGRES_PASSWORD: crm` in plain text. Secrets
are where credentials belong - mounted at runtime, never baked into an image.

**Honest caveat:** Kubernetes Secrets are base64-**encoded**, not encrypted.
Base64 is encoding, not security; anyone with cluster read access can decode
them. Production uses Sealed Secrets, Vault, or a cloud secrets manager. For
learning, a plain Secret is the right call.

**Verify:** `kubectl describe secret` shows the keys and hides the values.

## Step 6 — Deploy Postgres as a StatefulSet with storage

A **StatefulSet** for `postgres:17` with a `volumeClaimTemplate` mounted at
`/var/lib/postgresql/data`, taking its credentials from the Step 5 Secret.

**Why StatefulSet and not Deployment:** a Deployment assumes pods are
interchangeable and disposable. A database is neither - it has identity and a
disk that must reattach to the *same* pod after a restart. StatefulSet gives
stable names (`postgres-0`) and per-pod storage.

This replaces compose's `volumes: pgdata:`. Same idea, different mechanism:
a **PersistentVolumeClaim** is a request for storage, bound to a
**PersistentVolume**, which is the actual disk.

**Verify:** `kubectl get pvc` shows `Bound`; `postgres-0` is `Running`.

## Step 7 — Expose Postgres with a Service

A ClusterIP Service named `postgres` on port 5432.

**Why:** Same reasoning as Step 4, and the name matters again - `DATABASE_URL`
pointing at `@postgres:5432` then needs no change.

**Verify:** `kubectl get endpoints postgres` is not empty.

## Step 8 — Get the migrations to run

Put `0001_create_customers.sql`, `0002_add_updated_at.sql` and
`0003_create_logs.sql` into a **ConfigMap**, and mount it at
`/docker-entrypoint-initdb.d` in the Postgres StatefulSet.

**Why this needs its own step:** compose bind-mounts `./db/migrations` from the
laptop. A cluster has no laptop - the pod could run on any machine. A ConfigMap
is how files get injected into a pod. The Postgres image behaves identically
once they are there: it runs everything in that directory in filename order,
but **only on first initialisation of an empty data directory**.

**Gotcha:** if the PVC from Step 6 already initialised without the migrations,
adding them later does nothing. Delete the PVC and let it re-initialise.

**Alternative worth knowing:** a Kubernetes **Job** that runs the migrations as
a one-off task. That is closer to real production, because migrations should
not depend on a database being brand new. ConfigMap is simpler, Job is more
correct - either is fine here.

**Verify:** exec into the pod, run `\dt` in psql, see `customers` and `logs`.

---

# PART C — The Application

## Step 9 — Build the image and load it into minikube

Build with Docker, then `minikube image load` it.

**Why this step exists:** minikube's node is a separate container with its own
image store. It cannot see images in the host's Docker. With no registry
(Docker Hub, ECR) in the picture, the image has to be pushed in by hand.

**Critical companion setting:** set `imagePullPolicy: IfNotPresent`. The default
policy for a `:latest` tag is `Always`, which makes Kubernetes try to pull from
a registry, fail, and leave the pod stuck in `ErrImagePull`. Almost everyone
hits this once.

**Verify:** `minikube image ls | grep crm` lists the image.

## Step 10 — Create the app's ConfigMap

The non-secret settings: `GRPC_ADDR`, `DB_MAX_CONNS`, `CACHE_TTL`,
`STARTUP_TIMEOUT`, `REDIS_URL`, and the four `LOG_*` values.

**Why:** these are exactly the compose `environment:` block, and every one is
already documented in `internal/config/config.go`. Splitting config out of the
image means changing a setting needs no rebuild.

**Verify:** `kubectl describe configmap` lists all the keys.

## Step 11 — Create the app's Secret

A Secret holding `DATABASE_URL`.

**Why:** this is the split to internalise - **ConfigMap for settings, Secret for
credentials**. `DATABASE_URL` embeds `crm:crm@`, which makes it a credential.

## Step 12 — Deploy the app

A **Deployment** with `replicas: 1`, the loaded image,
`containerPort: 50051`, env pulled from the ConfigMap (`envFrom`) and the
Secret (`secretKeyRef`).

**Why `replicas: 1` first:** get one working before scaling. Scaling something
broken just produces three broken things and noisier logs.

**Verify:** `kubectl logs <pod>` shows the familiar startup lines:

```
connected to postgres (max_conns=80)
connected to redis (caching enabled, ttl=5m0s)
ingester started: 4 workers, batch=500, flush=2s, buffer=10000
gRPC server listening on :50051
```

**If it crashes:** `kubectl describe pod <name>` and read the **Events** at the
bottom. That is where Kubernetes explains itself.

## Step 13 — Expose the app with a Service

A Service on port 50051 selecting the app's pods.

**Why:** even inside the cluster this is how anything reaches the app, and later
it is what load-balances across replicas.

## Step 14 — Reach it from the laptop and test it

`kubectl port-forward` the Service's 50051 to a local port, then use `grpcurl`
or the existing `cmd/client`.

**Why port-forward:** the cluster is isolated. This is a temporary tunnel for
testing - production would use an Ingress or a LoadBalancer, but this is the
fastest way to prove things work.

**Verify:** `grpcurl -plaintext localhost:50051 list` returns both services,
then create a customer and read it back.

**This is the milestone: the whole stack now runs on Kubernetes.**

## Step 15 — Add health probes

A **readiness** probe and a **liveness** probe, both `tcpSocket` on 50051.

**Why tcpSocket and not grpc:** Kubernetes does support native gRPC probes, but
they require the app to implement the standard gRPC health protocol
(`grpc.health.v1.Health`). This codebase does not register it, so a `grpc:`
probe would fail every check and Kubernetes would kill the pod in a restart
loop. TCP proves the port accepts connections, which is a reasonable first
approximation. Part E fixes this properly.

**Understand the difference:**

| Probe | Question | On failure |
| --- | --- | --- |
| readiness | "should traffic come to me?" | removed from the Service, kept alive |
| liveness | "am I broken?" | container killed and restarted |

The app connects to Postgres and Redis at startup - for those seconds it is
alive but not ready. That is exactly why both probes exist.

**Verify:** `kubectl describe pod` shows both; the pod reaches `READY 1/1`.

---

# PART D — What Kubernetes Actually Buys You

These steps write almost nothing. They are experiments, and this is where the
concepts click.

## Step 16 — Break it and watch it heal

`kubectl delete pod` the app pod while watching `kubectl get pods -w`.

**Why:** a new pod appears within seconds without being asked for. Nothing
"restarted" it - the Deployment controller saw *desired 1, actual 0* and
reconciled. This is the single most important idea in Kubernetes, and watching
it beats reading about it.

Then delete `postgres-0` and watch the PVC reattach with the data intact.

## Step 17 — Scale, and do the connection math

Scale to 3 replicas and watch the Service load-balance across them.

**Why this step is dangerous for this project specifically:** each replica opens
its own pool of `DB_MAX_CONNS=80`. Three replicas is **240 connections** against
a Postgres with `max_connections=100`, which reproduces the exact
`SQLSTATE 53300: too many clients` collapse already diagnosed during load
testing.

**What to do about it:** work out the right per-replica value - roughly
`(max_connections - headroom) / replicas` - and update the ConfigMap. Connection
pools do not scale linearly with replicas, and this is precisely why PgBouncer
exists.

## Step 18 — Roll out an update, then roll it back

Change something visible (a log line), rebuild, load the image under a **new
tag**, update the Deployment, watch `kubectl rollout status`, then
`kubectl rollout undo`.

**Why a new tag:** with `:latest` the spec does not change, so Kubernetes rolls
out nothing. Real teams tag with a version or a git SHA for exactly this reason.

**What to watch for:** new pods start and pass readiness *before* old ones are
removed - that is the zero-downtime part. If the new version never becomes
ready, the rollout stops on its own and the old version keeps serving.

## Step 19 — Set resource requests and limits

`requests` and `limits` for CPU and memory on the app container.

**Why:** **requests** are what the scheduler uses to choose a node with room.
**limits** are the hard ceiling - exceed the memory limit and the container is
killed (`OOMKilled`). With no requests, Kubernetes assumes the app needs nothing
and can pack too much onto one node.

**Ground it in real data:** the service was measured at ~30,000 req/s during
load testing, so these can come from measurement rather than guesswork - run a
load test through port-forward and watch `kubectl top pod`.

---

# PART E — Optional Next Level

Only after Parts A-D work. Roughly in order of value:

| Topic | What it adds | Ties back to |
| --- | --- | --- |
| gRPC health service | register `grpc.health.v1` in Go, switch probes to native `grpc:` | fixes the Step 15 compromise |
| Fluent Bit sidecar | a second container in the pod shipping logs | the sidecar pattern |
| HorizontalPodAutoscaler | auto-scale replicas on CPU | Step 17's math, automated |
| Ingress | real external access | replaces Step 14's port-forward |
| Kustomize or Helm | manage all the YAML as one unit, dev/prod variants | once there are ~10 manifests |

---

# Debugging: the three commands

These answer about 90% of problems, in this order:

```bash
kubectl get pods
kubectl describe pod <pod-name>
kubectl logs <pod-name>
```

`describe` - read the **Events** section at the bottom. That is where
Kubernetes tells you what went wrong.

---

# Order matters

Work through the steps in sequence: each depends on the one before. Steps 3-4
(Redis) teach the Deployment + Service pattern that Steps 12-13 (the app)
repeat, so the hard parts get easier as you go.
