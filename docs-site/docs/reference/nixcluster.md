---
title: NixCluster
sidebar_position: 4
description: Groups Machines into node groups and drives one idempotent converge.
---

# NixCluster

Groups [`Machine`](./machine.md)s into node groups and drives **one** converge
[`NixCronJob`](./nixcronjob.md) against a downstream flake. Namespaced. No short
name.

NIO never interprets what the cluster means. Semantics live in your flake.

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata:
  name: prod
  namespace: infra
spec:
  source:
    gitRepo: https://github.com/example/cluster.git
    ref: main
  sshKeyRef:
    name: cluster-ssh
  ageKeyRef:
    name: cluster-age-key
  storeRef:
    name: store
  builderRef:
    name: builder
  dayTwoSchedule: "*/30 * * * *"
  nodeGroups:
    - name: control-plane
      selector:
        matchLabels:
          role: worker
          tier: cp
      count: 3
      values:
        role: controlplane
    - name: workers
      selector:
        matchLabels:
          role: worker
```

## `spec`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `source` | [NixSource](./nixspec.md#source) | yes | — | The downstream flake repository. Passed verbatim to the converge child. |
| `sshKeyRef` | SecretReference | no | — | Cluster-wide SSH private key. Secret key must be `ssh-privatekey`. |
| `ageKeyRef` | SecretReference | no | — | sops age key. Secret key must be `keys.txt`. |
| `storeRef` | LocalObjectReference | no | — | `NixStore` added to the converge pod as a substituter, and the source of the builder SSH identity. |
| `builderRef` | LocalObjectReference | no | — | `NixBuilder` the converge build is delegated to. Sets `max-jobs = 0` — no local fallback. |
| `dayTwoSchedule` | string | no | `*/30 * * * *` | Converge cadence. |
| `nodeGroups` | [][NodeGroup](#nodegroup) | yes | — | Minimum 1 item. |

### Secret mounts

| Field | Required Secret key | Mount path | Environment |
| --- | --- | --- | --- |
| `sshKeyRef` | `ssh-privatekey` | `/etc/nio/cluster-ssh` (mode `0400`) | `NIX_SSHOPTS=-i /etc/nio/cluster-ssh/ssh-privatekey -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null` |
| `ageKeyRef` | `keys.txt` | `/etc/nio/age` (mode `0400`) | `SOPS_AGE_KEY_FILE=/etc/nio/age/keys.txt` |

:::danger Neither key name is validated
Both are hard-coded paths in the operator. A Secret storing the key under a
different name is accepted at admission, the pod starts, and the failure appears
only at converge time as an SSH or sops error that does not mention NIO.

Check before relying on it:

```sh
kubectl get secret cluster-ssh --namespace infra \
  --output jsonpath='{range $k, $v := .data}{$k}{"\n"}{end}'
```
:::

`sshKeyRef` is a **single key for every member**. There is no per-member key. For
per-host key isolation use one [`NixosConfiguration`](./nixosconfiguration.md)
per host instead.

### `NodeGroup`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Unique within the cluster. Minimum length 1. Used in status. |
| `selector` | LabelSelector | yes | Standard Kubernetes label selector over `Machine` labels in this namespace. |
| `count` | integer | no | Caps the group to a stable, sticky subset. Unset means all matching Machines. |
| `values` | object | no | Schemaless, opaque. Mapped onto each member. NIO never interprets it. |

**A Machine belongs to exactly one group.** The **first** matching group in spec
order claims it; later groups cannot. Order specific groups before general ones.

An invalid `selector` puts the cluster in `Blocked`.

### Selection

With `count` **unset**, all matching machines are members.

With `count` **set**, selection is sticky:

1. Members already selected and still matching are kept.
2. Excess members are dropped, **highest name first**.
3. The shortfall is topped up from the remaining candidates.
4. The result is sorted by name.

Membership does not reshuffle because an unrelated label changed. When the group
cannot reach `count`, NIO **does not provision anything** — it sets the
`Underprovisioned` condition and converges the members it has.

### `values` and the generated node files

For each member, one Nix file is injected at `modules/nodes/<machine-name>.nix`
via the build-time `additionalFiles` mechanism:

```nix
{ lib, ... }:
{
  nixcluster."prod".members."node-a" =
    lib.recursiveUpdate (builtins.fromJSON "{\"role\":\"controlplane\"}") {
      install.ip = "node-a.example.internal";
    };
}
```

- `install.ip` comes from `machine.spec.host` and is supplied by NIO, not by you.
- `lib.recursiveUpdate` means your values **merge with** rather than replace the
  module's defaults.
- Because the values pass through `builtins.fromJSON`, they must be **JSON
  data**. They cannot carry a Nix value — no functions, no
  `nixosConfiguration` module. Members inherit the cluster-level default from
  your flake.

Machine names containing `/` or `..`, or empty names, are rejected.

## The converge child

Exactly one child: a [`NixCronJob`](./nixcronjob.md) named `<cluster>-converge`.

| Property | Value |
| --- | --- |
| `run` | `.#cluster-<cluster>` |
| `args` | `["converge"]` |
| `schedule` | `spec.dayTwoSchedule` |
| `concurrencyPolicy` | `Forbid` |
| `triggerOnChange` | `true` |
| `activeDeadlineSeconds` | `3600` |

