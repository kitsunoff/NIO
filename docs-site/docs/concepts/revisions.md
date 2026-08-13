---
title: Revisions and rollout
sidebar_position: 4
description: How NIO decides a revision changed, and what each kind does about it.
---

# Revisions and rollout

## Three ways to name a revision

`spec.nix.source` supports three mutually-exclusive modes, and the mode decides
whether NIO polls anything at all:

| Mode | Set | Behaviour |
| --- | --- | --- |
| **Tracking** | `gitRepo` + `ref` | The operator runs `git ls-remote` on an interval and resolves `ref` to a commit SHA. `ref` defaults to `main`. |
| **Pinned** | `rev` | Short-circuits everything. The SHA is used verbatim; no git contact, no polling. For GitOps-pinned deployments. |
| **Flux** | `fluxSourceRef` | The operator reads `status.artifact` off a Flux source object and uses its revision and tarball URL. `gitRepo`, `ref` and `credentialsRef` are ignored. |

The Flux mode accepts `GitRepository`, `OCIRepository` or `Bucket` — all three
expose the same `status.artifact` contract, and NIO never inspects the
type-specific spec. This means Flux owns authentication, verification and
retries; NIO just consumes the artifact.

:::note `pollInterval` is applied more broadly than documented
The field's own doc comment says it is ignored when `rev` or `fluxSourceRef` is
set. In practice the controller uses it as the **unconditional requeue cadence**
for the workload in all three modes. With a pinned `rev` it does not cause any
git traffic — there is nothing to poll — but it does still set how often the
object is reconciled.
:::

## The composite revision

A rollout is keyed on a short hash stamped into the generated pod template as
both a label and an annotation, `nio.homystack.com/revision`:

```text
r-a1b2c3d4e5f6
```

It is a SHA-256 over exactly **three** inputs, length-prefixed so that field
boundaries cannot be forged:

1. the resolved revision (the pinned SHA, the Flux artifact revision, or the
   `ls-remote` result)
2. `spec.nix.run`
3. every element of `spec.nix.args`

Changing the annotation is what triggers the native rolling update.

:::warning What is not in the hash
`prebuild`, `nixFlags`, `image`, `containerName`, `additionalFiles` content,
`source.dir`, `localStore`, and the resolved store/builder endpoints are **not**
hashed.

For `NixDeployment` and `NixStatefulSet` this is mostly harmless: the pod
template is rewritten on every reconcile anyway, so a real change to any of
those still produces a pod-spec diff and a native rollout.

For **`NixJob` it matters**. The run-Job's *name* is derived from this hash, and
an existing Job is never mutated. Change only `prebuild` or `nixFlags` on a
`NixJob` and the name is unchanged, so no new Job is created and your change
does not run. Change `run`, `args`, or the revision to force a new run.
:::

## What each kind does with a new revision

`spec.nix.triggerOnChange` controls the reaction — but its effect is **not
uniform across kinds**, and two kinds ignore it entirely:

| Kind | Default | Effect of the field |
| --- | --- | --- |
| `NixJob` | `true` | `false` means: do not create a new run-Job if any run-Job already exists. Genuine run-once semantics. |
| `NixCronJob` | `false` | `true` means: in addition to the schedule, fire an immediate one-off Job when the revision changes. |
| `NixDeployment` | — | **Field has no effect.** The controller never reads it. |
| `NixStatefulSet` | — | **Field has no effect.** The controller never reads it. |

For the two long-running kinds the pod template — including its new revision
annotation — is re-rendered and written on every reconcile, so a new revision
always rolls out. Setting `triggerOnChange: false` on a `NixDeployment` does not
pin it; there is no supported way to hold a `NixDeployment` on an old revision
other than pinning `source.rev` or setting `suspend`.

Note also that for a `NixCronJob`, the projected CronJob's job template is
re-pinned to the new revision **regardless** of `triggerOnChange`. The flag only
governs the extra immediate Job. So the next scheduled tick runs the new revision
either way.

## Holding a revision still

The reliable ways to stop a rollout:

- **`source.rev`** — pin an exact SHA. Nothing polls, nothing changes until you
  edit the object. This is the GitOps-friendly answer.
- **`spec.nix.suspend: true`** — stop reconciliation entirely.

## Next

- [Store and builder](./store-and-builder.md).
- [Reference: the shared `nix` block](../reference/nixspec.md).
