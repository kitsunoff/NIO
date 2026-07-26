# E2E harness & conventions

Shared reference for every per-controller e2e document in this folder. Read this
first; the per-controller docs assume the setup described here and only add the
scenarios specific to their CRD.

## What "e2e" means here

E2E tests run the **real controller-manager image** against a **real Kubernetes
cluster** (Kind), applying real CRs and asserting on real cluster state and CR
status. This is distinct from:

- **unit tests** — pure Go, table-driven, no cluster (`internal/...`, `cmd/...`).
- **envtest tests** — controller reconcile against a real kube-apiserver + etcd
  but **no kubelet** (no pods actually run). These live next to the controllers
  as `*_test.go` and use `suite_test.go`.
- **real-Nix integration tests** — historically exercised the bespoke v1alpha1
  apply-job runner against a real `nix`/`git` on the test host. That runner
  (`cmd/apply`, `internal/applyjob`) was **retired by the v1alpha2 rewrite**,
  which runs apply as `NixJob`/`NixCronJob`; the real-Nix path is now exercised
  through the workload family (`internal/controller/*_nix_test.go`, gated on
  `nix`/`git` being present).

E2E is the only layer where a Nix pod actually pulls the image, substitutes a
closure, and runs — and (for `NixosConfiguration`) the only layer that can drive
a real `nixos-rebuild`/`nixos-anywhere` over SSH to a live host.

## Stack

- **Cluster**: Kind, single control-plane node.
- **Node image**: pinned `kindest/node:v1.32.2` (`test/e2e/kind-config.yaml`).
  Pin is deliberate: containerd 2.2.0+ rejects the `nixos/nix` image because its
  `/etc/passwd`/`/etc/group` are absolute symlinks ("path escapes from parent",
  containerd#12683). The pinned node ships containerd 1.7.x, which accepts them.
  **Do not bump** without re-checking that Nix workload pods still start.
- **Framework**: Ginkgo v2 + Gomega, build tag `e2e` (`//go:build e2e`).
- **cert-manager**: installed in `BeforeSuite` (skippable with
  `CERT_MANAGER_INSTALL_SKIP=true`).

## How the suite runs

`make test-e2e` (target in `Makefile`) does, in order:

1. `setup-test-e2e` — create Kind cluster `go-operator-test-e2e` from
   `test/e2e/kind-config.yaml` if it does not already exist.
2. `manifests generate fmt vet` — regenerate CRDs/deepcopy, format, vet.
3. `go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout 60m`.
4. `cleanup-test-e2e` — `kind delete cluster`.

Inside the Go suite (`test/e2e/e2e_suite_test.go`):

- **`BeforeSuite`**: `make docker-build IMG=example.com/go-operator:v0.0.1` →
  load image into Kind → install cert-manager → create namespace
  `go-operator-system`, label it `pod-security.kubernetes.io/enforce=restricted`
  → `make install` (CRDs) → `make deploy IMG=...` → wait
  `deploy/go-operator-controller-manager` `condition=Available` (180s).
- **`AfterSuite`**: undeploy, uninstall CRDs, delete namespace, uninstall
  cert-manager.

The manager is deployed **once** for the whole suite and shared across every
`Describe`.

## Namespaces

- `go-operator-system` — the controller-manager, metrics service, RBAC. Enforces
  the **restricted** Pod Security Standard, so any pod the operator creates here
  must set a compliant `securityContext` (non-root, drop ALL caps,
  `readOnlyRootFilesystem`, seccomp `RuntimeDefault`).
- `nio-workloads` — where the Nix workload / store / builder CRs are exercised
  (`test/e2e/nixworkloads_test.go`).

## Test helpers (`test/e2e`)

- `utils.Run(cmd)` — run a shell command from the project root, return combined
  output; fails loudly with the output on error.
- `utils.GetNonEmptyLines(out)` — split output into non-empty lines.
- `utils.LoadImageToKindClusterWithName(name)` — `kind load docker-image`.
- `applyYAML(manifest string)` — write a manifest to a temp file and
  `kubectl apply -f` it (in `nixworkloads_test.go`).
- `kget(args...)` — `kubectl -n nio-workloads get ...`, trimmed stdout, empty
  string on error. Ideal for `Eventually(func() string {...}).Should(Equal(...))`
  polling on `jsonpath` of `.status`.

## Assertion conventions

- Poll CR convergence with `Eventually(func() string { return kget(kind, name,
  "-o", "jsonpath={.status.phase}") }, <timeout>, <interval>).Should(Equal("Ready"))`.
- Default `Eventually` timeout is 2m / poll 1s; heavy Nix scenarios override to
  **8–12m** because a cold node substitutes the whole closure from scratch.
- Assert the **owned native resource** too (e.g. `Deployment.status.availableReplicas`),
  not only the CR phase.
- On failure, `AfterEach` dumps controller logs, cluster events, and pod
  descriptions to `GinkgoWriter` — new scenarios inherit this automatically when
  added under the existing `Describe`s.

## Important environmental facts to design around

- The **operator image is distroless** — it has **no `git`**. Anything that must
  `git ls-remote` in-cluster will fail; workload/config specs in e2e therefore
  **pin an explicit resolved revision** (`rev:`/SHA) instead of a branch. The
  `nixworkloads` suite resolves the SHA on the host with `git ls-remote` in
  `BeforeAll` and injects it.
- Nix pods need a **shared store** — the `NixStore` (`store`) and `NixBuilder`
  (`builder`) are created once in the workloads `BeforeAll` and reused.
- External flakes run from public binary caches (substitute-only) so most
  scenarios need **no real build**; the one delegated-build scenario uses a flake
  with a unique marker guaranteed **not** to be cached.

## Where new e2e code goes

- Workload / store / builder / (future) machine + nixosconfiguration scenarios:
  add `It(...)` blocks under the relevant `Describe` in `test/e2e/`.
- Keep every new file behind the `//go:build e2e` tag.
- Any scenario that needs a real NixOS target host over SSH (see the
  `NixosConfiguration` and `Machine` docs) cannot run on Kind alone and is
  called out explicitly as requiring an out-of-cluster target.

## Per-controller documents in this folder

- `01-machine.md` — `Machine`
- `02-nixosconfiguration.md` — `NixosConfiguration`
- `03-nixstore.md` — `NixStore`
- `04-nixbuilder.md` — `NixBuilder`
- `05-nixdeployment.md` — `NixDeployment`
- `06-nixjob.md` — `NixJob`
- `07-nixcronjob.md` — `NixCronJob`
- `08-nixstatefulset.md` — `NixStatefulSet`
- `09-cluster.md` — `Cluster` (abstract cluster + nixcluster `converge`)
