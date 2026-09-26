# Workload Pod Rendering Specification

## Purpose

Describes how NIO turns a flake installable into a runnable pod: the revision key
that drives rollouts, the init-container pipeline that fetches and builds the source,
the Nix configuration that wires a store and a builder, and the file injection and
storage choices around them.

## Requirements

### Requirement: Composite revision key

Every rendered pod template SHALL carry a revision key derived from the resolved
revision, the installable and the arguments, published as a managed label and a pod
template annotation. Changing any of those three inputs changes the key and therefore
rolls the native workload.

#### Scenario: New commit rolls the workload

- **WHEN** the resolved revision changes
- **THEN** the revision key changes and the native workload's pod template is updated

#### Scenario: Changed arguments roll the workload

- **WHEN** `nix.args` changes while the revision stays the same
- **THEN** the revision key changes

#### Scenario: Distinct inputs cannot collide

- **WHEN** two workloads differ only in how their installable and arguments split the
  same characters
- **THEN** their revision keys differ, because the hashed fields are length-prefixed

### Requirement: Operator-owned pod contents

Rendering SHALL stamp only the parts NIO owns onto the user's pod template — the
init-containers, the app container's image, command, working directory, Nix
configuration and mounts, the nix and workspace volumes, the managed labels and the
revision annotation — and SHALL preserve everything else the user provided, such as
sidecars, probes, resources and scheduling. Re-rendering the same template SHALL be
idempotent.

#### Scenario: User fields survive rendering

- **WHEN** the template declares a sidecar container, tolerations and resource
  requests
- **THEN** all of them are present in the rendered template

#### Scenario: Re-render does not duplicate init-containers

- **WHEN** an already-rendered template is rendered again
- **THEN** each NIO init-container appears exactly once, at the front, in order

#### Scenario: App container is synthesized when absent

- **WHEN** the template declares no container named by `nix.containerName` (default
  `app`)
- **THEN** that container is created by the operator

### Requirement: Init-container pipeline

Rendered pods SHALL run, in order, a bootstrap init that seeds the pod-local `/nix`,
a fetch-source init that materialises the exact resolved revision into the workspace,
an optional inject-files init, and an instantiate init that builds the installable
before the app container starts.

#### Scenario: Direct git source

- **WHEN** the source is a git repository
- **THEN** fetch-source initialises a repository in the workspace and fetches exactly
  the resolved commit at depth 1, checking it out detached

#### Scenario: Flux artifact source

- **WHEN** the source is a Flux artifact
- **THEN** fetch-source downloads and unpacks the artifact tarball and synthesizes a
  git tree, so the checkout is a hermetic flake input

#### Scenario: Build happens before the app runs

- **WHEN** the instantiate init runs
- **THEN** it builds the installable together with every `nix.prebuild` entry, and a
  build failure keeps the pod from starting

#### Scenario: Flake in a subdirectory

- **WHEN** `source.dir` is set
- **THEN** both the instantiate init and the app container work in that subdirectory
  of the checkout, so a relative installable resolves against it

### Requirement: App container command

The app container SHALL run the installable with `nix run`, passing `nix.nixFlags`
before the installable and `nix.args` after the `--` separator. Its image defaults to
the upstream nix image unless `nix.image` is set.

#### Scenario: Installable with arguments

- **WHEN** `nix.run` is `.#web` and `nix.args` is `["--port", "8080"]`
- **THEN** the app container runs `nix run .#web -- --port 8080`

#### Scenario: Default installable

- **WHEN** `nix.run` is unset
- **THEN** the source flake's default app `.` is run

### Requirement: Nix configuration wiring

The generated `NIX_CONFIG` SHALL enable flakes and the nix command, list the
referenced `NixStore` as a substituter with its public key trusted, and always keep
`cache.nixos.org` as a trusted fallback. When a builder is in play it SHALL also
declare that builder as a build machine, enable substitutes on the builder, and set
`max-jobs = 0` so builds cannot silently fall back to the pod.

#### Scenario: Store only

- **WHEN** `nix.storeRef` is set and no builder is used
- **THEN** the store's substituter URL and public key precede the public cache entry,
  and no build machine is declared

#### Scenario: Builder in play

- **WHEN** a builder is resolved
- **THEN** the builder endpoint is declared with its systems, substitutes on the
  builder are enabled, and local jobs are disabled

#### Scenario: Unqualified builder

- **WHEN** the resolved builder declares no `spec.systems`
- **THEN** it is advertised for both common Linux architectures

#### Scenario: No store and no builder

- **WHEN** neither reference is set
- **THEN** build and run are ephemeral and pod-local, substituting only from the
  public cache

### Requirement: Build dispatch identity

When a builder is used, the SSH keypair owned by the relevant `NixStore` SHALL be
mounted into the build containers and carried in the build-machine entry, so the
dispatch authenticates itself. The app container's `NIX_SSHOPTS` SHALL NOT be
overwritten when the caller already set it, so a caller-injected target-host identity
survives.

#### Scenario: Caller injected a target identity

