---
title: Clusters
sidebar_position: 6
description: How NixCluster groups Machines and drives one idempotent converge.
---

# Clusters

## What a `NixCluster` is, and what it is not

A [`NixCluster`](../reference/nixcluster.md) groups `Machine`s into node groups,
maps opaque per-group values onto each member, and drives **one** converge
`NixCronJob` against a downstream flake repository.

**NIO never interprets what the cluster means.** It does not know whether you
are building Kubernetes, a Proxmox pool or a fleet of routers. The semantics
live entirely in your flake repository. NIO's job is selection, value mapping and
scheduling one idempotent converge.

This is the opposite decomposition from `NixosConfiguration`, which drives one
host per object. A `NixCluster` is one converge for the whole group — which is
what lets the flake reason about the cluster as a whole (quorum, seed nodes,
cross-node references) rather than converging hosts independently.

## Selection is stable and sticky

Each node group has a label `selector` over `Machine`s in the same namespace, and
optionally a `count`.

Two rules keep membership from churning:

1. **A `Machine` belongs to exactly one group.** The **first** matching group in
   spec order claims it; later groups cannot. Order your groups from most
   specific to least.
2. **Selection is sticky.** With `count` set, members already selected and still
   matching are **kept**. Only the shortfall is topped up, and only the excess is
   dropped. Membership does not reshuffle because a label changed elsewhere.

With `count` unset, all matching machines are members and stickiness is moot.

When a group cannot reach its `count`, the operator does **not** provision
anything — NIO never creates machines. It surfaces the gap as the
`Underprovisioned` condition and converges the members it has.

## Values are opaque

Each group carries a schemaless `values` object. NIO passes it through without
interpreting it, generating one Nix file per member at
`modules/nodes/<machine-name>.nix` in the checked-out repository:

```nix
{ lib, ... }:
{
  nixcluster."my-cluster".members."node-a" =
    lib.recursiveUpdate (builtins.fromJSON "{\"role\":\"worker\"}") {
      install.ip = "node-a.example.internal";
    };
}
```

Two things NIO adds on its own:

- The member's **address**, from `machine.spec.host`, as `install.ip`. This is
  the one value you do not supply.
- The `lib.recursiveUpdate` wrapper, so your JSON values merge with rather than
  replace the module's defaults.

These files are injected via the build-time `additionalFiles` mechanism, which
force-stages them with `git add --force` — so they are visible to a git-tree
flake even though they are not committed.

Because `values` is passed through `builtins.fromJSON`, it must be **JSON data**.
It cannot carry a Nix value: no functions, no `nixosConfiguration` module.
Members inherit the cluster-level default defined in your flake repository.

## One converge child

A `NixCluster` creates exactly one child: a `NixCronJob` named
`<cluster>-converge`, running `.#cluster-<cluster>` with the argument `converge`,
with `concurrencyPolicy: Forbid` and `triggerOnChange: true`.

`Forbid` is what makes the whole model safe. Two converges never overlap, so the
converge script does not need its own locking — it needs only to be idempotent.

## Per-member status is coarser than it looks

`status.nodeGroups[].members[].status` shows `Pending`, `Applying`, `Applied` or
`Failed` per machine. Be aware this is derived from the **job-level** state of
the single converge run, not from per-node reporting:

- converge cron has active jobs → every member reads `Applying`
- the last finished run failed → every member reads `Failed`
- the cron is Ready or has a successful run → every member reads `Applied`

So a converge that succeeded for four machines and failed for the fifth marks
**all five** `Failed`. Read member status as "how did the run that covers this
member go", not "what is the state of this host".

One deliberate exception: when the converge is stalled on missing infrastructure
(a `NixStore` or `NixBuilder` that is absent or not Ready), the cluster reports
`Blocked` and members **keep their previous status** rather than being marked
`Failed`. A converge that never ran has not failed on anything.

## Secrets

Two optional Secret references are mounted into the converge pod:

- **`sshKeyRef`** — a cluster-wide private key, expected under the key
  `ssh-privatekey`, mounted at `/etc/nio/cluster-ssh` and wired into `NIX_SSHOPTS`.
  This one key must reach **every** member.
- **`ageKeyRef`** — a sops age key, expected under `keys.txt`, mounted at
  `/etc/nio/age` and exposed as `SOPS_AGE_KEY_FILE`.

Both key names are fixed paths in the operator and are **not validated** when the
object is created. A Secret with the wrong key name is accepted happily and fails
at converge time. See [Secrets with sops and age](../how-to/secrets-sops-age.md).

## Next

- [Add or remove a cluster member](../how-to/cluster-membership.md).
- [Reference: NixCluster](../reference/nixcluster.md).
