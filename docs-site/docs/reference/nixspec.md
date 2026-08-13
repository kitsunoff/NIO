---
title: The nix block
sidebar_label: The nix block (NixSpec)
sidebar_position: 7
description: The spec.nix block shared by all four Nix workload kinds.
---

# The `nix` block

Every workload kind — [`NixJob`](./nixjob.md),
[`NixCronJob`](./nixcronjob.md), [`NixDeployment`](./nixdeployment.md),
[`NixStatefulSet`](./nixstatefulset.md) — has an identical required `spec.nix`
block. It is documented once here.

It holds everything Nix- and git-specific. The Kubernetes workload shape lives in
the sibling `<kind>Template` field, never here.

```yaml
spec:
  nix:
    source:
      gitRepo: https://github.com/example/services.git
      ref: main
    run: ".#api"
    args: ["--port", "8080"]
    storeRef:
      name: store
    builderRef:
      name: builder
```

## `nix`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `source` | [NixSource](#source) | yes | — | Flake origin and revision tracking. |
| `run` | string | no | `.` | The installable, exactly as typed after `nix run`. |
| `args` | []string | no | — | Arguments after `--`. Runtime data, **never built**. |
| `prebuild` | []string | no | — | Extra installables built before the app runs. |
| `image` | string | no | `nixos/nix:latest` | The nix-bearing runner image. |
| `containerName` | string | no | `app` | Which container in the template the operator owns. Synthesized if absent. |
| `nixFlags` | []string | no | — | Extra nix CLI flags for build and run. |
| `storeRef` | LocalObjectReference | no | — | A [`NixStore`](./nixstore.md) used as a substituter. |
| `builderRef` | LocalObjectReference | no | — | A [`NixBuilder`](./nixbuilder.md) to delegate builds to. Mutually exclusive with `builderTemplate`. |
| `builderTemplate` | NixBuilderSpec | no | — | A dedicated builder owned by this workload. Mutually exclusive with `builderRef`. |
| `localStore` | [NixLocalStore](#localstore) | no | — | Shape of the pod-local `/nix`. |
| `triggerOnChange` | boolean | no | per kind | Reaction to a new revision. **Ignored by two kinds** — see below. |
| `suspend` | boolean | no | `false` | Pause reconciliation. |
| `additionalFiles` | [][NixFile](#additionalfiles) | no | — | Build-time file injection. Max 64 items. |

### `run`, `args` and `prebuild`

The distinction is load-bearing:

- **`run`** is *built* and then executed: `nix run <run> -- <args...>`.
- **`prebuild`** entries are *built* alongside `run` in the `instantiate`
  init-container: `nix build <run> <prebuild...>`. If any fails, the init fails
  and the pod never starts.
- **`args`** are *never built*. They are runtime data passed after `--`.

`run` defaults to `.`, the source flake's default app. Both `run` and `prebuild`
entries may be local (`.#dep`) or external (`github:owner/repo#x`).

### `containerName`

Names the container in your `<kind>Template` that the operator **owns**: it sets
that container's `image` and `command`, and wires the local `/nix` and workspace
mounts. If no container of that name exists in your template, one is synthesized.

Every other container in your template is left alone.

## `source` {#source}

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `gitRepo` | string | no | — | Repository URL, https or ssh form. Max 2048 characters. Ignored when `fluxSourceRef` is set. |
| `ref` | string | no | `main` | Mutable branch or tag, re-resolved by polling. |
| `rev` | string | no | — | Exact commit SHA. Pattern `^[0-9a-f]{7,40}$`. When set, polling is disabled. |
| `dir` | string | no | — | Subdirectory holding the flake. Becomes the working directory. Must stay inside the checkout. |
| `credentialsRef` | SecretReference | no | — | Secret for private repository access. |
| `fluxSourceRef` | [FluxSourceRef](#fluxsourceref) | no | — | Consume a Flux source object instead of polling git. |
| `pollInterval` | duration | no | `1m` | How often `ref` is re-resolved. |

The three modes are mutually exclusive in effect, resolved in this order:

1. **`rev` set** — short-circuits everything. The SHA is used verbatim; no Flux
   lookup, no `ls-remote`.
2. **`fluxSourceRef` set** — reads `status.artifact` off the Flux object.
   `gitRepo`, `ref` and `credentialsRef` are ignored.
3. **otherwise** — `git ls-remote` resolves `ref` against `gitRepo`.

`credentialsRef` recognises `username` + `password`, or `ssh-privatekey`. It is
used both controller-side for `ls-remote` and mounted into the `fetch-source`
init at `/etc/nio/git-creds`.

:::note `pollInterval` is applied more broadly than its doc comment says
The field's own comment states it is ignored when `rev` or `fluxSourceRef` is
set. The controller in fact uses it as the **unconditional requeue cadence** in
all three modes. With a pinned `rev` this causes no git traffic — there is
nothing to poll — but it does set how often the object is reconciled.
:::

### `FluxSourceRef`

| Field | Type | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `kind` | enum | yes | — | `GitRepository`, `OCIRepository` or `Bucket`. |
| `name` | string | yes | — | The Flux object's name, same namespace. |
| `apiVersion` | string | no | `source.toolkit.fluxcd.io/v1` | Group/version of the Flux source. |

All three kinds expose the same `status.artifact` contract, so NIO treats them
uniformly and never inspects the type-specific spec. It reads
`status.artifact.revision` as the rollout key and `status.artifact.url` as the
tarball the pod fetches.

## `localStore` {#localstore}

Shapes the pod-local `/nix` the realized closure is substituted into. The content
is reproducible, so this volume does **not** need to survive a reschedule — the
choice is purely size and performance.

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `medium` | enum | `Disk` | `Disk`, `Memory` or `PodPVC`. |
| `sizeLimit` | quantity | — | Caps the emptyDir, or requests the size for `PodPVC`. |
| `storageClassName` | string | cluster default | **Only used when `medium: PodPVC`.** Ignored otherwise. |

| `medium` | Backing |
| --- | --- |
| `Disk` | `emptyDir` on node disk. Fast, node-local. |
| `Memory` | `emptyDir` on tmpfs. Fastest, RAM-bound; tiny closures only. |
| `PodPVC` | A per-pod generic ephemeral volume, deleted with the pod. |

## `additionalFiles` {#additionalfiles}

Files injected into the fetched source tree **before** the Nix build, and
force-staged with `git add --force` so a git-tree flake sees them even under
`.gitignore`.

This is **build-time injection into the Nix source**, not a runtime pod mount.
For runtime files, add volumes to the workload template directly.

Exactly one content source must be set per entry — enforced by a CEL validation
rule on the CRD, so violating it is rejected at admission.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `path` | string | yes | Destination relative to the checkout root. 1–4096 characters. No leading `/`, no `..` segment. |
| `inline` | string | no | Literal, non-sensitive content. |
| `configMapRef` | ConfigMapKeyReference (`name`, `key`) | no | Content from a ConfigMap key. |
| `secretRef` | SecretKeyReference (`name`, `key`) | no | Content from a Secret key, mounted mode `0400`. |

:::warning Never put secrets in `inline`
Use `secretRef`. Note though that inline content is **not** carried in the pod
spec — the operator materializes it into an owned ConfigMap named
`<workload>-nixfiles` and mounts that. It is still a ConfigMap, which is not a
secret store.
:::

## `triggerOnChange`

| Kind | Default | Effect |
| --- | --- | --- |
| `NixJob` | `true` | `false` suppresses creating a new run-Job when one already exists. |
| `NixCronJob` | `false` | `true` fires an immediate one-off Job on a new revision, in addition to the schedule. |
| `NixDeployment` | — | **Never read.** |
| `NixStatefulSet` | — | **Never read.** |

:::danger Two kinds ignore this field entirely
`NixDeployment` and `NixStatefulSet` never call the defaulting helper. Their pod
template is re-rendered and written on every reconcile, so a new revision always
rolls out. Setting `triggerOnChange: false` on them does nothing.

To hold those kinds still, pin `source.rev` or set `suspend: true`.
:::

There is no `+kubebuilder:default` marker on this field; the defaults above are
applied per call site in the controllers, so the field is genuinely absent from
the stored object unless you set it.

Note also that a `NixCronJob`'s projected job template is re-pinned to the new
revision **regardless** of this flag — the next scheduled tick runs the new
revision either way.

## `suspend`

`true` sets phase `Suspended` and returns before resolving or rendering. All four
kinds honour it.

The orchestrators use this mechanism themselves: a `NixosConfiguration` that
loses machine ownership suspends its day-two child, and a `NixCluster` with a
provable builder mismatch suspends its converge child.

## The generated pod

Up to four init-containers are prepended, in order. Any previous copies are
dropped first, so re-rendering is idempotent.

| Init-container | Does |
| --- | --- |
| `bootstrap` | Seeds the pod-local `/nix` from the image's own store. |
| `fetch-source` | Git mode: `git init`, `remote add`, `fetch --depth 1 <revision>`, `checkout --detach FETCH_HEAD`. Flux mode: downloads and untars the artifact. |
| `inject-files` | Only when `additionalFiles` is set. Copies each file in, then `git add --force`. |
| `instantiate` | `nix build <nixFlags...> <run> <prebuild...>` in the working directory. Optionally SSH-wrapped, optionally followed by the store push. |

The app container then runs `nix run <nixFlags...> <run> -- <args...>`.

### Nix configuration written into the pod

With a store:

```text
substituters = http://store.ns.svc:5000 https://cache.nixos.org
trusted-public-keys = ns-store-1:AbC123== cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
```

`cache.nixos.org` and its key are always appended, after your store,
unconditionally. This is not configurable.

With a builder, three more settings:

```text
builders = ssh-ng://root@builder.ns.svc x86_64-linux /etc/nio/ssh/ssh-privatekey
builders-use-substitutes = true
max-jobs = 0
```

### When a push into the store happens

Only when **both** an SSH identity is mounted (i.e. a builder is in play) **and**
a store push endpoint resolved:

```sh
nix build .#thing && nix copy --to ssh-ng://root@store.ns.svc .#thing
```

With `storeRef` alone and no builder, **nothing is ever pushed**.

## The composite revision

The pod-template label and annotation `nio.homystack.com/revision` carry a
SHA-256 over exactly three length-prefixed inputs — the resolved revision,
`run`, and each element of `args` — truncated to `r-` plus 12 hex characters.

:::warning Most of the `nix` block is not in the hash
`prebuild`, `nixFlags`, `image`, `containerName`, `additionalFiles` content,
`source.dir`, `localStore` and the resolved store/builder endpoints are **not**
hashed.

For `NixDeployment` and `NixStatefulSet` this rarely matters — the template is
rewritten each reconcile and a real diff still triggers a rollout.

For **`NixJob` it does matter**: the run-Job's name derives from this hash and an
existing Job is never mutated, so changing only `prebuild` or `nixFlags` creates
no new Job and your change does not run.
:::

*Verified against kitsunoff/NIO@e0dbe5e.*
