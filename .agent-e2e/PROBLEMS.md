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

