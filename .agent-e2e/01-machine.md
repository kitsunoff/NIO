# E2E — `Machine`

> Read `00-harness-and-conventions.md` first. This doc only adds the
> `Machine`-specific scenarios.

## What the controller does (recap)

`Machine` = an SSH-reachable target host. The `MachineReconciler` has exactly
one job: periodically test SSH connectivity to `spec.host` using the referenced
Secret(s) and reflect reachability in status. It:

- reconciles on a fixed **60s** interval (`DiscoveryInterval`), swallowing SSH
  errors (unreachable ≠ reconcile error, so no exponential backoff);
- manages finalizer `nio.homystack.com/finalizer` (deletion is **not** blocked
  even if a `NixosConfiguration` references it — current TODO);
- creates **no** child resources, sets **no** owner refs, does **no** git/Job
  work, and never populates the hardware fields (`hardwareFacts`,
  `nixFacterResult`, `HardwareScanned`) — those are API stubs;
- writes only status; `hasConfiguration`/`appliedConfiguration`/`appliedCommit`
  are written by the **NixosConfiguration** controller, not this one;
- watches referenced Secrets (indexes `spec.sshKeySecretRef.name`,
  `spec.sshPasswordSecretRef.name`) and re-reconciles on Secret change.

Key spec: `host` (required, pattern-validated), `sshUser` (default `root`),
`sshKeySecretRef` (Secret data key `ssh-privatekey`), `sshPasswordSecretRef`
(data key defaults to `password`). At least one auth method is required — but
**only at runtime**, there is no CRD-level validation for it.

Conditions set: `Ready`, `Discoverable`, `Reconciling`, `Stalled`. There is no
`phase` field — state lives in `status.discoverable` + conditions.

## E2E challenge: this CRD needs a real SSH endpoint

The whole controller is an SSH connectivity probe. Kind gives us no SSH target
out of the box. Two viable strategies, in order of preference:

1. **In-cluster SSH target pod (recommended, self-contained).** Deploy a tiny
   OpenSSH server pod + Service in `nio-workloads` (e.g. `linuxserver/openssh-server`
   or a `debian` running `sshd`), inject a generated keypair via a Secret, and
   point `Machine.spec.host` at the Service DNS name. This keeps the suite
   hermetic on Kind (no external hosts, no cloud). The controller only opens a
   TCP+SSH handshake — it runs no commands — so a stock sshd satisfies it.
   - **PSA note**: the sshd pod goes in `nio-workloads`, not the restricted
     `go-operator-system` namespace.
2. **External target host (only for the full NixosConfiguration path).** A real
   VM (e.g. via the `colima-vm` skill) is needed only when you also want to drive
   a real `nixos-rebuild`/`nixos-anywhere` (see `02-nixosconfiguration.md`). For
   `Machine` alone, strategy 1 is enough.

The generated keypair for strategy 1: create an RSA/ed25519 key on the host in
`BeforeAll`, put the **private** key in a Secret under data key `ssh-privatekey`,
and bake the **public** key into the sshd pod's `authorized_keys`.

## Scenarios to cover

### S1 — Reachable machine becomes Discoverable (happy path)

1. Start the sshd target pod + Service; wait until it accepts connections.
2. Create Secret `machine-key` with `ssh-privatekey`.
3. Apply:
   ```yaml
   apiVersion: nio.homystack.com/v1alpha1
   kind: Machine
   metadata: {name: target, namespace: nio-workloads}
   spec:
     host: sshd.nio-workloads.svc.cluster.local
     sshUser: root
     sshKeySecretRef: {name: machine-key}
   ```
4. Assert `Eventually`:
   - `status.discoverable == true`;
   - condition `Ready` = `True`, reason `SSHConnected`;
   - condition `Discoverable` = `True`, reason `SSHConnected`;
   - a Normal event with reason `Discoverable` was emitted;
   - `status.observedGeneration == metadata.generation`.

### S2 — Unreachable host

1. Apply a `Machine` whose `host` points at a closed port / nonexistent Service.
2. Assert:
   - `status.discoverable == false`;
   - `Ready` = `False`, reason `SSHFailed`;
   - `Discoverable` = `False`, reason `SSHFailed`;
   - condition `Stalled` is **absent** (unreachable is not a reconcile error);
   - the object keeps re-reconciling (~60s) rather than backing off — observe a
     `status` re-write / event over ~2 intervals.

