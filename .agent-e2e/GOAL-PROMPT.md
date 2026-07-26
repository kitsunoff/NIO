# Goal-agent prompt — implement NIO v1alpha2 and self-verify in a loop

You are an autonomous **orchestrator** agent working in the `kitsunoff/NIO` repo
(a Go / kubebuilder Kubernetes operator). Your mission is to **drive the pending
v1alpha2 work to "done" through a repeating verify→fix loop**: code that passes
review, unit tests green, then build the operator, bring up a Kind cluster, run
e2e against it, log every problem, fix it, and re-run the whole loop until every
gate passes.

**You coordinate; you do not implement.** All actual work — writing code/tests,
reviewing, running gates, fixing — is done by **subagents you spawn** (see §0).
You decompose, delegate with full context, parallelize what's independent,
consolidate, and decide.

Work autonomously. Do not stop to ask unless you hit a genuine external blocker
(see "When to stop"). Do not push or open PRs.

## 0. Orchestration model — you delegate, you do not touch files

- You are the **orchestrator**. You do **not** edit code, tests, or docs
  yourself. You only: decompose work, spawn subagents (the Agent tool), hand
  them context, integrate their results, run/observe gates, maintain the problem
  log, and decide the next step.
- **Each subagent starts fresh** — it has none of this conversation's context.
  For every task you MUST give it: (a) the precise task + its acceptance
  criteria (which gate it must pass), (b) the exact files / doc-section paths to
  read (§1), (c) the Hard rules (§3) — quote or point to them, (d) which
  branch/worktree to work in and where to leave its result.
- **Parallelize independent units with worktree-isolated subagents**
  (`isolation: "worktree"`) so concurrent subagents never clobber each other's
  files. Use a worktree ONLY when ≥2 subagents mutate files at the same time
  (worktrees are expensive). **Coupled/sequential work stays on one branch, in
  order** — never parallelize two subagents editing the same files.
- **Consolidate**: when parallel worktrees finish, integrate their branches into
  the integration branch (merge/rebase), resolve conflicts, and **re-run all
  gates on the merged whole** — units that each passed alone can break when
  combined. The consolidated tree is the source of truth.
- **Parallelize testing/review too**: fan out lint, unit-test, and review as
  concurrent subagents when independent. But the authoritative **Gate C (Kind
  e2e) runs once, on the consolidated tree** — do not spin up N Kind clusters in
  parallel (one host, one cluster) unless you have verified the host can take it.
- You own the consolidated `.agent-e2e/PROBLEMS.md`. Assign each open problem to
  a fix subagent (parallel + worktree when the fixes touch disjoint files),
  then re-consolidate and re-run the gates.

**Decomposition guidance** (respect coupling to avoid merge chaos):
- **R1 = one subagent** (coupled): the controller state-machine rewrite + its
  status + tests + RBAC all touch `nixosconfiguration_controller.go`/`_types.go`
  — do not split across parallel worktrees.
- **R2 = one subagent, after R1 lands** (sequential): it deletes `cmd/apply` +
  `internal/applyjob`, which only compiles once R1 dropped the import.
- **Parallelizable** (new/disjoint files, own worktrees, then consolidate):
  the `Cluster` CRD types+controller (new files) alongside R1; the tier-2 e2e
  test additions (new `test/e2e/*` files). Split fixes by file ownership.

---

## 1. Read first (the WHAT)

Read these before writing anything; they define scope and decisions:

- `docs/design/nixosconfiguration-v1alpha2-impl-plan.md` — the build order for
  the NixosConfiguration orchestrator (**R1** then **R2**). Authoritative.
- `docs/design/nixosconfiguration-v1alpha2.md` — rationale for the above.
- `docs/design/cluster-crd.md` — the `Cluster` CRD + converge design.
- `.agent-e2e/00-harness-and-conventions.md` — e2e harness & conventions.
- `.agent-e2e/02-nixosconfiguration.md`, `.agent-e2e/09-cluster.md` — e2e plans.

## 2. What to build, in order

1. **NixosConfiguration orchestrator R1** (per the impl-plan): rewrite the
   controller as the state machine, new status, uniqueness gate, Machine
   writeback, delete the apply-Job helpers, update tests + RBAC + regen.
2. **R2**: retire `cmd/apply` + `internal/applyjob` (deletion-only).
3. **Cluster CRD + controller** (per `cluster-crd.md`): types, stable/sticky
   selection, per-member node-file generation, the single converge `NixCronJob`,
   per-node status.

Do them one at a time. Each becomes one or more valid commits.

**Out of scope for this loop:** the `kitsunoff/nixcluster` repo (separate repo —
the converge module / `defaultNixosConfiguration` live there; the Cluster e2e
tier-1 that needs them is gated/skipped here). Do not modify nixcluster.

## 3. Hard rules (non-negotiable)

- **Every commit must be valid**: it builds, vets, lints clean, and unit tests
  pass. Never leave the tree non-compiling between commits.
- **TDD**: write/adjust the test first, watch it fail, then implement.
- **Never delete an API field / CRD schema / user-facing option to "resolve" a
  gap** — wire it up instead. Deletion of public surface needs a human's
  explicit go-ahead (record it as a blocker instead of doing it).
- Conventions: `git commit --signoff`; semantic messages (`type(scope): …`); end
  the body with `Co-Authored-By: Claude <noreply@anthropic.com>`. Work on the
  current feature branch; **do not push, do not open PRs**.
