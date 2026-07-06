# NIO 1.0 — Implementation Plan

Living checklist for the "Nix-native Workload Primitives" implementation
(spec: `docs/design/nix-workloads.md`). Tick boxes as work lands on `main`.

## Current focus

Phase D — controllers. Infra controllers (NixStore, NixBuilder) done. Pod
rendering + revision resolution core done as pure, unit-tested functions
(compositeRevision, buildNixConfig, renderPodTemplate, resolveRevision:
Rev>Flux>ls-remote). Next: the generic workload reconciler wiring these into
per-kind project() for NixDeployment/NixJob/NixCronJob/NixStatefulSet with
finalizer, Owns(), and Flux/git-creds watches.

## Blockers

None.

## A. Bootstrap the repo

- [x] Fork `homystack/NIO` → `kitsunoff/NIO`, clone locally.
- [x] Strip legacy Python/Kopf operator; promote `go-operator/` contents to repo root.
- [x] Rename Go module to `github.com/kitsunoff/nixos-operator`; update all imports; `go build ./...`.
- [x] Add `.gitattributes` (linguist-generated for `zz_generated.*.go`, generated CRDs).
- [x] CI runs `make test` + `make lint` on push/PR; `make test-e2e` is manual (documented). CI green on default branch.

## B. Toolchain readiness (local host)

- [x] Container runtime is OrbStack (`docker info` → OperatingSystem: OrbStack).
- [x] `kind` installed (v0.31.0 via nixpkgs).
- [x] `make build` and `make test` green on untouched (Machine/NixosConfiguration) baseline.
- [ ] Committed `flake.nix` dev shell pinning tools (kind, kubectl, go).

## C. API types (§3)

- [x] Shared types `api/v1alpha1/nixworkload_common_types.go`: `NixSource`, `FluxSourceRef`, `NixSpec`, `NixLocalStore`, `NixWorkloadStatus`, `SecretReference`, `LocalObjectReference`.
- [x] `NixStore` types (§3.5) + `NixBuilder` types (§3.6).
- [x] `NixDeployment` (§4.1), `NixJob` (§4.2), `NixCronJob` (§4.3), `NixStatefulSet` (§4.4).
- [x] `kubebuilder create api` for each kind (group `nio`, version `v1alpha1`, domain `homystack.com`); kubebuilder markers per doc.
- [x] `make manifests generate` → CRDs + deepcopy committed.

## D. Controllers & pod rendering (§4.5, §7)

- [x] `NixStore` controller: StatefulSet + headless Service + signing-key Secret; publish status.
- [x] `NixBuilder` controller: single-worker StatefulSet wired to `storeRef`; publish `builderEndpoint`.
- [ ] Generic workload reconciler + per-kind `project()`: resolve revision, infra preflight, SSA native workload with three init-containers, `NIX_CONFIG`, volumes, composite revision annotation, labels, app command.
- [ ] Deployment `maxUnavailable:0` default; StatefulSet ordered update; NixJob re-run + history GC; NixCronJob scheduling + optional immediate Job.
- [ ] Flux source watch/enqueue mapper; git-creds secret field-indexed watch; `Owns()` native kinds; finalizer; RBAC markers.

## E. Unit tests (TDD, envtest)

- [ ] Revision resolution, composite hash stability, pod-template rendering, defaulting, condition transitions, per-kind project(), NixStore/NixBuilder controllers.
- [ ] `make test` fully green.

## F. e2e tests on Kind (§2.3 gate)

- [ ] Extend `test/e2e`: NixStore Ready; NixBuilder Ready; NixDeployment rolls to Ready; NixJob completes; NixCronJob fires; NixStatefulSet ordered roll; broken-revision stalls rollout while old serves.
- [ ] Small fast public flake as test workload; Kind config for nixos/nix image documented.
- [ ] `make test-e2e` fully green on Kind.

## G. Polish

- [ ] `make lint` zero findings. `make docker-build` succeeds.
- [ ] README (root), `examples/` for new kinds, `docs/design/DECISIONS.md` complete.
- [ ] Final full run: `make build test lint test-e2e` all green, output captured.

## H. Release & migration (only after §2 gate honestly green)

- [ ] Re-verify §2 gate from clean `main` checkout.
- [ ] Annotated tag `v1.0.0`; `gh release create v1.0.0`.
- [ ] Archive `homystack/NIO` with README pointer to `kitsunoff/NIO`.
