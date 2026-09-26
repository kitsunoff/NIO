# Cluster Convergence Specification

## Purpose

Describes the `NixCluster` kind: how NIO groups `Machine` objects into node groups by
label, maps opaque per-group values onto each member, and drives one idempotent
whole-cluster converge run — while staying agnostic about what the cluster actually
is.

## Requirements

### Requirement: A cluster is node groups over a flake source

A `NixCluster` SHALL declare a downstream flake source and at least one node group.
Each group has a name unique within the cluster, a label selector matching `Machine`
objects in the cluster's namespace, an optional `count`, and an opaque `values`
object.

#### Scenario: Cluster without node groups is rejected

- **WHEN** a `NixCluster` is submitted with an empty `nodeGroups` list
- **THEN** the API server rejects it

#### Scenario: Values are never interpreted

- **WHEN** a group declares arbitrary nested `values`
- **THEN** NIO passes them through unchanged and derives no behaviour from their
  content

### Requirement: Deterministic, sticky member selection

Selection SHALL be stable and sticky. Candidates are the matching Machines sorted by
name; a Machine belongs to exactly one group, claimed by the first group in spec
order that matches it. With `count` unset every candidate is a member. With `count`
set, previously selected members that still match are kept, vacancies are filled from
candidates above the current highest member first and only then from the lowest
remaining, and an over-full group drops its highest-named extras.

#### Scenario: Machine matched by two groups

- **WHEN** a `Machine` matches the selectors of two node groups
- **THEN** only the earlier group in spec order selects it

#### Scenario: Adding a lower-sorting machine evicts nobody

- **WHEN** a group with `count: 3` has three members and a new Machine sorting below
  all of them starts matching
- **THEN** the existing three members remain selected

#### Scenario: Vacancy is filled upward

- **WHEN** a member of a `count: 3` group stops matching and other candidates exist
- **THEN** the vacancy is filled by the next candidate above the current members
  before any lower-sorting candidate is considered

#### Scenario: Count is reduced

- **WHEN** `count` is lowered below the current member number
- **THEN** the highest-named members are dropped and the rest are kept

#### Scenario: Members are reported sorted

- **WHEN** a group's selection is published
- **THEN** `status.nodeGroups[].members` is sorted ascending by name and
  `status.nodeGroups[].selected` matches its length

### Requirement: Underprovisioning is surfaced, not hidden

When a group cannot reach its requested `count`, the cluster SHALL still converge the
members it has and report the gap. `desired` reflects the requested count, or the
number of matching candidates when `count` is unset.

#### Scenario: Fewer machines than requested

- **WHEN** a group requests `count: 3` and only two Machines match
- **THEN** `Underprovisioned` is `True` with reason `Underprovisioned`
- **AND** both members are still rendered and converged

#### Scenario: All groups satisfied

- **WHEN** every group reaches its requested count
- **THEN** `Underprovisioned` is `False` with reason `FullyProvisioned`

### Requirement: Per-member node files

For every selected member the operator SHALL render one node file into the cluster's
flake checkout at `modules/nodes/<machine>.nix`, declaring the member under
`nixcluster.<cluster>.members.<machine>` as the group's `values` merged with the
machine's address as `install.ip`. The member's `nixosConfiguration` is deliberately
omitted so it inherits the cluster-level default.

#### Scenario: Node file content

- **WHEN** a member is selected
- **THEN** its node file recursively updates the group's values (parsed from JSON)
  with `install.ip` set to the `Machine`'s `spec.host`

#### Scenario: Hostile machine name is refused

- **WHEN** a selected `Machine` has a name containing a path separator or `..`
- **THEN** the cluster reports `Blocked` with reason `InvalidNodeFile` and no node file
  is written

#### Scenario: Values and host cannot break out of the Nix string

- **WHEN** the values JSON or the host contains a quote, a backslash or a `${`
  sequence
- **THEN** they are escaped so they cannot terminate the Nix string literal or
  introduce a live antiquotation

### Requirement: One owned converge child

The operator SHALL own exactly one converge `NixCronJob` named `<cluster>-converge`.
It runs the cluster's own app `.#cluster-<name>` with the argument `converge`,
carries every rendered node file, uses `concurrencyPolicy: Forbid`, a one hour active
deadline per run, trigger-on-change, and the schedule from `spec.dayTwoSchedule`
(default `*/30 * * * *`). Ordering, joining, secrets and post-operations belong to
the downstream flake, not to NIO.

#### Scenario: Converge child is created and kept in sync

- **WHEN** a cluster's selection or values change
- **THEN** the converge child's spec is updated in place and
  `status.convergeJobRef` names it

#### Scenario: Cluster credentials are mounted into the converge pod