### S3 — Missing credentials Secret

1. Apply a `Machine` referencing `sshKeySecretRef: {name: does-not-exist}`.
2. Assert `Discoverable` = `False`, reason `CredentialsMissing` (not `SSHFailed`),
   and a Warning event with reason `CredentialsMissing`.
3. **Reason precedence**: while the Secret is still missing, confirm the reason
   stays `CredentialsMissing` and is not overwritten by `SSHFailed`.

### S4 — Secret present but missing `ssh-privatekey` key

1. Create a Secret with some other data key only; reference it.
2. Assert `Discoverable` = `False`, reason `CredentialsMissing`, message
   referencing the missing key.

### S5 — Password auth

1. Configure the sshd pod to accept password auth for a user.
2. Create a Secret with the password under the default key `password`; reference
   it via `sshPasswordSecretRef`.
3. Assert Discoverable/Ready as in S1. Repeat with a **custom** `key:` to cover
   the non-default password key path.

### S6 — Secret-watch re-reconcile (unreachable → reachable via Secret update)

1. Apply a `Machine` referencing a Secret that initially has a **wrong** private
   key → `Discoverable=False`.
2. Update the Secret to the **correct** key.
3. Assert the Machine flips to `Discoverable=True` **without** editing the
   Machine CR — proving the Secret watch + field index re-triggers reconcile.

### S7 — State flips both directions

1. From Discoverable, stop the sshd pod (scale to 0) → machine goes
   `Discoverable=False`/`SSHFailed` within ~1 interval.
2. Restart it → back to `Discoverable=True`. (Mirrors the unit transition tests
   but against a live endpoint.)

### S8 — Admission validation of `host` / `sshUser`

1. Reject an invalid `host` (e.g. contains a space, or fails the pattern
   `^[a-zA-Z0-9][a-zA-Z0-9\-\.\:]*[a-zA-Z0-9]$|^[a-zA-Z0-9]$`) at `kubectl apply`.
2. Accept IPv4, a DNS name, and an IPv6-style string (the pattern allows `:`).
3. Reject an `sshUser` violating `^[a-zA-Z_][a-zA-Z0-9_\-]*$` or `MaxLength=32`;
   confirm omitting `sshUser` defaults to `root` in the stored object.

### S9 — Deletion / finalizer

1. Assert the finalizer `nio.homystack.com/finalizer` is added on first reconcile.
2. Delete the Machine → object disappears (finalizer removed).
3. **Document current behavior**: deletion is **not** blocked even when a
   `NixosConfiguration` references this Machine (the blocking TODO is
   unimplemented). An e2e should assert the delete succeeds today, and be updated
   if/when blocking lands.

### S10 — Cross-controller with `NixosConfiguration`

1. Create a `NixosConfiguration` with `machineRef` to a **not-yet-discoverable**
   Machine → config `Ready=False`, reason `MachineNotReady`.
2. Make the Machine discoverable (S1) → config proceeds to build an apply Job.
3. On apply success, assert the **Machine** status is written back by the config
   controller: `hasConfiguration == true`, `appliedConfiguration == <config name>`.
   (This proves the fields the Machine controller never touches are populated by
   the peer controller.) Full config apply detail lives in `02-nixosconfiguration.md`.

## Assertions cheat-sheet

| What | jsonpath |
| --- | --- |
| discoverable | `{.status.discoverable}` |
| Ready status | `{.status.conditions[?(@.type=='Ready')].status}` |
| Ready reason | `{.status.conditions[?(@.type=='Ready')].reason}` |
| Discoverable reason | `{.status.conditions[?(@.type=='Discoverable')].reason}` |
| Stalled present | `{.status.conditions[?(@.type=='Stalled')].status}` (expect empty when healthy) |
| observedGeneration | `{.status.observedGeneration}` |

## Out of scope for e2e (do not assert)

- `hardwareFacts`, `nixFacterResult`, `lastHardwareScanTime`, `HardwareScanned`
  condition — never populated by the Machine controller.
- SSH command execution / data written to the target — the controller only does
  a connection handshake.

## Suggested placement

Add a `Describe("Machine", Ordered, ...)` in a new `test/e2e/machine_test.go`
(behind `//go:build e2e`), with `BeforeAll` starting the sshd target pod +
keypair Secret in `nio-workloads`, and `AfterAll` tearing them down. S10 can
share fixtures with the `NixosConfiguration` suite if run in the same file.
