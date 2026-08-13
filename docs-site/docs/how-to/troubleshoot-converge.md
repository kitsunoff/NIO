---
title: Troubleshoot a failed converge
sidebar_position: 5
description: Work down from a Degraded or Blocked parent to the pod that actually failed.
---

# Troubleshoot a failed converge

## The one rule

**Orchestrators do not do the work; their children do.** A `NixosConfiguration`
or `NixCluster` that reports a problem is reporting *its child's* problem. The
logs you want are always in a pod, several objects down:

```text
NixosConfiguration / NixCluster      <- the phase you noticed
        |
        v
NixCronJob / NixJob                  <- the phase with the real reason
        |
        v
batch/v1 CronJob -> Job              <- the failure count
        |
        v
Pod                                  <- the logs
```

Work down that chain in order. Most time is lost by people staring at the top
object.

## Step 1: read the parent's reason, not just its phase

```sh
kubectl get nixosconfiguration node-a --namespace infra \
  --output jsonpath='{.status.phase}{"\n"}{range .status.conditions[*]}{.type}{"="}{.status}{" ("}{.reason}{") "}{.message}{"\n"}{end}'
```

The phase tells you which branch you are in; the reason tells you why.

| Phase | Reason | Meaning | Go to |
| --- | --- | --- | --- |
| `Blocked` | `MachineNotReady` | Target `Machine` missing or not `Discoverable` | Step 2 |
| `Blocked` | `MachineInUse` | Another config owns this `Machine` | Step 3 |
| `Blocked` | `BuilderSystemMismatch` | (`NixCluster`) builder cannot build a member's architecture | Step 4 |
| `Degraded` | — | Install exhausted retries, or the day-two cron is failing | Step 5 |
| `Converging` | — | Cron exists, no successful run recorded **yet** | Step 5 |

Note `Converging` is not itself an error. It means no run has *succeeded* yet —
which on a first converge is normal for as long as the build takes.

## Step 2: `Blocked` / `MachineNotReady`

The parent is refusing to proceed because the host is not reachable.

```sh
kubectl get machine node-a --namespace infra \
  --output jsonpath='{.status.discoverable}{" "}{range .status.conditions[*]}{.type}{"="}{.status}{"("}{.reason}{") "}{end}{"\n"}'
```

- **`SSHFailed`** — the operator's pods cannot reach `spec.host:22`, or the host
  rejected the key. Check routing from the operator namespace and the key itself.
- **`CredentialsMissing`** — the referenced Secret is absent, or does not contain
  a key named exactly `ssh-privatekey`.

```sh
kubectl get secret host-ssh-key --namespace infra \
  --output jsonpath='{range $k, $v := .data}{$k}{"\n"}{end}'
```

Reachability is re-probed every 60 seconds, so a fix should clear within a minute
without any action from you.

:::note A password-only Machine cannot be converged
`sshPasswordSecretRef` is enough for discovery and the architecture scan, but the
apply path mounts a private key and requires `sshKeySecretRef`. A `Machine` that
is `Discoverable` by password will still fail to produce children.
:::

## Step 3: `Blocked` / `MachineInUse`

Two configurations target one `Machine`. The **earliest-created** one wins (ties
broken lexicographically by name); the other is blocked and its day-two cron is
suspended, deliberately, so the two cannot race `nixos-rebuild` on one host.

```sh
kubectl get nixosconfiguration --namespace infra \
  --output custom-columns='NAME:.metadata.name,TARGET:.spec.machineRef.name,CREATED:.metadata.creationTimestamp,PHASE:.status.phase'
```

Delete whichever one is wrong. The survivor unblocks on its next reconcile.

## Step 4: `Blocked` / `BuilderSystemMismatch`

`NixCluster` only. The builder's declared `spec.systems` does not cover a
member's observed architecture, and because delegation sets `max-jobs = 0` there
is no local fallback — so the operator refuses up front and suspends the converge
child rather than letting it burn its deadline.

```sh
kubectl get nixcluster prod --namespace infra \
  --output jsonpath='{.status.conditions[?(@.type=="Ready")].message}{"\n"}'
```

Compare the two sides:

```sh
kubectl get nixbuilder builder --namespace infra --output jsonpath='{.spec.systems}{"\n"}'
kubectl get machines --namespace infra \
  --output custom-columns='NAME:.metadata.name,ARCH:.status.hardwareFacts.architecture'
```

