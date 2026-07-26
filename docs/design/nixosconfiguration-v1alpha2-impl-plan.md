# NixosConfiguration v1alpha2 orchestrator — implementation plan (agent-ready)

Executable plan for finishing the v1alpha2 `NixosConfiguration` rewrite. Design
rationale lives in [`nixosconfiguration-v1alpha2.md`](./nixosconfiguration-v1alpha2.md);
this file is the **build order**. Everything before the "Remaining work" section
is already done and on the branch — an agent should read it, then implement R1
then R2, keeping every commit valid.

## Hard constraints (apply to every commit)

- **Every commit must be valid**: `go build ./...`, `go vet ./...`,
  `golangci-lint run` (0 issues), and the envtest suite must pass. Validate
  before committing. Do not leave the tree non-compiling between commits.
- **TDD**: write/adjust tests first, then code. Red → green.
- Run envtest with `KUBEBUILDER_ASSETS="$(pwd)/bin/k8s/1.31.0-darwin-arm64"`
  (fetch once via `bin/setup-envtest use 1.31.0 --bin-dir bin`). Some host
  environments export `GIT_ASKPASS` (e.g. VS Code) which trips one pre-existing
  applyjob test; run tests with `env -u GIT_ASKPASS` if so.
- After any `api/` change run `make generate manifests`; commit the regenerated
  `config/crd`, `config/rbac`, and `zz_generated.deepcopy.go`.
- Repo conventions: `git commit --signoff`; semantic messages; end body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; feature branch, never main.

## Locked decisions

1. **In-place rewrite of v1alpha1** — NO `api/v1alpha2`, NO conversion webhook.
   The `NixosConfiguration` Kind stays `nio.homystack.com/v1alpha1`; its spec/
   status/controller change in place.
2. **Orchestrator model** — `NixosConfiguration` applies nothing itself; it
   drives child `NixJob`/`NixCronJob` workloads.
3. **One `NixosConfiguration` per `Machine`** (Gap 3 = uniqueness). A second
   config targeting the same `machineRef` is rejected/stalled.
4. **No global apply cap** (Gap 4 dropped) — per-machine crons apply in parallel
   across machines; that is desired, not a thundering herd to prevent.
5. **No `NixTarget` field** — target host/user/key come from the `Machine`; the
   orchestrator injects the SSH key + `NIX_SSHOPTS` into the child pod template.
6. **`NIX_SSHOPTS` permissive** — `-o StrictHostKeyChecking=no -o
   UserKnownHostsFile=/dev/null`. No host-key pinning in v1alpha2 (revisit later
   by adding a host-key source to `Machine`).
7. Defaults: `dayTwoSchedule` = `*/30 * * * *`; `Blocked` **suspends** the day-2
   cron (keeps history) rather than deleting it; `onRemoveFlake` is an attr in
   the same repo/ref (`.#<onRemoveAttr>`).

## Current state (done, on branch `feat/nixosconfiguration-orchestrator`)

Consolidated linear history off `main`; each commit built + tested:

- Workload capabilities the orchestrator relies on (Gap 1/2 + subdir + ssh):
  private-repo resolve + clone auth; `NixSpec.additionalFiles` (inject-files
  init, inline via owned `<workload>-nixfiles` ConfigMap); `NixSpec.Source.Dir`
  (flake-in-subdir); app wrapped in openssh whenever `NIX_SSHOPTS` is present.
- `spec.dayTwoSchedule` added to `NixosConfigurationSpec`.
- **Child builders** in `internal/controller/nixosconfiguration_children.go`
  (tested in `_children_test.go`) — REUSE these, do not reinvent:
  - `buildInstallNixJob(config, machine) (*NixJob, error)` — nixos-anywhere.
  - `buildDayTwoNixCronJob(config, machine) (*NixCronJob, error)` — nixos-rebuild
    switch, `triggerOnChange=true`, `ConcurrencyPolicy=Forbid`, schedule.
  - `buildDecommissionNixJob(config, machine) (*NixJob, error)` — onRemoveFlake.
  - Helpers: `mapAdditionalFiles`, `childNixSource`, `targetHost`,
    `targetSSHPodTemplate`, `install/dayTwo/decommissionChildName`.
  These inject the Machine's SSH key + `NIX_SSHOPTS` and map additionalFiles/
  subdir. The install/day-2 children get an ownerRef from the orchestrator; the
  decommission child is created WITHOUT an ownerRef (orphan) — see R1.

