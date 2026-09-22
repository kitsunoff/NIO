# Nix Workload Kinds Specification

## Purpose

Describes the four workload kinds that run a flake attribute as a Kubernetes
workload — `NixDeployment`, `NixJob`, `NixCronJob`, `NixStatefulSet` — including the
lifecycle they share, how each projects onto its native object, and how each reports
health.

## Requirements

### Requirement: Each kind projects onto one native workload

Every NIO workload kind SHALL own exactly one native Kubernetes object of the
corresponding type, in the same namespace, named after the NIO object (except
`NixJob`, whose runs are per-revision). The native workload spec is taken verbatim
from the kind's template field, with only the operator-owned parts stamped in.

#### Scenario: Deployment projection

- **WHEN** a `NixDeployment` is reconciled
- **THEN** an `apps/v1` `Deployment` of the same name is created and owned by it, and
  `status.workloadRef` names it

#### Scenario: CronJob projection

- **WHEN** a `NixCronJob` is reconciled
- **THEN** a `batch/v1` `CronJob` of the same name is created, its job template pinned
  to the resolved revision

#### Scenario: StatefulSet projection

- **WHEN** a `NixStatefulSet` is reconciled
- **THEN** an `apps/v1` `StatefulSet` of the same name is created

#### Scenario: Immutable native fields are not fought over

- **WHEN** an owned native workload already exists
- **THEN** only its mutable fields are updated and its selector is left alone

### Requirement: Minimal workloads are completed by the operator

The template fields are schemaless so a workload can omit what the operator fills in:
a missing selector is defaulted to the managed labels, and missing pod containers are
synthesized.

#### Scenario: Deployment without a selector

- **WHEN** a `NixDeployment` declares no selector
- **THEN** the owned Deployment selects the operator's managed labels

#### Scenario: Workload without containers

- **WHEN** a template declares no containers
- **THEN** the app container is created by the operator

### Requirement: Shared workload lifecycle

Every workload kind SHALL carry the NIO finalizer, report `observedGeneration`, set
`Reconciling` while working, resolve its source, run the infrastructure preflight,
project its native workload, observe it, and re-queue on the poll interval. The owned
native workload is garbage-collected through its owner reference on deletion.

#### Scenario: Deletion

- **WHEN** a workload is deleted
- **THEN** its finalizer is removed and the owned native workload is garbage-collected

#### Scenario: Steady state

- **WHEN** a reconcile completes without stalling
- **THEN** the workload is re-queued after the source poll interval

### Requirement: Suspension stops all NIO activity

`nix.suspend` SHALL pause reconciliation: no resolution, no projection, no new runs
or rollouts. The workload reports phase `Suspended`.

#### Scenario: Workload is suspended

- **WHEN** `nix.suspend` is set to `true`
- **THEN** phase is `Suspended` and no native workload is created or updated

### Requirement: Rollout health for replicated workloads

`NixDeployment` and `NixStatefulSet` SHALL report `Ready` only when the owned
workload has observed the current generation and every desired replica is updated and
ready. A rollout still building reports `Building`; otherwise `Progressing`.

#### Scenario: Rollout complete

- **WHEN** the owned workload has updated, ready and (for a Deployment) available
  replicas equal to the desired count, and has observed its current generation
- **THEN** phase is `Ready`, `Ready` is `True`, `Progressing` is `False`, and
  `status.rolledOutRevision` is the resolved revision

#### Scenario: New-revision pods are building

- **WHEN** new-revision pods are still running their instantiate init
- **THEN** phase is `Building` and `Progressing` is `True`

#### Scenario: New-revision build is failing

- **WHEN** a new-revision pod's instantiate init terminated non-zero or is in a crash
  loop
- **THEN** the workload is `Degraded` with `Stalled` `True` and reason
  `InitBuildFailing`

#### Scenario: Replicas cannot be created

- **WHEN** the owned Deployment reports a replica failure
- **THEN** the workload is `Degraded` with `Stalled` `True` and reason
  `ReplicaFailure`, carrying the native message

#### Scenario: Replica counts are mirrored

- **WHEN** the owned workload's status changes
- **THEN** the NIO workload mirrors its ready and updated replica counts, plus
  available replicas for a Deployment and current/update revisions for a StatefulSet

### Requirement: Deployment rollout default is surge-only

When a `NixDeployment` declares no strategy, the operator SHALL default to a rolling
update that adds capacity before removing any, so a broken revision stalls the
rollout without shedding healthy capacity.

#### Scenario: No strategy declared

- **WHEN** a `NixDeployment` template sets no strategy
- **THEN** the owned Deployment rolls with zero max-unavailable and a positive max
  surge

#### Scenario: Explicit strategy is respected

