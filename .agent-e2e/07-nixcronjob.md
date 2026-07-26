# E2E — `NixCronJob`

> Read `00-harness-and-conventions.md` and the **shared workload model** section
> in `05-nixdeployment.md` first.

## NixCronJob specifics

Wraps `batch/v1` **CronJob**. Scheduled runs, optionally also fires on a new
revision. shortName `nixcron`.

- **Spec**: `nix` + `cronJobTemplate` (`batchv1.CronJobSpec`, **required**,
  schemaless — `schedule` has no default; native `concurrencyPolicy`, `suspend`,
  history limits, embedded `jobTemplate`).
- **Status extras**: `lastScheduleTime`, `lastSuccessfulTime`, `activeJobs`
  (names). **No** `succeeded`/replica fields.
- **`triggerOnChange` default `false`** (unlike the other kinds).
- **Behavior**: `project` creates/updates the owned CronJob (whole `spec`
  replaced, jobTemplate pinned to the resolved revision). If the revision changed
  **and** `triggerOnChange` is true → `fireImmediateJob`: a one-off Job
  `<name>-<compositeRevision>-manual` from the jobTemplate; honors
  `concurrencyPolicy: Forbid` (skips if `status.activeJobs` non-empty);
  idempotent (skips if that Job name already exists).
- **Phase**: `observe` **always** `markReady` — the CronJob is Ready as
  *scheduling infrastructure*; individual run failures do **not** turn the
  NixCronJob `Degraded`/`Failed`. It only stalls on git/infra errors. If the
  owned CronJob can't be read → `markProgressing(Progressing)`.

## Scenarios to cover

### S1 — CronJob created + immediate fire on change (covered today)

Existing e2e "fires an immediate Job for a NixCronJob on a new revision":
`triggerOnChange:true`, schedule `*/5 * * * *` → owned CronJob `tick` exists with
`spec.schedule == */5 * * * *`, and ≥1 immediate Job with label
`nio.homystack.com/workload-name=tick`. Add: assert the manual Job name ends with
`-manual` and phase is `Ready`.

### S2 — `triggerOnChange:false` (the default) suppresses immediate fire (gap)

1. Create a NixCronJob **without** `triggerOnChange` (defaults false).
2. Assert the owned CronJob is created and phase `Ready`, but **no** `-manual`
   immediate Job is created on the initial revision. Only scheduled runs happen.

### S3 — `concurrencyPolicy: Forbid` suppresses immediate fire while active (gap)

1. `triggerOnChange:true`, `cronJobTemplate.concurrencyPolicy: Forbid`.
2. While a run is active (`status.activeJobs` non-empty), change the revision →
   assert **no** new `-manual` Job is fired (Forbid honored).
3. Once no jobs are active, a revision change fires the immediate Job.

### S4 — Run failure does NOT degrade the NixCronJob (gap)

Use a `run` that fails at runtime. Let a scheduled/manual Job fail. Assert the
**NixCronJob** stays phase `Ready` (it is infra) — only its child Job shows
failure. This encodes the deliberate "cron never goes Degraded on a run failure"
contract.

### S5 — Status bookkeeping (gap)

After at least one scheduled run, assert `lastScheduleTime`,
`lastSuccessfulTime`, and `activeJobs` populate from the owned CronJob. (A `*/5`
schedule is slow; for e2e prefer forcing a run via the immediate-fire path, or a
`* * * * *` schedule with a generous timeout.)

### S6 — Infra-not-ready / git-resolve-failure / suspend (gap)

- Not-yet-Ready store → phase `Degraded`, `Stalled` reason `InfraNotReady`, no
  CronJob; converges when store Ready.
- Bad `ref` → `GitSynced=False`, `Stalled` reason `GitError`.
- `spec.nix.suspend:true` → phase `Suspended` (distinct from
  `cronJobTemplate.suspend`, which only pauses the native CronJob scheduling).

## Assertions cheat-sheet

| What | jsonpath |
| --- | --- |
| phase | `{.status.phase}` |
| lastScheduleTime | `{.status.lastScheduleTime}` |
| activeJobs | `{.status.activeJobs}` |
| Stalled reason | `{.status.conditions[?(@.type=='Stalled')].reason}` |
| owned CronJob schedule | `kubectl get cronjob <name> -o jsonpath={.spec.schedule}` |
| manual Job | `kubectl get jobs -l nio.homystack.com/workload-name=<name>` (name suffix `-manual`) |

## Suggested placement

Add `It(...)` blocks under the existing `Describe("Nix workloads")` in
`test/e2e/nixworkloads_test.go` (S1 already there).
