# E2E — `NixJob`

> Read `00-harness-and-conventions.md` and the **shared workload model** section
> in `05-nixdeployment.md` first.

## NixJob specifics

Wraps `batch/v1` **Job**. Runs a flake attribute to completion; re-runs on a new
revision. shortName `nixj`.

- **Spec**: `nix` + `jobTemplate` (`*batchv1.JobSpec`, schemaless; native
  `backoffLimit`, `completions`, etc.).
- **Status extras**: `activeJob` (name of current run-Job), `lastRunTime`,
  `succeeded`, `failed`.
- **Run-Job naming**: `<name>-<compositeRevision>` = `<name>-r-<hash12>`,
  immutable per revision. An existing Job with that exact name is **never
  mutated**.
- **`triggerOnChange` default `true`**: a new revision creates a new run-Job.
  With `triggerOnChange:false` → **run-once**: if any owned run-Job already
  exists, no new Job is created even on a new revision.
- `desiredJob` forces `RestartPolicy=Never` when the template leaves it empty
  (Jobs reject `Always`).
- **Phase mapping (observe)**: Job `Complete=True` → `markReady`; Job
  `Failed=True` → phase `Failed`, `Ready=False` reason `Failed`, `Stalled=True`
  reason `Failed` ("run-Job exhausted its backoffLimit"); Job absent →
  `markProgressing(Progressing)`; else `Progressing`.
- **GC**: `gcOldJobs` keeps the current + newest `jobHistoryLimit=3` completed
  run-Jobs, deletes older (background propagation).

## Scenarios to cover

### S1 — Runs to completion (covered today)

Existing e2e: `nixpkgs#hello`, storeRef store → `.status.phase == Ready`,
`.status.succeeded == 1`. Add: assert `activeJob == <name>-r-<hash>` and a Job
with label `nio.homystack.com/workload-name=<name>` exists with `RestartPolicy
Never` and 3 init containers.

### S2 — `triggerOnChange` semantics (gap)

1. **Default (true)**: from a completed run on `rev` A, change `rev` to B →
   a new run-Job `<name>-r-<hashB>` is created; the old one is retained (subject
   to GC).
2. **Run-once (false)**: with `triggerOnChange:false`, change the revision →
   assert **no** new Job is created (the pre-existing run-Job suppresses it).

### S3 — Job failure → Failed + Stalled (gap)

Point `run` at a flake attribute that builds but exits non-zero, or `.#doesnotexist`
with `jobTemplate.backoffLimit: 0`. On exhaustion assert phase `Failed`,
`Ready=False` reason `Failed`, `Stalled=True` reason `Failed`.

### S4 — History GC (gap)

Trigger >4 revision changes (each creating a new run-Job) and assert only the
current + newest 3 completed run-Jobs remain (older ones deleted).

### S5 — Delegated non-cached build (covered today — headline)

Existing e2e "delegates a non-cached build": NixJob with `storeRef`+`builderRef`
on the `nio-e2e-flake` marker flake → `.status.phase == Ready`, and the realized
path lands in the store (`ls /nix/store/*nio-e2e-app` in `store-0`). Keep; see
`04-nixbuilder.md` S3 for the store-side extension (second consumer cache hit).

### S6 — `builderTemplate` (dedicated owned builder) (gap)

Use `spec.nix.builderTemplate` instead of `builderRef` → the controller creates
an owned `<name>-builder` NixBuilder (owner ref → the NixJob). Assert it appears,
becomes Ready, and the build offloads to it.

### S7 — Infra-not-ready gating (gap)

NixJob referencing a not-yet-Ready store → phase `Degraded`, `Stalled` reason
`InfraNotReady`, no run-Job; then converges once the store is Ready.

### S8 — Suspend / git-resolve-failure (gap)

`suspend:true` → phase `Suspended`. Bad `ref` → `GitSynced=False`, `Stalled`
reason `GitError`.

## Assertions cheat-sheet

| What | jsonpath |
| --- | --- |
| phase | `{.status.phase}` |
| succeeded | `{.status.succeeded}` |
| failed | `{.status.failed}` |
| activeJob | `{.status.activeJob}` |
| Stalled reason | `{.status.conditions[?(@.type=='Stalled')].reason}` |
| owned run-Jobs | `kubectl get jobs -l nio.homystack.com/workload-name=<name>` |

## Suggested placement

Add `It(...)` blocks under the existing `Describe("Nix workloads")` in
`test/e2e/nixworkloads_test.go` (S1 and S5 already there).
