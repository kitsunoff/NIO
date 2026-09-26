# Machine Discovery Specification

## Purpose

Describes the `Machine` kind: how NIO proves it can reach a host over SSH, which
credentials it accepts, and which facts about that host it publishes for other
capabilities (host convergence, cluster convergence) to depend on.

## Requirements

### Requirement: Machine identity and connection parameters

A `Machine` SHALL describe exactly one reachable host by address and SSH
credentials. The host address is required; the SSH user defaults to `root`.
Credential Secrets MUST live in the same namespace as the `Machine` —
cross-namespace references are not supported.

#### Scenario: Minimal machine

- **WHEN** a `Machine` is created with `spec.host` and `spec.sshKeySecretRef` only
- **THEN** the SSH user used for connections is `root`

#### Scenario: Host address is mandatory

- **WHEN** a `Machine` is submitted without `spec.host`
- **THEN** the API server rejects it

### Requirement: SSH credential resolution

The operator SHALL build an SSH configuration from the referenced Secrets before
every connection attempt. A key Secret MUST carry the `ssh-privatekey` data key; a
password Secret carries the key named by `spec.sshPasswordSecretRef.key`, which
defaults to `password`. At least one authentication method MUST be resolvable.

#### Scenario: Referenced key Secret is missing

- **WHEN** `spec.sshKeySecretRef` names a Secret that does not exist
- **THEN** `Discoverable` is `False` with reason `CredentialsMissing`
- **AND** a warning event `CredentialsMissing` is recorded on the `Machine`

#### Scenario: Key Secret lacks the expected data key

- **WHEN** the referenced Secret exists but has no `ssh-privatekey` entry
- **THEN** `Discoverable` is `False` with reason `CredentialsMissing`

#### Scenario: No authentication method configured

- **WHEN** neither `sshKeySecretRef` nor `sshPasswordSecretRef` resolves to a usable
  credential
- **THEN** `Discoverable` is `False` with reason `CredentialsMissing`

### Requirement: Reachability probing

The operator SHALL test SSH connectivity to `spec.host` on port 22 with a 30 second
timeout, and re-test every 60 seconds. The outcome is published as
`status.discoverable` and the `Discoverable` condition.

#### Scenario: Host answers the SSH handshake

- **WHEN** the SSH handshake to the host succeeds
- **THEN** `status.discoverable` is `true`
- **AND** `Discoverable` is `True` with reason `SSHConnected`
- **AND** a normal event `Discoverable` is recorded

#### Scenario: Host is unreachable

- **WHEN** the SSH connection fails for a reason other than missing credentials
- **THEN** `status.discoverable` is `false`
- **AND** `Discoverable` is `False` with reason `SSHFailed` carrying the connection
  error text
- **AND** the `Machine` is re-queued for another probe rather than reported as a
  reconcile error

### Requirement: Readiness mirrors reachability

`Ready` SHALL reflect discoverability: a reachable machine is Ready, an unreachable
one is not. A reconcile that fails outright sets `Stalled` as well.

#### Scenario: Reachable machine is Ready

- **WHEN** a probe succeeds
- **THEN** `Ready` is `True` with reason `SSHConnected`
- **AND** `Reconciling` is `False` with reason `Succeeded`
- **AND** the `Stalled` condition is removed

#### Scenario: Reconciliation fails

- **WHEN** the reconcile returns an error
- **THEN** `Stalled` is `True` and `Ready` is `False`, both with reason `Failed` and
  the error text as message

### Requirement: Architecture fact collection

While a connection is already proven, the operator SHALL record the host's CPU
architecture from `uname -m` into `status.hardwareFacts.architecture`. The scan runs
at most once every 12 hours per machine, accepts only a plausible single-token
answer, and writes status only when the value actually changed.

#### Scenario: First successful scan

- **WHEN** a reachable machine has never been scanned
- **THEN** `status.hardwareFacts.architecture` is set from `uname -m`
- **AND** `status.lastHardwareScanTime` is stamped

#### Scenario: Scan is skipped while fresh

- **WHEN** `status.lastHardwareScanTime` is less than 12 hours old
- **THEN** no SSH session is opened for the scan

#### Scenario: Unchanged architecture does not write status

- **WHEN** a scan returns the architecture already recorded
- **THEN** neither the facts nor `lastHardwareScanTime` are written, so the reconcile
  does not re-enqueue itself

#### Scenario: Implausible output is discarded

- **WHEN** the command output contains no line matching a single alphanumeric token
- **THEN** the previously collected facts are left untouched and the machine is not
  reported as broken

#### Scenario: Scan failure does not affect reachability

- **WHEN** the architecture scan fails on an otherwise reachable host
- **THEN** `Discoverable` and `Ready` remain `True`

### Requirement: Applied-configuration reporting

A `Machine` SHALL carry the identity of the configuration currently applied to it,
so `kubectl get machine` shows what the host runs. These fields are written by the
host-convergence capability and cleared when that configuration is removed.

#### Scenario: Configuration reported on the machine

- **WHEN** a `NixosConfiguration` targeting this machine reaches a converged state
- **THEN** `status.hasConfiguration` is `true`, `status.appliedConfiguration` names
  that configuration, and `status.appliedCommit` carries the rolled-out revision

### Requirement: Credential Secret changes re-trigger discovery

A change to a Secret referenced as an SSH key or SSH password SHALL enqueue every
`Machine` in that namespace referencing it, each machine exactly once.

#### Scenario: Secret is fixed after a failed probe

- **WHEN** a Secret previously missing its `ssh-privatekey` entry is updated
- **THEN** every `Machine` referencing it is reconciled without waiting for the
  60 second discovery interval

#### Scenario: Secret referenced as both key and password

- **WHEN** one Secret is referenced by a `Machine` as both the SSH key and the SSH
  password source
- **THEN** that `Machine` is enqueued once, not twice

### Requirement: Deletion is not blocked

A `Machine` carries the NIO finalizer, which SHALL be removed on deletion without
blocking on configurations that reference the machine.

#### Scenario: Machine with a referencing configuration is deleted

- **WHEN** a `Machine` referenced by a `NixosConfiguration` is deleted
- **THEN** the finalizer is removed and the object is allowed to disappear
