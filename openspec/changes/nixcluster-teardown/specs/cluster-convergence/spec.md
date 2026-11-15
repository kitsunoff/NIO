## ADDED Requirements

### Requirement: Deletion policy

A `NixCluster` SHALL declare `spec.deletionPolicy` as `Teardown` or `Retain`, with
`Teardown` as the default. The policy is read at deletion time, so it can be changed
on a cluster that is already being deleted.

#### Scenario: Policy omitted

- **WHEN** a `NixCluster` is submitted without `deletionPolicy`
- **THEN** it is stored with `deletionPolicy: Teardown`

#### Scenario: Unknown policy is rejected

- **WHEN** a `NixCluster` is submitted with a `deletionPolicy` other than `Teardown`
  or `Retain`
- **THEN** the API server rejects it

### Requirement: Teardown waits for converge to stop

Before a teardown run starts, the operator SHALL stop the converge child from
starting new runs and SHALL wait for an active converge run to finish, so a converge
and a teardown never run against the same members at the same time.

#### Scenario: Converge child is suspended first

- **WHEN** a `NixCluster` with `deletionPolicy: Teardown` is deleted
- **THEN** the converge child's native CronJob suspension is set and its
  trigger-on-change is turned off before any teardown run is created

#### Scenario: Converge run still active

- **WHEN** a Job controlled by the converge child, or by its CronJob, has not
  finished
- **THEN** no teardown run is created, `TearingDown` is `True` with reason
  `WaitingForConverge`, and the reconcile is requeued

#### Scenario: Suspension not projected yet

- **WHEN** the converge child's CronJob exists and does not report `suspend: true`
  yet
- **THEN** no teardown run is created and the reconcile is requeued

### Requirement: One owned teardown run

The operator SHALL run teardown as exactly one owned `NixJob` named
`<cluster>-teardown`, built from the converge child: the same source, node files,
store and builder references and pod template, with the arguments
`teardown --auto-approve`, a single run per `NixJob` and no pod-level retries.

#### Scenario: Teardown run is created

- **WHEN** the converge child is suspended and no converge run is active
- **THEN** a `NixJob` `<cluster>-teardown` controlled by the cluster exists, runs
  `.#cluster-<name>` with `teardown --auto-approve`, and carries the converge child's
  node files and pod template

#### Scenario: No duplicate runs

- **WHEN** the cluster is reconciled again while the teardown run exists
- **THEN** no second teardown `NixJob` is created and the existing one is observed

#### Scenario: Nothing to tear down

- **WHEN** a cluster being deleted has neither a converge child nor a teardown run
- **THEN** the finalizer is removed without a teardown run

### Requirement: Teardown outcome decides the finalizer

The teardown run's exit status SHALL decide whether the finalizer is removed. The
`::nixcluster:result::` line of the run SHALL be recorded per member in
`status.teardown` when it can be read.

#### Scenario: Teardown succeeded

- **WHEN** the teardown run completes successfully
- **THEN** its per-member results are recorded, the converge child is deleted and
  the finalizer is removed

#### Scenario: Unreachable members are not fatal

- **WHEN** the teardown run completes successfully with result `partial`
- **THEN** the finalizer is removed and a warning event names the skipped members

#### Scenario: Teardown failed

- **WHEN** the teardown run fails
- **THEN** the finalizer stays, `TeardownFailed` is `True` with the failure message,
  and the failure is recorded once per run

#### Scenario: Retry after backoff

- **WHEN** a failed teardown run's backoff has elapsed
- **THEN** the failed `NixJob` is deleted and a new teardown run is created, with
  the backoff doubling per recorded failure up to a fixed cap

#### Scenario: Result line unavailable

- **WHEN** the run's pod log cannot be read
- **THEN** the outcome still follows the run's exit status and the status says the
  result is unavailable

### Requirement: Retain releases the cluster without a teardown

With `deletionPolicy: Retain`, deletion SHALL remove the converge child and the
finalizer without a teardown run, also when the policy is switched to `Retain` while
a teardown is pending, running or failing.

#### Scenario: Retain from the start

- **WHEN** a `NixCluster` with `deletionPolicy: Retain` is deleted
- **THEN** the converge child is deleted, the finalizer is removed and no teardown
  run is created

#### Scenario: Escape hatch

- **WHEN** the policy of a cluster whose teardown failed is changed to `Retain`
- **THEN** the finalizer is removed on the next reconcile

## MODIFIED Requirements

### Requirement: Cluster phase reflects runs, not schedules

`status.phase` SHALL be one of `Ready`, `Converging`, `Degraded`, `Blocked`,
`TearingDown`. Outside deletion it is derived from the converge child's observed
runs. A cron keeps scheduling whether or not its runs succeed, so the outcome of the
latest finished run outranks the child's phase. During deletion with
`deletionPolicy: Teardown` it is `TearingDown`.

#### Scenario: No members selected

- **WHEN** no `Machine` is selected by any group
- **THEN** phase is `Blocked` and `Ready` is `False` with reason `Waiting`

#### Scenario: Converge child not created yet

- **WHEN** the converge child does not exist yet
- **THEN** phase is `Converging`

#### Scenario: Latest run failed

- **WHEN** the converge child's most recent finished run failed and no later run
  succeeded
- **THEN** phase is `Degraded`, even if the child's own phase says otherwise

#### Scenario: Converge succeeded

- **WHEN** the converge child is `Ready`, or reports a last successful run
- **THEN** phase is `Ready`, `Ready` is `True` with message `cluster converged`, and
  `Stalled` is removed

#### Scenario: Teardown in progress

- **WHEN** a cluster with `deletionPolicy: Teardown` is being deleted
- **THEN** phase is `TearingDown`, `TearingDown` is `True`, and `Ready` is `False`
  with reason `TearingDown`

## REMOVED Requirements

### Requirement: Deletion removes the converge child

**Reason**: Replaced by "Deletion policy", "One owned teardown run", "Teardown
outcome decides the finalizer" and "Retain releases the cluster without a teardown".

**Migration**: Set `deletionPolicy: Retain` to keep the previous behaviour.
