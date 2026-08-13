---
title: NixJob
sidebar_position: 8
description: Runs a Nix flake attribute to completion as a batch/v1 Job.
---

# NixJob

Runs a flake installable to completion. Compiles to a `batch/v1 Job`. Namespaced.
Short name `nixj`.

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixJob
metadata:
  name: migrate
  namespace: apps
spec:
  nix:
    source:
      gitRepo: https://github.com/example/services.git
      ref: main
    run: ".#migrate"
  jobTemplate:
    backoffLimit: 2
```

## `spec`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `nix` | [NixSpec](./nixspec.md) | yes | Source, revision tracking, build and run configuration. |
| `jobTemplate` | `batch/v1` JobSpec | no | The native JobSpec, verbatim. |

`jobTemplate` is **schemaless** in the CRD, so you may omit fields upstream marks
required — notably `template.spec.containers` — and the reconciler fills them in.
A minimal `NixJob` is just a `nix` block.

The operator owns the app container, the init-containers, the nix volumes and the
managed labels; everything else in your `jobTemplate` passes through untouched.

## Run semantics

One run-Job is created per composite revision, named `<name>-<revision>` — for
example `migrate-r-a1b2c3d4e5f6`.

`spec.nix.triggerOnChange` defaults to **`true`** for this kind: a new revision
creates a fresh run-Job. Setting it to `false` gives run-once semantics — no new
run-Job is created if any already exists.

:::warning An existing run-Job is never mutated
The Job name derives from the composite revision hash, which covers only the
resolved revision, `spec.nix.run` and `spec.nix.args`.

Change **only** `prebuild`, `nixFlags`, `image` or `additionalFiles` and the hash
is unchanged, so the Job name is unchanged, so **no new Job is created and your
change does not run**.

To force a new run, change `run`, `args`, or the revision. See
[Revisions and rollout](../concepts/revisions.md#the-composite-revision).
:::

## `status`

Embeds the shared workload status:

| Field | Type | Description |
| --- | --- | --- |
| `observedGeneration` | integer | Last reconciled generation. |
| `phase` | enum | `Pending`, `Resolving`, `Building`, `Progressing`, `Ready`, `Degraded`, `Failed`, `Suspended`. |
| `resolvedRevision` | string | The SHA `ref` currently points to. |
| `lastPolledTime` | timestamp | When `ref` was last resolved. |
| `rolledOutRevision` | string | The SHA stamped into the current pod template. |
| `workloadRef` | string | Name of the owned native object. |
| `conditions` | []Condition | `Ready`, `Reconciling`, `Stalled`, `GitSynced`, `Progressing`. |

Plus kind-specific fields:

| Field | Type | Description |
| --- | --- | --- |
| `activeJob` | string | Name of the run-Job for the current revision. |
| `lastRunTime` | timestamp | When the last run started. |
| `succeeded` | integer | Successful completions. |
| `failed` | integer | Failed completions. |

## Use as an orchestrator child

[`NixosConfiguration`](./nixosconfiguration.md) creates `NixJob` children for
both the `nixos-anywhere` install and the decommission run. The decommission
child is created **without an owner reference** so it survives its parent's
deletion.

## Print columns

```text
NAME   PHASE   REVISION   SUCCEEDED   AGE
```

*Verified against kitsunoff/NIO@e0dbe5e.*
