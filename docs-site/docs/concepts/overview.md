---
title: The object model
sidebar_label: Overview
sidebar_position: 1
description: How NIO's nine kinds fit together, and which ones actually touch machines.
---

# The object model

NIO has nine kinds in one API group, `nio.homystack.com/v1alpha1`. They fall into
three layers, and the layering is the single most useful thing to understand:
**the top layer does not touch machines, it compiles down to the middle layer.**

```text
  orchestrators      NixosConfiguration          NixCluster
                            |                        |
                            v                        v
  workloads          NixJob + NixCronJob        NixCronJob
                            |                        |
                            +-----------+------------+
                                        v
                          batch/v1 Job | CronJob  ->  pods that run nix
                                        |
                                        v
                                    SSH to your hosts

  infrastructure          NixStore  <----  NixBuilder
                       (substituter)     (remote build worker)
```

## Layer 1: the workload family

[`NixJob`](../reference/nixjob.md), [`NixCronJob`](../reference/nixcronjob.md),
[`NixDeployment`](../reference/nixdeployment.md) and
[`NixStatefulSet`](../reference/nixstatefulset.md) each take a flake installable
and a native Kubernetes workload spec, and compile them into the corresponding
native object — `batch/v1 Job`, `batch/v1 CronJob`, `apps/v1 Deployment`,
`apps/v1 StatefulSet`.

They all share one `spec.nix` block, documented once in
[the NixSpec reference](../reference/nixspec.md). That is where the source
repository, the installable, the store and the builder live.

The operator itself never builds anything. It writes pod specs; the pods run
`nix`. See [Workloads](./workloads.md).

## Layer 2: the orchestrators

[`NixosConfiguration`](../reference/nixosconfiguration.md) converges **one** host.
[`NixCluster`](../reference/nixcluster.md) converges a **group** of hosts through
a single downstream flake.

Neither opens an SSH connection itself. Both create children from layer 1 and
then watch those children's status:

| Parent | Children it creates |
| --- | --- |
| `NixosConfiguration` | `<name>-install` (`NixJob`, only when `fullInstall: true`), `<name>-day2` (`NixCronJob`), `<name>-onremove` (`NixJob`, only on delete and only when `onRemoveFlake` is set) |
| `NixCluster` | `<name>-converge` (`NixCronJob`) |

This is why debugging an orchestrator almost always means looking at its child.
See [Troubleshooting a failed converge](../how-to/troubleshoot-converge.md).

## Layer 3: build infrastructure

[`NixStore`](../reference/nixstore.md) is a binary cache **server**.
[`NixBuilder`](../reference/nixbuilder.md) is a single remote build worker.

The relationship between them is the most commonly misunderstood part of NIO,
and getting it wrong is the difference between a 20-second converge and a
20-minute one:

- A `NixStore` referenced on its own is a **substituter**. Pods read from it.
  **Nothing is ever pushed into it.**
- A push happens only when a **builder is also in play**, and then only for the
  paths that build dispatches directly.
- What actually makes repeat converges fast is the **builder's own `/nix`**, and
  only if that `NixBuilder` has `spec.storage`. Without it, `/nix` is an
  `emptyDir` that dies with the pod and you rebuild the world every time.

See [Store and builder](./store-and-builder.md), then
[Accelerate a converge](../how-to/accelerate-converge.md).

## The Machine

[`Machine`](../reference/machine.md) is the odd one out: it is not a workload
and not an orchestrator. It is a record of *how to reach a host* plus a small
amount of observed state. It is the only kind whose controller opens an SSH
connection directly — and the only command it ever runs remotely is `uname -m`.

## Everything is namespace-scoped

All nine kinds are namespaced, and **every cross-object reference is
same-namespace only**. `storeRef`, `builderRef`, `machineRef`, `credentialsRef`,
`sshKeyRef`, `ageKeyRef` and the `NixCluster` node-group selectors all resolve
within the referencing object's namespace. There is no cross-namespace form and
no namespace field on any reference type; this is a deliberate v1alpha1
constraint, not an oversight.

A namespace is therefore the unit of isolation: one team's `NixStore` cannot be
consumed by another team's workload, and a `NixCluster` selects only from
`Machine`s beside it.

## Next

- [Machines and configurations](./machines-and-configurations.md) — the
  single-host path and its phase machine.
- [Workloads](./workloads.md) — the compiler idea and the generated pod.
- [Revisions and rollout](./revisions.md) — what makes a new run happen.
