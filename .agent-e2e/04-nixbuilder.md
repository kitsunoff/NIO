# E2E — `NixBuilder`

> Read `00-harness-and-conventions.md` and `03-nixstore.md` first — the builder
> only earns its keep together with a `NixStore`.

## What the controller does (recap)

`NixBuilder` = a namespace-scoped **single-worker remote Nix build backend**, run
as a StatefulSet with **exactly 1 replica** (hardcoded — no `replicas` field, no
pool). The `builder` container runs a dropbear sshd (:22) that accepts remote
`nix build` dispatch. Per ADR-0008 the builder does **not** push results itself —
the runner pod that dispatched the build holds the shared SSH key and pushes the
realized closure into the `NixStore`.

Owned resources (owner ref → NixBuilder): headless Service `<name>`
(`ClusterIP: None`, port `ssh` 22) and StatefulSet `<name>`. It owns **no**
Secret — when `storeRef` is set it *mounts* the store's `<storeRef.name>-ssh`
Secret.

Spec: `storeRef` (`*LocalObjectReference`, the NixStore to realize into),
`image`, `maxJobs` (`*int32` → env `NIX_MAX_JOBS`), `storage`
(`*PVCSpec`; nil → emptyDir, set → volumeClaimTemplate `nix-store`), `template`.
**Declared but ignored by the controller in v1alpha1**: `spec.systems` — do
**not** assert behavior driven by it.

Status: `phase` (`Pending`/`Ready`/`Degraded`), `builderEndpoint`
(`ssh-ng://root@<name>.<ns>.svc`), **`ready` boolean**, `conditions`.
Ready gate: `sts.status.readyReplicas >= 1` → `Ready`/`BuilderReady`, phase
`Ready`, `ready=true`. Failure → phase `Degraded`, `Stalled=True`+`Ready=False`,
reason ∈ `ServiceError`, `StatefulSetError`. Requeue **30s**.

## Critical wiring fact for e2e

There is **no explicit controller gate** that blocks the builder on store
readiness. But when `storeRef` is set, the builder pod mounts Secret
`<storeRef.name>-ssh` (created by the NixStore controller). If that Secret does
not exist yet, the pod **fails to mount** and never becomes ready. So ordering is
enforced implicitly: **create/ready the NixStore before the NixBuilder.** The
workloads `BeforeAll` already does this (store Ready → then builder).

## Scenarios to cover

### S1 — Builder reaches Ready behind a Ready store (happy path; partly covered)

Already exercised by the workloads `BeforeAll` (waits `.status.ready == true`).
Make explicit:

1. With `store` already Ready (see `03-nixstore.md` S1), apply:
   ```yaml
   apiVersion: nio.homystack.com/v1alpha1
   kind: NixBuilder
   metadata: {name: builder, namespace: nio-workloads}
   spec:
     storeRef: {name: store}
   ```
2. Assert `Eventually` (6m):
   - `.status.ready == true`;
   - `.status.phase == "Ready"`;
   - condition `Ready` = `True`, reason `BuilderReady`;
   - `.status.builderEndpoint == "ssh-ng://root@builder.nio-workloads.svc"`;
   - StatefulSet `builder` has `spec.replicas == 1`;
   - the pod mounts volume `nio-ssh` (from Secret `store-ssh`).

### S2 — Builder blocks when its store's SSH Secret is absent (ordering)

1. In a **fresh** namespace (no NixStore yet, or a store that has not reconciled
   its `-ssh` Secret), apply a NixBuilder with `storeRef: {name: store}`.
2. Assert the builder does **not** become `ready` (pod stuck on missing-Secret
   mount) — `.status.ready` stays `false`, phase `Pending`.
3. Create the NixStore, wait for its `store-ssh` Secret, then assert the builder
   converges to `ready=true`. This documents the implicit gate.

### S3 — Delegated non-cached build → realized into store (headline; covered today)

This is the existing `nixworkloads_test.go` scenario "delegates a non-cached
build to the NixBuilder and realizes it into the NixStore". It uses a flake with
a unique marker guaranteed not to be in any public cache:

1. Apply a `NixJob` with both `storeRef: {name: store}` and
   `builderRef: {name: builder}` pointing at a marker flake.
2. Assert the NixJob reaches `.status.phase == "Ready"` (12m) — the build ran on
   the builder, not locally.
3. Assert the realized path landed in the store:
   `kubectl exec store-0 -c store -- ls -d /nix/store/*nio-e2e-app`.
4. **Extension**: run a **second** workload requesting the same derivation and
   assert it substitutes from the store (cache hit, fast, no rebuild) — proves
   the round trip (build on builder → push to store → substitute by peer).

### S4 — `maxJobs` propagates

1. Apply a NixBuilder with `maxJobs: 2`.
2. Assert the `builder` container env contains `NIX_MAX_JOBS=2`
   (`kubectl get sts builder -o jsonpath=...` on the container env).

### S5 — Persistent vs ephemeral store backing

1. `storage` set → StatefulSet has one volumeClaimTemplate `nix-store`, PVC binds;
   build cache survives a pod restart.
2. `storage` unset → pod-local `nix-store` **emptyDir**; cache lost on restart
   (document, do not treat as failure).

### S6 — `storeRef == nil` builder (negative)

1. Apply a NixBuilder with no `storeRef`.
2. Assert it still reaches `ready=true` (controller has no gate) **but** the pod
   has no `nio-ssh` mount and no `authorized_keys`, so a remote-build dispatch to
   `builderEndpoint` would be rejected. Document: a builder is only useful with a
   `storeRef`.

### S7 — Degraded on service/statefulset failure

Hard to force cleanly on Kind; if reproducible, assert phase `Degraded`,
`Stalled=True` with reason `ServiceError`/`StatefulSetError`, `ready=false`.

## Assertions cheat-sheet

| What | jsonpath |
| --- | --- |
| ready | `{.status.ready}` |
| phase | `{.status.phase}` |
| Ready reason | `{.status.conditions[?(@.type=='Ready')].reason}` |
| builderEndpoint | `{.status.builderEndpoint}` |
| replicas (sts) | `kubectl get sts builder -o jsonpath={.spec.replicas}` |

## Out of scope / do not assert

- Any behavior from `spec.systems` (declared but unused).
- A builder pool / >1 replica (fixed at 1 by design in v1alpha1).

## Suggested placement

Reuse the workloads suite fixtures (`store` + `builder` in `nio-workloads`). S2
needs its own fresh namespace to exercise the ordering gate. S3 already exists —
extend it with the S3.4 cache-hit assertion.
