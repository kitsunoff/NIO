## Why

Deleting a `NixCluster` today removes only the Kubernetes objects: the converge
`NixCronJob` is deleted and the finalizer is dropped. The k3s/Incus clusters the
converge runs built keep running on the member machines, with nothing left in
Kubernetes that knows about them. `kubectl delete nixcluster` looks like a
decommission and is not one.

The downstream cluster CLI now has the missing half: `nixclusterctl <cluster>
teardown` runs `converge` in reverse (workloads off, members leave, module cleanup,
switch back to the base NixOS configuration), is safe to repeat, reports an
unreachable member as `Skipped` instead of failing, and emits the same
`::nixcluster:result::` line as `converge`. NIO can now hold the finalizer until
that run has happened.

## What Changes

- New field `spec.deletionPolicy`: `Teardown` (default) or `Retain`. `Retain` is
  today's behaviour. **Behaviour change:** existing clusters read back with the
  default `Teardown`, so deleting them after the upgrade runs a teardown.
- On deletion with `Teardown` the finalizer stays until a teardown run succeeds:
  1. the converge child stops scheduling (native CronJob `suspend`, trigger on
     change off) and NIO waits until the suspension reached the CronJob and no
     converge Job is active;
  2. NIO creates one owned `NixJob` `<cluster>-teardown` from the converge child —
     same source, node files, store, builder, SSH and age key mounts — with the
     arguments `teardown --auto-approve`;
  3. a finished run's `::nixcluster:result::` line is read from the pod log and
     recorded per member in `status.teardown`;
  4. success (`success` or `partial`) deletes the converge child and removes the
     finalizer;
  5. failure keeps the finalizer, sets `TeardownFailed` with the message, and
     retries with a capped exponential backoff by replacing the failed `NixJob`.
- Changing `deletionPolicy` to `Retain` on a cluster that is being deleted releases
  the finalizer without a teardown — the escape hatch for a teardown that cannot
  succeed.
- New phase `TearingDown` and condition `TearingDown`, visible in `kubectl get
  nixcluster`.
- No converge child (NIO never converged anything, or a previous teardown already
  removed it) means there is nothing to tear down: the finalizer is released.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `cluster-convergence`: the deletion requirement changes from "remove the converge
  child" to a deletion policy with an owned teardown run; the phase requirement
  gains `TearingDown`.

## Impact

- `api/v1alpha1/nixcluster_types.go`: `spec.deletionPolicy`, `status.teardown`,
  phase `TearingDown`; regenerated CRD and deepcopy.
- `internal/controller/nixcluster_controller.go`: the deletion path; new RBAC for
  `nixjobs`, `jobs`, `cronjobs`, `pods` and `pods/log`.
- `cmd/main.go`: the reconciler gets a pod log reader (an uncached clientset) so the
  result line can be read.
- Depends on a downstream flake whose cluster app implements `teardown`. A flake
  without it fails the teardown run, which keeps the finalizer until the policy is
  switched to `Retain`.
- Out of scope, found while writing this: `nix.suspend` on a `NixCronJob` pauses
  NIO's reconciliation but does not suspend the projected `batch/v1` CronJob, so a
  "suspended" child keeps firing runs. The teardown path avoids it by suspending the
  native CronJob instead; the builder preflight's suspend still has the problem.
