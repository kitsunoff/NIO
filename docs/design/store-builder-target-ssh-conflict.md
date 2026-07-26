# Problem: storeRef/builderRef on a target-applying NixosConfiguration breaks the SSH apply

## Summary

The new `storeRef`/`builderRef` pass-through (commit `d5166a0`) lets a
`NixosConfiguration` point its child workloads at a shared `NixStore`/`NixBuilder`.
Tested live: the child **builds the system on the builder and caches into the
store correctly**, but the **final apply to the target host fails** with:

```
Received disconnect from <target> port 22:2: Too many authentication failures
error: failed to start SSH connection to '<target>'
Command 'nix-copy-closure --to root@<target> …nixos-system-…' returned non-zero exit status 1.
```

## Root cause — single global `NIX_SSHOPTS`, two SSH destinations, two keys

A store/builder-backed apply child has **two distinct SSH destinations that need
different keys**:

| Destination | Used by | Key | Mount |
| --- | --- | --- | --- |
| builder / store | nix `builders = ssh-ng://…` + store push | `store-ssh` | `/etc/nio/ssh/ssh-privatekey` |
| target host | `nixos-rebuild switch --target-host` / `nix-copy-closure` | machine key | `/etc/nio/target-ssh/ssh-privatekey` |

Both connections read the **same single `NIX_SSHOPTS` env**. There is no way for
one env to carry a per-destination `-i`.

- The orchestrator sets `NIX_SSHOPTS = -i /etc/nio/target-ssh/ssh-privatekey …`
  (`internal/controller/nixosconfiguration_children.go`, `targetSSHPodTemplate`
  / `targetNixSSHOpts`).
- When a store/builder ssh secret is present, the workload family **overrides**
  it: `NIX_SSHOPTS = -i <store/builder key> …`
  (`internal/controller/nixrender.go` ~L421-437, guarded by
  `if in.sshSecretName != ""`).
- Env upsert keeps ONE value → the store/builder key wins → the target apply
  authenticates with the **wrong key** → `Too many authentication failures`.

### Evidence (live)
- store-backed pod: `NIX_SSHOPTS = -i /etc/nio/ssh/ssh-privatekey …` (builder/store key)
- plain day-2 pod (works): `NIX_SSHOPTS = -i /etc/nio/target-ssh/ssh-privatekey …` (target key)
- log: `building …nixos-system-nio-target… on 'ssh-ng://root@builder'` → OK, then
  `nix-copy-closure --to root@<target>` → auth failure.

### This was foreseen by the design
`docs/design/nixosconfiguration-v1alpha2.md`, **Key decision 1**: *"Builder +
target on the same child is out of scope … that avoids the single NIX_SSHOPTS
serving two different keys."* The pass-through enabled exactly the excluded combo.
Note: `storeRef`-alone likely also trips the override (store push needs
`in.sshSecretName`) → verify, it is probably the same conflict.

## Fix options

### A. Per-host SSH config (recommended, robust)
Stop using a single global `-i` in `NIX_SSHOPTS`. Instead generate an ssh client
config with one `Host` block per destination, each with its own
`IdentityFile` + `IdentitiesOnly yes`, and point ssh at it via `NIX_SSHOPTS = -F <file>`:

```
Host <builder-host> <store-host>
  IdentityFile /etc/nio/ssh/ssh-privatekey
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
Host <target-host>
  IdentityFile /etc/nio/target-ssh/ssh-privatekey
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
```

Then nix's ssh-ng (builder/store) and `nixos-rebuild --target-host` each pick the
right key **by hostname** automatically. `IdentitiesOnly yes` also kills the
"too many authentication failures" (only the matched key is offered).

Coordination (the real work): the **workload family** (`nixrender.go`) knows the
builder/store hosts; the **orchestrator** knows the target host. Both must
contribute to ONE config. Cleanest: add a small `NixSpec` field the orchestrator
fills, e.g.

```go
// extra ssh identities keyed by host, merged into the pod's ssh_config
Nix.SSHHostKeys []SSHHostKey{ Host string; IdentityFileMount string }
```

`nixrender` writes the config from (builder/store entries it already knows) +
(these extra entries), sets `NIX_SSHOPTS=-F <file>`, and stops emitting the bare
`-i`. The orchestrator populates one entry for the target host + machine key and
drops its own `NIX_SSHOPTS` injection.

Scope: shared workload code (`nixrender`) + orchestrator children + new API
field + regen + unit tests + re-run the tier-1 apply e2e.

### B. Multi-key `NIX_SSHOPTS` (quick, fragile)
Merge instead of override: `NIX_SSHOPTS = -i target-key -i builder-key` (no
`IdentitiesOnly`), let ssh try both per connection. Simpler, but fragile:
key-offer order + `MaxAuthTries` (default 6) with any extra/default keys can still
trip "too many auth failures". Not recommended as the durable fix.

### C. Guard / revert (align with design)
Forbid `storeRef`/`builderRef` on a target-applying `NixosConfiguration`
(validation), or revert `d5166a0`. Safe, restores the design's exclusion, but
drops store/builder acceleration for NixosConfiguration entirely.

## Recommendation
Do **A**. It is the only option that actually delivers the feature (store/builder
acceleration) together with the SSH apply, and it hardens ssh auth generally
(`IdentitiesOnly`). Treat it as a workload-family change (per-host ssh config)
with the orchestrator cooperating via a new `NixSpec` ssh-hostkeys field.

## Regression guard
The plain day-2 path (no store/builder) works and must keep working. Any fix must
preserve: `NixosConfiguration` without store/builder → `NIX_SSHOPTS` uses the
target key → apply succeeds (verified: config reached `Ready`, day-2
`lastSuccessfulTime` set).
