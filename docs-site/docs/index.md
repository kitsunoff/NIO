---
title: NIO
sidebar_label: What NIO is
sidebar_position: 0
slug: /
description: A Kubernetes operator that converges real NixOS machines from custom resources.
---

# NIO

NIO is a Kubernetes operator that manages **real NixOS machines**. You describe a
host, a configuration and a cluster as Kubernetes custom resources; the operator
reaches those hosts over SSH and runs `nixos-anywhere` and `nixos-rebuild` until
the machines match what you declared.

It is not a simulator and not a manifest generator. A `NixosConfiguration` that
reports `Ready` means a real host was rebuilt.

## What it manages

| Kind | What it does |
| --- | --- |
| [`Machine`](./reference/machine.md) | A host NIO can reach over SSH. Reports reachability and CPU architecture. |
| [`NixosConfiguration`](./reference/nixosconfiguration.md) | Converges one host: optional first-time disk install, then a recurring day-two rebuild. |
| [`NixCluster`](./reference/nixcluster.md) | Selects `Machine`s into node groups and drives one whole-cluster converge. |
| [`NixStore`](./reference/nixstore.md) | A shared Nix binary cache that workloads substitute from. |
| [`NixBuilder`](./reference/nixbuilder.md) | A remote builder that workloads delegate `nix build` to. |
| [`NixJob`](./reference/nixjob.md), [`NixCronJob`](./reference/nixcronjob.md), [`NixDeployment`](./reference/nixdeployment.md), [`NixStatefulSet`](./reference/nixstatefulset.md) | Run a flake attribute as the corresponding Kubernetes workload. |

The four workload kinds are not a side feature. `NixosConfiguration` and
`NixCluster` are **orchestrators**: they do not talk to hosts themselves, they
compile down to `NixJob` and `NixCronJob` children that do. Understanding the
workload family is how you understand everything else.

## Install

```sh
kubectl apply --server-side \
  --filename https://github.com/kitsunoff/NIO/releases/download/v1.0.0/install.yaml
```

`--server-side` is **required, not a preference**. See
[Installation](./getting-started/installation.md) for why, and for the exact
error a plain `kubectl apply` produces.

For the newest version, see the
[releases page](https://github.com/kitsunoff/NIO/releases).

:::warning Known vulnerabilities
`v1.0.0` ships with **10 known security advisories reachable from NIO's own
code**, disclosed in that release's own notes. Do not deploy it anywhere exposed
without reading them. Check the
[releases page](https://github.com/kitsunoff/NIO/releases) for a newer version
that closes them.
:::

## Where to go next

- **New here?** [Install the operator](./getting-started/installation.md), then
  [converge your first machine](./getting-started/first-machine.md).
- **Want the model?** [Concepts](./concepts/overview.md) explains why the
  primitives are shaped the way they are.
- **Have a task?** [How-to guides](./how-to/shared-store-and-builder.md) are
  task-shaped.
- **Need a field?** [Reference](./reference/index.md) documents every CRD field,
  written from the Go type declarations.
- **Want the reasoning?** [Design](./design/index.md) is the design corpus.

## A note on this documentation

The reference section is hand-written from the Go type declarations in `api/`,
not generated. Every reference page ends with the commit it was verified
against. Where a field exists in the API but **nothing in the controller reads
it**, the page says so explicitly rather than describing what the name suggests.