`Forbid` is what makes the model safe: two converges never overlap, so your
converge script needs only to be idempotent, not to implement its own locking.

## Builder architecture preflight

Before driving the converge, the operator compares the builder's declared
`spec.systems` against members' observed architectures. On a **provable**
mismatch it sets phase `Blocked` with reason `BuilderSystemMismatch` and
**suspends the converge child**:

```text
NixBuilder "builder" builds only [aarch64-linux], but ...; converge delegates
every build (max-jobs = 0) so there is no local fallback
```

The check is deliberately conservative and stays **silent** when it cannot prove
a mismatch:

- `builderRef` is unset
- the builder cannot be read
- the builder's `spec.systems` is **empty**
- a Machine has no `hardwareFacts` yet

Architecture mapping: `x86_64`/`amd64` → `x86_64-linux`, `aarch64`/`arm64` →
`aarch64-linux`, anything else → unmapped and skipped.

The suspension is reversible automatically: the desired child does not set
`suspend`, so the next clean reconcile un-suspends it.

:::note This preflight exists only here
`NixosConfiguration` and the four workload kinds have **no** architecture
pre-check. They verify only that the referenced store and builder exist and are
Ready.
:::

## `status`

| Field | Type | Description |
| --- | --- | --- |
| `phase` | enum | `Ready`, `Converging`, `Degraded`, `Blocked`. |
| `nodeGroups` | [][NodeGroupStatus](#nodegroupstatus) | Stable, sticky selection per group. |
| `convergeJobRef` | string | Name of the converge child. |
| `conditions` | []Condition | `Ready`, `Stalled`, `GitSynced`, `Underprovisioned`. |
| `observedGeneration` | integer | Last reconciled generation. |

`phase: Blocked` is also set when the cluster has **zero** members in total.

### `NodeGroupStatus`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | The group name. |
| `members` | []MemberStatus | Selected Machines, stable and sorted by name. |
| `desired` | integer | `count` when set, otherwise the number of matching candidates. |
| `selected` | integer | Actual member count. |

### `MemberStatus`

| Field | Type | Description |
| --- | --- | --- |
| `name` | string | Machine name. |
| `status` | string | `Pending`, `Applying`, `Applied` or `Failed`. |

:::warning Member status is job-level, not per-node
It is derived from the state of the single converge run, not from per-host
reporting. A converge that succeeded on four machines and failed on the fifth
marks **all five** `Failed`.

The derivation, in order: no cron → `Pending`; converge stalled on
`InfraNotReady` → **carry over the previous status**; the last finished run
failed → `Failed`; cron phase `Failed`/`Degraded` → `Failed`; cron `Ready` →
`Applied`; active jobs → `Applying`; a recorded successful time → `Applied`;
otherwise `Pending`.

The carry-over case is deliberate: a converge blocked on a missing store or
builder has never run, so members are not marked failed for it.
:::

### `Underprovisioned`

| Status | Reason | Message |
| --- | --- | --- |
| `True` | `Underprovisioned` | `one or more nodeGroups have fewer matching Machines than requested` |
| `False` | `FullyProvisioned` | `all nodeGroups satisfied their requested count` |

## Timing

Steady-state requeue is **30 seconds**.

## No decommission hook

:::danger `NixCluster` has no `onRemoveFlake`
Removing a Machine from a group, or deleting the `NixCluster`, runs **nothing**
on the affected hosts. They keep their last-applied configuration and keep
running. Whatever "leaving the cluster" means must be implemented in your
converge script and must happen while the machine is still a member. See
[Add or remove a cluster member](../how-to/cluster-membership.md).
:::

## Print columns

```text
NAME   PHASE   CONVERGE   AGE
```

*Verified against kitsunoff/NIO@e0dbe5e.*
