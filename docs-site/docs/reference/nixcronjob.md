---
title: NixCronJob
sidebar_position: 9
description: Runs a Nix flake attribute on a schedule as a batch/v1 CronJob.
---

# NixCronJob

Runs a flake installable on a schedule. Compiles to a `batch/v1 CronJob`.
Namespaced. Short name `nixcron`.

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixCronJob
metadata:
  name: nightly
  namespace: apps
spec:
  nix:
    source:
      gitRepo: https://github.com/example/services.git
      ref: main
    run: ".#report"
  cronJobTemplate:
    schedule: "0 2 * * *"
    concurrencyPolicy: Forbid
```

## `spec`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `nix` | [NixSpec](./nixspec.md) | yes | Source, revision tracking, build and run configuration. |
| `cronJobTemplate` | `batch/v1` CronJobSpec | **yes** | The native CronJobSpec, verbatim. |

`cronJobTemplate` is **required** because `schedule` has no default. Scheduling,
`concurrencyPolicy`, `suspend`, history limits and the embedded `jobTemplate` all
live natively inside it — there are no NIO-specific duplicates.

It is **schemaless**, so a minimal workload may omit the otherwise-required pod
containers and let the reconciler fill them in.

The operator pins the embedded job template to the latest resolved revision and
owns the app container, init-containers and volumes.

:::note Two `suspend` fields exist and they are different
`spec.nix.suspend` pauses **NIO's** reconciliation entirely — no resolving, no
rendering.

`spec.cronJobTemplate.suspend` is the native Kubernetes CronJob field, which
stops the *schedule* while NIO keeps reconciling.
:::

## Revision behaviour

`spec.nix.triggerOnChange` defaults to **`false`** for this kind. Setting it to
`true` fires an immediate one-off Job named `<name>-<revision>-manual` when the
revision changes, in addition to the schedule.

:::note The schedule always runs the new revision
The projected CronJob's job template is re-pinned to the new revision
**regardless** of `triggerOnChange`. The flag only governs the extra immediate
Job — the next scheduled tick runs the new revision either way.
:::

## `status`

Embeds the shared workload status (`observedGeneration`, `phase`,
`resolvedRevision`, `lastPolledTime`, `rolledOutRevision`, `workloadRef`,
`conditions` — see [NixJob](./nixjob.md#status)), plus:

| Field | Type | Description |
| --- | --- | --- |
| `lastScheduleTime` | timestamp | When a run was last scheduled. |
| `lastSuccessfulTime` | timestamp | When a run last succeeded. |
| `lastFailedTime` | timestamp | When a run last failed. |
| `activeJobs` | []string | Currently running Jobs. |

:::tip `lastFailedTime` is the field that matters for health
A CronJob keeps scheduling regardless of whether its runs succeed, so a recent
`lastScheduleTime` proves nothing. `lastFailedTime` is what distinguishes
"scheduled and working" from "scheduled and failing every time", and consumers
such as [`NixCluster`](./nixcluster.md) map it to a failure status.
:::

## Use as an orchestrator child

Both orchestrators drive a `NixCronJob`:

| Parent | Child | Configuration |
| --- | --- | --- |
| [`NixosConfiguration`](./nixosconfiguration.md) | `<name>-day2` | `nixos-rebuild switch`, `triggerOnChange: true`, `concurrencyPolicy: Forbid` |
| [`NixCluster`](./nixcluster.md) | `<name>-converge` | `.#cluster-<name> converge`, `triggerOnChange: true`, `concurrencyPolicy: Forbid`, `activeDeadlineSeconds: 3600` |

`Forbid` in both cases is what stops two rebuilds racing on one host, and is why
the converge script needs only to be idempotent rather than to lock.

## Print columns

```text
NAME   PHASE   SCHEDULE   LASTSCHEDULE   AGE
```

`SCHEDULE` reads `.spec.cronJobTemplate.schedule`.

*Verified against kitsunoff/NIO@e0dbe5e.*
