---
title: NixosConfiguration
sidebar_position: 3
description: Converges one host — optional disk install, then a recurring rebuild.
---

# NixosConfiguration

Converges a single [`Machine`](./machine.md) to a flake attribute. Namespaced. No
short name. Note the capitalisation: lowercase `os`.

This is an **orchestrator**. It opens no SSH connections itself; it creates child
workload objects that do.

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixosConfiguration
metadata:
  name: node-a
  namespace: infra
spec:
  machineRef:
    name: node-a
  gitRepo: https://github.com/example/nixos-config.git
  ref: main
  flake: "#node-a"
  dayTwoSchedule: "*/30 * * * *"
  storeRef:
    name: store
  builderRef:
    name: builder
```

## `spec`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `machineRef` | [MachineReference](#machinereference) | yes | — | Target Machine, same namespace. |
| `gitRepo` | string | no | — | Repository URL holding the NixOS configuration. Max 2048 characters. |
| `ref` | string | no | `main` | Git branch, tag or commit. See [ref routing](#ref-routing). |
| `credentialsRef` | SecretReference | no | — | Secret for private repository access, same namespace. Recognised keys: `username` + `password`, or `ssh-privatekey`. |
| `flake` | string | no | — | Flake attribute. **Must include the leading `#`.** See [flake construction](#flake-construction). |
| `onRemoveFlake` | string | no | — | Flake attribute applied on deletion. See [Deletion](#deletion-and-onremoveflake). |
| `configurationSubdir` | string | no | — | Subdirectory holding the flake. Becomes the pod's working directory, not part of the installable string. Must stay inside the checkout. |
| `dayTwoSchedule` | string | no | `*/30 * * * *` | Cron schedule for the recurring rebuild. |
| `fullInstall` | boolean | no | `false` | Run `nixos-anywhere` once before the day-two cron. **Repartitions the target's disks.** |
| `additionalFiles` | [][AdditionalFile](#additionalfile) | no | — | Files injected into the source tree before the build. |
| `jobTemplate` | [JobTemplate](#jobtemplate) | no | — | **Nothing reads this.** See below. |
| `storeRef` | LocalObjectReference | no | — | `NixStore` passed to all children as a substituter, and the source of the builder SSH identity. |
| `builderRef` | LocalObjectReference | no | — | `NixBuilder` passed to all children. Sets `max-jobs = 0` in the children — no local fallback. |

### `MachineReference`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Machine name in the same namespace. Minimum length 1. |

### `flake` construction

The installable handed to the child is built by plain string concatenation:

```text
"." + spec.flake
```

**No separator is inserted and there is no validation.**

| `spec.flake` | Installable | Valid? |
| --- | --- | --- |
| `#node-a` | `.#node-a` | yes |
| `node-a` | `.node-a` | no — silently wrong |
| unset | `.` | yes — the flake's default output |

The `.` resolves against the checkout root, or `configurationSubdir` beneath it.

### `ref` routing

`spec.ref` is routed by **shape**, not by a mode field:

- A **full 40-character lowercase hex SHA** becomes a pinned revision. No git
  contact, no polling.
- **Anything else** is treated as a mutable ref and re-resolved by `git ls-remote`
  on the child's poll interval.

A 7-character short SHA is therefore treated as a *mutable ref*, not a pin.

### `AdditionalFile`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `path` | string | yes | Destination relative to the repository root. 1–4096 characters. Must stay inside the tree. |
| `valueType` | enum | yes | `Inline`, `SecretRef` or `NixosFacter`. |
| `inline` | string | no | Literal content, for `valueType: Inline`. |
| `secretRef` | [SecretKeyReference](#secretkeyreference) | no | Content from a Secret key, for `valueType: SecretRef`. |
| `nixosFacter` | boolean | no | **Never read.** |

:::danger `valueType: NixosFacter` is accepted by the schema and rejected by the controller
The CRD enum permits `NixosFacter`, so the object is created without complaint.
The controller has no branch for it and falls through to an error:

```text
additionalFile "...": valueType "NixosFacter" is not supported
```

That error fails child construction and errors the whole reconcile, so **no
children are created at all**. Use `Inline` or `SecretRef` only.

The separate `nixosFacter` boolean is never inspected either — the controller
switches only on `valueType`.
:::

Files are injected at **build time**: copied into the checkout, then force-staged
with `git add --force` so a git-tree flake sees them even under `.gitignore`.
They are not runtime mounts.

:::warning Do not put secrets in `inline`
Inline content is carried in the child's pod spec. Use `secretRef`.
:::

### `SecretKeyReference`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Secret name, same namespace. |
| `key` | string | yes | Key within the Secret. |

### `JobTemplate`

:::danger This entire field is dead API surface
`spec.jobTemplate` and every one of its sub-fields — `image`, `nodeSelector`,
`tolerations`, `resources`, `serviceAccountName` — are **read by nothing**. The
type has no reader anywhere in the operator, not even in tests.

Child pod templates are constructed entirely by the operator from the target
Machine. Setting `spec.jobTemplate` is accepted by the API server and has no
effect whatsoever.

To influence child pods, set the corresponding fields on the child workload
objects, or use `storeRef` / `builderRef` to move the build elsewhere.
:::

## Children

| Child | Kind | Created when |
| --- | --- | --- |
| `<name>-install` | [`NixJob`](./nixjob.md) | `fullInstall: true` and not yet completed |
| `<name>-day2` | [`NixCronJob`](./nixcronjob.md) | Always, after any install completes |
| `<name>-onremove` | [`NixJob`](./nixjob.md) | On deletion, only if `onRemoveFlake` is set |

All three carry labels `nio.homystack.com/config` and
`nio.homystack.com/machine`. The decommission job additionally carries
`nio.homystack.com/operation=decommission`.

Commands:

| Child | Runs | Arguments |
| --- | --- | --- |
| install | `github:nix-community/nixos-anywhere` | `--flake .<flake> -i /etc/nio/target-ssh/ssh-privatekey --ssh-option StrictHostKeyChecking=no --ssh-option UserKnownHostsFile=/dev/null <user>@<host>` |
| day-2 | `nixpkgs#nixos-rebuild` | `switch --flake .<flake> --target-host <user>@<host>` |
| decommission | `nixpkgs#nixos-rebuild` | `switch --flake .<onRemoveFlake> --target-host <user>@<host>` |

The day-two child is created with `triggerOnChange: true` and
`concurrencyPolicy: Forbid`, so a new commit fires promptly and two rebuilds
never overlap.

:::note The apply path requires a private key
Child construction fails if the target Machine has no `sshKeySecretRef`. A
password-only Machine cannot be converged.
:::

## `status`

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | integer | Last reconciled generation. |
| `phase` | string | See [Phases](#phases). |
| `fullDiskInstallCompleted` | boolean | The install ran successfully. Prevents it running again. |
| `resolvedRevision` | string | Commit SHA currently rolled out, taken from the day-two child. |
| `lastAppliedTime` | timestamp | Last successful application. |
| `targetMachine` | string | The Machine name. |
| `installJobRef` | string | Name of the install child. |
| `dayTwoCronJobRef` | string | Name of the day-two child. |
| `decommissionJobRef` | string | Name of the decommission child. |
| `installRetries` | integer | Bounded by 3. |
| `onRemoveRetries` | integer | Bounded by 3. |
| `conditions` | []Condition | See below. |

### Phases

| Phase | Set when |
| --- | --- |
| `Blocked` | Machine missing, not `Discoverable`, or owned by another configuration |
| `Installing` | The install child is running or being retried |
| `Converging` | The day-two cron exists but has no recorded successful run |
| `Ready` | The day-two cron is `Ready` **and** has a non-nil last-successful time |
| `Degraded` | Install retries exhausted, or the day-two cron is failing or stalled |
| `Removing` | Deletion in progress |

:::warning `Pending` is declared but never assigned
The API declares a `Pending` phase constant. No code path sets it. You will not
observe it.
:::

### Ownership

Two configurations cannot target one Machine. The **earliest-created** wins, ties
broken by lexicographic name. The loser goes `Blocked` with reason
`MachineInUse` and **its day-two cron is suspended**, so the two cannot race.

### Retries

| Path | Limit | On exhaustion |
| --- | --- | --- |
| install | 3 | `Degraded`, **no requeue, no child recreation**. Change the spec to restart. |
| decommission | 3 | Emits `OnRemoveFlakeFailed`, clears Machine status, deletes the child, **and finalizes anyway**. |

### Conditions

| Type | Notable reasons |
| --- | --- |
| `Ready` | `Blocked`, `Installing`, `Converging`, `Succeeded` |
| `Applied` | `ConfigurationApplied`, `ConfigurationRemoved`, `ApplyFailed` |
| `GitSynced` | `GitCloneSucceeded`, `GitCloneFailed` |
| `Stalled` | `MachineNotReady`, `MachineInUse` |

:::note `Applied` is sticky; `Ready` is not
`Applied` describes the **machine** — "this host has at some point converged" —
and deliberately survives a later `Degraded`. It is not "the most recent run
succeeded".
:::

## Deletion and `onRemoveFlake`

The day-two cron is always deleted first, so convergence stops immediately.

**`onRemoveFlake` unset** (default): the operator clears its back-reference on the
Machine and removes the finalizer. **The host is not reverted** — it keeps
whatever configuration it was last given.

**`onRemoveFlake` set**: a decommission `NixJob` runs `nixos-rebuild switch`
against that flake. It is created **without an owner reference**, deliberately,
so deleting the parent does not cascade it away mid-run. Its inner batch Job has
a 600-second TTL; the NIO `NixJob` object is deleted by the operator on
completion.

The operator finds this job **by label**, not by `status.decommissionJobRef`, so
an operator restart that lost status still finds and adopts it.

Deletion always completes. If the decommission job cannot even be built — for
instance the Machine has no `sshKeySecretRef` — the operator emits
`OnRemoveFlakeSkipped` and finalizes rather than blocking deletion forever.

Events: `OnRemoveFlakeSucceeded`, `OnRemoveFlakeFailed`, `OnRemoveFlakeSkipped`.

## Print columns

```text
NAME   READY   PHASE   TARGET   FLAKE   AGE
```

*Verified against kitsunoff/NIO@e0dbe5e.*
