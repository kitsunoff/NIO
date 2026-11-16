## Context

A `NixCluster` owns one converge `NixCronJob` that runs `.#cluster-<name> converge`.
Deletion used to drop that child and the finalizer. The cluster CLI now provides
`teardown`, with the same result line as `converge`. This change runs it from the
deletion path.

## Decisions

### The teardown run is a `NixJob` built from the converge child

The converge child already carries everything a run against the members needs: the
flake source, the rendered node files, the store and builder references, and the pod
template with the SSH and age keys. The teardown `NixJob` copies it and changes only
the arguments, so teardown reaches exactly the members the last converge reached.
Re-running selection during deletion was rejected: Machines may already be gone, and
the members that matter are the ones that were converged.

The `NixJob` sets `triggerOnChange: false` (one run, even if the revision moves) and
`backoffLimit: 0` (the controller owns retries, so a failure is visible at once
instead of after six pod restarts).

### Waiting for converge

`nix.suspend` on a `NixCronJob` pauses NIO's reconciliation of it, so the projected
CronJob keeps its old spec and keeps scheduling. The teardown path therefore sets the
native `cronJobTemplate.suspend` and turns `triggerOnChange` off, which the
`NixCronJob` controller projects as usual. The teardown `NixJob` is created only when

- the projected CronJob reports `suspend: true` (or does not exist), and
- no Job controlled by the CronJob or by the `NixCronJob` is still active.

Active Jobs are read from the Jobs themselves, not from the child's
`status.activeJobs`, which lags by one reconcile of the child.

### Retry semantics

`status.teardown.attempts` counts created teardown `NixJob`s and
`status.teardown.failedAttempts` counts recorded failures.

- A failed run is recorded once: when `failedAttempts < attempts`, the controller
  records the failure, sets `TeardownFailed` and stamps `lastFailureTime`.
- The failed `NixJob` is kept during the backoff so its pod log stays readable.
- Backoff is `30s * 2^(failedAttempts-1)`, capped at 10 minutes. When it has
  elapsed, the failed `NixJob` is deleted; once it is gone, the next reconcile creates
  a fresh one and increments `attempts`.
- There is no attempt limit. Teardown steps are idempotent, and the way out of a
  teardown that can never succeed is `deletionPolicy: Retain`.
- The name is fixed (`<cluster>-teardown`), so at most one teardown run exists at a
  time; a create that races another reconcile gets `AlreadyExists` and is a no-op.

### Reading the result

The controller cannot see the pod's stdout through the API objects, so the
reconciler reads the app container's log of the run's pod through an uncached
clientset (`pods` list, `pods/log` get). The log merges stdout and stderr, so the
parser scans from the end for the `::nixcluster:result::` marker and, as a fallback,
for a bare JSON object with `cluster` and `result` — the shape `--json` prints. The
run is started without `--json` because the marker is what makes the line findable in
a merged log, and because the converge child is started the same way.

The Job's exit status is the authority on success. The CLI exits non-zero only for
`failed` and `cancelled`; `partial` (unreachable members skipped) exits zero and
releases the finalizer with a warning event that names the skipped members. When the
log cannot be read (pod already gone, no permission), the outcome still follows the
exit status and `status.teardown.message` says the result is unavailable.

### Phase and conditions

During deletion with `Teardown` the phase is `TearingDown`. `TearingDown` is `True`
with a reason naming what the controller waits for (`WaitingForConverge`,
`Running`, `RetryScheduled`). `TeardownFailed` is `True` after a failed run until a
later run succeeds. `Ready` is `False` with reason `TearingDown`.

## Risks

- Existing clusters default to `Teardown` after the upgrade. Deleting one now
  removes k3s/Incus from its members. The README and the release notes must say so.
- A partial teardown releases the finalizer. A member that was unreachable keeps
  its cluster state; the warning event is the only record once the object is gone.
