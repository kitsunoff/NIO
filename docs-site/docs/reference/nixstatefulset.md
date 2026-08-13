---
title: NixStatefulSet
sidebar_position: 11
description: Runs a Nix flake attribute as an ordered apps/v1 StatefulSet.
---

# NixStatefulSet

Runs a flake installable as an ordered, stateful workload that rolls out on a new
revision. Compiles to an `apps/v1 StatefulSet`. Namespaced. Short name `nixsts`.

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixStatefulSet
metadata:
  name: db
  namespace: apps
spec:
  nix:
    source:
      gitRepo: https://github.com/example/services.git
      ref: main
    run: ".#db"
    storeRef:
      name: store
  statefulSetTemplate:
    serviceName: db
    replicas: 3
    volumeClaimTemplates:
      - metadata:
          name: data
        spec:
          accessModes: [ReadWriteOnce]
          resources:
            requests:
              storage: 50Gi
```

## `spec`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `nix` | [NixSpec](./nixspec.md) | yes | Source, revision tracking, build and run configuration. |
| `statefulSetTemplate` | `apps/v1` StatefulSetSpec | **yes** | The native StatefulSetSpec, verbatim. |

`statefulSetTemplate` is **required** because `serviceName` has no default.

`serviceName`, `volumeClaimTemplates`, `updateStrategy`, `podManagementPolicy`
and `replicas` all live natively inside it — there are no NIO-specific
duplicates.

It is **schemaless**, so you may omit the otherwise-required `selector` and pod
containers and let the reconciler fill them in.

### What the operator owns

- the app container's `image` and `command`
- the injected init-containers
- the nix and workspace volumes and mounts
- the pod-template revision annotation and managed labels
- the `selector`, when unset

Your `volumeClaimTemplates` are yours and are untouched. Note the operator's own
pod-local `/nix` volume is separate from them and is shaped by
[`spec.nix.localStore`](./nixspec.md#localstore).

## Revision behaviour

:::danger `spec.nix.triggerOnChange` has no effect on this kind
The `NixStatefulSet` controller **never reads** `triggerOnChange`. The pod
template is re-rendered and written on every reconcile, so a new revision always
rolls out, subject to the native `updateStrategy`.

To hold it still, pin `spec.nix.source.rev` or set `spec.nix.suspend: true`. To
control the *pace* of a rollout, use the native `updateStrategy` — including
`partition` for a staged rollout.
:::

## `status`

Embeds the shared workload status (`observedGeneration`, `phase`,
`resolvedRevision`, `lastPolledTime`, `rolledOutRevision`, `workloadRef`,
`conditions` — see [NixJob](./nixjob.md#status)), plus:

| Field | Type | Description |
| --- | --- | --- |
| `readyReplicas` | integer | Ready replicas of the owned StatefulSet. |
| `updatedReplicas` | integer | Replicas on the current revision. |
| `currentRevision` | string | The StatefulSet's own controller revision, **not** NIO's composite revision. |
| `updateRevision` | string | The StatefulSet's target controller revision. |

:::note Two different notions of "revision" appear in this status
`resolvedRevision` and `rolledOutRevision` are **NIO's** — a git SHA and the
composite hash derived from it.

`currentRevision` and `updateRevision` are **Kubernetes'** StatefulSet controller
revisions, hashes of the pod template. They are not comparable to each other; do
not expect `currentRevision` to look like a commit SHA.
:::

An ordered rollout combined with per-pod Nix builds means a stuck build blocks
the whole rollout at that ordinal. Give a `NixStatefulSet` a warm
[`NixStore`](./nixstore.md) and [`NixBuilder`](./nixbuilder.md) before scaling
it up.

## Print columns

```text
NAME   PHASE   REVISION   READY   AGE
```

*Verified against kitsunoff/NIO@e0dbe5e.*
