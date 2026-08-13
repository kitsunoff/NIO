---
title: Store and builder
sidebar_position: 5
description: Why a NixStore alone is read-only, and what actually makes converges fast.
---

# Store and builder

This page exists because the relationship between `NixStore` and `NixBuilder` is
the thing people most reliably get wrong, and getting it wrong means your
converges stay slow for reasons that look like nothing is happening.

## A `NixStore` alone is a substituter

A [`NixStore`](../reference/nixstore.md) is a StatefulSet running a binary-cache
server backed by a PVC. Referencing it from a workload adds two lines to that
workload's Nix configuration:

```text
substituters = http://store.ns.svc:5000 https://cache.nixos.org
trusted-public-keys = ns-store-1:AbC123== cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
```

That is **read-only**. Paths the store already holds are fetched instead of
built. Paths your pod builds are **not** sent back.

:::danger `storeRef` on its own does not make anything faster the second time
If you set only `storeRef`, every converge rebuilds everything the store did not
already have, every time, in a pod whose `/nix` is discarded when it exits. The
store never accumulates your builds. This is the single most common
misconfiguration.
:::

`cache.nixos.org` and its public key are always appended, unconditionally, after
your store. You do not need to configure it and you cannot remove it.

:::note `spec.upstreamSubstituters` does nothing
`NixStore` has an `upstreamSubstituters` field for configuring what the store
server itself falls through to on a miss. **Nothing reads it.** The store
server's generated configuration contains no `substituters` line at all. The
fall-through to `cache.nixos.org` you observe happens on the *client* side, in
every workload pod, not in the store.
:::

## A push happens only with a builder

The build-and-push path is a single command in the `instantiate` init-container,
and it is only assembled when **both** of these hold:

- a builder is in play, so an SSH identity is mounted, **and**
- a store with a push endpoint is resolved

```sh
nix build .#thing && nix copy --to ssh-ng://root@store.ns.svc .#thing
```

So the causal chain is: **builder → push → store accumulates**. Without a
builder, the store is a read-only mirror of whatever you seeded it with.

## What actually makes day-two converges fast

Not the store. The **builder's own `/nix`** — and only if you give it storage.

A [`NixBuilder`](../reference/nixbuilder.md) is a StatefulSet with exactly one
worker. Its `spec.storage` is the volume claim template for its `/nix`. Leave it
unset and `/nix` is an `emptyDir`:

| `NixBuilder` | `/nix` | Second converge |
| --- | --- | --- |
| no `spec.storage` | `emptyDir`, dies with the pod | rebuilds everything |
| with `spec.storage` | PVC, survives restarts | reuses the build cache |

The builder is deliberately a single worker with no pool and no routing.
`spec.maxJobs` — how many builds that one worker runs in parallel — is the only
concurrency knob.

## Delegation has no local fallback

Setting `builderRef` writes `max-jobs = 0` into the pod's Nix configuration.
Zero local build slots. If the builder cannot build what you asked for, the
build **fails**; it does not fall back to building locally.

That is intentional. With even one local slot, Nix would prefer the local
machine, the build would never reach the builder, and the builder-to-store push
would never run — you would get the slow path silently.

### The architecture trap

Which systems a builder advertises comes from `spec.systems`. When it is
**empty**, the delegation line advertises `x86_64-linux,aarch64-linux`
regardless of what the builder can actually build.

So an unset `spec.systems` is not "auto-detect" — it is "claim both". Point an
`aarch64` builder at an `x86_64` member with `spec.systems` unset and the build
is dispatched, then fails on the builder.

`NixCluster` has a preflight for exactly this. It compares the builder's
declared `spec.systems` against its members' observed architectures, and on a
provable mismatch it sets phase `Blocked` with reason `BuilderSystemMismatch`
and **suspends the converge child** rather than letting the run burn its
deadline. The check is deliberately conservative and stays silent when it cannot
prove a mismatch — including when `spec.systems` is empty, or when a `Machine`
has not been scanned yet.

:::warning The preflight is `NixCluster`-only
There is **no** equivalent architecture pre-check on `NixosConfiguration` or on
the four workload kinds. They verify only that the referenced store and builder
exist and are Ready. A mismatch there surfaces as a failing build inside the
pod.
:::

## The two-key SSH problem

A store/builder-backed apply has **two** SSH destinations needing **different**
keys:

| Destination | Key | Mounted at |
| --- | --- | --- |
| builder and store | owned by the `NixStore`, generated automatically | `/etc/nio/ssh/ssh-privatekey` |
| the target host | your `Machine`'s `sshKeySecretRef` | `/etc/nio/target-ssh/ssh-privatekey` |

Nix exposes only one global `NIX_SSHOPTS`, which cannot carry a per-destination
identity. The resolution is that the builder key travels as the **third field of
the `builders=` machine spec**, so `ssh-ng` authenticates the build dispatch
itself, leaving `NIX_SSHOPTS` free to carry the target-host key.

The practical consequence: **the SSH identity for reaching a builder comes from
the store side, not the builder.** It is the keypair owned by the builder's own
`spec.storeRef`, falling back to the workload's `storeRef`. If neither the
builder nor the workload names a store, no key is wired at all and the dispatch
has no identity.

This is why [the how-to](../how-to/shared-store-and-builder.md) tells you to set
`storeRef` **and** `builderRef` together, never `builderRef` alone.

## Next

- [Build on a shared store and builder](../how-to/shared-store-and-builder.md).
- [Accelerate a converge](../how-to/accelerate-converge.md).
- [Design note: the two-key SSH conflict](../design/store-builder-ssh.md).
