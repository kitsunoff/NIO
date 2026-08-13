---
title: NixStore
sidebar_position: 5
description: A centralized Nix binary-cache server for a namespace.
---

# NixStore

A StatefulSet running a Nix binary-cache server backed by a PVC. Namespaced.
Short name `nstore`.

Consumable only by workloads in the **same namespace**.

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

:::danger A `NixStore` on its own is read-only
Referencing a store from a workload adds it to `substituters` and its public key
to `trusted-public-keys`. Paths it already holds are fetched instead of built.
**Nothing is ever pushed into it** unless a [`NixBuilder`](./nixbuilder.md) is
also in play. See [Store and builder](../concepts/store-and-builder.md).
:::

## `spec`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `storage` | PersistentVolumeClaimSpec | **yes** | — | The volume claim template backing the real `/nix`. |
| `replicas` | integer | no | `1` | Store-server StatefulSet replicas. |
| `image` | string | no | `nixos/nix:latest` | Store server image. |
| `signingKeySecretRef` | SecretReference | no | — | Existing signing keypair. If absent, one is generated. |
| `upstreamSubstituters` | []string | no | — | **Nothing reads this.** See below. |
| `template` | PodTemplateSpec | no | — | Overrides for the server pods. Only `spec` is used. |

### `storage`

Required, and it becomes the StatefulSet's `volumeClaimTemplate`.

:::caution Immutable after creation
Kubernetes does not permit changing a StatefulSet's `volumeClaimTemplates`. Only
`replicas` and `template` are updated on an existing store. Size it with room to
grow.
:::

The volume is named `nix-store` and is mounted at `/nix` in the store container,
and at `/nix-vol` in a `bootstrap` init-container that seeds it from the image.

### `upstreamSubstituters`

:::danger Dead field
Nothing reads `spec.upstreamSubstituters`. The generated `/etc/nix/nix.conf` for
the store server contains only `experimental-features`, `trusted-users` and
`require-sigs` — **there is no `substituters` line at all**. The store server has
no upstream configured.

The fall-through to `cache.nixos.org` that you do observe happens on the
**client** side: every workload pod's Nix configuration appends
`https://cache.nixos.org` and its well-known public key unconditionally, after
your store. That behaviour is not configurable and does not come from this
field.
:::

### `image`

Defaults to `nixos/nix:latest` — the generic Nix image, not a purpose-built cache
image. The cache server itself is run from inside it via
`nix run nixpkgs#harmonia`.

### `template`

Only `template.spec` is used as the base pod spec; template metadata and labels
are discarded and replaced with the operator's managed labels.

The operator then force-overwrites the `store` container's `image`, `command`,
`args`, `ports`, `env` and `volumeMounts`, and prepends its own `bootstrap`
init-container. Use `template` for resources, scheduling, tolerations and node
selection.

## Signing keys

If `signingKeySecretRef` is **set**, the Secret must contain a key named
`nix-public-key`.

If it is **unset**, the controller generates an ed25519 keypair once into an
owned Secret named `<store-name>-signing-key`, with data keys:

| Data key | Content |
| --- | --- |
| `nix-signing-key` | `<name>:<base64 private key>` |
| `nix-public-key` | `<name>:<base64 public key>` |

The key name is `<namespace>-<name>-1`. The public half is published as
`status.publicKey`, and that is the `trusted-public-keys` entry clients receive.

## The SSH keypair

The controller also creates a **second** Secret, `<store-name>-ssh`, holding a
generated ed25519 SSH keypair (`ssh-privatekey`, `ssh-authorized-key`).

This is the identity workloads use to **reach a builder**. It is why a builder
without any store on either side has no SSH identity for its build dispatch, and
why you should set `storeRef` alongside `builderRef`. See
[the two-key SSH problem](../concepts/store-and-builder.md#the-two-key-ssh-problem).

## `status`

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | integer | Last reconciled generation. |
| `phase` | enum | `Pending`, `Ready`, `Degraded`. |
| `substituterURL` | string | `http://<name>.<namespace>.svc:5000` — the read endpoint clients pass to `--substituters`. |
| `storeURI` | string | `ssh-ng://root@<name>.<namespace>.svc`. **Written, never read.** |
| `publicKey` | string | The `trusted-public-keys` entry clients must trust. |
| `readyReplicas` | integer | Ready replicas of the StatefulSet. |
| `conditions` | []Condition | `Ready`, `Reconciling`, `Stalled`. |

:::note `status.storeURI` is unused
It is written on every reconcile and read by nothing. The push endpoint used by
workloads is derived independently, with the same format string, at the point of
use. The value is correct — it is simply not the field anything consumes.
:::

A workload referencing a store that is not `Ready`, or that has no
`substituterURL`, **stalls** with reason `InfraNotReady` rather than silently
building without it.

## Print columns

```text
NAME   PHASE   SUBSTITUTER   READY   AGE
```

*Verified against kitsunoff/NIO@e0dbe5e.*
