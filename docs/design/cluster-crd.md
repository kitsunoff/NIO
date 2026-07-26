# Cluster CRD + converge — design (agent-ready)

Abstract cluster CRD for NIO that integrates `kitsunoff/nixcluster`. Not
Kubernetes-specific, not Proxmox-specific: it groups `Machine`s, maps opaque
config data onto them, and drives an idempotent whole-cluster `converge`. The
cluster's *meaning* (k3s / cozystack / whatever) lives in the downstream
flake-parts repo and its nixcluster modules — NIO never interprets it.

Companion design: [`nixosconfiguration-v1alpha2.md`](./nixosconfiguration-v1alpha2.md)
(the single-host orchestrator). Both share one primitive: **an idempotent script
on a `Forbid` cron** — `NixosConfiguration` day-2 switches one host, `Cluster`
converge reconciles many.

## Locked decisions

1. **Abstract cluster.** CRD groups Machines + carries opaque `values`; it does
   not know k3s/proxmox. Semantics live in the flake repo + nixcluster modules.
2. **One `NixCronJob` per cluster running an idempotent `converge`** (not a
   phased state machine in NIO). Ordering (servers→agents), pre/post ops
   (secrets, cozystack) all live inside `converge` (nixcluster's domain).
3. **A Machine belongs to strictly one nodeGroup** (first matching group by spec
   order claims it; later groups exclude it).
4. **Stable, sticky selection**: sort candidates by Machine name; with a `count`,
   keep previously-selected members and top up deterministically.
5. **No `order`/`lifecycle` in the CRD** — converge owns ordering. nodeGroups only
   select + map values.
6. **Value mapping** → per-machine node file in the flake-parts repo:
   `nixcluster.<cluster>.members.<machine> = recursiveUpdate (fromJSON <values>)
   { install.ip = <host>; }`, injected as an `additionalFile` into the converge
   checkout (reuses `NixSpec.additionalFiles`).
7. **`values` are opaque data** (scalars/nested maps). `nixosConfiguration` (a Nix
   value) is NOT in values — it comes from a cluster-level default (below).
8. **Which cluster modules are loaded + the base config are repo-authored** only
   (`modules/clusters/<cluster>.nix`); NIO sets only per-node `values`.
9. **Per-node status** — converge emits per-member JSON; NIO reflects it.
10. **Setup + install + apply all happen inside converge** (install-once →
    switch); NIO does not split install out.
11. **No global apply cap**; **provisioning on under-fill is deferred** (PXE).
12. **Naming: `converge` everywhere** (command + option); `apply <member>` stays
    per-node.

## The nixcluster contract (downstream flake-parts repo)

`nix flake init --template github:kitsunoff/nixcluster` gives:

```text
flake.nix                 # flake-parts + nixcluster.flakeModules.default + import-tree ./modules
modules/
  nodes/<node>.nix        → nixcluster.<cluster>.members.<node> = { … }   (auto-imported)
  clusters/<cluster>.nix  → nixcluster.<cluster> = { imports=[clusterModules.*]; … }
  clusterModules/<name>.nix
```

`import-tree` merges `nixcluster.<cluster>` across files → `clusterConfigurations.<cluster>`
(+ `nixosConfigurations.<cluster>-<member>` + a per-cluster CLI app `cluster-<cluster>`).
A member attrset: `{ nixosConfiguration; install.{ip,disk,…}; <freeform NixOS
patches / module options e.g. k3s.role>; }`. Everything except
`nixosConfiguration`/`install` becomes a NixOS patch (`clusterOutputs.nix`).

## Required nixcluster additions

### A. `converge` module (always-loaded)

A deps-ordered idempotent reconcile engine, mirroring `system.activationScripts`
(NixOS): entries `{ text; deps; }` topologically sorted via `lib.textClosureMap`.

- New file `cluster-modules/converge.nix`, **imported by `core.nix`** so it is
  always present (transitively through `coreModule` in both `mkCluster` and the
  flake-parts `flakeModule`) — the option must always be declared so optional
  modules can contribute to it. Single wiring point (`core.nix imports`).
- Option:
  ```nix
  converge.steps = mkOption {
    type = attrsOf (submodule { options = {
      text = mkOption { type = lines; default = ""; };   # default "" ⇒ pure-deps anchors ok
      deps = mkOption { type = listOf str; default = []; };
    }; });
    default = {};
  };
  ```
- Core contributes per-member steps `member-<name>` (install-once → switch):
  ```bash
  ip=<install.ip>; flake=".#<cluster>-<name>"
  if ssh -o StrictHostKeyChecking=accept-new root@$ip 'test -e /run/current-system'; then
    nixos-rebuild switch  --flake "$flake" --target-host root@$ip     # idempotent
  else
    nixos-anywhere        --flake "$flake" --target-host root@$ip     # install-once
  fi
  ```
- Top-level command `converge` assembles + runs, emitting per-member JSON result:
  ```nix
  commands.converge.builder = { pkgs, cluster, lib, ... }: pkgs.writeShellApplication {
    text = "set -euo pipefail\n" +
      lib.textClosureMap (e: "#### converge step\n" + e.text) cluster.converge.steps
        (lib.attrNames cluster.converge.steps);
  };
  ```
- Optional modules AUGMENT `converge.steps` (guarded by their enable; `deps` is a
  `listOf` → merges by concatenation):
  - `sops`: `converge.steps.secrets` (generate-if-missing) + each `member-*.deps
    += [ "secrets" ]`.
  - `k3s`: ordering deps — non-first servers `deps=["member-<firstServer>"]`,
    agents `deps=[<all server steps>]`; plus a post step `kubeconfig`
    (`deps=[<servers>]`).
  - `cozystack`: `platform` step `deps=[<all members>, "kubeconfig"]`.
- **Idempotency contract** (documented, enforced by convention): every `text`
  must be safely re-runnable — install-once (probe `/run/current-system`),
  gen-secrets if-missing, post-ops no-op when already applied.

### B. cluster-level `defaultNixosConfiguration`

Members generated by NIO carry only data (no `nixosConfiguration`). Add a
cluster-level default (the "минимальная NixOS болванка": kernel + containerd +
k3s + ssh + sops-nix), repo-authored in `modules/clusters/<cluster>.nix`:

```nix
# core.nix
options.defaultNixosConfiguration = mkOption { type = raw; };
config.members = mapAttrs (_: _: { nixosConfiguration = mkDefault config.defaultNixosConfiguration; }) …;
```

NIO-generated node files omit `nixosConfiguration` → inherit the default; their
`values` patch it. A node file MAY override the default explicitly.

## Cluster CRD (NIO, `nio.homystack.com/v1alpha1`)

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: Cluster
metadata: { name: prod, namespace: infra }
spec:
  source:                         # downstream flake-parts repo (the cluster)
    gitRepo: https://github.com/acme/prod-cluster
    ref: main
    credentialsRef: { name: git-creds }     # optional (private)
  sshKeyRef:  { name: cluster-ssh }          # cluster-wide SSH key (mounted into converge)
  ageKeyRef:  { name: cluster-age }          # sops age key (input; mounted)
  dayTwoSchedule: "*/30 * * * *"             # converge cadence (default)
  nodeGroups:
    - name: control-plane
      selector: { matchLabels: { role: server } }   # label selector over Machine
      count: 3                                       # optional; stable subset
      values:                                        # opaque, nested → member attrset
        k3s: { role: server }
    - name: workers
      selector: { matchLabels: { role: worker } }
      values:
        k3s: { role: agent }
status:
  phase: Ready | Converging | Degraded | Blocked
  nodeGroups:
    - name: control-plane
      members:                                       # STABLE, sorted, sticky
        - { name: node-01, status: Applied }
        - { name: node-02, status: Applied }
        - { name: node-03, status: Applied }
      desired: 3
      selected: 3
  convergeJobRef: prod-converge
  conditions: [ … ]                                  # Ready / Stalled / GitSynced
```

Cluster is **namespaced** (same namespace as its Machines). `values` is a
schemaless nested object (`RawExtension`/`apiextensions JSON`), opaque to NIO.

## Selection algorithm (stable + sticky)

1. Candidates per group = Machines in the Cluster namespace matching
   `group.selector`. A Machine is claimed by the **first** group (spec order) it
   matches; later groups exclude it.
2. Sort candidates by **Machine name** (lexicographic) — fully deterministic (no
   timestamps/UIDs that could reshuffle).
3. `count` unset → all candidates are members.
4. `count` set → sticky: start from `status` members still matching (their
   order); top up remaining slots from the sorted candidates (lowest names
   first); if over `count`, drop the highest-name extras. Persist to `status`.
5. Under-fill (`count` > available) → condition `Underprovisioned` (provisioning
   deferred). Never silently pick fewer without surfacing it.

## Reconcile (NIO Cluster controller)

```text
Reconcile(cluster):
  ensureFinalizer
  if deletionTimestamp: reconcileRemoving; return
  for each nodeGroup (in spec order): stable+sticky select → status.members
  render per-member node files:
     modules/nodes/<m>.nix = recursiveUpdate (fromJSON <group.values>) { install.ip = <Machine.host>; }
  ensure one NixCronJob "<cluster>-converge":
     nix.source          = spec.source
     nix.run             = ".#cluster-<cluster>"
     nix.args            = [ "converge" ]
     nix.additionalFiles = the rendered node files (inline)      # import-tree picks them up
     mounts              = sshKeyRef, ageKeyRef                   # into the converge pod
     cronJobTemplate     = { schedule: dayTwoSchedule, concurrencyPolicy: Forbid,
                             activeDeadlineSeconds: <generous> }
     triggerOnChange     = true
  observe cron: parse per-member JSON from the last run → status.members[].status; set phase
  requeue

reconcileRemoving(cluster):
  (optional) one-shot converge with a decommission target, or just delete the cron
  clear generated state; removeFinalizer
```

NIO owns: the generated node files + the one converge `NixCronJob`. Ordering,
install/switch, secrets, post-ops are all inside `converge`. NIO stays abstract.

## Sharp edges (must handle)

- **install is NOT idempotent** (`nixos-anywhere` wipes disk) → converge does
  install-once (probe `/run/current-system`); the step as a whole is idempotent.
- **Per-node status** needs converge to emit structured per-member JSON; NIO
  parses it. Coarse job-level ok/fail is the fallback if absent.
- **Long single job + `Forbid`** → `activeDeadlineSeconds`; no overlap (good);
  intra-wave parallelism only if the converge script does it.
- **Multi-host SSH** → one cluster-wide `sshKeyRef` mounted; converge iterates
  member IPs (not per-member `--target-host` injection).
- **Secret persistence** → a fresh pod per tick has no memory. MVP: secrets are
  an **input** (committed sops files + mounted `ageKeyRef`); converge's secrets
  step is generate-if-missing (no-op when present). Generating + persisting new
  secrets from NIO is out of MVP.

## MVP boundary

In: Cluster CRD (nodeGroups select + stable/sticky + values), per-member node
file generation, the single converge `NixCronJob`, per-node status. Plus the two
nixcluster additions (converge module + `defaultNixosConfiguration`).

Out (later): provisioning on under-fill (PXE), install.mac/disk from hardware
facts, NIO-side secret generation/rotation, host-key pinning policy, cross-cluster
machine-uniqueness enforcement.

## Files (when implementing)

- nixcluster (separate repo, `kitsunoff/nixcluster`): `cluster-modules/converge.nix`
  (+ `core.nix` imports it); `defaultNixosConfiguration` in core + member default;
  per-member JSON output from `converge`; augment steps in `k3s`/`sops`/`cozystack`.
- NIO: `api/v1alpha1/cluster_types.go` (+ deepcopy/CRD); `internal/controller/
  cluster_controller.go` (select + render node files + ensure converge cron +
  status); reuse `NixSpec.additionalFiles` for node-file delivery; RBAC for
  `nixcronjobs` + `machines`.

## Testing

See `.agent-e2e/09-cluster.md` for the e2e plan (Kind-tier selection/stability/
generation; VM-tier real converge).
