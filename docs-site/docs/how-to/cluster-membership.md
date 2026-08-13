---
title: Add or remove a cluster member
sidebar_position: 3
description: Grow and shrink a NixCluster node group safely.
---

# Add or remove a cluster member

Membership is driven by labels on `Machine` objects, not by a list in the
`NixCluster`. You add a member by creating a `Machine` that matches a group's
selector.

## Add a member

### 1. Create the Machine with the group's label

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata:
  name: node-d
  namespace: infra
  labels:
    role: worker
spec:
  host: node-d.example.internal
  sshUser: root
  sshKeySecretRef:
    name: host-ssh-key
```

### 2. Wait for it to be discoverable

An undiscoverable `Machine` can still be selected, but the converge cannot reach
it and the architecture preflight has nothing to check.

```sh
kubectl get machine node-d --namespace infra
```

```text
NAME     HOST                      READY   DISCOVERABLE   CONFIG   AGE
node-d   node-d.example.internal   True    True                    40s
```

If it does not turn `Discoverable` within a minute or two, that is an SSH
problem. The controller re-probes every 60 seconds.

### 3. Raise `count`, if you set one

With `count` unset, `node-d` is now a member automatically. With `count` set, it
is a *candidate* — you must raise the number:

```sh
kubectl patch nixcluster prod --namespace infra --type merge \
  --patch '{"spec":{"nodeGroups":[{"name":"workers","selector":{"matchLabels":{"role":"worker"}},"count":4}]}}'
```

:::caution `nodeGroups` is a list — a merge patch replaces it wholly
The patch above rewrites the entire `nodeGroups` array. If your cluster has more
than one group, or the group carries `values`, edit the object instead:
`kubectl edit nixcluster prod --namespace infra`.
:::

### 4. Confirm selection

```sh
kubectl get nixcluster prod --namespace infra --output jsonpath='{range .status.nodeGroups[*]}{.name}{": "}{.selected}{"/"}{.desired}{"\n"}{end}'
```

```text
workers: 4/4
```

If `selected` is below `desired`, the `Underprovisioned` condition explains it:

```sh
kubectl get nixcluster prod --namespace infra \
  --output jsonpath='{.status.conditions[?(@.type=="Underprovisioned")].message}{"\n"}'
```

NIO will **not** provision a machine to close the gap. It converges the members
it has and reports the shortfall.

## Remove a member

Reverse the order: shrink the group first, then delete the machine.

### 1. Lower `count`, or remove the label

Lowering `count` drops the **highest-named** surplus members. If you need a
*specific* machine gone, remove its label instead — that takes it out of the
candidate set entirely:

```sh
kubectl label machine node-d --namespace infra role-
```

### 2. Verify it left the selection

```sh
kubectl get nixcluster prod --namespace infra --output jsonpath='{range .status.nodeGroups[*].members[*]}{.name}{" "}{end}{"\n"}'
```

`node-d` should be gone. Its generated `modules/nodes/node-d.nix` will no longer
be injected on the next converge.

### 3. Let one converge run

Give the converge a cycle to run without the member, so your flake's own teardown
logic (draining, removing it from quorum, revoking credentials) executes while
the machine is still reachable.

```sh
kubectl get nixcronjob prod-converge --namespace infra
```

:::danger NIO does not decommission cluster members
Removing a `Machine` from a `NixCluster` stops NIO managing it. It does **not**
run anything on that host. The host keeps its last-applied configuration and
keeps running. Whatever "leaving the cluster" means for your system has to be
implemented in your converge script and triggered before the machine stops being
a member.

`NixosConfiguration` has an `onRemoveFlake` for the single-host case.
`NixCluster` has **no** equivalent field.
:::

### 4. Delete the Machine

Only once the converge has run and the host is genuinely out.

## Group ordering matters

A `Machine` belongs to exactly one group, claimed by the **first** matching group
in spec order. Put specific groups before general ones:

```yaml
nodeGroups:
  - name: control-plane        # claims role=worker,tier=cp first
    selector:
      matchLabels:
        role: worker
        tier: cp
  - name: workers              # gets the rest
    selector:
      matchLabels:
        role: worker
```

Reversing these two puts every machine in `workers` and leaves `control-plane`
permanently empty — with no error, because that is a legal configuration.

## Why membership does not churn

With `count` set, selection is sticky: members already chosen and still matching
are kept, and only the shortfall is topped up or the excess dropped. Adding a
fifth candidate to a `count: 4` group does not reshuffle the existing four.

## Next

- [Concepts: clusters](../concepts/clusters.md).
- [Reference: NixCluster](../reference/nixcluster.md).
