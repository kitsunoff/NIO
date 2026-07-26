# NixCluster ↔ nixcluster integration testing

How we validate the integration layer between NIO's cluster CRD and the
downstream `kitsunoff/nixcluster` flake — the boundary where NIO renders
per-member node files and drives `converge`, and nixcluster actually installs /
switches / clusters the nodes. Also proposes renaming the CRD `Cluster` →
`NixCluster`.

Companion docs: [`cluster-crd.md`](./cluster-crd.md) (the CRD design),
[`.agent-e2e/09-cluster.md`](../../.agent-e2e/09-cluster.md) (the e2e plan),
[`store-builder-target-ssh-conflict.md`](./store-builder-target-ssh-conflict.md)
(the per-connection SSH-key handling the converge pod also relies on).

## 0. Rename `Cluster` → `NixCluster`

**Decision: rename the CRD Kind `Cluster` to `NixCluster`** before it grows users.

Rationale:
- Consistency with the rest of the API family — every other kind is `Nix*`
  (`NixJob`, `NixCronJob`, `NixStore`, `NixBuilder`, `NixDeployment`,
  `NixStatefulSet`, `NixosConfiguration`). A bare `Cluster` is the odd one out.
- `Cluster` is an overloaded noun in the k8s ecosystem (Cluster API's `Cluster`,
  fleet/cluster-registry `Cluster`, etc.). `NixCluster` is unambiguous and
  self-describing (a cluster converged by nix/nixcluster).
- The CRD only just merged (`main` @ v1alpha2 orchestrator work) and has **no
  external consumers yet**, so the rename is cheap now and expensive later.

Scope of the rename (one focused PR, no behavior change):
- `api/v1alpha1/cluster_types.go` → rename types `Cluster`/`ClusterList`/
  `ClusterSpec`/`ClusterStatus` → `NixCluster*`; keep `NodeGroup`/`NodeGroupStatus`/
  `MemberStatus` names (they read fine). Update `+kubebuilder:object:root`,
  printcolumns, `SchemeBuilder.Register`. File may be renamed to
  `nixcluster_types.go`.
- `internal/controller/cluster_controller.go` → `NixClusterReconciler`
  (file `nixcluster_controller.go`); update `managedLabels("Cluster", …)` →
  `"NixCluster"`, watches, `Named("nixcluster")`.
- RBAC markers `clusters`/`clusters/status`/`clusters/finalizers` →
  `nixclusters*`; run `make manifests generate`; commit regenerated
  `config/crd/bases/…_nixclusters.yaml`, `config/rbac/role.yaml`,
  `zz_generated.deepcopy.go`, `PROJECT`, `config/crd/kustomization.yaml`.
- e2e: `test/e2e/cluster_test.go` — kind name `nixclusters.nio.homystack.com`,
  Describe title.
- Keep the group/version `nio.homystack.com/v1alpha1`.

This is a plain identifier rename + regen; unit/e2e assertions move with it. Do
it as a standalone commit/PR ahead of (or at the start of) the integration work
below so the rest of the doc uses `NixCluster` throughout.

## 1. What NIO's NixCluster CRD does today (recap)

`NixCluster` is abstract: it groups `Machine`s, maps opaque per-group `values`
onto each member, and drives ONE idempotent `converge` `NixCronJob` against a
downstream flake-parts repo. NIO never interprets cluster meaning.

- **Spec**: `source` (`NixSource` — the downstream flake repo), `sshKeyRef`
  (cluster-wide SSH key mounted into the converge pod), `ageKeyRef` (sops age
  key input), `dayTwoSchedule`, `nodeGroups[]` (`name`, label `selector`,
  optional `count`, opaque `values` JSON).
