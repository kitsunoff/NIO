---
title: Machine
sidebar_position: 2
description: A host NIO can reach over SSH.
---

# Machine

A host NIO can reach over SSH. Namespaced. No short name.

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata:
  name: node-a
  namespace: infra
  labels:
    role: worker
spec:
  host: node-a.example.internal
  sshUser: root
  sshKeySecretRef:
    name: host-ssh-key
```

Labels on a `Machine` are how [`NixCluster`](./nixcluster.md) node groups select
it.

## `spec`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `host` | string | yes | — | Hostname or IP for the SSH connection. 1–253 characters, matching `^[a-zA-Z0-9][a-zA-Z0-9\-\.\:]*[a-zA-Z0-9]$` or a single alphanumeric. The colon in the pattern permits IPv6 literals. |
| `sshUser` | string | no | `root` | SSH username. Max 32 characters, matching `^[a-zA-Z_][a-zA-Z0-9_\-]*$`. |
| `sshKeySecretRef` | [SecretReference](#secretreference) | no | — | Secret holding the SSH private key. Same namespace. |
| `sshPasswordSecretRef` | [SSHPasswordSecretRef](#sshpasswordsecretref) | no | — | Secret holding an SSH password. Same namespace. |

There is **no port field**. The SSH port is fixed at 22.

### `SecretReference`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Secret name in the same namespace. Minimum length 1. |

The private key is read from the Secret key **`ssh-privatekey`**. This name is
hard-coded; there is no field to override it. A Secret without that key fails
with `secret "..." does not contain 'ssh-privatekey'`.

### `SSHPasswordSecretRef`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `name` | string | yes | — | Secret name in the same namespace. |
| `key` | string | no | `password` | The key within the Secret. Unlike the private-key reference, this one **is** overridable. |

:::warning Password auth cannot converge a host
`sshPasswordSecretRef` works for reachability probing and the architecture scan.
It does **not** work for applying configuration: the
[`NixosConfiguration`](./nixosconfiguration.md) apply path mounts a private key
file into the child pod and requires `sshKeySecretRef`. A password-only Machine
can be `Discoverable` while no configuration targeting it can ever produce a
child.
:::

## `status`

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | integer | Most recent `metadata.generation` reconciled. |
| `discoverable` | boolean | The host answered over SSH. |
| `hasConfiguration` | boolean | Written by the `NixosConfiguration` controller. **Nothing reads it.** |
| `appliedConfiguration` | string | Name of the `NixosConfiguration` that owns this Machine. Read as an ownership guard when clearing status. |
| `appliedCommit` | string | The owning configuration's resolved revision. Write-only — nothing reads it. |
| `lastAppliedTime` | timestamp | Copied from the owning configuration. Write-only. |
| `lastHardwareScanTime` | timestamp | When the architecture scan last ran. Read as the scan-interval gate. |
| `hardwareFacts` | [HardwareFacts](#status-hardwarefacts) | See below — mostly unpopulated. |
| `nixFacterResult` | object | **Never written.** See below. |
| `conditions` | []Condition | See [Conditions](#conditions). |

### `status.hardwareFacts` {#status-hardwarefacts}

:::danger Only `architecture` is ever populated
The schema describes a full hardware inventory. The controller writes exactly one
field.

| Sub-field | Populated? |
| --- | --- |
| `architecture` | **Yes** — the only one |
| `os.name`, `os.id` | No |
| `kernel.version` | No |
| `cpu.model`, `cpu.cores` | No |
| `memory.mb` | No |
| `hostname` | No |
| `virtualization.type`, `virtualization.containerEngine` | No |
| `disks` (map) | No |
| `interfaces` (map) | No |

The only remote command the Machine controller ever runs is `uname -m`. Do not
build tooling that reads any other sub-field; it will always be empty.
:::

`architecture` is the raw `uname -m` output — `x86_64`, `aarch64`. It is read in
exactly one place: the [`NixCluster`](./nixcluster.md) builder architecture
preflight, which maps `x86_64`/`amd64` to `x86_64-linux` and `aarch64`/`arm64` to
`aarch64-linux`. Anything else maps to empty and the preflight stays silent.

The value is written only when it **changed**, deliberately, to avoid a
status-write loop that would retrigger reconciliation forever.

### `status.nixFacterResult` {#status-nixfacterresult}

:::danger Never written
Despite the name, `nix-facter` is never invoked anywhere in the operator. This
field is always absent.
:::

## Conditions

| Type | Reasons |
| --- | --- |
| `Ready` | Mirrors `Discoverable`. |
| `Discoverable` | `SSHConnected`, `SSHFailed`, `CredentialsMissing` |
| `HardwareScanned` | `HardwareScanSucceeded`, `HardwareScanFailed` |

## Timing

| Behaviour | Interval |
| --- | --- |
| SSH reachability re-probe | **60 seconds**, unconditional — every reconcile requeues at this cadence |
| Architecture scan | **12 hours**, and only while `discoverable` is true |
| SSH connection timeout | **30 seconds**, for both the probe and the scan |

None of these are configurable through the API.

## Print columns

```text
NAME   HOST   READY   DISCOVERABLE   CONFIG   AGE
```

`READY` and `DISCOVERABLE` come from the conditions of those names; `CONFIG`
from `status.appliedConfiguration`.

## Deletion

`Machine` has no finalizer of its own. Deleting one while a
`NixosConfiguration` targets it moves that configuration to `Blocked` with
reason `MachineNotReady`; it does not touch the host.

*Verified against kitsunoff/NIO@e0dbe5e.*
