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

**Status: Parts A-D are done.** The service runs on minikube with Redis,
Postgres, config, secrets, probes, 3 replicas, rolling updates and resource
limits. Part E is optional; its first step, the gRPC health service, is done.
Everything that went wrong on the way is collected
at the end, under "Gotchas actually hit".

---

# Three facts about THIS project that shape the plan

1. **No gRPC health service was registered** in the Go code at first, so
   Kubernetes' native `grpc:` probes could not be used - they would have failed
   every check and put the pod in a restart loop. Step 15 therefore started
   with TCP probes. *Done since:* Part E's first step registered the service
   and switched both probes to `grpc:`.

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

**Quote the value in single quotes.** In zsh, `$` starts a variable, so an
unquoted (or double-quoted) `Iliya$2006` silently becomes `Iliya` - the
password is created truncated, with no error, and the mismatch only surfaces
later as `password authentication failed`. Single quotes are the only form
that keeps the value intact.

**Use the exact key names** `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`
with underscores. A hyphen (`POSTGRES-DB`) is not a valid environment variable
name, so `envFrom` skips that key without failing, and Postgres quietly names
the database after the user instead.

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

**Do Step 8's ConfigMap FIRST.** Postgres runs the migration files only while
initialising an empty data directory, so create the `postgres-migrations`
ConfigMap and mount it in this StatefulSet from the start. Adding it after the
PVC exists does nothing - the only fix then is deleting the PVC and starting
over.

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
credentials**. `DATABASE_URL` embeds the password, which makes it a credential.

**The user, password and database name must match the Step 5 Secret exactly.**
A mismatch does not look like a typo: Postgres is perfectly healthy and the app
sits in `CrashLoopBackOff` with `password authentication failed`.

**Quoting and encoding.** Wrap the whole `--from-literal=...` in single quotes:
zsh treats `?` as a wildcard (so `?sslmode=disable` fails with
`no matches found`) and `$` as a variable. Inside the URL itself, `$` is
allowed as-is - Go's URL parser accepts it, and `%24` works too - but `/`, `?`,
`#`, `%` and spaces in a password must be percent-encoded or the URL cannot be
parsed.

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

> **Update - replaced in Part E:** the app now registers `grpc.health.v1.Health`
> and both probes are `grpc:` on port `50051`. The port is a number on purpose:
> gRPC probes do not support named ports, so `port: grpc` is rejected.

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
| ✅ gRPC health service | register `grpc.health.v1` in Go, switch probes to native `grpc:`, report `NOT_SERVING` on shutdown | fixes the Step 15 compromise |
| Log collector ("vacuum") | a DaemonSet that reads every pod's stdout/stderr from the node's `/var/log/containers`, with no change to the app | the log pipeline |
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

---

# The files this produced