- **Controller** (`ClusterReconciler`, → `NixClusterReconciler`):
  1. per nodeGroup, **stable + sticky** selection of Machines (sort by name;
     first matching group claims a Machine; `count` keeps prior members and tops
     up deterministically; under-fill → `Underprovisioned` condition);
  2. renders one node file per member:
     `nixcluster.<cluster>.members.<machine> = lib.recursiveUpdate (builtins.fromJSON "<values>") { install.ip = "<host>"; };`
     (both `values` JSON and `host` escaped for safe Nix double-quoted strings);
  3. ensures ONE owned converge `NixCronJob` `<cluster>-converge`:
     `nix.run = ".#cluster-<cluster>"`, `nix.args = ["converge"]`,
     `triggerOnChange`, `concurrencyPolicy: Forbid`, node files as inline
     `additionalFiles`, cluster SSH key + age key mounted, `NIX_SSHOPTS` /
     `SOPS_AGE_KEY_FILE` set, `activeDeadlineSeconds ≈ 3600`;
  4. reflects a coarse, job-level per-member status + phase (per-member JSON
     parsing is a stub today — see §3).

The converge pod inherits the workload family's build/substitute machinery,
including the per-connection SSH-key handling (builder key in `builders=`, the
pod's injected `NIX_SSHOPTS` = the cluster key) from the store/builder fix.

## 2. The integration boundary (what actually crosses NIO ↔ nixcluster)

| Direction | Artifact | Producer | Consumer | Status |
| --- | --- | --- | --- | --- |
| NIO → repo | `modules/nodes/<machine>.nix` (member attrset via `fromJSON`+`recursiveUpdate`, `install.ip`) | NIO controller (`renderNodeFileContent`) | nixcluster `import-tree` → `nixcluster.<c>.members` | **works** (contract matches) |
| NIO → prebuild | `nix build .#cluster-<c>` (instantiate init, `nixrender.go`) | NIO converge child | nixcluster flake | **BROKEN — `cluster-<c>` is an `app`; `nix build` only resolves `packages`/`legacyPackages`, so this fails in the init container before converge runs** (see §3) |
| NIO → converge | `nix run .#cluster-<c> -- converge` | NIO converge child (app container) | nixcluster `cluster-<c>` app = `getExe (clusterConfigurations.<c>.cli pkgs)` | form correct; **BLOCKED — `converge` not registered in the cli** |
| converge → node(s) | install-once (`nixos-anywhere`) → `nixos-rebuild switch`, per member, ordered | nixcluster converge | the member hosts (SSH) | **BLOCKED — no converge / no install-once→switch state** |
| converge → NIO | per-member JSON result | nixcluster converge | NIO status (`MemberStatus`) | **partial — NIO has a coarse job-level fallback; per-member JSON not emitted** |
| default config | `defaultNixosConfiguration` | nixcluster (repo-authored) | member `nixosConfiguration` | **BLOCKED — no default; member `nixosConfiguration` is required** |

## 3. Contract status: exists vs must-build (from a fresh nixcluster inspection)

`kitsunoff/nixcluster` @ `master` (last commit 2026-06-24). **Assembly side is
ready; orchestration side is missing.**

**EXISTS (matches NIO's assumptions):**
- `nixcluster.<cluster>.members.<machine>` flake-parts option
  (`lib/flakeModule.nix`); freeform member attrset + `install.ip`
  (`cluster-modules/core.nix`) — NIO's `recursiveUpdate (fromJSON …) { install.ip
  = …; }` injects cleanly.
- `nixosConfigurations.<cluster>-<member>` and the `cluster-<cluster>` app
  (`nix run .#cluster-<cluster>`) (`lib/clusterOutputs.nix`, `lib/flakeModule.nix`).
- The `nix flake init --template github:kitsunoff/nixcluster` template with the
  exact `modules/nodes/`, `modules/clusters/`, `modules/clusterModules/` layout
  NIO mirrors.
- sops/age wiring (`cluster-modules/sops.nix`; age key at `/etc/age/key.txt`,
  delivered via `--extra-files` at install).
- **An Incus module** (`cluster-modules/incus.nix`, `modules/nixos/nixcluster-incus.nix`)
  plus a live single-node test (`tests/incus/flake.nix`, Colima aarch64, dir
  storage). Also k3s / cozystack / pxe / nebula / keepalived modules.

**How the cli is actually wired (verified in code, so we build against reality):**
Everything the operator drives lives in one place — the per-cluster CLI. The
flake output is `clusterConfigurations.<name> = { config; nixosConfigurations;
cli; }` (`lib/clusterOutputs.nix:41-46`), where `cli = pkgs: mkClusterCli {…}`
is a **derivation-producing function**. The per-system app `cluster-<name>` is
just `getExe (c.cli pkgs)` over it (`lib/flakeModule.nix:73-80`). The cli is a
two-level router (`mkClusterCli.nix`): top-level `commands` (core: apply /
gen-config / install) + namespaced `commandGroups.<g>.actions.<a>` contributed by
cluster modules. So **converge, per-member JSON, and everything else the operator
needs must be wired *into the cli*** — there is no other entry point.

**MISSING (hard prerequisites for tier-1 integration):**
1. **`converge` command — absent entirely, and it must be wired through the cli +
   all modules.** NIO runs `.#cluster-<c> -- converge`; the cli only has `apply`
   (nixos-rebuild switch), `install` (nixos-anywhere), `gen-config`. Needs a
   top-level `converge` (in `baseCommands` / `cluster.commands`) that iterates
   members and decides install-once vs switch. Because it is a *whole-cluster*
   reconcile, **every cluster module (k3s, sops, incus, cozystack, nebula,
   keepalived, …) must expose its converge contribution** (its ordered steps /
   pre-/post-ops) so converge assembles them — not just a per-member SSH loop.
   This is a cross-module change in nixcluster, not a single new file.
2. **No install-once→switch state.** converge must probe first-boot vs
   already-installed per member (e.g. `test -e /run/current-system` over SSH, or
   the existing install-time host-key pin file as a crude marker).
3. **No per-member JSON output.** `install`/`apply` print human logs; NIO needs
   machine-readable per-node results to fill `status.nodeGroups[].members[].status`.
4. **No `defaultNixosConfiguration`.** `core.nix` makes member `nixosConfiguration`
   **required with no default**. NIO injects members as JSON via `fromJSON`, and
   **`nixosConfiguration` is a Nix value/function that cannot be expressed in
   JSON** — so NIO-generated members that omit it will FAIL evaluation. This is a
   hard blocker: nixcluster must add a cluster-level `defaultNixosConfiguration`
   (and default `member.nixosConfiguration` to it) so NIO members carry only data.
5. **The operator prebuilds an `app` with `nix build` — broken, and the deeper
   fix is to route everything through the cli.** NIO's converge child always runs
   an instantiate init `nix build <Run>` = `nix build .#cluster-<c>`
   (`nixrender.go:475,544-551`), but `cluster-<c>` is an `app`, and `nix build`
   resolves only `packages`/`legacyPackages` — so the init container fails before
   the app container's `nix run` even starts. Two coordinated fixes are required:
   - **nixcluster:** also expose the same derivation as
     `packages.<sys>.cluster-<name> = c.cli pkgs` (trivial — it is exactly the
     derivation `getExe` already points at), so `nix build .#cluster-<c>`
     resolves.
   - **NIO:** the converge child must be built so the operator pushes *the whole
     invocation through the nixcluster cli* — prebuild the cli package (not an
     app), then `nix run` it with `converge`; the heavy per-member system
     closures (`.#<cluster>-<member>`) are built *inside* the cli run
     (nixos-rebuild / nixos-anywhere), not by NIO's prebuild step. This may mean a
     `NixSpec` knob to prebuild a different attr than `Run`, or to skip prebuild
     for converge. See §7.

## 3a. cli entry-point architecture & the single-source-of-truth refactor (B4)

The invocation NIO uses (`nix run .#cluster-<c> -- converge`) is the right shape,
but a code read of nixcluster showed the entry-point plumbing has **three
divergent paths** that generate different outputs. This section pins the target
architecture so the fixtures and NIO build against one consistent contract.

### The three entry levels (verified in code)
1. **`clusterConfigurations.<name>`** — top-level flake output, the source of
   truth. `{ config; nixosConfigurations; cli; }` where `cli = pkgs: <derivation>`
   is a **function** (`lib/clusterOutputs.nix:41-46`, `lib/mkCluster.nix`). It is
   deliberately a function of `pkgs` because the host system running the CLI is
   independent of the members' systems, and `clusterConfigurations` itself is not
   system-namespaced.
2. **`nixclusterctl`** — the human top-level router:
   `nixclusterctl <cluster> <command> [args]`.
3. **`apps.<sys>.cluster-<name>`** — per-cluster app = `getExe (c.cli pkgs)`
   (`lib/flakeModule.nix:73-80`; root `flake.nix:54-61`). This is what NIO targets
   and what `nixclusterctl` currently dispatches into.

### The divergence (the bug we are fixing)
Three producers wire these inconsistently:

| Path | Where | Generates | nixclusterctl model |
| --- | --- | --- | --- |
| `flakeModule` (flake-parts) | downstream template | `apps.<sys>.cluster-<name>` | — (no nixclusterctl) |
| `mkFlakeOutputs` (standalone) | `lib/mkFlakeOutputs.nix` | `apps.<sys>.nixclusterctl` (single, **static**, embeds `getExe (c.cli pkgs)`) | static, does **not** need `apps.cluster-<name>` |
| root `flake.nix` (plain) | nixcluster repo | `packages.nixclusterctl` (**dynamic**) + manual `apps.cluster-<name>` | `nix eval .#clusterConfigurations` + `nix run .#cluster-$CMD` |

So the **dynamic** `packages/nixclusterctl.nix` (`:45-47`) depends on
`apps.cluster-<name>` existing, which only the flake-parts / root-manual paths
create — on the `mkFlakeOutputs` path there is no `apps.cluster-<name>` and it
would break. There are effectively **two different nixclusterctl implementations**
that have drifted apart.

### Why not just `nix run .#clusterConfigurations.<name>.cli`
Two reasons it cannot be the entry point as-is:
1. `.cli` is a **function** `pkgs → derivation`, not an app/derivation — `nix run`
   cannot coerce a function.
2. `clusterConfigurations` is **not system-namespaced**; `nix run`/`nix build`
   auto-insert `<system>` only for the standard `apps.<sys>.`/`packages.<sys>.`
   prefixes, not mid-path. You would have to add a system layer
   (`…cli.<system>`) and spell the system explicitly — non-idiomatic and NIO would
   have to compute it. The idiomatic answer is a system-namespaced projection.

### Target architecture (B4)
**One source of truth (`clusterConfigurations.<name>.cli`), one shared projection
helper, one flake-parts-independent `nixclusterctl`.**

- **Shared projection helper** `lib/flakeOutputs.nix` (new):
  `mkPerSystemOutputs { pkgs, clusterConfigurations } → { apps; packages; }`,
  producing from the single source:
  - `apps.cluster-<name>     = { type="app"; program = getExe (c.cli pkgs); }`
  - `packages.cluster-<name> = c.cli pkgs`  (**buildable** — NIO's prebuild target)
  - `packages.nixclusterctl  = mkNixclusterctl { inherit pkgs; clusterConfigurations; }`
- **flake-parts does projection too** — `flakeModule.config.perSystem` calls
  `mkPerSystemOutputs` (flake-parts iterates systems); `mkFlakeOutputs` calls the
  same helper under its own `genAttrs systems`; the root flake likewise. Same
  helper everywhere ⇒ no drift. flake-parts is only the *system iterator*, never
  the definition of *what* is projected.
- **`mkNixclusterctl` is flake-parts-independent** (`lib/mkNixclusterctl.nix`,
  new): a pure function `{ pkgs, clusterConfigurations } → derivation` that
  dispatches **directly into `.cli` by store path** (`exec ${getExe (c.cli pkgs)}
  "$@"`), with a **static** case-list of cluster names from `attrNames
  clusterConfigurations`. It depends only on the source + `pkgs` — **not** on
  flake-parts and **not** on the `apps`/`packages` projection. The dynamic
  `packages/nixclusterctl.nix` (`nix eval` + `nix run .#cluster-*`) is deleted.
  - Trade-off accepted: static discovery (add a cluster → rebuild nixclusterctl),
    matching how `apps` already behave; runtime `nix eval` discovery is dropped.
  - The only flake-parts touchpoint is the one-line *exposure*
    (`packages.<sys>.nixclusterctl = mkNixclusterctl {…}`) — unavoidable for any
    runnable output, but trivial and identical on every path.

### What NIO targets (unchanged, and it does NOT use nixclusterctl)
NIO knows the cluster name and points at a single buildable+runnable per-cluster
target, both derived from the same `.cli`:
- prebuild: `nix build .#cluster-<c>`   → resolves as `packages.<sys>.cluster-<c>`
- run:      `nix run   .#cluster-<c> -- converge` → resolves as `apps.<sys>.cluster-<c>`
  (`nix run` prefers `apps`, then `packages`)

`nixclusterctl` stays the human-only router; NIO never depends on it. This means
**B4 (exposing `packages.cluster-<name>`) alone unblocks NIO's existing prebuild**
without a code change — see §3.5 / §7b.

## 4. Test strategy — two tiers

### Tier 2 — NIO-side, on Kind, no real converge (exists)
Already implemented (`test/e2e/cluster_test.go`, will become `nixcluster_test.go`
after the rename): deterministic + stable + sticky selection, one-group-per-machine,
node-file value mapping (+ hostile-name rejection), converge `NixCronJob` shape,
`Underprovisioned`. These need only labelled `Machine` CRs — no real hosts, no
converge run. **This tier is the regression guard for the rename** (assertions
move 1:1 to `NixCluster`).

### Tier 1 — real converge against real nodes (the integration layer)
This is the new work and the point of this doc: a real `NixCluster` drives a
real `converge` that installs/switches multiple nodes and forms a working
downstream cluster. It requires all four §3 prerequisites in nixcluster, a
downstream fixture repo, and multi-node VMs. Gate behind an env flag and skip
cleanly when the prerequisites/VMs are absent (mirror the NixosConfiguration
tier-1 gating).

The example cluster we converge is an **Incus cluster** (per the request):
several nodes that run Incus and join one Incus cluster, expressed with
nixcluster's incus module.

## 5. The example / fixtures (concrete deliverables)

### Task A — downstream fixture repo from the nixcluster template
`nix flake init --template github:kitsunoff/nixcluster` into a small **public**
fixture repo (e.g. `kitsunoff/nio-incus-cluster`), pinned by rev (the operator
image has no git — NIO fetches by SHA):
- `modules/clusters/<cluster>.nix` — `imports = [ clusterModules.incus … ];`,
  cluster-level options, and a **`defaultNixosConfiguration`** = a minimal
  aarch64-linux NixOS base (kernel + sshd + sops-nix + the Incus module enabled)
  so NIO-generated members inherit it.
- **No `modules/nodes/*.nix`** — NIO injects those per member at converge time
  via `additionalFiles`.
- Committed sops secrets (encrypted) + `.sops.yaml`; the age key is an input
  (mounted via `NixCluster.spec.ageKeyRef`).
- Configure the incus module for **clustering** (nodes join one Incus cluster),
  not just per-node standalone Incus. If the current `incus` module only does
  single-node, extend it (nixcluster side) — see §7.

### Task B — example `NixCluster` CR (the k8s manifest)
A worked example pointing at Task A's repo:
```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata: { name: incus-lab, namespace: infra }
spec:
  source:
    gitRepo: https://github.com/kitsunoff/nio-incus-cluster
    ref: <pinned-sha>
  sshKeyRef: { name: cluster-ssh }     # reaches every node
  ageKeyRef: { name: cluster-age }     # sops input
  dayTwoSchedule: "*/30 * * * *"
  nodeGroups:
    - name: incus
      selector: { matchLabels: { role: incus } }
      values:
        incus: { member: true }        # opaque data → member attrset
```
Note the `values` carry **data only** — never `nixosConfiguration` (see §3.4).
Members inherit the repo's `defaultNixosConfiguration`; `install.ip` is injected
by NIO from `Machine.spec.host`.

### Task C — the Incus cluster the downstream repo sets up
The fixture repo's cluster config, when converged across the selected Machines,
must stand up a real Incus cluster: each node runs Incus (`nixcluster-incus`
NixOS module) and the nodes form one cluster (first node bootstraps, others
join). Assertions in tier-1 verify the Incus cluster actually formed (`incus
cluster list` shows all members online).

## 6. Test infrastructure (how to run tier-1 locally)

Reuse the NixosConfiguration tier-1 pattern (already proven this session):
- **Nodes = Lima aarch64 VMs** on the socket_vmnet `shared` network with
  **static IPs** (DHCP reassigns across kexec — static keeps the address stable
  and matches `Machine.spec.host`). ≥2 nodes for a real cluster (e.g. 3).
- **Operator in Kind** (OrbStack); pod → VM SSH reachability is proven for the
  shared network. The converge pod SSHes to every member using
  `NixCluster.spec.sshKeyRef` (one cluster-wide key authorized on all nodes).
- Nodes start as fresh Linux (Ubuntu cloud image) → converge does install-once
  (`nixos-anywhere`, needs a disko layout in the base config) → then
  `nixos-rebuild switch`.
- age key Secret (`ageKeyRef`) + cluster SSH key Secret (`sshKeyRef`);
  committed sops secrets in the fixture repo.
- **Networking / SSH-key note:** the converge pod contends for the same
  `NIX_SSHOPTS` as any builder — the store/builder fix (builder key in
  `builders=`, cluster key in the pod's `NIX_SSHOPTS`) applies here too. If a
  `NixStore`/`NixBuilder` accelerates the converge build, verify the cluster key
  reaches the nodes and the builder key stays in `builders=`.
- **GitHub rate limit / `nixos-anywhere`**: converge shells `nixos-anywhere`;
  pin it or provide a token in the node/converge egress (same lesson as the
  NixosConfiguration tier-1).
- Gate the whole tier behind `NIO_E2E_NIXCLUSTER=1` (+ the nixcluster `converge`
  module present); skip cleanly otherwise so CI stays green.

## 7. Work to build (nixcluster + NIO integration)

### 7a. nixcluster side (separate repo, out of NIO's PR)
1. **Implement `converge` in the cli, and update ALL cluster modules to feed it.**
   Add a top-level `converge` command (in `mkClusterCli.nix` `baseCommands` /
   `cluster.commands`): iterate members; per member probe install-once vs switch;
   assemble and run ordered steps. Crucially this is **not** a single new file —
   converge is a whole-cluster reconcile, so **every module (core, disko, k3s,
   sops, incus, cozystack, pxe, nebula, keepalived) must contribute its converge
   steps / pre-/post-ops** through a shared step contract (e.g. a
   `converge.steps` option: `{ text; deps; }` topologically sorted). Update the
   modules to declare their steps instead of relying on the current manual
   `apply`/`install`/group-action commands.
2. **Per-member JSON output** from converge (so NIO fills per-node status;
   coarse job-level status is the fallback if absent). Define the schema.
3. **`defaultNixosConfiguration`** cluster-level option, defaulting
   `member.nixosConfiguration` to it in `core.nix`/`clusterOutputs.nix` — the
   hard blocker (NIO members carry no Nix value).
4. **Incus clustering** in the incus module if only single-node Incus exists
   today (nodes joining one cluster, first-node bootstrap).
5. **Single-source-of-truth entry-point refactor (B4, see §3a).** Collapse the
   three divergent entry-point paths into one:
   - Add a shared projection helper `lib/flakeOutputs.nix`
     `mkPerSystemOutputs { pkgs, clusterConfigurations } → { apps; packages; }`
     that projects from `clusterConfigurations.<name>.cli`:
     `apps.cluster-<name>` (= `getExe (c.cli pkgs)`),
     `packages.cluster-<name>` (= `c.cli pkgs`, **buildable** — NIO's prebuild
     target), and `packages.nixclusterctl`.
   - Call it from **all** paths: `flakeModule.config.perSystem` (flake-parts
     iterates systems), `mkFlakeOutputs` (own `genAttrs systems`), root flake.
     Same helper ⇒ no drift; flake-parts is only the system iterator.
   - `packages.<sys>.cluster-<name>` unblocks NIO's `nix build .#cluster-<c>`
     prebuild (§3.5).
6. **Flake-parts-independent `nixclusterctl` (see §3a).** Add
   `lib/mkNixclusterctl.nix`: a pure `{ pkgs, clusterConfigurations } → derivation`
   that dispatches **directly into `.cli` by store path** with a **static** list of
   cluster names — no `nix eval`, no `nix run .#cluster-*`, no dependency on
   flake-parts or the `apps`/`packages` projection. Delete the dynamic
   `packages/nixclusterctl.nix`. Exposure is one line per path
   (`packages.<sys>.nixclusterctl = mkNixclusterctl {…}`).

### 7b. NIO side — route the whole invocation through the nixcluster cli
The operator must drive converge *entirely through the cli*, not by re-deriving
steps or by prebuilding an app it cannot build:
- **Fix the prebuild/run mismatch** (§3.5) — largely solved *by B4*. Once
  nixcluster exposes `packages.<sys>.cluster-<name>` (§7a.5), NIO's existing
  `nix build .#cluster-<c>` (prebuild → resolves as the package) and
  `nix run .#cluster-<c> -- converge` (run → resolves as the app) both work with
  **no NIO code change**, because `nix run` prefers `apps` and `nix build`
  resolves `packages`. A `NixSpec` "run-only / no-prebuild" knob is now
  **optional** — add it only to avoid rebuilding the tiny cli each run; the heavy
  per-member system closures (`.#<cluster>-<member>`) are built *inside* the cli
  run regardless. Cover with unit tests.
- **Everything the cluster does goes into the cli call.** NIO contributes only
  data: per-member node files (`additionalFiles`), the source rev, the cluster
  SSH key (`NIX_SSHOPTS`), the age key (`SOPS_AGE_KEY_FILE`). All orchestration
  (install-once→switch, per-module steps, per-member JSON) is the cli's job. NIO
  never shells `nixos-rebuild`/`nixos-anywhere`/module tooling directly for a
  cluster — that stays behind `nix run .#cluster-<c> -- converge`.
- **Parse the cli's per-member JSON** into `MemberStatus` (replaces the coarse
  `coarseMemberStatus` stub) once §7a.2 lands.

These nixcluster tasks (7a) block tier-1; NIO's tier-1 e2e is written against
them and stays skipped until they land. The NIO integration tasks (7b) can start
against a stub cli that accepts `converge` and emits the JSON shape.

## 8. What tier-1 asserts

- The converge `NixCronJob` fires (triggerOnChange), the run **succeeds**, and
  `status.nodeGroups[*].members[*].status == Applied` (per-node, once per-member
  JSON exists; else coarse ok/fail).
- On the VMs: every node runs NixOS with Incus enabled; **`incus cluster list`
  shows all members online** (the cluster formed); ordering held (bootstrap node
  before joiners) via converge's step deps.
- **Idempotent re-run / self-heal**: a second converge is a no-op (install-once
  held; switch reports no changes); break something on a node by hand → next
  converge restores it.
- **Membership change**: add a `role=incus` Machine/VM → it is selected, gets a
  node file, and the next converge brings it into the Incus cluster; remove a
  Machine → its member drops from status (node teardown deferred).
- **Deletion**: delete the `NixCluster` → the converge cron + generated state are
  removed, finalizer cleared (node wipe on delete is deferred — document it).

## 9. Open questions / decisions

- **defaultNixosConfiguration vs NIO injecting the config**: NIO cannot inject a
  Nix `nixosConfiguration` (JSON only), so `defaultNixosConfiguration` in
  nixcluster is the chosen path. A node file MAY still override the default
  explicitly (repo-authored), but NIO-generated ones never do.
- **converge steps engine**: minimal `converge` (a loop over members) vs a
  `converge.steps` deps-graph engine. Start minimal; add the engine only if
  ordered pre/post ops (secrets, k3s bootstrap, cozystack) need it.
- **Incus clustering topology**: how many nodes, storage backend (`dir` for the
  test), and whether the incus module already supports multi-node clustering.
- **Node count / cost**: tier-1 spins N NixOS VMs + runs `nixos-anywhere` on
  each — slow. Keep N small (2–3) and gate/skip.
- **Per-member status parsing**: define the JSON schema converge emits and where
  NIO parses it (replace the coarse `coarseMemberStatus` stub).
- **converge step contract across modules**: what interface every cluster module
  implements so converge assembles them (a `converge.steps` option vs a fixed
  hook set), and how ordering/deps are expressed.
- **prebuild strategy**: **decided** (§3a/§7a.5) — expose `packages.cluster-<name>`
  in nixcluster; NIO's existing prebuild then resolves with no code change. A NIO
  "no-prebuild" flag is optional polish, not required.
- **entry-point single source**: **decided** (§3a) — `clusterConfigurations.<name>.cli`
  is the sole source; `apps`/`packages`/`nixclusterctl` are projections via one
  shared helper; `mkNixclusterctl` is flake-parts-independent and dispatches into
  `.cli` statically; the dynamic `packages/nixclusterctl.nix` is removed. Trade-off
  accepted: static cluster discovery (rebuild on cluster add) over runtime `nix
  eval`.
- **NIO entry**: **decided** (§3a) — NIO targets `packages`/`apps.cluster-<name>`
  directly (it knows the name); it never routes through `nixclusterctl`.

## 10. Task list (ordered)

1. **[NIO]** Rename `Cluster` → `NixCluster` (§0): types, controller, RBAC,
   PROJECT, CRD/deepcopy regen, tier-2 e2e; one PR, no behavior change.
2. **[nixcluster]** Add `defaultNixosConfiguration` (§7a.3) — unblocks
   NIO-generated members.
3. **[nixcluster]** Implement `converge` in the cli **and update all cluster
   modules** to contribute their converge steps (§7a.1); add install-once→switch
   state and per-member JSON (§7a.2).
4. **[nixcluster]** Entry-point single-source refactor (§3a/§7a.5): add
   `lib/flakeOutputs.nix` `mkPerSystemOutputs` (projects `apps.cluster-<name>` +
   `packages.cluster-<name>` + `packages.nixclusterctl` from
   `clusterConfigurations.<name>.cli`); wire it into `flakeModule` (perSystem),
   `mkFlakeOutputs`, and the root flake. `packages.cluster-<name>` unblocks NIO's
   prebuild.
5. **[nixcluster]** Flake-parts-independent `nixclusterctl` (§3a/§7a.6): add
   `lib/mkNixclusterctl.nix` (static dispatch into `.cli`), delete the dynamic
   `packages/nixclusterctl.nix`.
6. **[nixcluster]** Ensure the incus module supports clustering (§7a.4).
7. **[NIO]** Route the whole invocation through the cli (§7b): with B4 done,
   verify prebuild+run resolve unchanged; add the optional `NixSpec`
   "no-prebuild" knob + unit tests only if wanted; keep NIO contributing data only.
8. **[fixture]** Create the downstream repo from the template with an incus
   cluster config + `defaultNixosConfiguration`, no node files (§5 Task A);
   commit sops secrets; pin a rev.
9. **[NIO]** Write the example `NixCluster` CR (§5 Task B) and the tier-1
   e2e (`test/e2e/nixcluster_test.go`, gated `NIO_E2E_NIXCLUSTER=1`) that
   provisions Lima nodes (§6), applies the CR, and asserts §8.
10. **[NIO]** Replace the coarse per-member status stub with the converge JSON
   parser once the JSON schema is fixed (§7b).