Fix by adding the missing system to the builder's `spec.systems` — and making
sure the builder can genuinely produce it — or by dropping `builderRef`.

The converge child was suspended by the operator; it un-suspends automatically on
the next clean reconcile. You do not need to edit the child.

## Step 5: `Degraded` — go to the child

```sh
kubectl get nixcronjob node-a-day2 --namespace infra \
  --output jsonpath='{.status.phase}{" rolledOut="}{.status.rolledOutRevision}{"\n"}{range .status.conditions[*]}{.type}{"="}{.status}{"("}{.reason}{") "}{.message}{"\n"}{end}'
```

Two distinct shapes:

**`Stalled` with reason `InfraNotReady`** — the child cannot start because a
referenced `NixStore` or `NixBuilder` is missing or not Ready. Nothing has run.

```sh
kubectl get nixstore,nixbuilder --namespace infra
```

For a `NixCluster` this is why members keep their previous status instead of
being marked `Failed`: a converge that never ran has not failed at anything.

**A recorded failure time** — runs are happening and failing:

```sh
kubectl get nixcronjob node-a-day2 --namespace infra \
  --output jsonpath='lastSchedule={.status.lastScheduleTime} lastSuccess={.status.lastSuccessfulTime} lastFailed={.status.lastFailedTime}{"\n"}'
```

`lastFailedTime` is what distinguishes "scheduled and working" from "scheduled
and failing every time" — a CronJob keeps scheduling regardless of outcomes, so a
recent `lastScheduleTime` proves nothing.

## Step 6: the pod logs

```sh
kubectl get jobs --namespace infra \
  --selector nio.homystack.com/workload-name=node-a-day2 \
  --sort-by=.status.startTime \
  --output custom-columns='NAME:.metadata.name,SUCCEEDED:.status.succeeded,FAILED:.status.failed'

kubectl get pods --namespace infra --selector job-name=<job-name>
```

Check the init-containers **in order**, because a failure in an early one means
the later ones never ran:

```sh
kubectl logs --namespace infra <pod> --container bootstrap
kubectl logs --namespace infra <pod> --container fetch-source
kubectl logs --namespace infra <pod> --container inject-files   # only if additionalFiles is set
kubectl logs --namespace infra <pod> --container instantiate
kubectl logs --namespace infra <pod>                            # the app container
```

| Fails in | Usually means |
| --- | --- |
| `fetch-source` | Bad repository URL, bad ref, missing or wrong `credentialsRef`, or a private repo over the wrong protocol |
| `inject-files` | An `additionalFiles` entry references a missing ConfigMap/Secret key, or a path escaping the tree |
| `instantiate` | A genuine Nix build failure, or the builder cannot build this system |
| the app container | The rebuild itself — this is where `nixos-rebuild` output lives |

## Common failures and what they mean

### `Too many authentication failures`

Seen during the final apply to the target host, in the app container. The SSH
client offered too many identities. NIO's design deliberately keeps the builder
key in the `builders=` machine spec so that `NIX_SSHOPTS` can carry only the
target-host key. Seeing this means both keys are being offered to the target;
see [the design note](../design/store-builder-ssh.md).

### The build runs locally when you asked for a builder

Check for `max-jobs = 0` in the pod's `NIX_CONFIG`. If `builders` is absent, the
builder never resolved.

### The flake attribute is not found

`spec.flake` is concatenated as `"." + flake` with no separator. A value of
`node-a` produces the invalid `.node-a`. It must be `#node-a`.

### Nothing gets faster on repeat runs

Not a failure — a misconfiguration. See
[Accelerate a converge](./accelerate-converge.md).

### An install stopped retrying

The install path retries **3 times** and then sits `Degraded` with no requeue and
no child recreation. It will not resume on its own; change the spec to restart
it.

### A `NixosConfiguration` will not delete

It should always finish eventually: if the decommission job exhausts its 3
retries, or cannot even be built, the operator removes the finalizer anyway
rather than leaving the object stuck. If one is genuinely wedged, check for the
orphan decommission job, which is intentionally created **without** an owner
reference so it survives its parent:

```sh
kubectl get nixjob --namespace infra --selector nio.homystack.com/operation=decommission
```

## Events

The orchestrator emits events for the decommission path in particular:

```sh
kubectl describe nixosconfiguration node-a --namespace infra
```

Look for `OnRemoveFlakeSucceeded`, `OnRemoveFlakeFailed` and
`OnRemoveFlakeSkipped`.
