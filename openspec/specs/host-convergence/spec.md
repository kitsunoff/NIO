# Host Convergence Specification

## Purpose

Describes the `NixosConfiguration` kind: how NIO converges exactly one `Machine` to
a flake attribute — the optional first-time full-disk install, the recurring day-two
convergence, and the decommission run performed when the configuration is deleted.

## Requirements

### Requirement: Configuration targets one machine in the same namespace

A `NixosConfiguration` SHALL reference exactly one `Machine` by name via
`spec.machineRef`, resolved in the configuration's own namespace. The flake repo
(`spec.gitRepo`), git ref (`spec.ref`, default `main`), flake attribute
(`spec.flake`), optional subdirectory (`spec.configurationSubdir`) and optional
credentials Secret define what is applied.

#### Scenario: Target machine is resolved

- **WHEN** a `NixosConfiguration` names an existing, reachable `Machine`
- **THEN** `status.targetMachine` reports that machine's name

### Requirement: The orchestrator applies nothing itself

The controller SHALL NOT run Nix or SSH directly. It drives child NIO workloads: an
install `NixJob`, a day-two `NixCronJob`, and a decommission `NixJob`. Their names
are published in `status.installJobRef`, `status.dayTwoCronJobRef` and
`status.decommissionJobRef`.

#### Scenario: Children are created for a converging configuration

- **WHEN** a configuration with `fullInstall: true` starts converging
- **THEN** a child `NixJob` named `<config>-install` is created and owned by the
  configuration
- **AND** after the install completes, a child `NixCronJob` named `<config>-day2` is
  created and owned by the configuration

### Requirement: One configuration owns a machine

At most one `NixosConfiguration` SHALL drive a given `Machine`. Ownership belongs to
the configuration with the earliest `creationTimestamp`, with the lexicographically
smaller name winning a tie. A configuration that loses the contest is Blocked and its
day-two cron is suspended.

#### Scenario: Second configuration targets an owned machine

- **WHEN** a newer `NixosConfiguration` references a `Machine` already owned by an
  older one
- **THEN** the newer one reports phase `Blocked` with reason `MachineInUse` naming the
  owner
- **AND** a warning event `MachineOwned` is recorded
- **AND** an existing day-two cron of the newer configuration is suspended

#### Scenario: Owner is deleted

- **WHEN** the owning configuration is removed
- **THEN** the remaining configuration becomes the owner and leaves `Blocked`

### Requirement: Machine readiness gates convergence

A configuration SHALL make no progress while its target `Machine` is missing or not
discoverable. It reports `Blocked` and suspends any existing day-two cron so no run
fires against an unreachable host.

#### Scenario: Target machine does not exist

- **WHEN** `spec.machineRef` names a `Machine` that is absent
- **THEN** phase is `Blocked` with reason `MachineNotReady`
- **AND** a warning event `MachineNotFound` is recorded

#### Scenario: Target machine is unreachable

- **WHEN** the target `Machine` reports `status.discoverable: false`
- **THEN** phase is `Blocked` with reason `MachineNotReady` and the day-two cron is
  suspended

### Requirement: Full-disk install runs at most once

With `spec.fullInstall: true` the operator SHALL run `nixos-anywhere` through a child
`NixJob` before day-two convergence, and SHALL NOT run it again once completed.
Completion is persisted durably (retrying on conflict) **before** the child is
deleted, so no later reconcile can observe the install as incomplete and re-wipe the
host.

#### Scenario: Install succeeds

- **WHEN** the install child reports a succeeded run
- **THEN** `status.fullDiskInstallCompleted` is persisted as `true` and
  `status.installJobRef` is cleared before the child is deleted
- **AND** a normal event `InstallSucceeded` is recorded
- **AND** the configuration proceeds to day-two convergence

#### Scenario: Completed install is never repeated

- **WHEN** a configuration with `fullInstall: true` already has
  `status.fullDiskInstallCompleted: true`
- **THEN** no install child is created on subsequent reconciles

#### Scenario: Install is in flight

- **WHEN** the install child has neither succeeded nor failed
- **THEN** phase is `Installing` and the configuration is re-queued

### Requirement: Bounded install retry