- **WHEN** the app container already declares `NIX_SSHOPTS` — for example the
  host-convergence orchestrator's target-host key
- **THEN** that value is left untouched and the build dispatch still authenticates
  through the build-machine entry

#### Scenario: Builder present and no caller identity

- **WHEN** a builder is used and the caller set no `NIX_SSHOPTS`
- **THEN** the app container receives permissive host-key options only, carrying no
  identity

#### Scenario: SSH is available when needed

- **WHEN** a container has to reach a builder or a target host over SSH
- **THEN** its command is wrapped so an SSH client is on `PATH`, because the nix image
  ships none

### Requirement: Built closure is pushed to the shared store

When both a store and a builder are in play, the instantiate init SHALL push the
paths it built into the store after a successful build, so other pods substitute them
instead of rebuilding.

#### Scenario: Delegated build is shared

- **WHEN** a workload with both `storeRef` and a builder builds its installable
- **THEN** the installable and every prebuild entry are copied into the store

### Requirement: Referenced infrastructure gates the rollout

A workload SHALL NOT advance its rollout while a referenced `NixStore` or
`NixBuilder` is absent or not ready. It reports the unresolved reference and retries
shortly.

#### Scenario: Store missing

- **WHEN** `nix.storeRef` names a `NixStore` that does not exist
- **THEN** the workload is `Degraded` with `Stalled` `True`, reason `InfraNotReady`,
  and a message naming the store

#### Scenario: Store not ready

- **WHEN** the referenced store is not in phase `Ready` or publishes no substituter
  URL
- **THEN** the workload stalls with reason `InfraNotReady`

#### Scenario: Builder not ready

- **WHEN** the resolved builder is not ready or publishes no endpoint
- **THEN** the workload stalls with reason `InfraNotReady`

### Requirement: Dedicated builder from a template

`nix.builderTemplate` SHALL materialise a `NixBuilder` owned by the workload, created
once and defaulting its store reference to the workload's own. It is mutually
exclusive with `nix.builderRef`.

#### Scenario: Owned builder is created once

- **WHEN** a workload declares `builderTemplate`
- **THEN** a `NixBuilder` named `<workload>-builder` is created and owned by the
  workload, and subsequent reconciles do not recreate it

#### Scenario: Store inherited by the owned builder

- **WHEN** the template sets no store reference and the workload sets one
- **THEN** the created builder realizes into the workload's store

### Requirement: Build-time file injection

`nix.additionalFiles` SHALL be written into the fetched source tree before the build
and force-staged, so a git-tree flake input includes them even when ignored. Exactly
one content source per file — inline, ConfigMap key or Secret key — is required.
Inline content is materialised into an operator-owned ConfigMap rather than baked
into every pod spec.

#### Scenario: Inline file

- **WHEN** a file declares inline content
- **THEN** it is stored in an owned ConfigMap named `<workload>-nixfiles` and copied
  into the checkout from there

#### Scenario: Ignored path is still visible to Nix

- **WHEN** an injected file's path matches a `.gitignore` rule in the source
- **THEN** it is force-staged so the Nix build sees it

#### Scenario: Multiple content sources

- **WHEN** a file declares both inline content and a Secret reference
- **THEN** the API server rejects the object

#### Scenario: Inline content removed

- **WHEN** the last inline file is removed from a workload
- **THEN** the owned ConfigMap is deleted

### Requirement: Injected paths are validated

An injected file destination, and `source.dir`, SHALL be a relative path inside the
checkout, built only from letters, digits, `.`, `_`, `-` and `/`. A violation stalls
the workload rather than reaching a pod.

#### Scenario: Absolute path

- **WHEN** an additional file targets an absolute path
- **THEN** the workload stalls with a message naming the path

#### Scenario: Traversal

- **WHEN** a path escapes the source tree through `..`
- **THEN** the workload stalls with a message naming the path

#### Scenario: Shell metacharacter

- **WHEN** a path contains a character outside the allowed set
- **THEN** the workload stalls with a message naming the offending character

### Requirement: Pod-local store volume

The pod-local `/nix` that the realized closure is substituted into SHALL follow
`nix.localStore.medium`: a node-disk `emptyDir` by default, a memory-backed
`emptyDir`, or a per-pod ephemeral volume. Its size limit and storage class are
honoured.

#### Scenario: Default medium

- **WHEN** `localStore` is unset
- **THEN** `/nix` is a node-local `emptyDir`

#### Scenario: Memory medium

- **WHEN** `medium` is `Memory`
- **THEN** `/nix` is a memory-backed `emptyDir` bounded by the size limit

#### Scenario: Per-pod volume

- **WHEN** `medium` is `PodPVC`
- **THEN** `/nix` is a generic ephemeral volume requesting the given size from the
  named storage class, deleted with the pod

### Requirement: Managed labels identify generated pods

Every rendered pod template SHALL carry labels naming the owning workload kind and
name, the revision key, and NIO as the manager, so pods of a specific revision can be
found.

#### Scenario: New-revision pods are identifiable

- **WHEN** a rollout to a new revision starts
- **THEN** its pods are selectable by workload kind, workload name and revision key
