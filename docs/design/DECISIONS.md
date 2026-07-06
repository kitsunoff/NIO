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

**Shipped in 1.0 (confirmed on Kind).**

- `NixStore` server runs **harmonia from nixpkgs inside the `nixos/nix` image**
  (there is no maintained standalone harmonia OCI image, and mounting the store
  PVC at `/nix` would shadow the image's own nix). A `bootstrap` init seeds nix
  into the PVC-backed `/nix`; the server then writes a minimal harmonia config
  (harmonia rejects `workers = 0`, so `workers = 4`; `sign_key_paths` points at
  the generated/mounted signing key) and execs `nix run nixpkgs#harmonia`. It
  publishes `substituterURL` (HTTP) + `publicKey`; `storeURI` is an `ssh-ng://`
  address for future pushes.
- `NixBuilder` runs a `bootstrap` init plus a foreground `nix-daemon` so the
  single worker stays Ready as a nix build backend, wired to its `storeRef`.
- **Runner pods build in-pod and substitute from the `NixStore`** (which falls
  through to cache.nixos.org). This is the reliable, e2e-proven path: all six
  kinds pass on a real Kind cluster via substitution.

**Deferred to v1.1.** The fully-delegated builder→store→substitute remote-build
handoff (pods dispatch the build to the `NixBuilder`, which realizes directly
into the shared `NixStore` with no `nix copy` plumbing) is a documented follow-up.
The `NixBuilder` is a real, Ready worker in 1.0; the remote-build *transport* is
what lands in v1.1. Blocking 1.0 on it was not warranted.

## ADR-0007 — Pin the e2e Kind node image to containerd < 2.2

**Context.** The Nix runner/store/builder pods use the `nixos/nix` image, whose
`/etc/passwd` and `/etc/group` are absolute symlinks into `/nix/store`.
containerd 2.2.0 (built with Go 1.24) rejects such images at container-create
with `openat etc/passwd: path escapes from parent` (containerd#12683). The
default `kind` 0.31 node image ships containerd 2.2.0.

**Decision.** Pin the e2e Kind cluster to `kindest/node:v1.32.2` (containerd
2.0.3) via `test/e2e/kind-config.yaml`, wired into `make setup-test-e2e`.

**Consequences.** e2e runs Nix pods reliably. Production clusters must likewise
run containerd < 2.2 (or a fixed release) until upstream resolves the symlink
handling; noted for operators in the README.