- **WHEN** `spec.sshKeyRef` or `spec.ageKeyRef` is set
- **THEN** the corresponding Secret is mounted read-only into the converge pod and
  exported as `NIX_SSHOPTS` or `SOPS_AGE_KEY_FILE` respectively

#### Scenario: Acceleration references are passed through

- **WHEN** `spec.storeRef` or `spec.builderRef` is set
- **THEN** the converge child receives them, so the run substitutes from the store and
  delegates builds to the builder

### Requirement: Builder architecture preflight

Because delegation sets `max-jobs = 0` and leaves no local fallback, the operator
SHALL refuse a cluster whose referenced `NixBuilder` provably cannot build a selected
member's system, and suspend the converge child so it stops firing runs that cannot
succeed. The check is conservative: it fires only when every input is known.

#### Scenario: Provable mismatch

- **WHEN** the builder declares an explicit `spec.systems` that does not include the
  system of a member whose architecture has been collected
- **THEN** the cluster reports `Blocked` with reason `BuilderSystemMismatch`, naming
  the member and the system it needs
- **AND** the converge child is suspended

#### Scenario: Unqualified builder proves nothing

- **WHEN** the referenced builder declares no `spec.systems`
- **THEN** the preflight passes silently

#### Scenario: Unknown member architecture proves nothing

- **WHEN** a selected `Machine` has not reported its architecture yet
- **THEN** that member is skipped by the preflight

#### Scenario: Missing or unreadable builder proves nothing

- **WHEN** the referenced `NixBuilder` does not exist or cannot be read
- **THEN** the preflight passes and the converge child's own stall is what surfaces

### Requirement: Cluster phase reflects runs, not schedules

`status.phase` SHALL be one of `Ready`, `Converging`, `Degraded`, `Blocked`, derived
from the converge child's observed runs. A cron keeps scheduling whether or not its
runs succeed, so the outcome of the latest finished run outranks the child's phase.

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

### Requirement: Stalled infrastructure is mirrored, not re-diagnosed

When the converge child is stalled on an unresolvable `NixStore` or `NixBuilder`
reference, the cluster SHALL mirror that diagnosis so `kubectl describe` names the
broken reference, and SHALL report `Blocked` rather than `Degraded`.

#### Scenario: Referenced store is missing

- **WHEN** the converge child reports `Stalled` with reason `InfraNotReady`
- **THEN** the cluster reports phase `Blocked`, `Stalled` `True` with the child's
  reason, and `Ready` `False` with the child's message

#### Scenario: Stall clears

- **WHEN** a reconcile completes selection, rendering, the builder preflight and the
  converge child without error
- **THEN** any `Stalled` condition is removed

### Requirement: Per-member status is honest about what ran

`status.nodeGroups[].members[].status` SHALL be one of `Pending`, `Applying`,
`Applied`, `Failed`, derived from the converge run. A stalled converge applied
nothing, so members keep the status they last reported.

#### Scenario: Converge stalled

- **WHEN** the converge child is stalled on infrastructure
- **THEN** every member keeps its previously reported status, or `Pending` if it never
  had one

#### Scenario: Latest converge run failed

- **WHEN** the most recent finished run failed
- **THEN** every member reports `Failed`

#### Scenario: Converge succeeded

- **WHEN** the converge child is `Ready` or reports a last successful run
- **THEN** every member reports `Applied`

### Requirement: Git synchronisation reporting

`GitSynced` SHALL report whether the converge child has resolved a revision yet.

#### Scenario: Revision resolved

- **WHEN** the converge child publishes a resolved revision
- **THEN** `GitSynced` is `True` and its message names that revision

#### Scenario: Revision not resolved yet

- **WHEN** the converge child has no resolved revision
- **THEN** `GitSynced` is `False` with reason `Waiting`

### Requirement: Observed generation advances only on success

`status.observedGeneration` SHALL be advanced only by a reconcile that completed
selection, rendering, the preflight and the converge child.

#### Scenario: Failing reconcile

- **WHEN** a reconcile returns early because a selector is invalid or a node file
  cannot be rendered
- **THEN** `status.observedGeneration` is left at its previous value and `Stalled` is
  set

### Requirement: Machine changes re-trigger cluster reconciliation

Because selectors are label-based and not indexable, any change to a `Machine` SHALL
enqueue every `NixCluster` in that namespace.

#### Scenario: Machine gains a matching label

- **WHEN** a `Machine` is labelled so that it matches a group's selector
- **THEN** the cluster is reconciled and the new candidate enters selection

### Requirement: Deletion removes the converge child

Deleting a `NixCluster` SHALL delete its converge child and then remove the
finalizer. Node teardown on the hosts themselves is out of scope.

#### Scenario: Cluster is deleted

- **WHEN** a `NixCluster` is deleted
- **THEN** the converge `NixCronJob` is deleted and the finalizer is removed
- **AND** no decommission run is performed against the member hosts
