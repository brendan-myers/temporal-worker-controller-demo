# Demo cheatsheet

Everything you need while presenting, on one page. See [README.md](README.md)
for how the demo is built and why.

| | |
|---|---|
| Demo UI | <http://localhost:8080> |
| Temporal UI | <http://localhost:8233> |
| Temporal gRPC | `localhost:7233` |
| Worker Deployment | `demo/order-service` |
| Task queue | `order-service` |

## Before the room fills

Sign in to Docker Desktop first — `minikube` and every image build fail without
it. Terminals 1 and 3 stay running for the whole session.

```bash
make temporal   # terminal 1, leave running — dev server with the versioning APIs on
make setup      # terminal 2, once — cluster, controller, images, resources
make ui         # terminal 3, leave running — http://localhost:8080
```

`make setup` takes a few minutes the first time, mostly pulling the Go base
image into minikube. Later runs are quick.

## The run

### 1. Establish the baseline

Nothing to do — `make setup` leaves the deployment on v1.

> One box, `v1-…`, marked **♡ Current**, **3/3 pods**, no dots. Header reads
> `order-worker:v1 · AllAtOnce · 3 replicas`.

### 2. Start some orders

Start **5 Pinned**, then **5 Auto-Upgrade**. Both run the same workflow code;
only the registered versioning behaviour differs.

> **10 dots** in the v1 box, **5 carrying a pin**. Orders live ~25 minutes, so
> they outlast the demo.

### 3. Roll out v2, progressively

Image **v2**, strategy **Progressive**, steps **25% / 30s** then **50% / 30s**,
Apply. This patches the `WorkerDeployment`; the controller decides when traffic
actually moves.

**Do this once, early.** A rollout to a version that was Current in the last
hour is treated as a rollback and jumps straight to 100%, ignoring your steps.
The UI warns you before you Apply.

> Over ~2 minutes: a second box appears and its pods come up; the ramp row in
> the deployment frame climbs **25% → 50%**; Auto-Upgrade dots **move to v2**
> while pinned dots **stay**; v1 flips to **⇩ Draining**, v2 to **♡ Current**.

### 4. Show the handoff

Click an auto-upgraded dot. Every step records the worker version that ran it.

> History with a visible switch, e.g. `v1: step 82 → v2: step 83`.
> **Open in Temporal UI ↗** jumps to that run. **×** or **Esc** closes the panel.

### 5. Move the pinned orders

Click **Upgrade N pinned** on the v1 box, or a single pinned dot then **Pin to
current**. This is a per-execution versioning override, which is why it works
even though the controller owns routing.

> Pinned dots **jump to the v2 box**, still pinned. v1 empties, goes **Drained**,
> and its box disappears 5 minutes later.

### 6. Do it from the terminal

Leave the browser on screen. The UI watches the resource rather than owning it.

```bash
make rollout VERSION=v1 STRATEGY=AllAtOnce
```

> The UI updates **within a second**, unprompted. Rollback to a version that was
> current in the last hour is **instant** — the controller's fast path.

### 7. Optional — break it on purpose

v3 adds an unguarded activity inside the workflow loop: a real non-determinism.
Start orders on v2 first, *then* roll out v3, or nothing breaks.

```bash
make rollout VERSION=v3 STRATEGY=AllAtOnce
```

> Auto-Upgrade dots turn **red and pulse**; pinned ones carry on. The panel shows
> `cause=WorkflowTaskFailedCauseNonDeterministicError — while moving to v3-…`,
> and the Temporal UI shows `[TMPRL1100]` a Timer command was expected.
> Red dots stay in the **old** box — see below.

### 8. Optional — recover

Click **Roll back to v2** on the failing box, or roll out by hand.

```bash
make rollout VERSION=v2 STRATEGY=AllAtOnce
```

> Most red dots clear on their next workflow task. Stragglers sit in retry
> backoff for minutes; reset one to skip the wait:
>
> ```bash
> temporal workflow reset -w <workflow-id> --type LastWorkflowTask --reason "bad rollout"
> ```

## Commands

| | |
|---|---|
| `make status` | What the controller and Temporal each believe |
| `make logs` | Follow the worker controller |
| `make reset` | Terminate every running order |
| `make recover` | Reset to a clean slate after a wedged rollout |
| `make images` | Rebuild the three worker images |
| `make rollout VERSION=v2 STRATEGY=Progressive` | Roll out from the terminal |
| `make teardown` | Remove the demo; leaves the controller installed |
| `kubectl --context minikube get workerdeployment -n demo -w` | Watch Current / Target / Ramp % |
| `temporal worker deployment describe --name demo/order-service` | Temporal's own view of routing |

## Reading the diagram

| | |
|---|---|
| ♡ | **Current** — receives new work |
| ⇩ | **Draining** — only pinned orders left; pods stay up |
| 📌 | **Pinned** — stays on its version |
| red, pulsing | **Failing** — not making progress |

## Timings

| | |
|---|---|
| Order lifetime | ~25 min |
| Progressive step pause | ≥ 30 s (enforced) |
| Ramp percentage range | 1–99 (integers) |
| Rollback fast-path window | 1 hour |
| UI refresh | 1 s |
| Drained box disappears | 5 min after drained |

## When it misbehaves

| Symptom | Cause | Fix |
|---|---|---|
| `docker` refuses everything | Docker Desktop is signed out | Sign in, re-run `make setup` |
| Worker pods stuck **Pending** | Node CPU reserved by something else on the cluster | `kubectl --context minikube describe node minikube`, free what is holding it |
| Header shows `TemporalStateFetchFailed` | A rollout is wedged; the retry loop tripped the server's rate limit. The error banner names the real cause | `make recover` |
| Rollout never promotes the new version | Target is missing a task queue the current version has — usually a version that was deleted and resurrected | `make recover`, then roll out again |
| Progressive ramp never appears; everything moves at once | Target was Current in the last hour, so it is treated as a **rollback**: 100% immediately, strategy ignored. Controller logs `Detected rollback scenario using LastCurrentTime` | Ramp to a version not Current recently, or wait out the hour |
| Progressive skipped on the very first rollout | Skipped while there is no Current version and no unversioned pollers | Establish v1 first, then ramp to v2 |
| Red dots minutes after a rollback | Workflow task retry backoff, not a stuck rollout | Wait, or `temporal workflow reset --type LastWorkflowTask` |
| Your own `kubectl` shows nothing | Docker Desktop reset the context to its own | The demo pins `--context minikube`; do the same |

## Answers to have ready

**Does ramping move workflows already running?** Yes — Auto-Upgrade executions
follow the split, not just new ones. Measured on 100 of them: **21%** on the new
version at a 25% ramp, **54%** at 50%, **100%** once promoted to Current.

**Why do red dots stay in the old box?** An order is filed under the last version
that *completed* a workflow task for it. It cannot complete one on the broken
version, so it stays filed under the old one. The panel's **Moving to** row names
what it is failing against.

**Why not just override the broken orders?** An in-flight version transition
still routes the next task to the version that cannot replay, so the override
lands but changes nothing. Roll the deployment back first.

**Where does the build ID come from?** `<image tag>-<pod template hash>`, hashed
over the whole pod template — change env or resources and you mint a new version
too.

**Can I set the current version by hand?** Not while the controller runs it. It
claims `ManagerIdentity` and will undo you on the next reconcile. Change the
resource instead.

**Where does the deployment name come from?** `<k8s namespace>/<resource name>`.
Not configurable.

**Why two workflow types?** Versioning behaviour is a property of the code. The
same function is registered twice — once Pinned, once Auto-Upgrade — so the UI
just picks which type to start.