A failed install SHALL be retried by deleting and recreating the child, at most 3
times. Once the cap is reached the configuration holds in `Degraded` without
recreating the child and without re-queueing, so a permanently failing install does
not storm the reconcile loop.

#### Scenario: Install fails below the cap

- **WHEN** the install child fails and `status.installRetries` is below 3
- **THEN** the counter is incremented, the child is deleted and recreated, and phase
  stays `Installing`

#### Scenario: Install retries are exhausted

- **WHEN** the install child fails and `status.installRetries` has reached 3
- **THEN** phase is `Degraded` with `Applied` `False`
- **AND** no further child is created and no requeue is scheduled
- **AND** the warning event `InstallFailed` is recorded only on the transition into
  `Degraded`

#### Scenario: A failure already being retried is not counted twice

- **WHEN** the failed install child still carries a deletion timestamp
- **THEN** the retry counter is left unchanged and the controller waits for the
  deletion to finish

### Requirement: Day-two convergence

The operator SHALL maintain a day-two `NixCronJob` that runs `nixos-rebuild switch`
against the target host on `spec.dayTwoSchedule` (default `*/30 * * * *`), with
`concurrencyPolicy: Forbid` and trigger-on-change enabled so a new revision applies
promptly rather than waiting for the next tick.

#### Scenario: Day-two child shape

- **WHEN** the day-two child is created
- **THEN** it runs `nixpkgs#nixos-rebuild` with `switch --flake .<flake>
  --target-host <user>@<host>`
- **AND** its schedule is `spec.dayTwoSchedule`, its concurrency policy is `Forbid`,
  and trigger-on-change is `true`

#### Scenario: Healthy convergence

- **WHEN** the day-two child is `Ready` and reports a last successful run
- **THEN** phase is `Ready`, `Ready` is `True`, `status.lastAppliedTime` mirrors that
  run, and `status.resolvedRevision` mirrors the child's rolled-out revision

#### Scenario: Convergence run failed

- **WHEN** the day-two child reports phase `Degraded` or `Failed`
- **THEN** phase is `Degraded` with message `Day-2 convergence run failed`

#### Scenario: Convergence stalled on infrastructure

- **WHEN** the day-two child is `Stalled` because a referenced `NixStore` or
  `NixBuilder` cannot be resolved
- **THEN** phase is `Degraded` and the message names the unresolved reference rather
  than claiming a run failed

### Requirement: Applied describes the machine, not the latest run

The `Applied` condition SHALL report whether a configuration is present on the host.
It stays `True` across a failing or stalled convergence once a full-disk install has
completed or any day-two run has succeeded.

#### Scenario: Broken convergence on a configured host

- **WHEN** the host has a successful run in its history and the current convergence is
  failing
- **THEN** phase is `Degraded` while `Applied` remains `True`

#### Scenario: Host that was never configured

- **WHEN** no install completed and no day-two run ever succeeded
- **THEN** `Applied` is `False` with reason `Waiting`

### Requirement: Phase drives the standard conditions

`status.phase` SHALL be one of `Pending`, `Blocked`, `Installing`, `Converging`,
`Ready`, `Degraded`, `Removing`, and the kstatus conditions are derived from it.

#### Scenario: Ready phase

- **WHEN** phase is `Ready`
- **THEN** `Ready` is `True`, `Reconciling` is `False`, and `Stalled` is absent

#### Scenario: Blocked or Degraded phase

- **WHEN** phase is `Blocked` or `Degraded`
- **THEN** `Stalled` is `True` and `Reconciling` is `False`

#### Scenario: Converging phase

- **WHEN** phase is `Converging`
- **THEN** `Reconciling` is `True`, `Ready` is `False`, and `Stalled` is absent

#### Scenario: Git synchronisation is mirrored from the child

- **WHEN** the day-two child publishes a `GitSynced` condition
- **THEN** that condition's status, reason and message are copied onto the
  configuration

### Requirement: Machine status write-back

On reaching `Ready` the operator SHALL record the applied configuration on the target
`Machine`, and SHALL clear it on removal — but only while the machine still points at
this configuration.

#### Scenario: Converged host is stamped

