# E2E — `Cluster`

> Read `00-harness-and-conventions.md` and `02-nixosconfiguration.md` first.
> Design: `docs/design/cluster-crd.md`. This CRD groups `Machine`s, generates
> per-member node files into a flake-parts repo, and drives one idempotent
> `converge` `NixCronJob`.
>
> **Naming note:** this doc predates the decision to rename the CRD Kind
> `Cluster` → `NixCluster` (see `docs/design/nixcluster-integration-testing.md`
> §0). References to `Cluster` below map 1:1 to `NixCluster` once the rename
> lands. The tier-1 example was originally k3s; the **current tier-1 target is an
> Incus cluster** (see the integration-testing plan §4–§5).

## What the controller does (recap)

`Cluster` = abstract grouping of Machines + opaque per-group `values`, reconciled
by ONE `NixCronJob` running the downstream cluster's `converge` (idempotent,
ordered by the cluster's nixcluster modules). NIO: stable-select machines per
nodeGroup → render `modules/nodes/<machine>.nix` (`nixcluster.<c>.members.<m> =
recursiveUpdate (fromJSON <values>) { install.ip=<host>; }`) as `additionalFiles`
→ ensure the converge `NixCronJob` (Forbid + triggerOnChange, mounts cluster SSH
key + age key) → reflect per-node status.

## Prerequisites / blockers (before tier-1 can pass)

- **nixcluster `converge` module must exist** (see `docs/design/cluster-crd.md`
  → "Required nixcluster additions"): the always-loaded `converge.steps` engine,
  the `converge` command emitting per-member JSON, and `defaultNixosConfiguration`.
  Until then only tier-2 (NIO-side selection/generation) is testable.
- **Downstream flake-parts fixture repo**: a small public repo initialized from
  the nixcluster template with a `modules/clusters/<c>.nix` (imports k3s,
  `defaultNixosConfiguration` = a minimal aarch64-linux NixOS base) and NO node
  files (NIO injects them). Pin a `rev` (operator image has no git).
- **Converge job image** needs `nix`+`git`+`ssh` (same B1 constraint as
  `02-nixosconfiguration.md`); the converge pod builds aarch64-linux and SSHes to
  member VMs — set `jobTemplate.image` accordingly and ensure a writable `/nix`.
- **Machines + targets**: `Machine` objects labeled for the selectors; for real
  converge, each maps to a reachable Colima NixOS VM (see `01-machine.md` /
  `02` tier-1 for the VM pattern). Cluster SSH key must reach all member VMs.

## Tier 2 — NIO-side, on Kind (no real hosts, no converge run)

Verify selection, stability, node-file generation, and cron shape without running
converge. Create `Machine` objects (no real SSH needed if the Cluster is created
paused / the cron never scheduled, or assert before first run).

### S1 — Deterministic + stable selection

1. Create 5 Machines `m-01..m-05` labeled `role=worker`.
2. Apply a Cluster with a `workers` nodeGroup, `selector role=worker`, `count: 3`.
3. Assert `status.nodeGroups[workers].members` = the **3 lowest names**
   (`m-01,m-02,m-03`), in sorted order.
4. Reconcile again (touch the Cluster) → **identical** member list/order (stable).
5. Add `m-00` (sorts first) → members stay `m-01,m-02,m-03` (**sticky**: an
   already-selected member is not evicted for a lower name).
6. Delete `m-02` → it is replaced by the next sorted candidate (`m-04`), others
   unchanged.

### S2 — One nodeGroup per machine

1. Two nodeGroups whose selectors both match some Machine.
2. Assert that Machine appears in exactly the **first** group (spec order); the
   later group excludes it.

### S3 — Node-file generation (value mapping)

1. Cluster with `values: { k3s: { role: server } }`, a selected Machine `node-01`
   with `spec.host=10.0.0.5`.
2. Assert the generated `additionalFiles` on the converge cron contain
   `modules/nodes/node-01.nix` whose content assigns
   `nixcluster.<cluster>.members.node-01` = `recursiveUpdate (fromJSON …) {
   install.ip = "10.0.0.5"; }`, includes `k3s.role = "server"` (via the JSON),
   and does **not** set `nixosConfiguration` (inherits the default).
