---
title: Machines and configurations
sidebar_position: 2
description: The single-host path — what a Machine observes, and how the NixosConfiguration phase machine drives it.
---

# Machines and configurations

## What a `Machine` is

A `Machine` records how to reach one host and what NIO has observed about it. It
carries an address, an SSH user and a credential reference — nothing about what
the host should run.

Its controller does exactly two things on a loop:

1. **Probe reachability**, every **60 seconds**, unconditionally. Success sets
   `status.discoverable: true` and the `Discoverable` condition.
2. **Scan the architecture**, at most every **12 hours**, and only while
   `discoverable` is true. The scan is one remote command: `uname -m`.

That is the whole of it. `Machine` is deliberately thin — it does not apply
anything, and it does not know which `NixosConfiguration` targets it (though a
configuration writes a back-reference into the Machine's status).

:::note What `hardwareFacts` really contains
`status.hardwareFacts` has a rich schema — OS, kernel, CPU, memory, disks,
network interfaces, virtualization type. **Only `architecture` is ever
populated.** Every other sub-field is unreachable dead schema. The adjacent
`status.nixFacterResult` field is likewise never written: despite its name,
`nix-facter` is never invoked anywhere in the operator. See
[the Machine reference](../reference/machine.md#status-hardwarefacts).
:::

### Authentication

Two credential forms exist, and they are **not interchangeable**:

| Field | Secret key | Works for |
| --- | --- | --- |
| `sshKeySecretRef` | `ssh-privatekey` (fixed, not overridable) | discovery, scan, **and** applying configuration |
| `sshPasswordSecretRef` | `spec.sshPasswordSecretRef.key`, default `password` | discovery and scan **only** |

A `Machine` with only a password can go `Discoverable`, but a
`NixosConfiguration` targeting it cannot build its children: the apply path
mounts a private key into the pod and requires `sshKeySecretRef`. If it is
missing, the child is never created.

## What a `NixosConfiguration` is

A `NixosConfiguration` binds one `Machine` to one flake attribute and keeps the
host converged to it. It is an orchestrator: it creates children and reads their
status.

### Ownership is exclusive

Two configurations cannot target the same `Machine`. When they do, the operator
picks a winner deterministically — the **earliest-created** configuration wins,
ties broken by lexicographic name — and the loser goes `Blocked` with reason
`MachineInUse`, with its day-two cron **suspended** so it cannot fight the winner.

This is a real safety property: it stops two schedules racing `nixos-rebuild` on
one host.

### The phase machine

`status.phase` is coarse and human-facing:

| Phase | What it means |
| --- | --- |
| `Blocked` | The target `Machine` is missing, not `Discoverable`, or owned by another configuration. No children are driven forward. |
| `Installing` | The `fullInstall` child is running (or being retried). |
| `Converging` | The day-two cron exists but has not yet been confirmed healthy. |
| `Ready` | The day-two cron is healthy **and** has a recorded successful run. |
| `Degraded` | An install exhausted its retries, or the day-two cron is failing or stalled on missing infrastructure. |
| `Removing` | Deletion is in progress. |

:::warning `Pending` is declared but never set
The API declares a `Pending` phase constant. Nothing in the controller ever
assigns it. You will not observe `phase: Pending` on a real object.
:::

Two behaviours worth internalising:

- **`Ready` requires evidence, not absence of failure.** It is set only when the
  child cron reports both a healthy phase and a non-nil last-successful-run
  timestamp. A cron that has been scheduled but has never completed a run stays
  `Converging`.
- **`Applied` is sticky, `Ready` is not.** The `Applied` condition describes the
  *machine* — "this host has at some point been converged" — so it survives a
  later `Degraded`. Do not read `Applied` as "the last run succeeded".

### Retries are bounded and then stop

Both the install path and the decommission path retry **3 times**. On exhausting
install retries the configuration sits `Degraded` with **no requeue and no child
recreation** — it will not quietly keep trying. You must change the spec to
restart it.

The decommission path is deliberately more forgiving: if it exhausts its
retries, or if the child cannot even be built, the operator **removes the
finalizer anyway**. A failed teardown must never make an object undeletable.

## How a configuration becomes a command

The child's argument list is assembled from four separate spec fields, and the
assembly is more literal than you might expect:

- `spec.flake` is concatenated as `"." + flake`, with **no separator inserted**.
  So `#node-a` yields `.#node-a`; a bare `node-a` yields the meaningless
  `.node-a`. The leading `#` is your responsibility.
- `spec.configurationSubdir` never enters the installable string. It becomes the
  pod's **working directory**, which is what makes the `.` resolve to the subdir.
- `spec.ref` is routed by shape: a full 40-character hex SHA becomes a pinned
  revision resolved without contacting git; anything else is treated as a
  mutable ref and re-resolved by polling.
- The target is `spec.machineRef` → the Machine's `sshUser@host`.

The resulting day-two command is:

```sh
nixos-rebuild switch --flake .#node-a --target-host root@node-a.example.internal
```

and the install command is `nixos-anywhere` with the same flake, from
`github:nix-community/nixos-anywhere`.

## Deletion

Deleting a configuration always deletes the day-two cron first, so convergence
stops immediately. What happens next depends entirely on `spec.onRemoveFlake`:

- **Unset** (the default): the operator clears its back-reference on the
  `Machine` and finalizes. **The host keeps its current configuration.** Nothing
  is reverted. This surprises people.
- **Set**: a decommission `NixJob` runs `nixos-rebuild switch` against the
  removal flake. It is created **without an owner reference** precisely so that
  deleting the parent does not cascade it away mid-run.

## Next

- [Workloads](./workloads.md) — what the children actually do.
- [Reference: NixosConfiguration](../reference/nixosconfiguration.md).