## Target design recap

### Status schema (replace the v1alpha1 status)

Keep: `observedGeneration`, `fullDiskInstallCompleted`, `lastAppliedTime`,
`targetMachine`, `conditions`. Add: `phase`, `resolvedRevision`,
`installJobRef`, `dayTwoCronJobRef`, `decommissionJobRef`, `installRetries`,
`onRemoveRetries`. **Remove**: `operationState`, `configurationHash`,
`additionalFilesHash`, `appliedCommit` (superseded by `resolvedRevision`, which
is sourced from the day-2 child's `status.rolledOutRevision`).

Conditions (derived from child status): `Ready` ← `phase==Ready`; `Stalled` ←
`phase∈{Degraded,Blocked}`; `GitSynced` ← child `GitSynced`; `Applied` ←
install succeeded and/or day-2 `lastSuccessfulTime` for the current revision.

### Phases & transitions (state machine)

| Phase | Meaning |
| --- | --- |
| `Pending` | finalizer added; resolving machine |
| `Blocked` | target Machine missing/undiscoverable; day-2 cron suspended |
| `Installing` | full-disk install NixJob running (nixos-anywhere) |
| `Converging` | day-2 NixCronJob created/updated; not yet confirmed healthy |
| `Ready` | day-2 cron healthy; last apply succeeded |
| `Degraded` | an install or day-2 run failed |
| `Removing` | deletion requested; decommission NixJob running |

Reconcile (pseudocode):

```text
if deletionTimestamp: return reconcileRemoving(cfg)
ensureFinalizer(cfg)
enforce uniqueness: if another NixosConfiguration in the ns has the same
    machineRef and an earlier creationTimestamp → Blocked/Stalled ("machine
    already owned by <name>"), requeue.
machine = get(cfg.spec.machineRef)
if machine == nil or !machine.status.discoverable:
    setPhase(Blocked); suspend(dayTwoCron if it exists); requeue
if cfg.spec.fullInstall and !cfg.status.fullDiskInstallCompleted:
    job = ensureInstallNixJob(cfg, machine)        # buildInstallNixJob, ownerRef
    Succeeded → fullDiskInstallCompleted=true; delete(job); continue
    Failed    → installRetries++; if over cap → Degraded; else recreate; requeue
    else      → Installing; requeue
cron = ensureDayTwoNixCronJob(cfg, machine)         # buildDayTwoNixCronJob, ownerRef
cfg.status.resolvedRevision = cron.status.rolledOutRevision
if cron healthy and lastSuccessfulTime for current rev:
    setPhase(Ready); writeBackMachineStatus(cfg, machine)   # Gap 6
elif cron last job failed: setPhase(Degraded)
else: setPhase(Converging)
aggregateConditions(cfg, cron); requeue

reconcileRemoving(cfg):
    suspend or delete dayTwoCron
    if onRemoveFlake == "" or machine gone: removeFinalizer; return
    job = ensureDecommissionNixJob(cfg, machine)    # buildDecommissionNixJob, NO ownerRef
    Succeeded → removeFinalizer
    Failed & onRemoveRetries exhausted (MaxOnRemoveRetries=3) → removeFinalizer (warn event)
    else → Removing; requeue
```

### Machine writeback (Gap 6)

On install/day-2 success write `machine.status`: `hasConfiguration=true`,
`appliedConfiguration=cfg.Name`, `appliedCommit=cfg.status.resolvedRevision`,
`lastAppliedTime`. On delete, clear them if `appliedConfiguration==cfg.Name`.

### Watches & RBAC

`For(&NixosConfiguration{}).Owns(&NixJob{}).Owns(&NixCronJob{}).
Watches(&Machine{}, findConfigsForMachine).Watches(&NixJob{},
findConfigsForOrphanRemoveJob)`. RBAC markers: `nixjobs`/`nixcronjobs`
(get;list;watch;create;update;patch;delete), `machines` (get;list;watch),
`machines/status` (get;update;patch). Regenerate.

## Remaining work

### R1 — rewrite the controller as the state machine (one valid commit)

Files: `api/v1alpha1/nixosconfiguration_types.go` (status schema),
`internal/controller/nixosconfiguration_controller.go` (Reconcile rewrite),
its `*_test.go` (rewrite to the new behavior), `config/*` (regen).

Do:
1. Rewrite `NixosConfigurationStatus` per the schema above; `make generate`.
2. Rewrite `Reconcile` as the state machine, consuming the child builders.
   Add: uniqueness gate; `ensureInstallNixJob`/`ensureDayTwoNixCronJob`
   (create-or-update, ownerRef via `controllerutil.SetControllerReference`);
   `ensureDecommissionNixJob` (orphan, labeled, `ttlSecondsAfterFinished`);
   `writeBackMachineStatus`; condition aggregation from child status;
   `apierrors.IsConflict` → requeue.
3. **Delete** `createApplyJob`, `createAndSubmitApplyJob`, `monitorJob`,
   `handleJobSuccess`, `handleJobFailure`, `applyOnRemoveFlake`,
   `resolveAdditionalFiles`, `cancelRunningJobs`, and the per-machine/global
   Job-concurrency helpers (`hasActiveJobForMachine`, `countActiveJobs`,
   `findExistingJob`). Drop the `internal/applyjob` import from the controller.
   Keep `readGitCredentials`/`resolveConfigRevision` only if still used; the
   orchestrator delegates revision resolution to the child, so
   `resolveConfigRevision` likely goes too.
4. Update `SetupWithManager` (Owns NixJob/NixCronJob; the Machine watch stays;
   add the orphan-NixJob watch) and the RBAC markers.
5. Rewrite the controller tests: `Pending→Installing→Converging→Ready` (install
   child created then deleted, `fullDiskInstallCompleted` set); non-install
   `Pending→Converging→Ready` (cron created); not-discoverable → `Blocked` (cron
   suspended); uniqueness (2nd config for a machine → Blocked/Stalled); deletion
   with `onRemoveFlake` → `Removing`, orphan decommission NixJob (no ownerRef),
   finalizer removed on success; deletion without `onRemoveFlake` → finalizer
   removed immediately; Machine writeback asserted on success.

Acceptance: build + vet + lint + envtest green. `internal/applyjob` and the
`apply` subcommand still exist and compile (untouched here).

### R2 — retire the bespoke apply path (one valid commit, deletion-only)

After R1 nothing in `internal/controller` imports `internal/applyjob`, but
`cmd/apply` still does and `cmd/main.go` still dispatches the `apply` subcommand.
The orchestrator no longer spawns `/manager apply` Jobs (children run `nix run`),
so this path is dead.

Do: remove `cmd/apply/`, remove the `apply` dispatch in `cmd/main.go`, delete
the `internal/applyjob` package (incl. `runner*.go` and its tests). Grep to
confirm no remaining importers.

Acceptance: build + vet + lint + `go test ./...` green (the applyjob/apply tests
are gone with the code).

## Testing beyond unit/envtest (later)

- e2e (Kind + Colima NixOS VM): a `NixosConfiguration` reaches `Ready`, the
  day-2 `NixCronJob` exists, the config lands on the VM; deletion with/without
  `onRemoveFlake` (wiped vs persists). See `.agent-e2e/02-nixosconfiguration.md`
  (tiers/blockers there predate the orchestrator; update once R1/R2 land).

## Deferred (not in R1/R2)

- Host-key pinning (a host-key source on `Machine`; `ssh-keyscan` at discovery).
- `NixosFacter` additionalFiles (never worked in v1alpha1; `mapAdditionalFiles`
  currently errors on it).
- `JobPending`-style stall surfaced from the orchestrator.
- Immediate re-apply on `additionalFiles` content change (today: picked up on
  the next day-2 tick, since each run re-injects current content).
