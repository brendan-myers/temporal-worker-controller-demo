# Temporal Worker Controller demo

A live, visual demo of deployment-based Worker Versioning driven by the
[Temporal Worker Controller](https://github.com/temporalio/temporal-worker-controller).

The UI reproduces the diagram from the *How workflows are routed to versions*
slide: a Worker Deployment frame containing one box per version, each holding a
dot for every running workflow execution.

![The demo mid-rollout: a Worker Deployment frame holding two version boxes.
v1-fcc8 is draining with ten pinned orders still on it, while v2-fc69 is current
with ten auto-upgraded orders that moved across.](docs/images/rollout.png)

*Mid-rollout: `v1-fcc8` is ⇩ Draining, still running the ten **pinned** orders that
refuse to move. `v2-fc69` is ♡ Current, holding the ten **auto-upgrade** orders
that followed it across. `Upgrade 10 pinned` moves the stragglers on demand.*

**Presenting it?** [CHEATSHEET.md](CHEATSHEET.md) is the one-page runbook:
commands, what should appear at each step, and how to recover when it wedges.

From the browser you can start orders as **Pinned** or **Auto-Upgrade**, roll
the deployment onto a new worker image, watch Auto-Upgrade orders migrate while
Pinned ones stay put, and manually move a pinned order onto the new version.

Everything the UI does to the deployment, you can also do with `kubectl` — the
UI watches the resource rather than owning it, so a terminal `kubectl apply`
shows up on screen immediately.

## How the pieces fit

| Where | What |
|---|---|
| Your machine | `temporal server start-dev`, bound to `0.0.0.0` so pods can reach it |
| minikube | cert-manager, the worker controller, and namespace `demo` holding a `Connection` and a `WorkerDeployment` |
| Your machine | `demoui`, a single Go binary serving the UI on `:8080` |

The controller creates one Kubernetes `Deployment` of worker pods per version
and drives Temporal's routing config. The demo UI only ever *reads* that routing
config — it changes the `WorkerDeployment` resource and lets the controller
react, which is the behaviour being demonstrated.

## Prerequisites

- minikube, kubectl, helm, Go 1.24+
- Temporal CLI ≥ 1.4.1 (this demo was built against 1.6.1 / server 1.30.1)
- A working Docker daemon — minikube needs it, and on Docker Desktop you must
  be signed in or every `docker` command fails

## Quick start

```bash
make temporal   # terminal 1 — leave running
make setup      # terminal 2 — cluster, controller, images, resources
make ui         # terminal 3 — then open http://localhost:8080
```

`make setup` takes a few minutes the first time, mostly pulling cert-manager and
building the three worker images.

Every script and Make target pins the kubeconfig context to the minikube profile
(`PROFILE`, default `minikube`) rather than trusting the current context —
Docker Desktop resets kubectl to its own context whenever it restarts, and a
demo that silently talks to the wrong cluster is a bad way to find that out.
Override with `make PROFILE=my-profile setup`.

Useful while presenting:

```bash
make status   # what the controller and Temporal each believe
make logs     # follow the controller
make reset    # terminate every running order
```

## Running the demo

**1. Establish v1.** After setup the deployment is on `order-worker:v1` with an
`AllAtOnce` strategy, so one version box shows ♡ Current with 3 pods.

There is nothing to ramp *to* on a first rollout, so a Progressive strategy
would be skipped here — the controller jumps straight to Current when there is
no current version and no unversioned pollers. Establish v1 first, then ramp.

**2. Start some orders.** Start 5 Pinned and 5 Auto-Upgrade. Ten dots appear in
the v1 box, five carrying pins. Each order runs 40 steps at a few seconds each,
so they stay alive through a rollout.

**3. Roll out v2.** Pick `v2`, strategy `Progressive`, steps 25% / 30s and
50% / 30s, and Apply. Then watch:

- a second version box appears as the controller starts v2 pods
- the ramp percentage climbs as each step's pause elapses
- **new** orders split between the two versions according to the ramp
- **Auto-Upgrade** orders move to v2 on their next workflow task
- **Pinned** orders stay on v1, and v1 flips to ⇩ Draining

v2 is a deliberately replay-safe change: it issues exactly the same workflow
commands as v1, so orders can migrate mid-flight.

**4. Show the handoff.** Click an auto-upgraded dot. Its step history names the
worker version that ran each step, so you can point at the exact step where it
changed hands.

**5. Move the pinned orders.** Click a pinned dot and *Pin to current*, or use
*Upgrade N pinned* on the v1 box to move them all at once. This is a per-
execution versioning override, which is why it works even though the controller
owns the deployment's routing config. Once v1 is empty it drains and, a minute
later, the controller deletes it and the box disappears.

**6. Do it again from a terminal**, with the UI on screen:

```bash
make rollout VERSION=v1 STRATEGY=AllAtOnce
```

### Optional: why pinning matters, and how to recover

`v3` is deliberately **not** replay-safe — it adds an unguarded activity call
inside the workflow loop. Start some orders on v2, then roll out v3:

```bash
make rollout VERSION=v3 STRATEGY=AllAtOnce
```

**What you'll see.** Auto-Upgrade orders migrate onto v3, fail to replay, and
turn red in the UI. Their detail panel shows the cause:

```
category=WorkflowTaskFailed · cause=WorkflowTaskFailedCauseNonDeterministicError
  — while moving to v3-c856
```

and the underlying failure, via *Open in Temporal UI*, is:

```
[TMPRL1100] During replay, a matching Timer command was expected in history
event position 11. However, the replayed code did not produce that.
```

Pinned orders are untouched and keep running. That contrast is the whole point.

Note the red dots stay in the **old** version's box. That is correct: an order is
filed under the last version that *completed* a workflow task for it, and it
cannot complete one on v3. The panel's *Moving to* row names the version it is
failing against.

**The fix: roll the deployment back.** Click *Roll back to v2* on the failing
version's box, or:

```bash
make rollout VERSION=v2 STRATEGY=AllAtOnce
```

Because v2 was current within the last hour, the controller's rollback
fast-path makes it current immediately, and the wedged orders resume on their
next workflow task.

**If some orders lag behind.** Workflow task retries back off, so an order that
has already failed ~10 times can take several minutes to retry after the
rollback. It does recover on its own; to skip the wait, reset it:

```bash
temporal workflow reset -w <workflow-id> --type LastWorkflowTask --reason "bad rollout"
```

**What does *not* work: overriding the wedged order's version.** Pinning a
failing execution back to the good version looks like the obvious fix and it is
not — an in-flight version transition still routes the next workflow task to the
version that cannot replay, so it keeps failing. Its history shows the task
being started by a v3 worker even after the override lands. Versioning overrides
are for moving *healthy* executions; roll the deployment back first.

## How versioning behaviour is chosen

Behaviour is a property of the code, so the same Go function is registered twice
under two workflow type names:

```go
w.RegisterWorkflowWithOptions(v1.ProcessOrder, workflow.RegisterOptions{
    Name:               "ProcessOrder",
    VersioningBehavior: workflow.VersioningBehaviorPinned,
})
w.RegisterWorkflowWithOptions(v1.ProcessOrder, workflow.RegisterOptions{
    Name:               "ProcessOrderAutoUpgrade",
    VersioningBehavior: workflow.VersioningBehaviorAutoUpgrade,
})
```

The UI's Pinned / Auto-Upgrade choice just picks which type to start.

## Layout

```
cmd/worker      the versioned worker that runs in the cluster
cmd/demoui      the UI and control plane that runs on your machine
internal/orders the order workflow: shared types, activities, and v1/v2/v3
internal/kube   reads and patches the WorkerDeployment
internal/temporalstate  reads versions and workflows, applies versioning overrides
internal/server poll loop, SSE, HTTP API
web             the single-page UI, embedded into the binary
deploy          Connection and WorkerDeployment manifests
scripts         setup, image builds, terminal rollout, teardown
```

Each worker image contains exactly one workflow version, selected by build tag
(`demo_v1` / `demo_v2` / `demo_v3`) from the Dockerfile's `WORKER_VERSION` arg —
so "the new image has the new code" is literally true.

## Things that will bite you

- **The dev server needs `--ip 0.0.0.0`** and the two `--dynamic-config-value`
  flags in `make temporal`. The Worker Deployment APIs are off by default.
- **A Progressive rollout is skipped for the first version.** See step 1.
- **`pauseDuration` must be at least 30s** and `rampPercentage` must be 1–99;
  the CRD enforces both, so there is no five-second ramp.
- **The controller owns routing.** It claims `ManagerIdentity` on the Worker
  Deployment, so calling `temporal worker deployment set-current-version` by
  hand will be fought by the next reconcile. Change the resource instead.
- **Build IDs are `<image tag>-<pod template hash>`**, computed over the whole
  pod template — changing resources or env mints a new version too. A corollary
  that is easy to get wrong: patch the CR with a **JSON Patch**, not a merge
  patch. A merge patch on `spec.template.spec.containers` replaces the whole
  array, quietly dropping `imagePullPolicy`, `env` and `resources` — and because
  those feed the hash, the "same" image then produces a different build ID, so
  rolling back never lands on the version you rolled away from.
- **The Temporal-side deployment name is `<k8s namespace>/<resource name>`**,
  here `demo/order-service`, and is not configurable.
- **The CRD kinds are `WorkerDeployment` and `Connection`.** The older
  `TemporalWorkerDeployment` / `TemporalConnection` kinds are deprecated and the
  controller rejects creating them.
- **An older controller already on the cluster needs upgrading, not installing
  alongside.** The CRDs are cluster-scoped but the Helm release that owns them is
  not, and Helm refuses to adopt CRDs owned by a release in another namespace.
  `setup.sh` finds the existing CRD release and upgrades it in place.
- **Don't let drained versions be deleted if you might roll back to them.** A
  build ID is a hash of the pod template, so rolling v1 → v2 → v1 → v2 reuses the
  same v2 build ID. If that version was already deleted, it comes back missing
  its activity task queue, and the controller refuses to promote it — *"missing
  active task queues from the current version"* — wedging the rollout, which
  surfaces as a `TemporalStateFetchFailed` condition once the retry loop trips
  the server's rate limit. `sunset.deleteDelay` is set to **5m** as a compromise:
  long enough to roll back to a version within a normal demo beat, short enough
  that a drained version's box still disappears while you are talking. If you do
  get stuck, `make recover` resets it.
- **`make teardown` leaves the controller and cert-manager installed**, since
  other things on the cluster may depend on them. It removes only the demo
  namespace and resources. Use `make teardown-all` to delete the cluster.
