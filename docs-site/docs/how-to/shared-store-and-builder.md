---
title: Build on a shared store and builder
sidebar_position: 1
description: Stand up a NixStore and NixBuilder and point workloads at them.
---

# Build on a shared store and builder

**Goal:** stop every pod rebuilding the world in its own throwaway `/nix`.

Read [Store and builder](../concepts/store-and-builder.md) first if you have not
— the short version is that a store alone will not do this, and a builder without
storage will not either.

## 1. Create the store

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixStore
metadata:
  name: store
  namespace: apps
spec:
  storage:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: 100Gi
```

`spec.storage` is required. Everything else has a default: one replica, the
`nixos/nix:latest` image, and a signing keypair generated for you.

Wait for it, and note what it publishes:

```sh
kubectl get nixstore store --namespace apps
```

```text
NAME    PHASE   SUBSTITUTER                     READY   AGE
store   Ready   http://store.apps.svc:5000      1       1m
```

The controller also generates two Secrets you did not create:

- `store-signing-key` — the Nix signing keypair. `status.publicKey` is its public
  half; that is the `trusted-public-keys` entry clients get.
- `store-ssh` — an ed25519 SSH keypair. **This is the identity builds use to
  reach the builder**, which is why the store must exist even when what you
  actually want is the builder.

:::caution `spec.storage` is immutable
It becomes the StatefulSet's `volumeClaimTemplate`, which Kubernetes will not let
you change after creation. Only `replicas` and `template` are updated on an
existing store. Size it with room to grow.
:::

## 2. Create the builder

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixBuilder
metadata:
  name: builder
  namespace: apps
spec:
  storeRef:
    name: store
  systems:
    - x86_64-linux
  maxJobs: 4
  storage:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: 200Gi
  template:
    spec:
      containers:
        - name: builder
          resources:
            requests:
              cpu: "4"
              memory: 8Gi
```

Three fields here are load-bearing and each is a documented trap:

- **`storage`** — without it the builder's `/nix` is an `emptyDir`. It will work
  and it will be slow forever. This is the field that makes the second build
  fast.
- **`systems`** — list what this builder can *actually* build. Leave it empty and
  the delegation advertises `x86_64-linux,aarch64-linux` regardless of the truth,
  and the `NixCluster` architecture preflight goes silent.
- **`storeRef`** — this is what wires the SSH keypair. A builder with no
  `storeRef` and a workload with no `storeRef` means no identity for the build
  dispatch.

```sh
kubectl get nixbuilder builder --namespace apps
```

```text
NAME      PHASE   ENDPOINT                          READY   AGE
builder   Ready   ssh-ng://root@builder.apps.svc    true    1m
```

## 3. Point a workload at both

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixDeployment
metadata:
  name: api
  namespace: apps
spec:
  nix:
    source:
      gitRepo: https://github.com/example/services.git
      ref: main
    run: ".#api"
    storeRef:
      name: store
    builderRef:
      name: builder
  deploymentTemplate:
    replicas: 3
```

**Always set both.** `builderRef` without `storeRef` leaves the build dispatch
without an SSH identity — unless the builder's own `storeRef` supplies it, which
is fragile to rely on. `storeRef` without `builderRef` gets you a read-only
substituter and no accumulation.

## 4. Confirm the delegation is real

The proof is in the rendered pod, not in the spec you wrote:

```sh
kubectl get pods --namespace apps --selector nio.homystack.com/workload-name=api \
  --output jsonpath='{.items[0].spec.initContainers[?(@.name=="instantiate")].env}'
```

You are looking for `NIX_CONFIG` containing all three of:

```text
builders = ssh-ng://root@builder.apps.svc x86_64-linux /etc/nio/ssh/ssh-privatekey
builders-use-substitutes = true
max-jobs = 0
```

If `builders` is absent, the builder did not resolve and you are building
locally. If `max-jobs = 0` is present but `builders` is not, nothing can build at
all.

You can also watch the push happen — the `instantiate` init runs
`nix build ... && nix copy --to ssh-ng://root@store.apps.svc ...`:

```sh
kubectl logs --namespace apps <pod> --container instantiate
```

## A dedicated builder instead of a shared one

`builderTemplate` creates a `NixBuilder` owned by the workload, instead of
referencing a shared one. It is mutually exclusive with `builderRef`, and its
`storeRef` defaults to the workload's.

```yaml
spec:
  nix:
    storeRef:
      name: store
    builderTemplate:
      systems: [x86_64-linux]
      storage:
        accessModes: [ReadWriteOnce]
        resources:
          requests:
            storage: 200Gi
```

:::warning A templated builder is created once and never updated
The owned builder is created on first reconcile and thereafter returned as-is.
Editing `builderTemplate` afterwards **does not** update the running builder. To
change it, delete the generated `<workload>-builder` object and let it be
recreated.
:::

## Next

- [Accelerate a converge](./accelerate-converge.md) — the same machinery applied
  to `NixosConfiguration` and `NixCluster`.