- **WHEN** the configuration reaches `Ready`
- **THEN** the `Machine` reports `hasConfiguration: true`, `appliedConfiguration`,
  `appliedCommit` and `lastAppliedTime`

#### Scenario: Another configuration took the machine over

- **WHEN** the machine's `appliedConfiguration` names a different configuration
- **THEN** removal of this configuration leaves the machine's fields untouched

### Requirement: Decommission on deletion

When a `NixosConfiguration` with `spec.onRemoveFlake` is deleted, the operator SHALL
stop day-two convergence and apply the removal flake to the host through a
decommission `NixJob` created **without** an owner reference, so it outlives the
parent. The finalizer is removed only after a terminal outcome.

#### Scenario: Decommission starts

- **WHEN** a configuration with `onRemoveFlake` and a resolvable machine is deleted
- **THEN** the day-two cron is deleted
- **AND** a `NixJob` named `<config>-onremove`, labelled as the decommission
  operation and carrying no owner reference, is created
- **AND** phase is `Removing` and a normal event `OnRemoveFlakeStarted` is recorded

#### Scenario: Decommission succeeds

- **WHEN** the decommission job succeeds
- **THEN** the machine's applied-configuration fields are cleared
- **AND** the orphan `NixJob` is deleted before the finalizer is removed, so it does
  not leak
- **AND** a normal event `OnRemoveFlakeSucceeded` is recorded

#### Scenario: Decommission is retried and finally abandoned

- **WHEN** the decommission job fails
- **THEN** `status.onRemoveRetries` is persisted (retrying on conflict) before the
  child is deleted and recreated
- **AND** once 3 attempts are reached the machine is cleared, the orphan job is
  deleted, a warning event `OnRemoveFlakeFailed` is recorded, and the finalizer is
  removed anyway so deletion is never blocked forever

#### Scenario: Nothing to decommission

- **WHEN** the configuration has no `onRemoveFlake`, or its target `Machine` no longer
  exists
- **THEN** the finalizer is removed without creating a decommission job

#### Scenario: Decommission cannot be built

- **WHEN** the decommission child cannot be constructed — for example the machine has
  no SSH key Secret
- **THEN** a warning event `OnRemoveFlakeSkipped` is recorded and the configuration is
  finalized rather than blocked

#### Scenario: Orphan job is rediscovered after an operator restart

- **WHEN** the operator restarts and `status.decommissionJobRef` is lost
- **THEN** the existing decommission job is found by its labels rather than recreated

### Requirement: Child workload shaping

Children SHALL be derived from the configuration and its target `Machine`: the
machine's SSH key is mounted read-only and exported through `NIX_SSHOPTS` with
permissive host-key options, the target host is `<sshUser>@<host>`, and
`spec.storeRef` / `spec.builderRef` are passed through to every child.

#### Scenario: Machine has no SSH key

- **WHEN** the target `Machine` has no `sshKeySecretRef`
- **THEN** no child can be built and the apply path reports that the machine cannot be
  reached over SSH

#### Scenario: Install child passes SSH options explicitly

- **WHEN** the install child is built
- **THEN** `nixos-anywhere` receives the identity file and the permissive host-key
  options as explicit arguments, because it runs its own SSH and does not honour
  `NIX_SSHOPTS`

### Requirement: Git ref routing for children

A `spec.ref` that is a full 40-character commit SHA SHALL be passed to children as a
pinned revision; any other value is passed as a mutable ref to be polled.

#### Scenario: Pinned commit

- **WHEN** `spec.ref` is a 40-character hex SHA
- **THEN** children resolve it verbatim without invoking git

#### Scenario: Branch or tag

- **WHEN** `spec.ref` is `main` or a tag name
- **THEN** children resolve it by polling the remote

### Requirement: Additional file injection

`spec.additionalFiles` SHALL be translated into the children's build-time file
injection. `Inline` and `SecretRef` entries are supported; `NixosFacter` is not.

#### Scenario: Inline and Secret-sourced files

- **WHEN** a configuration declares `Inline` and `SecretRef` additional files
- **THEN** every child carries the equivalent injected files

#### Scenario: Unsupported value type

- **WHEN** an additional file declares `valueType: NixosFacter`
- **THEN** the child cannot be built and the reconcile reports that the value type is
  not supported
