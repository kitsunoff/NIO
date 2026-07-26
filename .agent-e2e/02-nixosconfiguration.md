# E2E — `NixosConfiguration`

> Read `00-harness-and-conventions.md` and `01-machine.md` first. This is the
> heaviest CRD to e2e because a full happy path drives a real
> `nixos-rebuild`/`nixos-anywhere` over SSH to a live NixOS host.

## What the controller does (recap)

`NixosConfiguration` = a declarative NixOS config (git repo / flake) realized on a
target `Machine` over SSH via a per-apply Kubernetes **Job** (`/manager apply`).
The controller resolves the git ref to an immutable SHA, gates on machine
reachability + concurrency, monitors the Job, and records applied state on both
the config and the `Machine`.

Key facts (verified against code — corrections vs. the handoff noted):

- **No `status.phase`** field. State = conditions + `operationState.phase`
  (free-form `Starting`/`Running`, cleared on finish).
- Conditions used: `Ready`, `Reconciling`, `Stalled`, `Applied`, `GitSynced`.
- **No `status.resolvedRev`** field (only local var + Job annotation
  `nio.homystack.com/resolved-revision`). **No `status.installRetries`** field
  (onRemove retries live in annotation `nio.homystack.com/on-remove-retries`,
  cap 3). `status.additionalFilesHash` exists but is **never written** — do not
  assert it.
- Timers: `RequeueInterval=30s`, `GitPollInterval=5m` (steady-state converged),
  `JobPendingTimeout=5m`, `DefaultJobTimeout=30m`, `FullInstallJobTimeout=60m`,
  `MaxConcurrentJobs=5`.
- SSH user/key come from the **Machine**, not the config spec.
- `additionalFiles`: only `valueType: Inline` is delivered; `SecretRef` /
  `NixosFacter` make `createApplyJob` **error** (fail loudly).
- Apply Job: `BackoffLimit=0`, non-root UID 1000, read-only rootfs, only
  `/workspace` writable, secrets mounted `0o400` readable via FSGroup 1000.

## E2E topology — this needs a real NixOS target

The apply Job SSHes into `Machine.spec.host` and runs `nixos-rebuild switch`
(or `nixos-anywhere`). Kind alone provides no NixOS host. Options:

1. **NixOS VM target (required for the real apply path).** Use the `colima-vm`
   skill to spin a NixOS VM reachable from the Kind node, inject its SSH key as a
   Secret, and point the `Machine` at it. This is the only way to exercise a real
   `nixos-rebuild`/`nixos-anywhere` (call this the **"full apply e2e"**, gated
   behind an env flag / label because it needs the VM and is slow).
2. **Kind-only "controller-contract e2e" (no real host).** Everything up to and
   including Job creation, env/annotation/security-context assertions, and the
   concurrency/gate/degraded transitions can be verified **without** a working
   target: point the Machine at a host that accepts SSH but where
   `nixos-rebuild` will fail, and assert the controller's Job-failure handling.
   Split the suite so CI can run tier-2 on Kind and tier-1 (full apply) only
   where a VM is available.

For tier-1, the target repo/flake must be a real NixOS flake. Pin `ref` to a
branch you can push to (for the drift scenario) or a `rev` for determinism.

## Scenarios to cover

### Tier 2 — controller contract on Kind (no real NixOS host needed)

#### S1 — Machine gates block apply

1. Config with `machineRef` to a **missing** Machine → `Ready=False`, reason
   `MachineNotReady`, event `MachineNotFound`, requeue 30s, **no Job**.
2. Machine exists but `status.discoverable == false` → same `MachineNotReady`.
3. Make the Machine discoverable → controller proceeds (Job created). Proves the
   `findConfigsForMachine` watch re-enqueues.

#### S2 — Apply Job is well-formed (env / annotation / security)

Trigger one apply and assert on the created Job (label
`nio.homystack.com/config=<name>`):

- container `Command == ["/manager","apply"]`, no args;
- env: `NIO_GIT_REPO`, `NIO_GIT_REF` (defaults `main` when `spec.ref` empty),
  `NIO_TARGET_HOST`, `NIO_SSH_USER`, `NIO_WORK_DIR=/workspace`, `TMPDIR=/workspace`,
  `NIO_OPERATION=NixosRebuild`, `NIO_SSH_KEY_PATH=/secrets/ssh/ssh-privatekey`;