| File | What it holds |
| --- | --- |
| `k8s/redis-deployment.yaml` | Redis Deployment (compose's `command:` becomes `args:`) |
| `k8s/redis-service.yaml` | ClusterIP Service `redis` |
| `k8s/postgres-statefulset.yaml` | Postgres StatefulSet, PVC template, migrations mount, `pg_isready` probe |
| `k8s/postgres-service.yaml` | ClusterIP Service `postgres` |
| `k8s/app-configmap.yaml` | The 9 non-secret settings, plus `GOMAXPROCS` and `GOMEMLIMIT` |
| `k8s/app-deployment.yaml` | The app Deployment: image, envFrom, probes, resources |
| `k8s/crm-server-service.yaml` | ClusterIP Service `crm-server` |

Two Secrets are deliberately **not** files: `postgres-secret` and
`crm-server-secret` are created with `kubectl create secret`, so no password
ever reaches this public repository.

---

# Gotchas actually hit

Every one of these cost real debugging time.

**1. zsh ate part of a password (Steps 5 and 11).** `Iliya$2006` became
`Iliya`, because zsh expanded `$2006` as a variable and found nothing. No
error, no warning. Always single-quote values containing `$`.

**2. `POSTGRES-DB` with a hyphen was silently ignored (Step 5).** Hyphens are
invalid in environment variable names, so `envFrom` skipped the key.

**3. Migrations never ran (Steps 6 and 8).** The ConfigMap has to exist and be
mounted before Postgres initialises its data directory, not after.

**4. `--previous` is often useless on a crash loop (Step 12).** It fails with
`unable to retrieve container logs` once the old container has been cleaned
up. While the pod shows `Error`, plain `kubectl logs <pod>` is the crashed
container's own output - that is where the real error is.

**5. `envFrom` has a specific shape (Step 12).** Each `-` is ONE source, and
each source is an object with a `name:` field. Two sources means two list
items, not two keys under one item.

**6. The Go client hung with `DeadlineExceeded` while grpcurl worked (Step
14).** grpc-go's `dns` resolver also asks the network's DNS server for a TXT
record (`_grpc_config.localhost`) and waits for it before connecting. The home
router never answered, so the 5s deadline passed before any connection was
attempted. Fixed with `grpc.WithDisableServiceConfig()` in `cmd/client`.

**7. `port-forward` binds to ONE pod, not the Service (Steps 14, 17, 18).** It
dies with `lost connection to pod` whenever that pod is replaced, and it never
spreads load across replicas.

**8. `kubectl describe pod -l <label>` prints no Events when it matches several
pods (Step 15).** Use `kubectl get events --field-selector reason=Unhealthy
--sort-by=.lastTimestamp`, or describe one pod by name.

**9. readiness and liveness proved themselves (Step 15).** Pointing readiness
at a wrong port left the pod `Running 0/1` with `Restart Count: 0`, emptied its
endpoints, and stalled the rollout while the old pod kept serving. Doing the
same to liveness made the restart count climb instead. That is the whole
difference in one experiment.

**10. `DB_MAX_CONNS` is per pod (Step 17).** Settled on `20`: three replicas
use 60 connections, and the fourth pod that exists briefly during a rolling
update takes it to 80 - still under the 97 that `max_connections=100` leaves
after the 3 reserved for superusers. Pools are also lazy, so at rest
`pg_stat_activity` shows almost nothing; the ceiling only matters under load.

**11. gRPC balances per connection, not per request (Steps 13 and 17).** One
client connection means one pod does all the work. Watch which pod answers
with `kubectl logs -l app=crm-server --prefix -f`, and send requests from
inside the cluster to see them spread.

**12. The namespace resets after `minikube start`.** kubectl goes back to
`default`, and objects in `crm` appear to have vanished. Re-run
`kubectl config set-context --current --namespace=crm`.

**13. `kubectl scale` and `kubectl rollout undo` change the cluster, not the
files.** The next `kubectl apply` silently undoes them. The YAML is the source
of truth; use those commands for experiments and emergencies only.

**14. minikube's metrics-server is off by default (Step 19).** `kubectl top`
fails with `Metrics API not available` until
`minikube addons enable metrics-server`.

**15. The Go runtime does not see container limits (Step 19).** It sizes itself
for the whole node - 12 CPUs here - so `GOMAXPROCS` and `GOMEMLIMIT` have to be
set alongside `resources`, and updated whenever those limits change.

**16. gRPC probes do not support named ports (Part E).** `tcpSocket` and
`httpGet` accept `port: grpc`, but a `grpc:` probe must use the number `50051`
or the manifest is rejected.

**17. `GracefulStop` waits forever on streams that never end (Part E).** With a
client holding a health `Watch` stream open, deleting a pod took 31s: the drain
never finished, Kubernetes sent SIGKILL at the end of its grace period, `Close`
never ran, and the ingester never drained - buffered log entries would have
been lost. A long-lived `IngestLogs` stream would do the same. Fixed by
bounding the drain to 10s and then calling `Stop`; the same test then took 11s
and ended with `ingester stopped, all workers drained`.

**18. Health probes flooded the logs (Part E).** The logging interceptor printed
every probe call - 90 of every 90 log lines, about 18 a minute per pod.
Successful health checks are no longer logged; failing ones still are.

**19. Never rebuild an existing image tag.** Rebuilding `v3` from newer code
made `v3` and `v4` the same image, so `v3` no longer means what the rollout
history says it means: a `kubectl rollout undo` to that revision would run the
new code. Every build gets a new tag.

**20. `custom-columns` with `[0]` needs single quotes in zsh.** Unquoted, zsh
treats `[0]` as a filename pattern and fails with `no matches found`.
