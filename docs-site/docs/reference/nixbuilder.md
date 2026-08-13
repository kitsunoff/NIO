---
title: NixBuilder
sidebar_position: 6
description: A single-worker remote Nix build backend for a namespace.
---

# NixBuilder

A StatefulSet with **one** builder worker that runs `nix build`. Namespaced.
Short name `nbuilder`.

Deliberately a single worker: no pool, no routing, no balancing. `spec.maxJobs`
is the only concurrency knob.

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
```

## `spec`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `storeRef` | LocalObjectReference | no | workload's `storeRef` when created from `builderTemplate` | The `NixStore` this builder realizes into, and the source of its SSH identity. |
| `image` | string | no | `nixos/nix:latest` | Builder pod image. |
| `systems` | []string | no | — | Systems this builder can build. See the warning below. |
| `maxJobs` | integer | no | — | Parallel builds on the single worker. Sets `NIX_MAX_JOBS` in the pod. |
| `storage` | PersistentVolumeClaimSpec | no | — | The builder's own persistent `/nix`. **Set this.** |
| `template` | PodTemplateSpec | no | — | Overrides for the builder pod. Only `spec` is used. |

`replicas` is not configurable — it is hard-coded to 1.

### `storage` — the field that makes builds fast

:::danger Without `spec.storage` the builder's `/nix` is an `emptyDir`
| `spec.storage` | `/nix` | Second build |
| --- | --- | --- |
| unset | `emptyDir`, dies with the pod | rebuilds everything |
| set | PVC, survives restarts | reuses the cache |

This is the field that makes day-two converges fast. A builder without it works
correctly and is permanently slow. See
[Accelerate a converge](../how-to/accelerate-converge.md).
:::

Both cases use a volume named `nix-store`.

### `systems` — declare the truth

:::warning An empty `systems` claims both architectures
When `spec.systems` is **empty**, the delegation line written into consuming
pods advertises `x86_64-linux,aarch64-linux` regardless of what the builder can
actually build:

```text
builders = ssh-ng://root@builder.apps.svc x86_64-linux,aarch64-linux /etc/nio/ssh/ssh-privatekey
```

Unset does **not** mean "auto-detect". It means "claim both".

It also silences the [`NixCluster`](./nixcluster.md) architecture preflight,
which skips entirely when `systems` is empty — so the mismatch that would
otherwise have been caught up front becomes a failing build instead.

Always list what the builder can genuinely produce.
:::

### `maxJobs`

Sets the environment variable `NIX_MAX_JOBS` on the builder container. Note this
is the env var, not a `max-jobs` line in the builder's `nix.conf`.

### `template`

Only `template.spec` is used as the base pod spec. The operator overwrites the
`builder` container's image, command, args, ports, env and volume mounts, and
prepends a `bootstrap` init-container. Use it for resources, tolerations and
scheduling — a builder usually wants a large, dedicated node.

## What delegation writes into consumers

A workload with `builderRef` gets three settings in its `NIX_CONFIG`:

```text
builders = <endpoint> <systems> [<sshKeyPath>]
builders-use-substitutes = true
max-jobs = 0
```

:::danger `max-jobs = 0` means no local fallback
Zero local build slots. If the builder cannot build the requested system, the
build **fails**; it does not build locally more slowly.

This is deliberate. With a local slot available, Nix would prefer the local
machine, the build would never reach the builder, and the builder-to-store push
would never run — you would silently get the slow path.
:::

The third field of the `builders=` machine spec is the SSH key path, so `ssh-ng`
authenticates the dispatch itself and the global `NIX_SSHOPTS` stays free to
carry a target-host identity.

## The SSH identity comes from the store

The keypair used to reach a builder is owned by a **`NixStore`**, in a Secret
named `<store>-ssh`, mounted read-only at `/etc/nio/ssh` with the private key at
`/etc/nio/ssh/ssh-privatekey`.

Which store is resolved, in order:

1. the builder's own `spec.storeRef`
2. otherwise the consuming workload's `spec.nix.storeRef`

:::warning A builder with no store on either side has no identity
If neither the builder nor the workload names a store, no SSH key is wired at all
and the build dispatch has no identity. **Always set `storeRef` alongside
`builderRef`.**
:::

When `storeRef` is set, the builder's start-up additionally installs the store's
`ssh-authorized-key` so the dispatch is accepted.

Note that `storeRef` does **not** configure the builder to use the store as a
substituter or push target — no substituter configuration is emitted by this
controller. Pushing happens from the consuming workload's `instantiate` step.

## `status`

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | integer | Last reconciled generation. |
| `phase` | enum | `Pending`, `Ready`, `Degraded`. |
| `builderEndpoint` | string | `ssh-ng://root@<name>.<namespace>.svc`. No port — the service exposes 22. |
| `ready` | boolean | True when the single worker has at least one ready replica. |
| `conditions` | []Condition | `Ready`, `Reconciling`, `Stalled`. |

A workload referencing a builder that is not Ready **stalls** with reason
`InfraNotReady`.

## Dedicated builders

`spec.nix.builderTemplate` on a workload creates a `NixBuilder` named
`<workload>-builder`, owned by that workload, with `storeRef` defaulting to the
workload's. Mutually exclusive with `builderRef`.

:::warning A templated builder is created once and never updated
An existing owned builder is returned as-is; the template is not reapplied.
Editing `builderTemplate` afterwards has no effect. Delete
`<workload>-builder` to have it recreated.
:::

## Print columns

```text
NAME   PHASE   ENDPOINT   READY   AGE
```

*Verified against kitsunoff/NIO@e0dbe5e.*