- when resolution succeeded: annotation `nio.homystack.com/resolved-revision=<SHA>`
  and env `NIO_GIT_REV=<SHA>`;
- when `credentialsRef` set: `NIO_GIT_CREDENTIALS_PATH=/secrets/git` + a
  `git-credentials` secret volume mounted `/secrets/git`;
- container securityContext: `runAsNonRoot`, `runAsUser 1000`,
  `readOnlyRootFilesystem`, `allowPrivilegeEscalation=false`, drop ALL caps;
- pod: `fsGroup 1000`, seccomp `RuntimeDefault`, `restartPolicy Never`;
- Job `backoffLimit == 0`; owner ref → the config.

#### S3 — Degraded resolve does not block first apply

1. Config with an unresolvable `ref` (or unreachable repo) → `GitSynced=False`,
   reason `GitCloneFailed`, event `GitResolveDegraded` (Warning).
2. Assert a Job is **still** created (first apply not blocked) and `NIO_GIT_REV`
   is **absent** from its env. Drift detection degrades to spec-hash.

#### S4 — `additionalFiles` validation

1. `additionalFiles` with `valueType: Inline` → Job env has `NIO_ADDITIONAL_FILES`
   as valid JSON containing the path+content.
2. `valueType: SecretRef` (or `NixosFacter`) → config goes `Stalled/Ready=False`
   (createApplyJob errors), **no** Job created. Encodes the "fail loudly" contract.

#### S5 — Per-machine concurrency gate

1. Two configs targeting the **same** Machine; make the first create an active
   Job (a slow/pending apply).
2. Assert the second reports `Reconciling=True`, reason `MachineInUse`, event
   `MachineInUse`, and creates **no** second Job while the first is active.

#### S6 — Global concurrency cap (`MaxConcurrentJobs=5`)

With ≥5 active apply Jobs cluster-wide (across machines), a 6th config reports
`Reconciling=True`, reason `Queued`, event `Queued`, no Job. (Pending/unscheduled
Jobs count as active in all gates.)

#### S7 — Job pending timeout

Create a config whose Job cannot schedule (e.g. `jobTemplate.nodeSelector` that
matches nothing). After >5m assert `Stalled=True`, reason `JobPending`.

#### S8 — Job failure handling

Point the Machine at a reachable SSH host where `nixos-rebuild` fails (non-NixOS
sshd is enough). On Job failure assert `Applied=False`/`Stalled=True`/`Ready=False`
all reason `ApplyFailed`, event `ApplyFailed`, `operationState` cleared.

#### S9 — Deletion / finalizer + onRemove

1. Assert finalizer `nio.homystack.com/finalizer` present.
2. Set `onRemoveFlake` and delete the config → a `<name>-onremove` Job is created
   (label `nio.homystack.com/operation=onRemove`); running apply Jobs are
   cancelled; Machine status cleared if it pointed at this config; finalizer
   removed; object deleted. Retry cap 3 via `nio.homystack.com/on-remove-retries`.

### Tier 1 — full apply against a real Colima NixOS VM (operator-in-Kind)

**Chosen execution model**: the operator runs **in Kind**; real `Machine` +
`NixosConfiguration` CRs drive the controller, which spawns the **apply Job
in-cluster**; the Job SSHes into a **Colima VM** and runs `nixos-rebuild switch`
(or `nixos-anywhere`). This exercises the whole system — controller gating, Job
orchestration, and the real SSH apply + decommission — end to end.

The target VM is created with the `colima-vm` skill (aarch64-linux node profile
with a reachable IP). Kind on Apple-Silicon Colima is also aarch64-linux, so the
in-pod Nix build is a **native** aarch64-linux build (no cross-compilation).

#### Prerequisites / blockers to resolve BEFORE this tier can pass

