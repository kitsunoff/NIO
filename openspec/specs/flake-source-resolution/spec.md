# Flake Source Resolution Specification

## Purpose

Describes how every NIO workload turns a `nix.source` into one immutable revision to
build and run: pinned commits, Flux artifacts, polled git refs, private-repository
credentials, and the watches that make a source change take effect promptly.

## Requirements

### Requirement: Resolution priority

A source SHALL be resolved to an immutable revision in a fixed priority order: an
explicit `rev`, otherwise a referenced Flux source's published artifact, otherwise
the commit that `ref` currently points to on the remote.

#### Scenario: Pinned revision wins

- **WHEN** `source.rev` is set
- **THEN** it is used verbatim, no remote is contacted, and `pollInterval` is
  irrelevant

#### Scenario: Flux source outranks git polling

- **WHEN** `source.fluxSourceRef` is set alongside `gitRepo` and `ref`
- **THEN** the Flux artifact is used and `gitRepo`, `ref`, `pollInterval` and
  `credentialsRef` are ignored

#### Scenario: Ref defaults to main

- **WHEN** neither `rev`, `fluxSourceRef` nor `ref` is set
- **THEN** the ref `main` is resolved against the remote

#### Scenario: Pinned revision must look like a commit

- **WHEN** `source.rev` is not 7 to 40 lowercase hex characters
- **THEN** the API server rejects the object

### Requirement: Mutable refs are resolved without a clone

A mutable `ref` SHALL be resolved to a commit SHA by querying the remote's
references, never by cloning in the operator. Resolution prefers the exact
`refs/heads/<ref>` or `refs/tags/<ref>` match, with an annotated tag's peeled commit
winning over the tag object itself.

#### Scenario: Branch resolves to its head commit

- **WHEN** `ref` names a branch that exists on the remote
- **THEN** the commit of `refs/heads/<ref>` becomes the resolved revision

#### Scenario: Annotated tag resolves to the commit it points at

- **WHEN** `ref` names an annotated tag
- **THEN** the peeled `refs/tags/<ref>^{}` commit is preferred over the tag object

#### Scenario: Ref does not exist

- **WHEN** the remote publishes no reference matching `ref`
- **THEN** resolution fails with an error naming the ref

### Requirement: Polling cadence

A workload tracking a mutable ref SHALL re-resolve it on `source.pollInterval`,
default one minute. Pinned and Flux-backed sources are not polled for that purpose.

#### Scenario: Default cadence

- **WHEN** `pollInterval` is unset or not positive
- **THEN** the workload is re-queued after one minute

#### Scenario: Explicit cadence

- **WHEN** `pollInterval` is `10m`
- **THEN** the workload is re-queued after ten minutes

### Requirement: Flux artifact consumption

With `fluxSourceRef` the operator SHALL read `status.artifact.revision` and
`status.artifact.url` from the referenced `GitRepository`, `OCIRepository` or
`Bucket` in the same namespace, treating all three uniformly and never inspecting
their kind-specific spec. The revision is normalised to its digest portion and used
as the rollout key; the URL is the tarball the pod fetches.

#### Scenario: Artifact is published

- **WHEN** the referenced Flux source publishes `main@sha1:<sha>` and an artifact URL
- **THEN** the resolved revision is `<sha>` and the artifact URL is carried to the pod

#### Scenario: Artifact is not ready

- **WHEN** the referenced Flux source has no artifact revision or no artifact URL yet
- **THEN** resolution fails with an error naming the kind and object

#### Scenario: API version default

- **WHEN** `fluxSourceRef.apiVersion` is unset
- **THEN** the source-controller group version the operator is built against is used

### Requirement: Private repository credentials

`source.credentialsRef` SHALL supply repository credentials from a Secret in the same
namespace, using one key convention for both the operator-side resolution and the
pod-side clone: `ssh-privatekey` with optional `known_hosts` for SSH remotes, or
`username` plus `password` — with `token` accepted in place of `password` — for HTTPS
remotes. Username, password and token values are trimmed so a trailing newline does
not break authentication.

#### Scenario: SSH credentials

- **WHEN** the Secret carries `ssh-privatekey`
- **THEN** ref resolution authenticates over SSH without the key ever appearing in a
  command line

#### Scenario: HTTPS token

- **WHEN** the Secret carries `username` and `token` but no `password`
- **THEN** the token is used as the password

#### Scenario: Credentials Secret is unreadable

- **WHEN** the referenced Secret does not exist
- **THEN** resolution fails with an error naming the Secret

#### Scenario: Public repository

- **WHEN** no `credentialsRef` is set
- **THEN** no Secret is read and resolution proceeds unauthenticated

### Requirement: Resolution outcome is published

The resolved revision and the time it was resolved SHALL be published on the
workload, and a resolution failure SHALL stall the workload instead of advancing its
rollout.

#### Scenario: Successful resolution

- **WHEN** the revision resolves
- **THEN** `status.resolvedRevision` and `status.lastPolledTime` are updated and
  `GitSynced` is `True`

#### Scenario: Failed resolution

- **WHEN** resolution fails
- **THEN** `GitSynced` is `False` with reason `GitError` carrying the error text
- **AND** the workload is `Degraded` with `Stalled` `True`
- **AND** the workload is retried shortly without changing what its pods run

### Requirement: Source changes are watched

Changes to a referenced credentials Secret or Flux source SHALL enqueue the
workloads that reference it, in that object's namespace, without waiting for the
poll interval. The operator SHALL start cleanly when the Flux CRDs are not installed.

#### Scenario: Credentials Secret updated

- **WHEN** a Secret referenced as `source.credentialsRef` changes
- **THEN** every workload referencing it is reconciled

#### Scenario: Flux source publishes a new artifact

- **WHEN** a referenced Flux source's status changes
- **THEN** every workload referencing it is reconciled

#### Scenario: Flux is not installed

- **WHEN** no Flux source CRD is registered in the cluster
- **THEN** the manager starts without those watches and resolution still works by
  polling