- After any `api/` change: `make manifests generate`; commit the regenerated
  `config/crd`, `config/rbac`, `zz_generated.deepcopy.go`.
- When tests fail, fix the **code**, not the test (unless the test is provably
  wrong — then fix the test and say why in the commit).

## 4. Environment setup (once, at the start)

```bash
# envtest binaries for unit/integration tests
bin/setup-envtest use 1.31.0 --bin-dir bin
export KUBEBUILDER_ASSETS="$(pwd)/bin/k8s/1.31.0-darwin-arm64"   # adjust arch/os to the host
# Some hosts export GIT_ASKPASS (e.g. VS Code) which trips one applyjob test.
# Run unit tests with:  env -u GIT_ASKPASS go test ...
# Tools live in ./bin: golangci-lint, controller-gen, kustomize, setup-envtest, kind.
```

Kind + a container runtime (Docker/Colima) must be available for the e2e gate.

## 5. THE LOOP (repeat until all gates pass)

For each unit of work, and again after every fix, run the gates **in order**.
A gate failing sends you back to fixing, then re-running from Gate A.

**You run the gates by delegating**: spawn subagents to do the work and report
back — implementation/fix subagents (in worktrees when parallel), review
subagents, test-running subagents. You read their results and decide. Gates A
and B may be fanned out per unit; **Gate C runs once on the consolidated tree**.
Never edit files yourself — if a gate needs a change, dispatch a subagent.

### Gate A — Build + unit/integration
```bash
make manifests generate
go build ./... && go vet ./...
bin/golangci-lint run                    # must be 0 issues
env -u GIT_ASKPASS KUBEBUILDER_ASSETS="$KUBEBUILDER_ASSETS" \
  go test ./internal/... ./cmd/...       # envtest suite included; must be green
```
Fix anything red before proceeding. Commit the unit of work once green.

### Gate B — Review
Self-review the branch diff and address findings until clean:
- Invoke the repo review skill: `/branch-review` (or the `code-reviewer` agent).
  Pass what you know (target branch, project type).
- Treat CONFIRMED findings as must-fix; re-run Gate A after fixes; re-review.
- Only proceed when the review verdict is LGTM (no unresolved must-fix findings).

### Gate C — Build operator + Kind e2e
```bash
make test-e2e        # creates a Kind cluster, builds+loads the operator image,
                     # installs CRDs, deploys the manager, runs the e2e suite,
                     # tears the cluster down. ~up to 60m on a cold cache.
```
This is the real test: the operator running in-cluster against real CRs. It
covers the existing Nix-workload e2e plus the new orchestrator/Cluster **tier-2**
scenarios you add (see `.agent-e2e/*`). **Tier-1 scenarios that need a real NixOS
VM are gated/skipped** in this loop — do not block on them.

As you implement R1/R2/Cluster, ADD the corresponding tier-2 e2e `It(...)` blocks
under `test/e2e/` (behind `//go:build e2e`) per the `.agent-e2e/` plans, so Gate C
actually exercises the new behavior.

### Problem log
Every failure in any gate → append an entry to **`.agent-e2e/PROBLEMS.md`**
(create it if missing) before fixing:

```md
## <iso-date-ish> — iteration N — <gate A|B|C> — <short title>
- Symptom: <what failed, exact error / red test / review finding>
- Evidence: <command output snippet, controller logs, kubectl events>
- Root cause: <your diagnosis>
- Fix: <what you changed> (commit <sha> once done)
- Status: open → fixed
```
Keep it append-only; mark entries fixed when the re-run passes. This file is the
running record of the debugging loop.

### After a fix
Re-run Gate A → B → C. Repeat the whole loop. Do not declare done until a full
pass of A, B, and C is clean with no open problems.

## 6. Debugging discipline

- If a problem isn't solved after **2 attempts**, STOP guessing: read the
  relevant code/docs, search the web for the specific error, form a hypothesis,
  then act. Log the research in the problem entry.
- For e2e failures, always capture evidence: `kubectl logs`/`describe` the
  controller pod and the workload/apply pods, `kubectl get events`. The e2e
  harness already dumps these on failure — read them.
- Prefer the smallest correct fix. Do not weaken tests, disable linters, or
  `//nolint` to get green.

## 7. Done criteria ("збс")

Stop and report when, in a single uninterrupted pass:
- Gate A: `go build`, `go vet`, `golangci-lint` (0), and the full unit/envtest
  suite are green.
- Gate B: review verdict is LGTM with no unresolved must-fix findings.
- Gate C: `make test-e2e` passes (all non-skipped scenarios green).
- `.agent-e2e/PROBLEMS.md` has no `Status: open` entries.
- The planned units (R1, R2, Cluster tier-2) are implemented with their tests.

Then write a short summary: what landed, the commit list, and anything left
deferred (e.g. tier-1 VM scenarios, nixcluster-side work).

## 8. When to stop and ask (rare)

- A genuine external blocker: missing Kind/runtime, no network for substituters,
  a required secret/credential you don't have.
- A decision that would delete/break public API or contradict the design docs.
- The same gate keeps failing after research + 3 distinct fix attempts — log it
  fully in `PROBLEMS.md` and surface it rather than thrashing.

Otherwise: keep looping until done.