These are grounded in the current v1alpha1 code (`createApplyJob`) and are hard
gates. **Important framing**: B1/B2 are **not** newly-discovered design gaps —
the repo already has a decided, shipped solution for a nix-capable pod with a
writable `/nix` (the workload family): `DECISIONS.md` (ADR: *"A `bootstrap` init
seeds nix into the PVC-backed `/nix`… Runner pods build in-pod"*) and
`nix-workloads.md §4.5` (init `bootstrap` → `/nix-vol` → `emptyDir` at `/nix`,
stock `nixos/nix` image). And the **v1alpha2 design**
(`docs/design/nixosconfiguration-v1alpha2.md`) explicitly re-plumbs
NixosConfiguration apply onto that **workload family** (Install = one-shot
`NixJob` running `nixos-anywhere`, Day-2 = `NixCronJob` running `nixos-rebuild
switch`, Decommission = one-shot `NixJob`), which inherits the writable-`/nix` +
`nixos/nix`-image machinery for free. So under v1alpha2 **B1/B2 do not exist**;
they are artifacts of the v1alpha1 bespoke `internal/applyjob` apply Job. The
"fix" is therefore *porting the already-decided pattern* onto the v1alpha1 apply
Job, or letting the v1alpha2 rewrite obviate it — not inventing anything.

- **B1 — apply Job image must contain `nix` + `git` + `ssh`.** The v1alpha1
  container runs `["/manager","apply"]` and the runner shells out to `git`
  (init/remote/fetch/checkout/add), `nix`, `nixos-rebuild`, `nixos-anywhere`, and
  `ssh`. But the repo `Dockerfile` builds a **distroless** manager
  (`gcr.io/distroless/static`) with none of these. Fix = the same choice the
  workload family already made: base the apply pod on the `nixos/nix` image (+
  `git`/`openssh`) with the `manager` binary, and/or seed nix via a `bootstrap`
  init as in §4.5. (Whether `DefaultApplyImage`
  `ghcr.io/homystack/nixos-operator:latest` is already such an image is
  unverified here — confirm, or build one and set `spec.jobTemplate.image`.)
- **B2 — writable `/nix` vs `ReadOnlyRootFilesystem=true`.** The v1alpha1 apply
  container has `readOnlyRootFilesystem: true` and only `/workspace` (+ read-only
  `/secrets/*`) writable — there is **no `/nix` volume**. A real
  `nixos-rebuild`/`nixos-anywhere` must realise derivations into `/nix/store`,
  which the read-only rootfs forbids. `jobTemplate` can override only
  `image`/`nodeSelector`/`tolerations`/`resources`/`serviceAccountName` — **not**
  the security context and **not** volumes — so it cannot be fixed from the CR;
  it is a controller change. The established pattern (workload §4.5) is exactly
  the fix: a `bootstrap` init + an `emptyDir` mounted at `/nix` (writable),
  seeded from the image's baked store. Alternatives if in-pod build is undesired:
  `nixos-rebuild --build-host <target>` (build on the VM; runner has no
  `--build-host` today) or a relocated `--store` under `/workspace` (breaks
  binary-cache path identity). **Recommended: adopt the workload §4.5
  bootstrap + writable-`/nix` pattern on the apply Job.**
- **B3 — Kind pod → Colima VM networking.** The apply pod (inside the Kind node
  container, inside the Colima docker/containerd VM) must reach the node VM's
  `--network-address` IP over SSH (22). Verify reachability **before** relying on
  it (see "Networking check"). If the shared vmnet is not routable from pods, use
  a fallback: expose the VM via the host and target it through
  `host.docker.internal`, or run the VM on a network the Kind node can route to.
- **B4 — arch match.** Colima node default arch is aarch64 → the flake host must
  be `aarch64-linux`; the in-pod build is aarch64-linux native. Do not use an
  x86_64 flake host unless the VM is x86_64 (slow emulation).
- **B5 — egress + substituters + timeout.** The in-pod build substitutes the
  `nixos-rebuild` toolchain from `cache.nixos.org`; the pod needs internet egress
  and the run is slow. `FullInstallJobTimeout` is 60m, `DefaultJobTimeout` 30m —
  the Ginkgo `Eventually` must allow the full window.

#### Colima VM setup (in the suite's `BeforeAll`, host-side)

Use the `colima-vm` skill. Two target shapes depending on the scenario:

- **Pre-installed NixOS VM** (for `nixos-rebuild switch`, S10/S11/S13): provision
  NixOS onto the VM first (via `nixos-anywhere` from the `nix-builder` control
  VM), so the target already runs NixOS and `nixos-rebuild switch --target-host`
  works.
- **Fresh Linux VM** (for `fullInstall`/`nixos-anywhere`, S15): a plain Colima
  Linux VM with root SSH enabled; `nixos-anywhere` kexecs and installs NixOS.

Steps (aarch64, reachable IP, root SSH):

```bash
NAME=nio-e2e-target
colima start --profile "$NAME" --vm-type vz --arch aarch64 \
  --cpus 2 --memory 4 --disk 40 --network-address --runtime containerd
IP="$(colima list --json | jq -r "select(.name==\"$NAME\") | .address")"
# enable root SSH with a generated keypair; put the PRIVATE key in a k8s Secret
# (data key ssh-privatekey) referenced by Machine.spec.sshKeySecretRef, and the
# PUBLIC key in the VM's /root/.ssh/authorized_keys.
```

Point `Machine.spec.host` at `$IP`, `sshUser: root`, `sshKeySecretRef` at the
generated-key Secret. Teardown in `AfterAll`: `colima stop --profile $NAME`
(keep disk) / `colima delete --profile $NAME` (destructive — confirm).

#### Networking check (gate the suite on it)

Before the apply scenarios, verify pod → VM SSH reachability from inside Kind,
e.g. a throwaway pod: `nc -z $IP 22` (or an `ssh -o BatchMode=yes` probe). If it
fails, skip tier-1 with a clear message rather than producing a confusing apply
timeout.

#### S10 — Happy path `nixos-rebuild switch`

1. Machine → discoverable pre-installed-NixOS VM (see `01-machine.md` S1).
2. Config with a valid `aarch64-linux` flake `ref`/`flake`/`configurationSubdir`,
   `jobTemplate.image` = the Nix apply image (B1).
3. Assert: Job succeeds → `Applied=True`/`Ready=True` reason `Succeeded`/
   `ConfigurationApplied`, `Stalled` removed, event `Applied`.
4. Assert `status.appliedCommit == <resolved SHA>` (from Job annotation) and the
   **Machine** status: `hasConfiguration=true`, `appliedConfiguration=<config>`,
   `appliedCommit=<SHA>`, `lastAppliedTime` set.
5. **Assert the change is actually on the VM**: SSH into `$IP` and check a marker
   the flake defines (a file, a systemd unit, `readlink /run/current-system`).
6. Steady state: config converges → requeues at **5m** (`GitPollInterval`). No new
   Job while up to date.

#### S11 — Drift: new commit on same branch

1. From converged S10, push a new commit to the tracked `ref` (changing the marker).
2. Without editing the config, the controller re-resolves (immediately on a spec
   touch, or within the 5m poll) → `needsApply` "revision changed" → new Job →
   `appliedCommit` advances on both config and Machine.
3. Verify the **new** marker is live on the VM.

#### S12 — Exact-SHA fetch (TOCTOU closed)

Resolve, then advance the branch tip **before** the Job runs. Assert the applied
marker corresponds to the controller-resolved SHA (runner does `fetch --depth 1
origin <SHA>` + `checkout FETCH_HEAD`, not `clone --branch`) — the moving tip is
not what got applied.

#### S13 — `additionalFiles` inline reaches the Nix build

1. Config with an inline `additionalFiles` entry the flake imports.
2. Apply succeeds and the injected file's effect is present on the VM — proving
   `git add --force` staging (PR #15) made Nix include it.
3. Variant: a `.gitignore`d target path still lands (force defeats gitignore).

#### S14 — Private repo (HTTPS + SSH)

1. `gitRepo` → private repo, `credentialsRef` → Secret.
2. HTTPS: `username`+`password`, token-only (`token` key), trailing-newline
   trimming all resolve at ls-remote (controller) AND clone (Job).
3. SSH repo: `ssh-privatekey` (+ optional `known_hosts`) works; missing key fails
   loudly. Same Secret drives both controller `ls-remote` and Job clone.

#### S15 — Full-disk install (`nixos-anywhere`) on a fresh VM

1. `fullInstall: true` against a **fresh Linux** Colima VM → Job env
   `NIO_OPERATION=FullInstall`, 60m timeout, runs `nixos-anywhere` (kexec install).
2. On success `status.fullDiskInstallCompleted=true`; the VM now boots NixOS
   (`ssh $IP nixos-version`).
3. Subsequent reconciles must **not** redo the full install — operation switches
   to `NixosRebuild`; `needsApply` "full install not completed" fires only once.

#### S16 — Deletion WITHOUT `onRemoveFlake` → machine is NOT wiped (persistence)

This is the "or not" branch of "applies and gets wiped or not". NixOS is
declarative/stateful: deleting the CR does **not** revert the machine.

1. From converged S10 (marker present on VM), delete the `NixosConfiguration`
   (no `onRemoveFlake` set).
2. Assert the controller path: running apply Jobs cancelled; Machine status
   cleared (`hasConfiguration=false`, `appliedConfiguration=""` when it pointed at
   this config); finalizer `nio.homystack.com/finalizer` removed; object deleted.
3. **Assert the VM still has the marker** — nothing was reverted. This documents
   that removal of the CR alone leaves the last-applied system in place.

#### S17 — Deletion WITH `onRemoveFlake` → machine IS decommissioned (wiped)

The "gets wiped" branch.

1. Apply a `NixosConfiguration` with `onRemoveFlake` set to a decommission flake
   that removes the marker (e.g. disables the unit / deletes the file).
2. Delete the CR → controller creates a `<name>-onremove` Job (label
   `nio.homystack.com/operation=onRemove`) that applies the `onRemoveFlake` to the
   VM; retries up to 3 via annotation `nio.homystack.com/on-remove-retries`;
   finalizer held until it completes, then removed.
3. **Assert the marker is gone on the VM** after the onRemove Job succeeds, and
   the object is deleted. If the onRemove Job keeps failing, assert it retries at
   most 3 times before the finalizer is force-dropped (per `MaxOnRemoveRetries`).

## Assertions cheat-sheet

| What | jsonpath / query |
| --- | --- |
| Ready status/reason | `{.status.conditions[?(@.type=='Ready')].status}` / `.reason` |
| GitSynced reason | `{.status.conditions[?(@.type=='GitSynced')].reason}` |
| Applied reason | `{.status.conditions[?(@.type=='Applied')].reason}` |
| Stalled reason | `{.status.conditions[?(@.type=='Stalled')].reason}` |
| Reconciling reason | `{.status.conditions[?(@.type=='Reconciling')].reason}` |
| appliedCommit | `{.status.appliedCommit}` |
| fullDiskInstallCompleted | `{.status.fullDiskInstallCompleted}` |
| targetMachine | `{.status.targetMachine}` |
| apply Job env | `kubectl get job -l nio.homystack.com/config=<name> -o jsonpath=...` |
| resolved-revision annotation | `{.metadata.annotations.nio\.homystack\.com/resolved-revision}` on the Job |

## Out of scope / do not assert

- `status.phase` (does not exist), `status.resolvedRev`, `status.installRetries`,
  `status.additionalFilesHash` (declared but never written).
- `SSHPasswordSecretRef` on the Machine — the apply Job mounts only the SSH
  **key** volume; password auth is not consumed by the apply path.

## Suggested placement

Two files behind `//go:build e2e`:

- `test/e2e/nixosconfiguration_contract_test.go` — tier 2 (S1–S9), runs on Kind
  with no external target.
- `test/e2e/nixosconfiguration_apply_test.go` — tier 1 (S10–S17,
  operator-in-Kind + Colima VM), gated by an env var (e.g.
  `NIO_E2E_NIXOS_TARGET=1`). The suite's `BeforeAll` provisions the Colima VM
  (via the `colima-vm` skill) and builds/loads the Nix apply image (B1);
  `AfterAll` tears the VM down. The suite must **skip with a clear message**
  (not fail) when `colima`/`nix` are unavailable or the pod→VM networking check
  (B3) fails, so CI without the VM stays green.

Blockers **B1** (Nix apply image) and **B2** (writable `/nix` on the apply pod)
are code-level prework for the v1alpha1 apply Job — tier-1 apply cannot pass
until both are done. They are **not** open design questions: the fix is to adopt
the workload family's decided `bootstrap` + writable-`/nix` + `nixos/nix`-image
pattern (`nix-workloads.md §4.5`, `DECISIONS.md`), which the **v1alpha2** rewrite
already plans to inherit by running the apply as `NixJob`/`NixCronJob`. Track B1/B2
as implementation tasks (or fold them into the v1alpha2 migration), not test flakes.
