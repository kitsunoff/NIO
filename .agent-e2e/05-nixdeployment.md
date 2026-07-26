# E2E — `NixDeployment`

> Read `00-harness-and-conventions.md` first, and the **shared workload model**
> section below (identical for `05`–`08`).

## Shared workload model (applies to NixDeployment / NixJob / NixCronJob / NixStatefulSet)

Every kind embeds `spec.nix` (`NixSpec`) plus a kind-specific `*Template`.

- **Source**: `spec.nix.source.{gitRepo, ref (default "main"), rev (pinned SHA,
  disables polling), fluxSourceRef, credentialsRef, pollInterval (default 1m)}`.
  `dir` exists in the type but is **not wired** — do not rely on it.
- **Important limitation**: the workload family passes **`nil` credentials** to
  the revision resolver (`resolveRevision`). `credentialsRef` only affects Secret
  **watch** indexing, **not** actual ls-remote resolution. So **private-repo
  revision resolution fails** for workloads today — pin `rev` for private
  sources. (This is v1alpha2 Gap 1 in the handoff.)
- **No `additionalFiles`** in the workload family (that is NixosConfiguration).
- **run/args/prebuild/image/containerName/nixFlags/localStore/suspend/triggerOnChange**.
- **Infra wiring**: `storeRef` (→ NixStore substituter), `builderRef` (shared
  NixBuilder) XOR `builderTemplate` (dedicated owned `<workload>-builder`).
  `resolveInfra` requires the store `phase==Ready`+`substituterURL` and the
  builder `ready`+`builderEndpoint`; otherwise → `markStalled(InfraNotReady)`,
  requeue 15s, **no native object created**.
- **Rendering** (`nixrender`): pod template gets 3 init containers in order —
  `bootstrap` (seed `/nix`), `fetch-source` (git fetch-by-SHA or Flux tarball),
  `instantiate` (`nix build`; its failure is what the controllers detect as a
  stall) — plus the app container running `nix run <run> -- <args>`. A
  **compositeRevision** `r-<sha256(revision,run,args)[:12]>` is stamped as label
  and annotation `nio.homystack.com/revision`; changing revision/run/args rolls
  the workload.
- **Shared status**: `observedGeneration`, `phase`
  (`Building`/`Progressing`/`Ready`/`Degraded`/`Failed`/`Suspended` are the ones
  actually set; `Pending`/`Resolving` constants exist but are never assigned),
  `resolvedRevision`, `lastPolledTime`, `rolledOutRevision`, `workloadRef`,
  `conditions` (`Ready`, `Reconciling`, `Stalled`, `GitSynced`, `Progressing`).
- **Reconcile skeleton**: get → deleting? drop finalizer → ensure finalizer
  `nio.homystack.com/finalizer` → `Reconciling=True` → `suspend`? → phase
  `Suspended` → resolve revision (fail → `Stalled/GitError`, requeue 15s) →
  `GitSynced=True` → resolveInfra (notReady → `Stalled/InfraNotReady`, 15s) →
  project native object → observe → status update → requeue at `pollInterval`
  (default 1m). `infraRequeue=15s`.
- **Watches**: `For(kind)`, `Owns(native)`, Secret watch by
  `spec.nix.source.credentialsRef.name`, Flux source watches (skipped if CRDs
  absent). NixCronJob also `Owns(Job)`.
- **Env facts for e2e**: operator image is distroless (no git) → pin `rev` in
  e2e. Nix pods need the shared `store` (+ `builder` for delegated builds).

Already covered by `test/e2e/nixworkloads_test.go`: store+builder `BeforeAll`;
this kind's happy path; a delegated build (via NixJob). The per-kind docs list
**gaps** on top of that.

---

## NixDeployment specifics

Wraps `apps/v1` **Deployment**. Long-running service; rolls out on revision
change. shortName `nixdeploy`.

- **Spec**: `nix` + `deploymentTemplate` (`*appsv1.DeploymentSpec`, schemaless).
  Reads `replicas`, `template`, `strategy`, `selector`.
