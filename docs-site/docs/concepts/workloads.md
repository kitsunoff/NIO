---
title: Workloads
sidebar_position: 3
description: The four Nix workload kinds, the compiler idea, and the anatomy of a generated pod.
---

# Workloads

## The operator is a compiler

NIO's four workload kinds take a flake installable plus a native Kubernetes
workload spec and emit the corresponding native object:

| NIO kind | Compiles to |
| --- | --- |
| [`NixJob`](../reference/nixjob.md) | `batch/v1 Job` |
| [`NixCronJob`](../reference/nixcronjob.md) | `batch/v1 CronJob` |
| [`NixDeployment`](../reference/nixdeployment.md) | `apps/v1 Deployment` |
| [`NixStatefulSet`](../reference/nixstatefulset.md) | `apps/v1 StatefulSet` |

**The operator never builds anything.** It does not run `nix` in its own process
and it does not create a build Job of its own. It writes a pod spec whose
init-containers run `nix build` and whose app container runs `nix run`. All the
Nix work happens inside pods you can `kubectl logs`.

This has a consequence worth stating plainly: NIO inherits Kubernetes' scheduling,
retry, backoff and RBAC rather than reimplementing them. A failing build is a
failing pod.

## The native template is verbatim

Each kind embeds the real upstream spec — `batchv1.JobSpec`, `appsv1.DeploymentSpec`
and so on — under a `<kind>Template` field, not a reduced subset. Anything you
can express in a `Deployment` you can express in a `NixDeployment`: affinity,
topology spread, sidecars, volumes, probes.

These embedded schemas are *why* six of the nine CRDs are over half a megabyte
and why the install
[requires a server-side apply](../getting-started/installation.md#--server-side-is-required-not-a-preference).

The template is **schemaless** in the CRD, which is what lets you omit fields
upstream marks required — `selector`, `template.spec.containers` — and have the
reconciler fill them in. A minimal `NixDeployment` really is just a `nix:` block.

### What the operator owns

Within your template, the operator takes ownership of a specific, bounded set of
things and overwrites them on every reconcile:

- the app container's `image` and `command` (which container is "the app" is
  `spec.nix.containerName`, default `app`; it is synthesized if absent)
- the init-containers it injects (see below)
- the `/nix` and workspace volumes and their mounts
- the pod-template revision annotation and the managed labels
- the `selector`, when you leave it unset

Everything else in the template is yours and is passed through untouched.

## Anatomy of a generated pod

Up to four init-containers are prepended, in this order:

1. **`bootstrap`** — seeds the pod-local `/nix` volume from the image's own store.
2. **`fetch-source`** — materializes the source at the resolved revision. With a
   git source it does a shallow `git init` / `fetch` / `checkout --detach` of the
   exact commit. With a Flux source it downloads and untars the artifact.
3. **`inject-files`** — present only when `spec.nix.additionalFiles` is set. It
   copies each file into the checkout and then runs `git add --force` on them, so
   files that are `.gitignore`d are still visible to a git-tree flake. This is
   **build-time** injection into the Nix source, not a runtime mount.
4. **`instantiate`** — runs `nix build` for `run` plus every `prebuild`
   installable. If a build fails, the init fails and the pod never starts.

The app container then runs `nix run <run> -- <args...>`.

The split matters: `run` and `prebuild` are **built**; `args` are **runtime data
that is never built**.

## Where things build

Three configurations, materially different:

| Setup | Where the build happens | Survives the pod? |
| --- | --- | --- |
| Neither `storeRef` nor `builderRef` | Inside each pod's own init-container | No. Dev fallback only. |
| `storeRef` alone | Still inside the pod, but cache hits are substituted from the store | The store is read-only here; nothing is pushed |
| `builderRef` (plus `storeRef`) | On the builder, over `ssh-ng` | Only if the `NixBuilder` has `spec.storage` |

Delegating with `builderRef` writes three Nix settings, one of which has teeth:

```text
builders = ssh-ng://root@builder.ns.svc x86_64-linux,aarch64-linux /etc/nio/ssh/ssh-privatekey
builders-use-substitutes = true
max-jobs = 0
```

`max-jobs = 0` means the pod has **zero local build slots**. There is no local
fallback. If the builder cannot produce the system you need, the build fails —
it does not quietly build locally and take longer. This is deliberate: with a
local slot available, Nix would build locally and the builder-to-store push
would never happen.

See [Store and builder](./store-and-builder.md) for the whole picture.

## Suspending

`spec.nix.suspend: true` stops reconciliation on all four kinds: the phase goes
`Suspended` and the controller returns before resolving or rendering anything.

The orchestrators use this mechanism themselves — a `NixosConfiguration` that
loses machine ownership suspends its day-two child, and a `NixCluster` with a
provable builder/architecture mismatch suspends its converge child.

## Next

- [Revisions and rollout](./revisions.md) — what triggers a new run.
- [Reference: the shared `nix` block](../reference/nixspec.md).
