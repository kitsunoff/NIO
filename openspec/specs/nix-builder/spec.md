# Nix Builder Specification

## Purpose

Describes the `NixBuilder` kind: a namespace-scoped, single-worker remote build
backend that workloads delegate `nix build` to, so closures are realized once on the
builder instead of in every pod.

## Requirements

### Requirement: A builder is one worker, deliberately

A `NixBuilder` SHALL manage a StatefulSet with exactly one worker replica and a
headless Service exposing its SSH port. There is no pool, no routing and no load
balancing; `spec.maxJobs` is the only concurrency knob.

#### Scenario: Builder is created

- **WHEN** a `NixBuilder` is created
- **THEN** a single-replica StatefulSet and a headless Service are created and owned
  by it

#### Scenario: Parallelism is configured on the worker

- **WHEN** `spec.maxJobs` is set
- **THEN** the worker runs that many builds in parallel, and no additional replicas
  are created

### Requirement: Builder endpoint is published

The builder SHALL publish the remote-build endpoint that dispatching pods use, and
report readiness from its worker.

#### Scenario: Endpoint in status

- **WHEN** a `NixBuilder` is reconciled
- **THEN** `status.builderEndpoint` is the SSH-based endpoint of its Service

#### Scenario: Worker ready

- **WHEN** the worker replica is ready
- **THEN** `status.ready` is `true`, phase is `Ready`, `Ready` is `True` with reason
  `BuilderReady`, and `Stalled` is removed

#### Scenario: Worker not ready

- **WHEN** the worker replica is not ready
- **THEN** `status.ready` is `false`, phase is `Pending`, and `Ready` is `False` with
  reason `BuilderNotReady`

#### Scenario: Managed object fails

- **WHEN** the Service or StatefulSet cannot be reconciled
- **THEN** phase is `Degraded`, `status.ready` is `false`, `Stalled` is `True` naming
  the failing object, and the error is returned so the builder is retried

### Requirement: Builder storage decides whether the cache survives

`spec.storage` SHALL back the builder's own `/nix` with a persistent volume claim
template. Without it, `/nix` is a pod-local ephemeral volume, so the build cache dies
with the pod.

#### Scenario: Persistent builder

- **WHEN** `spec.storage` is set
- **THEN** the builder's `/nix` is a persistent volume that survives restarts

#### Scenario: Ephemeral builder

- **WHEN** `spec.storage` is unset
- **THEN** the builder's `/nix` is an `emptyDir` and nothing it builds outlives the pod

### Requirement: Dispatch is authorized by the store's key pair

When `spec.storeRef` is set, the builder SHALL mount that store's SSH key pair Secret
and authorize its public half, so pods holding the private half can dispatch builds.

#### Scenario: Store-backed builder

- **WHEN** `spec.storeRef` names a `NixStore`
- **THEN** the store's authorized key is installed on the builder's SSH server

#### Scenario: Builder without a store reference

- **WHEN** neither the builder nor the dispatching workload names a store
- **THEN** no shared key is mounted and the build dispatch is unauthenticated

### Requirement: Advertised systems

`spec.systems` SHALL declare which systems the builder can build. An unqualified
builder is advertised for both common Linux architectures, because the in-cluster
worker matches the runner pods' architecture.

#### Scenario: Explicit systems

- **WHEN** `spec.systems` lists one system
- **THEN** dispatching workloads advertise the builder for exactly that system, and
  the cluster preflight can prove a mismatch against it

#### Scenario: No systems declared

- **WHEN** `spec.systems` is empty
- **THEN** the builder is advertised for both common Linux architectures and no
  mismatch is provable

### Requirement: Worker pod composition

The worker pod SHALL seed nix into its `/nix` volume before starting and run an SSH
server that accepts remote builds, trusting root for the build daemon.

#### Scenario: Volume seeding

- **WHEN** the worker starts with an empty `/nix` volume
- **THEN** a bootstrap init copies the image's nix into it so the daemon binary is not
  shadowed by the mount

#### Scenario: Pod overrides are honoured

- **WHEN** `spec.template` requests large resources or tolerations
- **THEN** they are applied while the operator keeps ownership of the worker
  container's image, command, ports, environment and mounts

### Requirement: Builds are realized into the builder's store

Builds dispatched to a builder SHALL be realized in the builder's own `/nix`. Pushing
those results into a shared `NixStore` is performed by the dispatching pod, not by the
builder.

#### Scenario: Delegated build result

- **WHEN** a workload with a store and a builder builds an installable
- **THEN** the closure is realized on the builder and the dispatching pod copies it
  into the store
