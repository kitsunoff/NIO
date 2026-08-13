---
title: Accelerate a converge
sidebar_position: 2
description: Make day-two converges fast with storeRef and builderRef, and verify it worked.
---

# Accelerate a converge

**Symptom:** the day-two `nixos-rebuild` takes as long on the hundredth run as on
the first.

**Cause:** by default every run builds inside a pod whose `/nix` is an `emptyDir`.
The pod exits, the store is discarded, the next run starts from nothing.

**Fix:** give the converge a builder whose `/nix` persists.

## The two fields

Both `NixosConfiguration` and `NixCluster` take `storeRef` and `builderRef` at
the top level of their spec and pass them straight through to their children.

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
  storeRef:
    name: store
  builderRef:
    name: builder
```

For a `NixCluster` the fields sit in the same place:

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
  storeRef:
    name: store
  builderRef:
    name: builder
  nodeGroups:
    - name: workers
      selector:
        matchLabels:
          role: worker
```

For creating the store and builder themselves, see
[Build on a shared store and builder](./shared-store-and-builder.md).

## Getting this wrong in the three available ways

### 1. `storeRef` only

Nothing gets faster on the second run. The store is a **substituter**: your pod
reads from it, and nothing is written back. If the store was empty it stays
empty.

This configuration is still useful — it is how you consume a store somebody else
populated — but on its own it does not accelerate anything.

### 2. `builderRef` on a builder with no `spec.storage`

The build moves to the builder, which is architecturally correct, and the
builder's `/nix` is an `emptyDir` that dies with its pod. You have moved the slow
build, not eliminated it.

```sh
kubectl get nixbuilder builder --namespace infra \
  --output jsonpath='{.spec.storage.resources.requests.storage}{"\n"}'
```

Empty output means you have this problem.

### 3. `builderRef` without `storeRef`

The SSH identity used to reach the builder is owned by the **store**, not the
builder. With no store named on either object, no key is wired and the dispatch
has no identity.

Set both. Always.

## Verify it actually worked

Do not trust the spec. Check the rendered child and then check the clock.

**Is delegation configured?** The orchestrator's child carries the refs:

```sh
kubectl get nixcronjob node-a-day2 --namespace infra \
  --output jsonpath='{.spec.nix.builderRef.name}{" / "}{.spec.nix.storeRef.name}{"\n"}'
```

**Is `max-jobs = 0` in the pod?** This is the definitive check that the pod
cannot build locally:

```sh
kubectl get job --namespace infra --selector nio.homystack.com/workload-name=node-a-day2 \
  --output jsonpath='{.items[-1:].spec.template.spec.initContainers[?(@.name=="instantiate")].env[?(@.name=="NIX_CONFIG")].value}'
```

**Did the second run get faster?** Compare two consecutive runs:

```sh
kubectl get jobs --namespace infra --selector nio.homystack.com/workload-name=node-a-day2 \
  --sort-by=.status.startTime \
  --output custom-columns='NAME:.metadata.name,START:.status.startTime,END:.status.completionTime'
```

If run two is not materially shorter than run one, the builder is not retaining
anything — go back and check `spec.storage`.

## The architecture trap

Delegation sets `max-jobs = 0`, so there is **no local fallback**. If the builder
cannot build the target's system, the converge fails rather than merely slowing.

Make `spec.systems` on the builder tell the truth. An empty `spec.systems`
advertises `x86_64-linux,aarch64-linux` whether or not the builder can deliver
both.

For a `NixCluster`, a provable mismatch is caught before the run: the cluster goes
`Blocked` with reason `BuilderSystemMismatch` and the converge child is
suspended.

```sh
kubectl get nixcluster prod --namespace infra \
  --output jsonpath='{.status.phase}{" "}{.status.conditions[?(@.type=="Ready")].reason}{"\n"}'
```

```text
Blocked BuilderSystemMismatch
```

The message names both sides:

```text
NixBuilder "builder" builds only [aarch64-linux], but ... ; converge delegates
every build (max-jobs = 0) so there is no local fallback
```

Fix it by adding the missing system to the builder's `spec.systems` (and making
sure the builder can genuinely produce it — declaring it does not make it true),
or by removing `builderRef`.

:::note This preflight is `NixCluster`-only
`NixosConfiguration` has no architecture pre-check. A mismatch there shows up as
a failing build in the child's pod logs, not as a `Blocked` parent.
:::

## Next

- [Troubleshoot a failed converge](./troubleshoot-converge.md).
