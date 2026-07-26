# E2E — `NixStore`

> Read `00-harness-and-conventions.md` first.

## What the controller does (recap)

`NixStore` = a namespace-scoped **Nix binary-cache server + push target**, backed
by a PVC, run as a **StatefulSet** (default 1 replica). The pod runs `harmonia`
(binary cache over HTTP :5000) and a **dropbear** sshd (:22, ssh-ng push target).
It is the shared `/nix` that runner/builder pods substitute from and push into.

Owned resources (all with owner ref → NixStore):

- Secret `<name>-signing-key` (only when `signingKeySecretRef` is unset) — keys
  `nix-signing-key`, `nix-public-key`, ed25519, format `name:base64`.
- Secret `<name>-ssh` — shared remote-build keypair, keys `ssh-privatekey`,
  `ssh-authorized-key`.
- Headless Service `<name>` (`ClusterIP: None`), ports `http` 5000, `ssh` 22.
- StatefulSet `<name>` with volumeClaimTemplate PVC `nix-store` (= `spec.storage`),
  an init container `bootstrap` (seeds image `/nix` into the empty PVC) and a
  `store` container running harmonia.

Status: `phase` (`Pending`/`Ready`/`Degraded`), `substituterURL`
(`http://<name>.<ns>.svc:5000`), `storeURI` (`ssh-ng://root@<name>.<ns>.svc`),
`publicKey`, `readyReplicas`, `conditions`. **No `ready` boolean** (that is
NixBuilder). Steady-state requeue **30s**.

Ready gate: `sts.status.readyReplicas >= desired && desired > 0` →
`Ready`/`StoreReady` + phase `Ready`. Failure paths set phase `Degraded`,
`Stalled=True` + `Ready=False` with reason ∈ `SigningKeyError`, `SSHKeyError`,
`ServiceError`, `StatefulSetError`.

**Fields declared but ignored by the controller in v1alpha1**:
`spec.upstreamSubstituters` — do **not** assert behavior driven by it.

## Why this needs e2e

Envtest never schedules pods, so the store is stuck at `Pending` there. Only e2e
proves: PVC binds, bootstrap seeds `/nix`, harmonia actually serves, dropbear
accepts pushes, signing works, and the Ready transition fires. The workloads
suite already relies on this (it creates a `store` in `BeforeAll` and waits for
`.status.phase == Ready`).

## Prerequisite: a working StorageClass

`spec.storage` is **required**. The Kind cluster must have a default StorageClass
that provisions `ReadWriteOnce` PVCs (Kind ships `standard`). Assert the PVC binds.

## Scenarios to cover

### S1 — Store reaches Ready and serves (headline; partly covered today)

Already exercised implicitly by the workloads `BeforeAll`. Make it explicit:

1. Apply:
   ```yaml
   apiVersion: nio.homystack.com/v1alpha1
   kind: NixStore
   metadata: {name: store, namespace: nio-workloads}
   spec:
     storage: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 3Gi}}}
   ```
2. Assert `Eventually` (8m):
   - `.status.phase == "Ready"`;
   - condition `Ready` = `True`, reason `StoreReady`;
   - `.status.readyReplicas >= 1`;
   - `.status.substituterURL == "http://store.nio-workloads.svc:5000"`;
   - `.status.storeURI` contains `ssh-ng://`;
   - `.status.publicKey` is non-empty.
3. Assert the owned objects exist: StatefulSet `store`, headless Service `store`
   (`spec.clusterIP == None`), Secret `store-signing-key`, Secret `store-ssh`,
   PVC `nix-store-store-0` bound.

### S2 — harmonia actually serves the cache over HTTP

1. From a throwaway pod (or `kubectl exec` into `store-0`), `curl
   http://store.nio-workloads.svc:5000/nix-cache-info` and assert a valid
   cache-info response (contains `StoreDir: /nix/store`).
2. Optionally fetch `/<hash>.narinfo` for a path known to be in the store and
   confirm it is **signed** with `.status.publicKey`.

### S3 — Bootstrap seeding is correct and idempotent

1. Confirm the store pod's `nix` binary works (`kubectl exec store-0 -c store --
   nix --version`) — i.e. the empty PVC did not shadow the image's `/nix`.
2. Delete pod `store-0`; on restart the bootstrap init container must **not**
   re-copy (`[ -e /nix-vol/store ]` guard) and the store returns to Ready with
   the same `publicKey` (signing key Secret is stable across restarts).

### S4 — Provided signing key

1. Pre-create a Secret with `nix-public-key` (and `nix-signing-key`).
2. Apply a NixStore with `signingKeySecretRef: {name: <that secret>}`.
3. Assert **no** `<name>-signing-key` Secret is generated, and `.status.publicKey`
   equals the provided public key. Served narinfos are signed with it (tie to S2).

### S5 — Degraded on bad signing-key ref

1. Apply a NixStore with `signingKeySecretRef` to a Secret **missing**
   `nix-public-key`.
2. Assert `.status.phase == "Degraded"`, `Stalled` = `True` reason
   `SigningKeyError`, `Ready` = `False`.

### S6 — Store is the push/substitute hub for the delegated build

Cross-CRD, shared with `04-nixbuilder.md` S-build: a non-cached derivation built
on the `NixBuilder` is pushed by the runner pod into this store, and a subsequent
workload substitutes it (cache hit, no rebuild). The workloads suite already
asserts the realized path lands in the store (`ls /nix/store/*nio-e2e-app`);
this doc's contribution is verifying the **store** side: the path appears and is
served to a second consumer.

### S7 — Storage is effectively immutable (document behavior)

The update path only mutates `spec.replicas` and `spec.template`
(volumeClaimTemplates are immutable). Editing `spec.storage` after creation is
silently not applied. Assert the controller does **not** crash/loop and document
that storage cannot be resized in-place in v1alpha1.

## Assertions cheat-sheet

| What | jsonpath |
| --- | --- |
| phase | `{.status.phase}` |
| Ready reason | `{.status.conditions[?(@.type=='Ready')].reason}` |
| readyReplicas | `{.status.readyReplicas}` |
| substituterURL | `{.status.substituterURL}` |
| storeURI | `{.status.storeURI}` |
| publicKey | `{.status.publicKey}` |

## Out of scope / do not assert

- Any behavior from `spec.upstreamSubstituters` (declared but unused).
- A `ready` boolean (NixStore has none — use phase + condition).

## Suggested placement

The `store` fixture already lives in `test/e2e/nixworkloads_test.go` `BeforeAll`.
Add explicit `It(...)` blocks (S1–S3, S7) there or in a dedicated
`Describe("NixStore")`. S2 needs a curl pod (reuse the metrics curl pattern from
`e2e_test.go`).
