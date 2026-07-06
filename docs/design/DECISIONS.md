# Architecture Decision Records — NIO Nix-native Workloads

Resolves the open questions from `docs/design/nix-workloads.md` §11 and records
bootstrap-time decisions. Each ADR is short: context, decision, consequences.

## ADR-0001 — Repository layout: module root == repo root

**Context.** The Go operator lived under `go-operator/` in the legacy repo, next to
a Python/Kopf operator at the root. GitHub reads `.github/` and CI only from the
repo root, and a subdir module complicates paths.

**Decision.** Promote `go-operator/` contents to the repo root and delete the legacy
Python operator. The Go module is the repository.

**Consequences.** CI workflows (`.github/workflows`) need no `working-directory`.
Makefile/PROJECT relative paths are unchanged. Legacy Python history remains
reachable on the old `homystack/NIO` branches.

## ADR-0002 — Go module path renamed, API group unchanged

**Context.** The repo moved to `github.com/kitsunoff/NIO`. The Go import path must
match, but the public API identity in the design doc is `nio.homystack.com`.

**Decision.** Rename the module to `github.com/kitsunoff/nixos-operator`. Keep the
API group `nio` / domain `homystack.com` (`nio.homystack.com`) verbatim.

**Consequences.** Existing CRs and RBAC referencing `nio.homystack.com` keep working.

## ADR-0003 — E2E CI is manual (O-none, ops decision)

**Context.** E2E needs a privileged Kind cluster running `nixos/nix` and performs
real Nix builds — heavy and flake-prone on hosted runners per commit.

**Decision.** `make test` + `make lint` run on every push/PR; `make test-e2e` runs
via `workflow_dispatch` (manual) and locally.

**Consequences.** PR CI stays fast and reliable; e2e is exercised locally on Kind
(the release gate) and on demand in CI.

## ADR-0004 — O3: shared generic workload reconciler

**Context.** §11 O3 — one controller per workload kind, or a shared generic
reconciler with a per-kind `project()` strategy.

**Decision.** One shared generic reconciler parameterized by a per-kind strategy
(`project()` + `observe()`); `NixStore` and `NixBuilder` get their own controllers.

**Consequences.** Less duplicated resolve/preflight/observe code across the four
workload kinds. Per-kind behavior isolated to small strategy implementations.

## ADR-0005 — O7: defaulting webhook for embedded native specs

**Context.** §11 O7 — embedding native specs makes `selector` /
`template.spec.containers` required upstream; a workload should be just a `nix:`
block. Resolve via a defaulting webhook or by relaxing those fields in the CRD.

**Decision.** Provide a defaulting webhook that fills operator-owned fields
(selector, app container, init-containers, volumes) so native required-field
validation passes; cert-manager is already wired into the e2e scaffold.
**Fallback:** if webhook certs prove flaky in Kind, relax the two required fields
in the generated CRD schema and default them in the reconciler instead. The chosen
path is recorded here once e2e is proven.

**Update (chosen path).** Implemented the fallback, not the webhook: the native
`<kind>Template` fields are marked `+kubebuilder:validation:Schemaless` +
`+kubebuilder:pruning:PreserveUnknownFields`, so the CRD accepts a partial (or
absent) template and the reconciler fills the operator-owned fields (selector,
app container, init-containers, volumes, strategy). This avoids webhook-cert
flakiness in Kind and needs no cert-manager for the workload path.

**Consequences.** A minimal workload is a bare `nix:` block. Less server-side
validation of the embedded template (acceptable for v1alpha1; the reconciler and
native controllers validate downstream).

## ADR-0006 — O8: NixStore = harmonia, NixBuilder realizes into the store

**Context.** §11 O8 — "builder realizes into the store, others substitute" needs a
concrete mechanism; plain nix `builders=` copies outputs back to the requesting
pod, not into the shared store.

**Decision (target).**

- `NixStore` server = `harmonia` (HTTP binary cache) in front of a nix daemon on
  the PVC. Publishes `substituterURL` (HTTP read) + `publicKey`; `storeURI` =
  `ssh-ng://` to the daemon for pushes.
- `NixBuilder` = single nix-daemon/sshd worker whose own store IS the `NixStore`
  PVC (or which pushes via a `post-build-hook` running `nix copy --to "$STORE_URI"`).
  Whichever is demonstrably green in e2e is the shipped path.

**Fallback (documented).** If the full builder→store→substitute handoff cannot be
made green within bounded effort, ship the working subset — local in-pod build with
`NixStore` substitution (a real path) — keep everything green, and mark delegated
remote build as a `v1.1` follow-up. A deep, unavoidable infeasibility here is a §8
blocker, not a silent stub, because "all six kinds fully implemented" is a gate
condition.

**Consequences.** Highest-risk area; the concrete mechanism is confirmed during
Phase D/F and this ADR updated with the final shipped design.
