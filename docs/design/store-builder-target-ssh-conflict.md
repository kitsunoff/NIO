# storeRef/builderRef on a target-applying NixosConfiguration — the two-key SSH conflict (RESOLVED)

> Status: **RESOLVED** on `main` (fix squashed into `4d9075d`; originally
> `b750a34 fix(nixrender): keep builder key in builders= so target apply uses its
> own ssh key`). Guarded by `TestRenderDayTwoChildKeepsTargetKey`
> (`internal/controller/nixrender_test.go:450`). This note is a retrospective:
> what the bug was, why it happened, and the shape of the fix that shipped — so
> the same two-key reasoning carries over to the cluster converge pod, which
> reuses the identical machinery.

## Summary

The `storeRef`/`builderRef` pass-through (commit `d5166a0`) lets a
`NixosConfiguration` point its child workloads at a shared `NixStore`/`NixBuilder`.
The child **builds the system on the builder and caches into the store
correctly**, but — before the fix — the **final apply to the target host failed**:

```text
Received disconnect from <target> port 22:2: Too many authentication failures
error: failed to start SSH connection to '<target>'
Command 'nix-copy-closure --to root@<target> …nixos-system-…' returned non-zero exit status 1.
```

## Root cause (the original bug) — one global `NIX_SSHOPTS`, two SSH destinations, two keys

A store/builder-backed apply child has **two distinct SSH destinations that need
different keys**:

| Destination | Used by | Key | Mount |
| --- | --- | --- | --- |
| builder / store | nix `builders = ssh-ng://…` + store push | `store-ssh` | `/etc/nio/ssh/ssh-privatekey` |
| target host | `nixos-rebuild switch --target-host` / `nix-copy-closure` | machine key | `/etc/nio/target-ssh/ssh-privatekey` |

`NIX_SSHOPTS` is a single global env; it cannot carry a per-destination `-i`. The
orchestrator sets `NIX_SSHOPTS = -i /etc/nio/target-ssh/ssh-privatekey …` for the
target apply. The original workload-family code then **overrode** that env with
the store/builder key whenever an ssh secret was present, so the target apply
authenticated with the wrong key → `Too many authentication failures`.

This was foreseen by the design (`docs/design/nixosconfiguration-v1alpha2.md`,
Key decision 1: *"Builder + target on the same child is out of scope … that
avoids the single NIX_SSHOPTS serving two different keys."*). The pass-through
enabled exactly the excluded combo, so the code had to actually solve the two-key
problem rather than exclude it.

## Resolution (shipped) — keep the builder key in `builders=`, leave `NIX_SSHOPTS` for the target

The fix separates the two keys by **channel** instead of trying to multiplex one
`NIX_SSHOPTS`:

- **Builder/store key travels in the nix `builders=` machine-spec** (its 3rd
  field is the SSH identity), inside `NIX_CONFIG`. `ssh-ng://` build dispatch and
  store push pick it up from there — they never need it in `NIX_SSHOPTS`.
- **The app container's `NIX_SSHOPTS` is left for the target key.** The workload
  family no longer stamps `-i <infra key>` over it. A guard makes this explicit
  (`internal/controller/nixrender.go:571-573`):

  ```go
  // ...we must NOT stamp -i K_infra here — that would override a caller-injected
  // target key and break the apply (this is the core regression the fix addresses).
  if !hasEnvVar(app.Env, "NIX_SSHOPTS") && in.sshSecretName != "" {
      app.Env = upsertEnv(app.Env, corev1.EnvVar{Name: "NIX_SSHOPTS", Value: sshHostKeyOpts})
  }
  ```

  Behavior by case: caller already set `NIX_SSHOPTS` (target key) → left
  untouched; builder present but caller set nothing → host-key opts only, no
  identity, so the app's own build dispatch uses the `builders=` key; neither →
  nothing.
- The **instantiate init container** still sets
  `NIX_SSHOPTS = -i /etc/nio/ssh/ssh-privatekey …` (`nixrender.go:438-457`) —
  correct, because that container only dispatches the build to the builder/store,
  never to the target.

### Options that were considered (and why the shipped fix differs)

- **A. Per-host `ssh_config` with `-F`** (one `Host` block per destination,
  `IdentitiesOnly yes`) — robust but needs a new API field plus orchestrator ↔
  workload coordination to assemble one config.
- **B. Multi-key `NIX_SSHOPTS` (`-i target -i builder`)** — fragile; key-offer
  order and `MaxAuthTries` can still trip "too many auth failures".
- **C. Guard/revert** — forbid the combo; drops store/builder acceleration.

The shipped fix is a fourth, simpler approach: the builder key never enters
`NIX_SSHOPTS` at all (it lives in `builders=`), so the two channels do not
collide and no per-host config file or multi-key offering is needed.

## Regression guard

- `TestRenderDayTwoChildKeepsTargetKey` (`internal/controller/nixrender_test.go:450`):
  a `storeRef`+`builderRef` day-2 child on a target-applying config asserts the
  app container keeps the caller-injected **target** key in `NIX_SSHOPTS`, and
  that the `builders=` line carries the **builder** key so build dispatch still
  works.
- The plain day-2 path (no store/builder) keeps `NIX_SSHOPTS` = target key and
  applies successfully.

## Relevance to the cluster converge pod

The `NixCluster` converge `NixCronJob` inherits this exact machinery: if a
`NixStore`/`NixBuilder` accelerates the converge build, the builder key must stay
in `builders=` and the cluster SSH key (`spec.sshKeyRef`) must reach the member
hosts via the pod's `NIX_SSHOPTS`. The same guard keeps the cluster key from
being clobbered by an infra key.
