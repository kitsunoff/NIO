---
title: API reference
sidebar_label: Overview
sidebar_position: 1
description: The nio.homystack.com/v1alpha1 API group, hand-written from the Go type declarations.
---

# API reference

All nine kinds live in one group and version:

```yaml
apiVersion: nio.homystack.com/v1alpha1
```

The group is `nio.homystack.com`. It is inherited from the operator's original
home and is **not** derived from the GitHub repository name.

| Kind | Short names | Reference |
| --- | --- | --- |
| `Machine` | — | [Machine](./machine.md) |
| `NixosConfiguration` | — | [NixosConfiguration](./nixosconfiguration.md) |
| `NixCluster` | — | [NixCluster](./nixcluster.md) |
| `NixStore` | `nstore` | [NixStore](./nixstore.md) |
| `NixBuilder` | `nbuilder` | [NixBuilder](./nixbuilder.md) |
| `NixJob` | `nixj` | [NixJob](./nixjob.md) |
| `NixCronJob` | `nixcron` | [NixCronJob](./nixcronjob.md) |
| `NixDeployment` | `nixdeploy` | [NixDeployment](./nixdeployment.md) |
| `NixStatefulSet` | `nixsts` | [NixStatefulSet](./nixstatefulset.md) |

The four workload kinds share one `spec.nix` block, documented once:
**[the `nix` block](./nixspec.md)**.

Operator flags are on the [CLI reference](./cli.md).

Note the capitalisation of `NixosConfiguration` — lowercase `os`, not
`NixOSConfiguration`.

## How to read these pages

Every page is **hand-written from the Go type declarations** in `api/v1alpha1/`,
not generated and not copied from an older document. Each ends with the commit it
was verified against.

Two conventions matter:

- **Defaults** shown are the ones the CRD or the controller actually applies. Where
  the CRD default and the controller's behaviour differ, both are stated.
- **Fields nothing reads are marked.** This repository has shipped fields that no
  code consumes. Rather than describe what a name suggests, those fields carry an
  explicit warning. They are listed together below.

## Fields that exist but do nothing

Documented here in one place so you do not configure something expecting an
effect. Each is also flagged on its own page.

| Kind | Field | Status |
| --- | --- | --- |
| `NixosConfiguration` | `spec.jobTemplate` (all of `image`, `nodeSelector`, `tolerations`, `resources`, `serviceAccountName`) | Nothing reads any of it. Child pod templates are built entirely by the operator. |
| `NixosConfiguration` | `spec.additionalFiles[].valueType: NixosFacter` | In the CRD enum, but the controller rejects it with an error and the reconcile fails. |
| `NixosConfiguration` | `spec.additionalFiles[].nixosFacter` (the boolean) | Never read. |
| `NixStore` | `spec.upstreamSubstituters` | Never read. The store server's config has no `substituters` line. |
| `NixStore` | `status.storeURI` | Written, never read. The push URL is re-derived independently. |
| `Machine` | `status.nixFacterResult` | Never written. `nix-facter` is never invoked. |
| `Machine` | `status.hardwareFacts.*` except `architecture` | Never written. |
| `Machine` | `status.hasConfiguration` | Written, never read. |
| `NixDeployment`, `NixStatefulSet` | `spec.nix.triggerOnChange` | Never read by these two controllers. |
| `NixosConfiguration` | `status.phase: Pending` | Declared but never assigned. |

## Conventions across every kind

**All references are same-namespace.** `storeRef`, `builderRef`, `machineRef`,
`credentialsRef`, `sshKeyRef`, `ageKeyRef`, `secretRef` and `configMapRef` all
resolve in the referencing object's namespace. No reference type has a namespace
field. This is a deliberate v1alpha1 constraint.

**Conditions follow kstatus.** Every kind carries `Ready`, and most carry
`Reconciling` and `Stalled`. Kind-specific additions are `Discoverable` and
`HardwareScanned` (Machine), `Applied` and `GitSynced` (NixosConfiguration),
`Underprovisioned` (NixCluster), and `Progressing` (workloads).

**The finalizer** is `nio.homystack.com/finalizer`.

**Managed labels** stamped on generated pod templates:

| Label | Meaning |
| --- | --- |
| `nio.homystack.com/workload-kind` | The NIO kind that owns the pod |
| `nio.homystack.com/workload-name` | The owning object's name |
| `nio.homystack.com/revision` | The composite revision hash |
| `app.kubernetes.io/managed-by` | Always `nio` |

The annotation `nio.homystack.com/revision` carries the same hash on the pod
template; changing it is what triggers a native rolling update.

*Verified against kitsunoff/NIO@e0dbe5e.*