- **Status extras**: `readyReplicas`, `updatedReplicas`, `availableReplicas`.
- **Default strategy = surge-only**: when `strategy` unset, `RollingUpdate` with
  `MaxUnavailable=0`, `MaxSurge=25%` — a broken new revision stalls **without**
  shedding old-revision capacity.
- **observe() stall precedence**:
  1. new-revision pods' `instantiate` init failing → `Stalled`, reason
     `InitBuildFailing`, phase `Degraded`.
  2. native `DeploymentReplicaFailure=True` → `Stalled`, reason `ReplicaFailure`.
  3. rolled out (updated==available==ready==desired, gen current) → `markReady`.
  4. init building → `markProgressing(Building)`.
  5. else → `markProgressing(Progressing)`.

## Scenarios to cover

### S1 — Happy path to Ready (covered today — keep/expand)

Existing e2e: `nixpkgs#bash sleep 3600`, replicas 1, storeRef store,
substitute-only → `.status.phase == Ready`, `deploy web availableReplicas == 1`.
Add: assert `rolledOutRevision` non-empty and the Deployment pod template carries
label/annotation `nio.homystack.com/revision`.

### S2 — Revision-change rollout (gap)

1. Start Ready on `rev` A.
2. Update `spec.nix.source.rev` to a different valid commit B.
3. Assert the pod-template `nio.homystack.com/revision` annotation changes,
   `rolledOutRevision` advances, phase goes `Progressing`/`Building` → `Ready`.
4. During a **good** rollout, surge-only means old pods stay up:
   `availableReplicas` never dips below the desired count.

### S3 — Broken new revision stalls without shedding capacity (gap)

1. From Ready (replicas ≥ 2), update `run` to `.#doesnotexist`.
2. Assert phase `Degraded`, `Stalled=True` reason `InitBuildFailing`.
3. Assert old-revision pods keep serving: `availableReplicas` stays at the old
   count (surge-only, `MaxUnavailable=0`).

### S4 — Build-failure from a cold start (covered today)

Existing e2e "broken": `run: .#doesnotexist` on a fresh NixDeployment → phase
`Degraded`, `Stalled` condition `True`. Keep.

### S5 — Infra-not-ready gating (gap, in-cluster)

1. NixDeployment referencing a **not-yet-Ready** store → phase `Degraded`,
   `Stalled` reason `InfraNotReady`, **no** Deployment created.
2. Bring the store to Ready → transition out of Stalled, Deployment created,
   → Ready.

### S6 — Delegated build for a Deployment (gap)

Existing delegated-build e2e only covers NixJob. Add a NixDeployment with
`storeRef`+`builderRef` on a non-cached flake → builds on the builder, realizes
into the store, pods substitute, → Ready.

### S7 — Suspend (gap)

Set `spec.nix.suspend: true` → phase `Suspended`, Deployment left as-is (no
rollout).

### S8 — Git resolve failure (gap)

Bad `ref` / unreachable repo → `GitSynced=False`, `Stalled` reason `GitError`,
requeue 15s, no Deployment. (Also documents the private-repo-without-creds
limitation: a private source with only `credentialsRef` set still fails here.)

## Assertions cheat-sheet

| What | jsonpath |
| --- | --- |
| phase | `{.status.phase}` |
| rolledOutRevision | `{.status.rolledOutRevision}` |
| availableReplicas (CR) | `{.status.availableReplicas}` |
| Stalled reason | `{.status.conditions[?(@.type=='Stalled')].reason}` |
| GitSynced status | `{.status.conditions[?(@.type=='GitSynced')].status}` |
| Deployment availability | `kubectl get deploy <name> -o jsonpath={.status.availableReplicas}` |
| pod-template revision | `kubectl get deploy <name> -o jsonpath={.spec.template.metadata.annotations.nio\.homystack\.com/revision}` |

## Suggested placement

Add `It(...)` blocks under the existing `Describe("Nix workloads")` in
`test/e2e/nixworkloads_test.go` (S1 already there; add S2/S3/S5/S6/S7/S8).
