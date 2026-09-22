# Nix Store Specification

## Purpose

Describes the `NixStore` kind: a namespace-scoped Nix binary cache server backed by
persistent storage, which workloads substitute from over HTTP and push built paths
into over SSH.

## Requirements

### Requirement: A store is a persistent binary cache server

A `NixStore` SHALL manage a StatefulSet serving a Nix binary cache from a
PVC-backed `/nix`, fronted by a headless Service. Storage is required; replicas
default to one; the server image defaults to an operator-provided nix-bearing image.

#### Scenario: Store is created

- **WHEN** a `NixStore` with a storage claim is created
- **THEN** a StatefulSet, a headless Service and the store's Secrets are created and
  owned by it

#### Scenario: Storage is mandatory

- **WHEN** a `NixStore` is submitted without `spec.storage`
- **THEN** the API server rejects it

#### Scenario: Pod overrides are honoured

- **WHEN** `spec.template` sets resources or scheduling constraints
- **THEN** they are applied to the server pods while the operator keeps ownership of
  the server container's image, command, ports, environment and mounts

### Requirement: Store endpoints are published

The store SHALL publish the read endpoint clients pass as a substituter, the
realize/push endpoint, and the public key clients must trust.

#### Scenario: Endpoints in status

- **WHEN** a `NixStore` is reconciled
- **THEN** `status.substituterURL` is the in-cluster HTTP URL of its Service,
  `status.storeURI` is the SSH-based push endpoint, and `status.publicKey` carries the
  trusted public key

### Requirement: Signing key management

The store SHALL sign the paths it serves. When `spec.signingKeySecretRef` is set that
Secret is used and must carry the public key; otherwise an owned Secret with a freshly
generated key pair is created once and reused.

#### Scenario: Generated key

- **WHEN** no signing key Secret is referenced
- **THEN** a Secret named `<store>-signing-key` is created once, holding the secret and
  public halves in Nix binary-cache key format, and its public key is published

#### Scenario: Externally provided key

- **WHEN** `spec.signingKeySecretRef` names a Secret
- **THEN** that Secret is used and its public key is published

#### Scenario: Provided key Secret is incomplete

- **WHEN** the referenced Secret carries no public key entry
- **THEN** the store is `Degraded` with `Stalled` `True` and reason `SigningKeyError`

#### Scenario: Concurrent creation

- **WHEN** two reconciles race to create the generated Secret
- **THEN** the loser re-reads the existing Secret instead of failing

### Requirement: Remote-build key pair

Each store SHALL own an SSH key pair Secret named `<store>-ssh`, generated once. Its
private half lets runner pods dispatch builds and push into the store; its public half
authorizes those connections on the store's and the builder's SSH server.

#### Scenario: Key pair is created

- **WHEN** a `NixStore` is first reconciled
- **THEN** a Secret holding an OpenSSH private key and the matching authorized-key line
  is created and owned by the store

#### Scenario: Key pair is stable

- **WHEN** the store is reconciled again
- **THEN** the existing key pair is reused, so already-authorized clients keep working

### Requirement: Server pod composition

The server pod SHALL seed nix into the PVC-backed `/nix` before starting, accept
pushes from the shared key as a trusted user, and serve the store over HTTP with the
mounted signing key.

#### Scenario: Volume seeding

- **WHEN** the server pod starts with an empty persistent volume
- **THEN** a bootstrap init copies the image's nix into it, so the store's own tooling
  is not shadowed by the mount

#### Scenario: Pushes are accepted

- **WHEN** a runner pod or builder pushes a path using the shared key
- **THEN** the store accepts it as a trusted user, including paths it cannot verify a
  signature for

#### Scenario: Cache is served

- **WHEN** the server is running
- **THEN** it serves the binary cache over HTTP on the published port and signs served
  paths with the mounted key

### Requirement: Store readiness

`status.phase` SHALL be `Ready` only when the requested number of server replicas is
ready; it is `Pending` while they are not and `Degraded` when a managed object cannot
be reconciled.

#### Scenario: Server becomes ready

- **WHEN** the requested replicas report ready
- **THEN** phase is `Ready`, `Ready` is `True` with reason `StoreReady`, and `Stalled`
  is removed

#### Scenario: Server not ready yet

- **WHEN** fewer replicas are ready than requested
- **THEN** phase is `Pending` and `Ready` is `False` with reason `StoreNotReady` naming
  the counts

#### Scenario: Managed object fails

- **WHEN** the Service or StatefulSet cannot be reconciled
- **THEN** phase is `Degraded`, `Stalled` is `True` with a reason naming the failing
  object, and the error is returned so the store is retried

### Requirement: Immutable StatefulSet fields are not rewritten

On update the operator SHALL change only the mutable parts of the server StatefulSet,
leaving the volume claim templates and the selector alone.

#### Scenario: Replicas or image changed

- **WHEN** `spec.replicas` or `spec.image` changes
- **THEN** the existing StatefulSet's replica count and pod template are updated while
  its claim templates stay as created

### Requirement: Consumption is namespace-local

A `NixStore` SHALL be consumable only by objects in its own namespace.

#### Scenario: Reference from another namespace

- **WHEN** a workload in a different namespace references the store by name
- **THEN** it resolves against its own namespace and stalls because no such store
  exists there
