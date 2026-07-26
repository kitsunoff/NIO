# E2E — `NixStatefulSet`

> Read `00-harness-and-conventions.md` and the **shared workload model** section
> in `05-nixdeployment.md` first.

## NixStatefulSet specifics

Wraps `apps/v1` **StatefulSet**. Ordered stateful workload; rolls out on a new
revision. shortName `nixsts`.

- **Spec**: `nix` + `statefulSetTemplate` (`appsv1.StatefulSetSpec`,
  **required**, schemaless — `serviceName` has no default; native `replicas`,
  `volumeClaimTemplates`, `updateStrategy`, `podManagementPolicy`).
- **Status extras**: `readyReplicas`, `updatedReplicas`, `currentRevision`,
  `updateRevision`. **No `availableReplicas`.**
- **project** creates if absent, else updates only `replicas`, `template`,
  `updateStrategy` (selector + volumeClaimTemplates immutable).
- **No operator `maxUnavailable` default** — ordered `RollingUpdate` already
  halts on the first unready pod (highest ordinal first).
- **observe() stall precedence**:
  1. new-revision pods' `instantiate` init failing → `Stalled` reason
     `InitBuildFailing`, phase `Degraded`.
  2. rolled out (`updatedReplicas==desired && readyReplicas==desired`, gen
     current, desired>0) → `markReady`. **No** `availableReplicas` check and
     **no** native ReplicaFailure check (unlike NixDeployment).
  3. init building → `markProgressing(Building)`.
  4. else → `markProgressing(Progressing)`.

## Scenarios to cover

### S1 — Happy path to Ready (covered today — keep/expand)

Existing e2e: `serviceName stateful`, replicas 1, substitute-only → `.status.phase
== Ready`. Add: assert the StatefulSet preserves `serviceName`, the pod template
carries `nio.homystack.com/revision`, and `rolledOutRevision`/`currentRevision`
are set.

### S2 — Ordered rollout halts on a broken new revision (gap; key differentiator)

1. Start Ready with **replicas ≥ 2** and persistent `volumeClaimTemplates`.
2. Update `run` to `.#doesnotexist`.
3. Assert phase `Degraded`, `Stalled=True` reason `InitBuildFailing`.
4. Assert the rollout halts at the **highest-ordinal** pod while lower ordinals
   keep serving on the old revision (ordered `RollingUpdate`, no maxUnavailable).
   This is distinct from NixDeployment's surge-only + ReplicaFailure behavior.

### S3 — Revision-change rollout (gap)

From Ready on `rev` A, set `rev` B → pod-template `nio.homystack.com/revision`
changes, `updateRevision` advances, phase `Progressing`/`Building` → `Ready`,
`readyReplicas == desired`.

### S4 — PVC / volumeClaimTemplates (gap)

With `volumeClaimTemplates`, assert the per-pod PVCs bind (needs a StorageClass
on Kind — `standard`). Note volumeClaimTemplates are **immutable** post-create;
editing them is silently not applied (document, no crash).

### S5 — Infra-not-ready / delegated build / suspend / git-failure (gap)

- Not-yet-Ready store → phase `Degraded`, `Stalled` reason `InfraNotReady`, no
  StatefulSet; converges when store Ready.
- Delegated build via `builderRef`/`builderTemplate` on a non-cached flake →
  builds on the builder, realizes into store, → Ready (untested for this kind).
- `suspend:true` → phase `Suspended`.
- Bad `ref` → `GitSynced=False`, `Stalled` reason `GitError`.

## Assertions cheat-sheet

| What | jsonpath |
| --- | --- |
| phase | `{.status.phase}` |
| rolledOutRevision | `{.status.rolledOutRevision}` |
| readyReplicas (CR) | `{.status.readyReplicas}` |
| updateRevision | `{.status.updateRevision}` |
| Stalled reason | `{.status.conditions[?(@.type=='Stalled')].reason}` |
| STS readiness | `kubectl get sts <name> -o jsonpath={.status.readyReplicas}` |
| serviceName | `kubectl get sts <name> -o jsonpath={.spec.serviceName}` |

## Out of scope / do not assert

- `availableReplicas` on the CR (NixStatefulSet does not track it).
- A native `ReplicaFailure`-driven stall (only `InitBuildFailing` applies here).

## Suggested placement

Add `It(...)` blocks under the existing `Describe("Nix workloads")` in
`test/e2e/nixworkloads_test.go` (S1 already there).
