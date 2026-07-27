# v1alpha2 orchestrator — problem log

Append-only record of every gate failure and its fix, per iteration. Newest at
the bottom of each section.

## Baseline (iteration 0)

- 2026-07-24 — Gate A baseline on `feat/nixosconfiguration-orchestrator` before
  any R1 work: `go build ./...` OK, `go vet ./...` OK, `golangci-lint run` = 0
  issues. Children builders (`nixosconfiguration_children.go`) already landed and
  tested. No open problems yet.

---

## 2026-07-24 — iteration 1 — Gate B — R1 review findings (commit 391fb18)

Gate A was green (build/vet/lint=0/unit+envtest), independently verified by the
orchestrator. Gate B review (against the design docs) returned CHANGES NEEDED.

### P1 (must-fix) — install-Degraded infinite requeue + unbounded counter
- Symptom: on full-disk install failure, `InstallRetries++` runs before the cap
  check and the over-cap (Degraded) branch returns `RequeueAfter` without
  deleting the failed job and without `FullDiskInstallCompleted`. Next reconcile
  re-enters `reconcileInstall`, sees `Failed>0`, increments again — forever.
- Evidence: `nixosconfiguration_controller.go:205-226`; existing retry test only
  asserts Degraded is reached, so it misses the churn.
- Root cause: increment-then-check ordering + requeue on a terminal state.
- Fix: check cap BEFORE incrementing; once terminal-Degraded, re-degrade
  idempotently and return without requeue (no counter churn). Strengthen the test
  to assert the counter does not grow past the cap on repeated reconciles.
- Status: fixed (commit 963fa40)