- **WHEN** the template declares its own strategy
- **THEN** that strategy is used unchanged

### Requirement: NixJob runs are immutable and per revision

A `NixJob` SHALL create one `batch/v1` Job per revision key, named
`<workload>-<revision-key>`, and SHALL never mutate an existing run Job. With
trigger-on-change enabled (the default) a new revision produces a new run; with it
disabled the workload runs once and creates no further Jobs.

#### Scenario: New revision triggers a new run

- **WHEN** the resolved revision changes and trigger-on-change is enabled
- **THEN** a new run Job for that revision key is created and the previous one is left
  untouched

#### Scenario: Run-once semantics

- **WHEN** trigger-on-change is disabled and a run Job already exists
- **THEN** no further run Job is created on a new revision

#### Scenario: Existing run is never rewritten

- **WHEN** a run Job for the current revision key already exists
- **THEN** it is not updated, whatever changed in the workload spec

#### Scenario: Batch restart policy is made valid

- **WHEN** a Job template leaves the pod restart policy empty
- **THEN** it is set to `Never`, because Jobs reject the `Always` default

### Requirement: NixJob health and history

A `NixJob` SHALL report the current run's outcome and keep a bounded history of
finished runs.

#### Scenario: Run completed

- **WHEN** the current run Job reports completion
- **THEN** phase is `Ready`, `Ready` is `True`, and `status.succeeded` mirrors the Job

#### Scenario: Run failed

- **WHEN** the current run Job reports failure
- **THEN** phase is `Failed`, `Ready` is `False` and `Stalled` is `True` with reason
  `Failed`

#### Scenario: Run in progress

- **WHEN** the current run Job has not finished
- **THEN** phase is `Progressing` and `status.lastRunTime` reflects its start

#### Scenario: Old runs are pruned

- **WHEN** more than three finished run Jobs exist besides the current one
- **THEN** the oldest ones beyond that limit are deleted, and a garbage-collection
  failure does not fail the reconcile

### Requirement: NixCronJob schedules from its native template

A `NixCronJob` SHALL take its schedule, concurrency policy, suspension and history
limits from the native CronJob spec it carries, and SHALL keep that CronJob's job
template pinned to the currently resolved revision.

#### Scenario: Schedule is required

- **WHEN** a `NixCronJob` is submitted without a schedule
- **THEN** the API server rejects it

#### Scenario: Revision is repinned

- **WHEN** a new revision resolves
- **THEN** the owned CronJob's job template is updated so subsequent scheduled runs
  use it

### Requirement: NixCronJob immediate run on a new revision

`nix.triggerOnChange` SHALL default to `false` for a `NixCronJob`. When enabled, a
newly resolved revision fires one additional Job immediately, honouring the native
concurrency policy, named `<workload>-<revision-key>-manual` and bounded by a
time-to-live so it does not accumulate.

#### Scenario: Immediate run fires

- **WHEN** the resolved revision changes and trigger-on-change is enabled
- **THEN** a one-off Job for that revision is created from the CronJob's job template

#### Scenario: Forbidden concurrency

- **WHEN** the concurrency policy forbids concurrent runs and a run is active
- **THEN** no immediate Job is created

#### Scenario: Immediate run is reaped

- **WHEN** a one-off Job finishes and its template set no time-to-live
- **THEN** it is removed a day later, since CronJob history limits do not cover it

#### Scenario: Immediate run failure does not fail the reconcile

- **WHEN** creating the one-off Job fails
- **THEN** the error is logged and the rest of the reconcile completes

### Requirement: NixCronJob health follows runs, not the schedule

A CronJob keeps scheduling whether or not its runs succeed, so a `NixCronJob` SHALL
judge health by the outcome of its most recent finished run, counting both scheduled
runs and immediate ones. The recorded last-failed and last-successful times are
monotonic so a garbage-collected Job cannot resurrect a stale verdict.

#### Scenario: Latest run failed

- **WHEN** a failure exists and no success happened after it
- **THEN** phase is `Degraded`, `Ready` is `False` with reason `RunFailed` naming the
  failure time, and any earlier `Stalled` condition is cleared because a run did
  happen

#### Scenario: Recovery

- **WHEN** a later run succeeds after a failure
- **THEN** phase is `Ready`

#### Scenario: Both populations are counted

- **WHEN** an immediate run fails while scheduled runs succeed
- **THEN** the failure is observed, because failures and successes are read from the
  same set of Jobs

#### Scenario: Job history is pruned

- **WHEN** the Jobs behind an observed failure are deleted
- **THEN** the recorded failure time is retained and the workload does not flip back
  to `Ready` on its own

#### Scenario: Job list cannot be read

- **WHEN** listing Jobs fails during observation
- **THEN** the previously observed times are kept and the phase does not flip
