---
title: Design
sidebar_label: Overview
sidebar_position: 1
description: "The design corpus behind NIO's primitives, published as written."
---

# Design

These are NIO's design documents, published as written. They record **intent and
reasoning at the time of the decision** — why the primitives are shaped the way
they are, what alternatives were rejected, and what was deliberately left out.

:::warning Design documents are not the reference
Some of this was written before implementation and some describes behaviour that
shipped differently. Where a design document and the
[Reference](../reference/index.md) disagree, the reference is correct — it is
written from the current Go type declarations and states the commit it was
verified against.

Read these for *why*. Read the reference for *what*.
:::

## The documents

| Document | What it covers |
| --- | --- |
| [Architecture decisions](./decisions.md) | The ADR log. Short entries: context, decision, consequences. Start here. |
| [Nix-native workload primitives](./nix-workloads.md) | The largest document. The "operator is a compiler" idea, the API types, the generated pod anatomy, the state machine, and a worked example. |
| [The NixosConfiguration orchestrator](./nixosconfiguration-orchestrator.md) | The state machine that turns one host's desired configuration into child workloads, and why the day-two path is a cron rather than on-change only. |
| [NixCluster and converge](./cluster-converge.md) | How an abstract cluster maps onto Machines, the sticky selection algorithm, and the contract with the downstream flake repository. |
| [The two-key SSH conflict](./store-builder-ssh.md) | A retrospective: a real bug where the builder key displaced the target-host key, and the shape of the fix. |

## Two ideas that explain most of the rest

**The operator is a compiler.** NIO never runs `nix` in its own process and never
creates a build job of its own. It writes pod specs whose init-containers run
`nix build` and whose app container runs `nix run`. Everything else follows:
scheduling, retries, backoff and RBAC are inherited from Kubernetes rather than
reimplemented, and a failed build is a pod you can read the logs of.

**Orchestrators compile to workloads.** `NixosConfiguration` and `NixCluster` do
not talk to hosts. They create `NixJob` and `NixCronJob` children and watch their
status. This is why the workload family is the foundation and not a side feature,
and why debugging an orchestrator means looking at its child.

## What is deliberately not here

The design corpus also contains working documents that are not published:
implementation plans that are now stale, and integration-test notes that
reference internal run logs. They add nothing for a reader of the shipped
operator.

## Also worth reading

The [release notes](https://github.com/kitsunoff/NIO/releases) carry the
behavioural changes and the security state of each release, which the design
documents predate.
