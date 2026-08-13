---
title: NixDeployment
sidebar_position: 10
description: Runs a Nix flake attribute as a rolling apps/v1 Deployment.
---

# NixDeployment

Runs a flake installable as a long-running service that rolls out on a new
revision. Compiles to an `apps/v1 Deployment`. Namespaced. Short name
`nixdeploy`.

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixDeployment
metadata:
  name: api
  namespace: apps
spec:
  nix:
    source:
      gitRepo: https://github.com/example/services.git
      ref: main
    run: ".#api"
    storeRef:
      name: store
    builderRef:
      name: builder
  deploymentTemplate:
    replicas: 3
```

## `spec`

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `nix` | [NixSpec](./nixspec.md) | yes | Source, revision tracking, build and run configuration. |
| `deploymentTemplate` | `apps/v1` DeploymentSpec | no | The native DeploymentSpec, verbatim. |

`deploymentTemplate` is **schemaless**, so you may omit fields upstream marks
required — `selector` and `template.spec.containers` — and the reconciler fills
them in. A minimal `NixDeployment` is just a `nix` block.

### What the operator owns

Set on every reconcile, overwriting whatever you wrote:

- the app container's `image` and `command`
- the three or four injected init-containers
- the nix and workspace volumes and mounts
- the pod-template revision annotation and managed labels
- the `selector`, when unset
- a surge-only update strategy, when unset

Everything else passes through untouched: affinity, topology spread constraints,
sidecar containers, extra volumes, probes, resources.

The surge-only default matters for Nix workloads — a new pod must build before it
is ready, so allowing unavailability during a rollout would take capacity away
for the whole build.

## Revision behaviour

:::danger `spec.nix.triggerOnChange` has no effect on this kind
The `NixDeployment` controller **never reads** `triggerOnChange`. The pod
template — including its new revision annotation — is re-rendered and written on
every reconcile, so a new revision always rolls out.

Setting `triggerOnChange: false` does not pin the deployment. To hold it still,
pin `spec.nix.source.rev` or set `spec.nix.suspend: true`.
:::

Rollout is triggered by the `nio.homystack.com/revision` annotation changing on
the pod template, which Kubernetes then handles as an ordinary rolling update.

## `status`

Embeds the shared workload status (`observedGeneration`, `phase`,
`resolvedRevision`, `lastPolledTime`, `rolledOutRevision`, `workloadRef`,
`conditions` — see [NixJob](./nixjob.md#status)), plus:

| Field | Type | Description |
| --- | --- | --- |
| `readyReplicas` | integer | Ready replicas of the owned Deployment. |
| `updatedReplicas` | integer | Replicas on the current revision. |
| `availableReplicas` | integer | Available replicas. |

The `Progressing` condition reflects the owned Deployment being mid-rollout.

Note that a pod which is still **building** counts as not-ready: the
`instantiate` init-container has not finished. On a first rollout with no warm
store or builder, expect `readyReplicas` to sit at zero for as long as the build
takes. The controller observes init-container state to distinguish building from
crash-looping.

## Print columns

```text
NAME   PHASE   REVISION   READY   AGE
```

*Verified against kitsunoff/NIO@e0dbe5e.*