### P2 (should-fix) — orphan decommission NixJob CR leaks (never GC'd)
- Symptom: the decommission NixJob CR is created without an ownerRef (correct, by
  Key decision #3). `TTLSecondsAfterFinished` is set on the inner batch Job, not
  on the NixJob CR; there is no CR-level TTL. After the parent finalizer is
  removed, nothing ever deletes the orphan NixJob CR (+ its `-nixfiles`
  ConfigMap) → unbounded accumulation on every `onRemoveFlake` deletion.
- Evidence: `nixosconfiguration_controller.go:517-519` + `ensureDecommissionNixJob`;
  `nixjob_controller.go` never self-deletes the CR; TTL only on `batchv1.JobSpec`.
- Root cause: relied on a CR-level TTL that does not exist.
- Fix: on observed decommission success/exhaustion, delete the orphan NixJob CR
  (best-effort) BEFORE removing the finalizer (the finalizer keeps the
  orchestrator alive to clean up); correct the misleading comment; add a test
  asserting the orphan CR is deleted on success.
- Status: fixed (commit 963fa40)

### P3 (nit/correctness) — Applied condition + reconcileRemoving conflict
- Symptom: (a) after a full install completes but day-2 is still Converging,
  `Applied` stays False though the spec says `Applied ← install-success OR day-2
  lastSuccessfulTime`. (b) a status-update conflict in `reconcileRemoving` after
  `Delete(job)`+`OnRemoveRetries++` can lose the increment → could exceed the cap.
- Fix: derive `Applied` from install-success too; make the OnRemoveRetries
  increment robust to conflict (persist before acting / retry).
- Status: fixed (commit 963fa40)

## 2026-07-24 — iteration 2 — Gate B — Cluster review findings (commits db44de3/4bb4539)

Gate A green (consolidated at e21edc9); Cluster selection algorithm verified
correct against all of 09-cluster S1. Gate B returned CHANGES NEEDED for
node-file rendering.

### P4 (must-fix, security) — Nix injection / eval-break via `values`
- Symptom: `escapeNixIndentedString` (`cluster_controller.go:358-362`) applies two
  non-composable `ReplaceAll` passes; a `'` in the JSON followed by `${` forms
  `'''${...}` which Nix reads as a live antiquotation. Ordinary values like
  `{"script":"echo '${VAR}'"}` break every converge eval; malicious values like
  `{"x":"'${builtins.getEnv \"HOME\"}"}` execute arbitrary Nix during converge.
- Evidence: reviewer reproduced both with `nix eval` v2.31.3.
- Root cause: escaping an indented (`''…''`) Nix string with independent passes.
- Fix: switch to a double-quoted Nix string with ONE uniform escaper
  (`\`→`\\`, `"`→`\"`, `${`→`\${`, backslash first); delete
  `escapeNixIndentedString`. Verified round-tripping by the reviewer.
- Status: fixed (commit 35613a1)

### P5 (must-fix, security) — Nix injection via Machine `host`
- Symptom: `install.ip = %q` (`cluster_controller.go:349`) — Go `%q` does not
  escape `${`, so a host like `10.0.0.${builtins.getEnv "HOME"}` becomes a live
  antiquotation. `Machine.Spec.Host` is unvalidated for Nix metacharacters.
- Fix: render host through the same shared escaper in a double-quoted string.
- Status: fixed (commit 35613a1)

### P6 (should-fix) — S3 escaping untested
- Symptom: `TestRenderMemberNodeFile_Content` only tests benign input and asserts
  substrings; it passes on the broken escaper.
- Fix: add a table test round-tripping `${`/`'`/quote/backslash values + hostile
  host, asserting no unescaped `${`/`''` boundary (or a gated `nix eval`).
- Status: fixed (commit 35613a1)

### P7 (nit) — Desired doc mismatch + ObservedGeneration on failure
- (a) `gs.Desired=len(candidates)` when Count unset contradicts the type doc
  ("Desired is 0 when Count unset"); reconcile the doc to the (nicer) behavior.
- (b) `ObservedGeneration` is advanced at the top of reconcile, so a failed
  reconcile still reports the generation observed; move it to the success tail.
- Status: fixed (commit 35613a1)

## 2026-07-24 — iteration 3 — Gate C — first full e2e run

First `make test-e2e` was SIGTERM'd ~4min in during the pre-existing "Nix
workloads" BeforeAll (NixBuilder wait) — environmental reap, not a test failure.
It left the Kind cluster + manager deployed. A second run reused that cluster.

Re-run result: **Ran 20 of 20 Specs, 19 PASSED, 1 FAILED** in 614s. All 12 new
tier-2 specs (5 Cluster + 7 NixosConfiguration) and all heavy nixworkloads
scenarios passed. The only failure:

### P8 — metrics-endpoint e2e (`test/e2e/e2e_test.go:176`) flaked on the reused cluster
- Symptom: `verifyMetricsServerStarted` greps the manager pod logs for
  "Serving metrics server" and timed out after 190s.
- Evidence: manager pod restartCount=0, Running; deployed with
  `--metrics-bind-address=:8443`; metrics Service HAS a live endpoint
  (10.244.0.8:8443) → metrics ARE serving. Pod started 00:47:50Z but the
  earliest RETRIEVABLE log is 00:48:55Z — the one-time startup banner rotated out
  of the ~64k-line retention window. The pod was 9min old (reused across the
  killed run + the re-run) and the cluster held TWO runs' worth of auth-less
  Machine CRs, whose pre-existing Machine-controller errors
  (`failed to build SSH config` / `Reconciler error`, ~135 lines/s) drove the
  rotation.
- Root cause: NOT a code defect. Artifact of reusing/contaminating the cluster
  (killed run 1 left objects + manager pod; run 2 reused them). Metrics genuinely
  serve; the banner just scrolled out of a 9-min-old pod's log before the test
  grepped it.
- Fix: run Gate C on a FRESH cluster. A single-run cluster accumulates ~17k lines
  by the time the metrics test runs (~+4.5min) < 64k retention → banner present →
  test passes.
- Status: superseded by P9 (a fresh-cluster re-run ALSO failed the metrics test —
  the real cause is the log flood below, not pod reuse).

### P9 — manager dev-mode logging floods logs and rotates the metrics banner
- Symptom: even on a FRESH cluster + fresh manager pod, `test/e2e/e2e_test.go:176`
  ("Serving metrics server" grep) times out. Fresh pod started 01:15:08Z but the
  earliest retained log is 01:16:12Z — the startup banner rotated out within ~64s.
- Evidence: metrics genuinely serve (endpoint 10.244.0.8 live); 43.7% of log
  lines (4366/10000) are the Machine controller's SSH-failure logs
  (`failed to build SSH config` / `failed to check discoverability`) — each of
  which, under `cmd/main.go`'s `zap.Options{Development: true}`, prints a full
  ~10-line stacktrace, plus DEBUG-level event lines. The new e2e legitimately
  creates ~10 auth-less/unreachable Machine CRs (selection + not-discoverable
  scenarios), reconciled every DiscoveryInterval=60s, so the flood rotates the
  one-time INFO startup banner before the metrics test greps it.
- Root cause: development-mode zap logging (DEBUG + per-error stacktraces) — a
  kubebuilder scaffold default inappropriate for a shipped operator. NOT a defect
  in R1/R2/Cluster; exposed by the new e2e's Machine load. Metrics work.
- Fix: set production zap options in `cmd/main.go` (Info level, no stacktrace on
  Error via StacktraceLevel≈DPanic); flags still allow re-enabling debug. Cuts
  the flood ~10× so the banner survives past the metrics test. General
  improvement, logging-only, no behavior change.
- Status: fixed (commit 7d8077c)

## 2026-07-24 — iteration 4 — Gate C — CLEAN PASS

After the P9 zap-logging fix, a fresh-cluster `make test-e2e` run:

```
Ran 20 of 20 Specs in 573.346 seconds
SUCCESS! -- 20 Passed | 0 Failed | 0 Pending | 0 Skipped
```

All gates green, no open problems. Note (environmental, not a code issue): the
Bash-tool background runner reaps long-running background commands intermittently
(~7–10 min); the passing run was launched detached via `nohup` so it survived to
completion. Gate C must be run detached in this environment.

## 2026-07-28 — night run iteration 1 — Gate B — storeRef/builderRef review (NOT LGTM)

Gate A was green on `feat/nixcluster-store-builder-ref` as-is (build/vet/lint=0/
unit+envtest). Before the review, a missing regression test for the §H two-key
SSH rule on the *converge* path was added (`TestRenderConvergeKeepsClusterKey`,
commit defa6e2) — it passes, so the new builderRef combination does not shadow
the cluster key. `/branch-review` then returned NOT LGTM with four blocking
findings, all about the contract the two new fields advertise.

### N1 (must-fix) — `StoreRef` doc comment promises persistence the code never delivers
- Symptom: the field comment (and therefore the CRD description users read via
  `kubectl explain`) says storeRef makes converge "build artifacts persist across
  runs". Nothing pushes into the store on a store-only configuration.
- Evidence: the only push site is `nixrender.go:482`, gated on
  `in.sshSecretName != ""`, which `resolveInfra` sets only inside the
  `builderName != ""` branch (`nixworkload.go:123-151`). With storeRef alone the
  store is a read-only substituter. ADR-0006 says exactly that. Even with
  builderRef, the pod pushes only `Run`/`Prebuild` paths — the member NixOS
  toplevels are built by `nixos-rebuild --target-host` at converge runtime and
  never enter the store; the real cache is the builder's own persistent `/nix`
  (and only when the NixBuilder has `spec.storage`).
- Root cause: field comments describe an intended mechanism, not the implemented one.
- Fix: rewrite both comments to the real contract, regenerate the CRD, and pin
  the behaviour with a render test (store-only ⇒ no `nix copy --to`).
- Status: fixed (commit 4b61717)

### N2 (must-fix) — an unready/typo'd storeRef or builderRef reports members as Failed
- Symptom: a missing NixStore/NixBuilder stalls the converge NixCronJob
  (`PhaseDegraded`), which `coarseMemberStatus` maps to `MemberStatusFailed` and
  the cluster reports `Ready=False, reason=Converging, "converge in progress"` —
  implying an apply was attempted and broke nodes. The only accurate message
  (`NixStore "x" not found`) stays on the child.
- Evidence: `nixcronjob_controller.go:95-101`, `nixworkload.go:255-259`,
  `nixcluster_controller.go:477-482,500-509,554-565`.
- Root cause: the new fields introduce an infra-stall state the cluster status
  mapping does not distinguish from a failed apply.
- Fix: propagate the child's Stalled/InfraNotReady reason+message onto the
  NixCluster conditions and stop mapping that state to `Failed`. Cluster phase
  becomes `Blocked`; members keep their last known status; the mirrored condition
  clears when the reference resolves.
- Status: fixed (commit 0bb2b9f)

### N3 (must-fix) — `builderRef` removes the local build fallback silently
- Symptom: resolving a builder emits `max-jobs = 0` (all builds remote) and an
  unqualified NixBuilder is advertised as `x86_64-linux,aarch64-linux`
  regardless of what the builder pod can actually build. Pointing an aarch64
  cluster at an x86_64-only builder turns a slow converge into a failing one.
- Evidence: `nixrender.go:59-61`, `nixrender.go:130-147`.
- Root cause: pre-existing default, newly reachable from NixCluster.
- Fix: state the requirement in the `BuilderRef` comment and pin the behaviour
  with a `buildNixConfig` test. Changing the default advertised system set would
  alter the already-shipped workload path — logged as out of scope here.
- Status: open

### N4 (must-fix) — `docs/design/cluster-crd.md` no longer matches the code
- Symptom: the normative spec example omits the two new fields, the converge
  child field-mapping enumerates every NixSpec field except the new ones, and the
  doc still says `kind: Cluster` / `api/v1alpha1/cluster_types.go` (stale since
  the rename in d7cd482).
- Fix: update the spec example, the child mapping, and the stale kind/path, plus
  a table stating what storeRef/builderRef really do.
- Status: fixed (commit ac26131)

### N5 (should-fix) — store-only push claimed in two more docs; no NixCluster example
- `docs/design/examples.yaml:64` and `examples/README.md` assert the same
  store-only push N1 disproves; there is no NixCluster manifest anywhere, so the
  new knobs have no discoverable usage example.
- Fix: corrected both claims; added `examples/nixcluster.yaml`.
- Status: fixed (commit ac26131)

### N6 (noted, not actioned) — `NixSpec.LocalStore` is unreachable from a NixCluster
- The review flagged that a converge pod still materialises every member closure
  in an unbounded emptyDir `/nix` even with a builder, and that `LocalStore` (which
  would bound it) has no NixCluster field. Adding a new API surface is beyond this
  PR; recorded so the decision is explicit rather than silent.
- Status: open (deferred — needs a design decision, not a fix)

## 2026-07-28 — night run iteration 2 — Gate B — re-review after the N1–N5 fixes

Gate A green (lint 0, full suite green, no manifest drift). Gate C had passed on
defa6e2 (20/20 specs, 694s). The re-review confirmed N1/N2/N4/N5 fixed in
substance but found that the N2 fix introduced defects of its own and that the
same two defects live in the NixosConfiguration neighbour.

### B1 (must-fix) — `convergeStall` matches ANY Stalled, not just InfraNotReady
- Symptom: NixCronJob sets `Stalled` for `GitError` too (`nixcronjob_controller.go:84`),
  so a broken git ref now yields phase `Blocked` and members `Applied`, and the
  mirrored condition can never be cleared because `isMirroredStall` only
  recognises `InfraNotReady` — the setting and clearing branches disagree.
- Fix: narrow `convergeStall` to `reasonInfraNotReady`, making the pair symmetric.
- Status: fixed (commit c0d9658)

### B2 (must-fix) — a converge that fails every run reports Ready / all members Applied
- Symptom: `observe()` calls `markReady` as soon as the batch CronJob exists,
  regardless of whether its Jobs fail; `PhaseFailed` is never set for a
  NixCronJob and `PhaseDegraded` only came from `markStalled`. After the N2 fix
  the stall branch intercepts that, so `MemberStatusFailed`/`Degraded` became
  unreachable: a permanently failing converge looks healthy.
- Evidence: `nixcronjob_controller.go:211-229`, `nixworkload.go:255-259`,
  `nixcluster_controller.go:501-502,529-530`.
- Root cause: pre-existing hole (failures were never surfaced); the N2 fix
  removed the one accidental path that showed anything.
- Fix: NixCronJob already `Owns(&batchv1.Job{})` — enumerate owned Jobs, detect
  `JobFailed`, record it in status, and map it to `Failed`/`Degraded`.
- Status: fixed (commit c0d9658)

### B3 (must-fix) — "members keep their last known status" was not what the code did
- Symptom: on an infra stall the code returned `Applied` whenever
  `LastSuccessfulTime != nil` — so a member that failed on the last run flipped
  to `Applied` the moment someone typo'd `storeRef`.
- Fix: carry the previous per-member status from `cluster.Status.NodeGroups`
  (already read for sticky selection) and report exactly that.
- Status: fixed (commit c0d9658)

### B4 (must-fix) — the same false storeRef/builderRef claim in NixosConfiguration
- Symptom: `nixosconfiguration_types.go:77-87` still said "build/substitute
  caching (much faster day-2 convergence)" and "typically set together" — the
  exact wording N1 was raised for, on the same `kubectl explain` surface.
- Fix: restate both fields there too, regenerate the CRD.
- Status: fixed (commit c0d9658)

### B5 (must-fix) — the same N2 defect in NixosConfiguration
- Symptom: `nixosconfiguration_controller.go:275-277` maps a Degraded day-two
  cron to "Day-2 convergence run failed", so a typo'd storeRef claims a run
  failed when no run happened.
- Fix: treat an infra stall as a stall, surfacing the child's message.
- Status: fixed (commit c0d9658)

### B6 (must-fix) — `examples/nixcluster.yaml` could not be applied as written
- Symptom: it referenced `store`/`linux-builder` while living in namespace
  `infra`; those objects are in `apps` in the sibling examples and refs are
  strictly same-namespace. It also pointed at an x86_64-only builder while its
  own comment warns that a builder must cover the members' systems.
- Fix: same namespace as the rest of the examples, and spell out the arch match.
- Status: fixed (commit c0d9658)

### B7 (must-fix) — the builder/arch foot-gun was closed by prose only
- Symptom: N3 was "fixed" with a field comment and a test that pins the trap
  rather than preventing it. NIO already knows each member's architecture
  (`Machine.Status.HardwareFacts.CPU.Architecture`) and the builder's declared
  `spec.systems`.
- Fix: a conservative preflight in the NixCluster reconcile — when a referenced
  NixBuilder declares an explicit system list and a selected Machine reports an
  architecture that list cannot build, report `Blocked` with the mismatch instead
  of letting converge burn its activeDeadline. Missing facts or an unqualified
  builder never block (nothing is proven).
- Status: fixed (commit c0d9658)

Decision on the untracked design notes (E1 step 5): `docs/design/architecture.md`
and `docs/design/nixcluster-deep-dive.md` are ~50% Russian prose, and committing
to a public repo is publishing, so both stay local and are now gitignored along
with `nio-go-rewrite.md`, `SESSION-HANDOFF.md` and this run's TODO/report. Their
content is the input for E4's English Design section, not a commit.

## 2026-07-28 — night run iteration 3 — Gate B — the B2 fix reopened B2

Gate C had passed again (20/20 specs, 725s). The third review round found four
defects, each demonstrated with a failing test rather than argued — three of them
introduced by the round-2 fixes themselves.

### C1 (must-fix) — a stale Stalled condition outranked a live failure
- Symptom: the new RunFailed branch in `observe()` returned before `markReady`,
  so `clearStalled` never ran. Because `coarseMemberStatus` checks the stall
  BEFORE the run outcome, a workload whose runs kept failing reported phase
  `Blocked` with a long-dead `NixStore "…" not found` message and members
  `Applied` — the exact B2 symptom, reintroduced.
- Fix: a run happening at all proves the stall is resolved, so the RunFailed
  branch clears it.
- Status: fixed (commit be7e58e)

### C2 (must-fix) — one-off runs could degrade the workload but never recover it
- Symptom: failures were counted from all owned Jobs (including the `-manual`
  Jobs `fireImmediateJob` creates) while success came only from the projected
  CronJob's `lastSuccessfulTime`, which the kube CronJob controller updates for
  scheduled Jobs only. A failed one-off followed by a successful one-off stayed
  Degraded until a *scheduled* run happened to succeed — up to a full day at the
  default converge cadence, and NixCluster sets `triggerOnChange` on its cron, so
  this is the cluster's default path.
- Fix: read both signals from the same scan of owned Jobs, keep the CronJob's own
  bookkeeping as an additional success source, and make both monotonic so a Job
  pruned by a TTL or history limit cannot resurrect a stale verdict. One-off Jobs
  now also carry a TTL, since the CronJob's history limits never applied to them.
- Status: fixed (commit be7e58e)

### C3 (must-fix) — the builder preflight was dead code
- Symptom: `checkBuilderCoversMembers` reads
  `Machine.status.hardwareFacts.architecture`, and nothing in the repository ever
  wrote `HardwareFacts` — no controller, no job. The preflight always saw nil and
  returned nil, so the builder/arch foot-gun was still guarded by prose only.
- Fix: the Machine controller now collects the architecture from `uname -m` on
  each successful discovery (best-effort — a fact we cannot gather is not a
  machine we cannot reach) and stamps `LastHardwareScanTime`.
- Status: fixed (commit be7e58e)

### C4 (must-fix) — Stalled conditions the reconciler set itself were never cleared
- Symptom: only `InfraNotReady` was cleared, so the new `BuilderSystemMismatch`
  (and `InvalidNodeFile`, `SelectionComplete`) stayed True with a false message
  until the cluster happened to reach `Ready`.
- Fix: `setConditions` runs only after a reconcile completed selection,
  rendering, the preflight and the child, so every Stalled reason is obsolete
  there. A persisting failure re-sets it on the next failing reconcile, which
  returns long before that point.
- Status: fixed (commit be7e58e)

Should-fix items from the same round, also addressed in 8d9df26: the RunFailed
message no longer says "scheduled" (it fires for one-off runs too), and the design
doc now documents the preflight, the new reason, and how a failing converge is
reported. Left as noted follow-ups: `r.fail` requeues with error backoff for a
configuration mistake that cannot self-heal without a spec edit (consistent with
every other `fail` call site, so changing it is a separate decision).