3. Assert path/charset validation (reuse `validateFilePath`) rejects a hostile
   Machine name.

### S4 — Converge NixCronJob shape

1. Assert exactly one owned `NixCronJob` `<cluster>-converge` with:
   - `nix.run == ".#cluster-<cluster>"`, `nix.args == ["converge"]`;
   - `cronJobTemplate.schedule == spec.dayTwoSchedule` (default `*/30 * * * *`);
   - `cronJobTemplate.concurrencyPolicy == Forbid`;
   - `spec.nix.triggerOnChange == true`;
   - the cluster SSH key + age key volumes mounted; the node files present as
     `additionalFiles`;
   - `activeDeadlineSeconds` set.

### S5 — Under-provisioned

1. `count: 3` but only 2 matching Machines → `status` condition
   `Underprovisioned` (surfaced, not silent), members = the 2 available.

## Tier 1 — real converge against Colima NixOS VMs (gated)

Needs the nixcluster `converge` module + VMs. Gate behind an env flag; skip
cleanly when absent.

### S6 — Converge a 2-node cluster to Ready

1. Two Colima NixOS VMs; two Machines (`role=server`, `role=agent`) pointing at
   them; cluster SSH key Secret; age key Secret; committed sops in the repo.
2. Apply a Cluster (control-plane count 1 server, workers 1 agent).
3. Assert the converge `NixCronJob` fires (triggerOnChange), the run **succeeds**,
   and `status.nodeGroups[*].members[*].status == Applied` (**per-node**).
4. Assert on the VMs: server is up (`nixos-version`; k3s server running), agent
   joined. Ordering held (server before agent) — from converge's deps graph.

### S7 — Idempotent re-run (self-heal)

1. Let the converge cron run twice (or trigger a second run).
2. Assert the second run is a **no-op convergence** (no reinstall — install-once
   held; `nixos-rebuild switch` reports no changes), cluster stays Ready. Prove
   drift-heal: change something on a node by hand → next converge restores it.

### S8 — Membership change re-selection

1. Add a third `role=worker` Machine/VM → it is selected, gets a node file, and
   the next converge brings it up. Remove a Machine → its member drops from
   `status` and (later) the node.

### S9 — Deletion

1. Delete the Cluster → the converge `NixCronJob` (and generated state) is
   removed; finalizer cleared. (Node teardown / decommission on the VMs is
   deferred — document current behavior: deleting the Cluster stops converging,
   it does not wipe the nodes unless a decommission path is added.)

## Assertions cheat-sheet

| What | jsonpath / query |
| --- | --- |
| group members | `{.status.nodeGroups[?(@.name=='workers')].members[*].name}` |
| per-node status | `{.status.nodeGroups[*].members[*].status}` |
| Underprovisioned | `{.status.conditions[?(@.type=='Underprovisioned')].status}` |
| converge schedule | `kubectl get nixcronjob <cluster>-converge -o jsonpath={.spec.cronJobTemplate.schedule}` |
| converge concurrency | `…{.spec.cronJobTemplate.concurrencyPolicy}` (expect `Forbid`) |
| generated node file | inspect the converge `NixCronJob` `spec.nix.additionalFiles` |

## Out of scope / do not assert

- Provisioning on under-fill (PXE) — deferred.
- Node decommission/wipe on Cluster deletion — deferred.
- NIO-side secret generation — secrets are an input (committed sops + age Secret).
- Which cluster modules are loaded — that is repo-authored, not CRD-driven.

## Suggested placement

`test/e2e/cluster_test.go` behind `//go:build e2e`: tier-2 (`S1`–`S5`) on Kind;
tier-1 (`S6`–`S9`) gated by an env var + skipped without the nixcluster
`converge` module and provisioned VMs. Reuse the `nio-workloads`/store fixtures
where useful; tier-1 shares the Colima VM pattern from `02-nixosconfiguration.md`.
