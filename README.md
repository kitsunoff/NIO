<div align="center">

# NIO

**A Kubernetes operator that manages NixOS hosts and clusters declaratively**

Describe machines, configurations and clusters as custom resources; NIO installs, converges and reconciles the real hosts to match — and removes the ones you took away.

[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg?style=flat-square)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?style=flat-square&logo=go&logoColor=white)](go.mod)
[![Lint](https://img.shields.io/github/actions/workflow/status/kitsunoff/NIO/lint.yml?branch=main&label=lint&style=flat-square)](../../actions/workflows/lint.yml)
[![Tests](https://img.shields.io/github/actions/workflow/status/kitsunoff/NIO/test.yml?branch=main&label=tests&style=flat-square)](../../actions/workflows/test.yml)
[![Security](https://img.shields.io/github/actions/workflow/status/kitsunoff/NIO/security.yml?branch=main&label=security&style=flat-square)](../../actions/workflows/security.yml)
[![Nix-native](https://img.shields.io/badge/workloads-Nix--native-5277C3?style=flat-square&logo=nixos&logoColor=white)](#nix-native-workloads)

</div>

---

> [!WARNING]
> A `NixosConfiguration` with `fullInstall: true` runs `nixos-anywhere`, which
> **wipes the target disk**. NIO decides install-vs-switch by probing the host, so a
> `Machine` pointed at the wrong address is a host you may destroy.

> [!IMPORTANT]
> `storeRef` is a **substituter**, not a cache the pods fill. Nothing is pushed into
> a `NixStore` unless a `builderRef` is set too. What actually makes day-two
> convergence fast is the builder's own `/nix` — and only when that `NixBuilder` has
> `spec.storage`, otherwise its `/nix` is an `emptyDir` that dies with the pod.

## Table of contents

- [What you get](#what-you-get)
- [Install](#install)
- [Converge one host](#converge-one-host)
- [Converge a cluster](#converge-a-cluster)
- [Nix-native workloads](#nix-native-workloads)
- [Build acceleration](#build-acceleration)
- [What the status actually means](#what-the-status-actually-means)
- [Development](#development)
- [Architecture](#architecture)
- [Known limitations](#known-limitations)
- [License](#license)

## What you get

| Kind | What it does |
| --- | --- |
| **`Machine`** | A host NIO can reach over SSH. Reports discoverability and collects facts (architecture from `uname -m`) that other kinds depend on. |
| **`NixosConfiguration`** | Converges **one** host: optional first-time install via `nixos-anywhere`, then a day-two cron that keeps it in sync, then a decommission job on deletion. |
| **`NixCluster`** | Selects Machines into node groups by label, maps opaque `values` onto each, and drives **one** whole-cluster converge. Ordering and post-ops live in the cluster's own flake, not in NIO. |
| **`NixStore`** | A shared Nix binary cache that workloads substitute from over HTTP. |
| **`NixBuilder`** | A remote builder that workloads delegate builds to. Give it `spec.storage`, or its `/nix` dies with the pod. |
| **`NixDeployment` / `NixJob` / `NixCronJob` / `NixStatefulSet`** | Run a flake attribute as the corresponding Kubernetes workload, pinned to a resolved revision. |

## Install

CRDs and the controller, from source:

```sh
make install                                      # CRDs only
make deploy IMG=ghcr.io/kitsunoff/nio:main
```

Or build one consolidated manifest with an image pinned:

```sh
make build-installer IMG=ghcr.io/kitsunoff/nio:main
kubectl apply --server-side --filename dist/install.yaml
```

Or straight from the release:

```sh
kubectl apply --server-side \
  --filename https://github.com/kitsunoff/NIO/releases/latest/download/install.yaml
```

> [!IMPORTANT]
> `--server-side` is required, not a preference. These CRD schemas are larger than
> the 256 KB limit on the annotation a client-side apply writes, so a plain
> `kubectl apply` fails with `metadata.annotations: Too long`.

The image is published multi-arch (`linux/amd64`, `linux/arm64`) on every push to
`main` as `ghcr.io/kitsunoff/nio:main` and `:sha-<short>`; a tagged release publishes
`:v1.0.0`, `:1.0.0` and `:1.0`.

## Converge one host

1. **Give NIO a host and a key.**

   ```yaml
   apiVersion: nio.homystack.com/v1alpha1
   kind: Machine
   metadata: {name: web-01, namespace: apps}
   spec:
     host: 10.0.0.11
     sshUser: root
     sshKeySecretRef: {name: machine-ssh}   # a Secret with ssh-privatekey
   ```

2. **Point a configuration at a flake attribute.**

   ```yaml
   apiVersion: nio.homystack.com/v1alpha1
   kind: NixosConfiguration
   metadata: {name: web, namespace: apps}
   spec:
     machineRef: {name: web-01}
     source:
       gitRepo: https://github.com/acme/infra
       ref: main
     flakeAttr: web-01
     fullInstall: false          # true wipes the disk and installs from scratch
     dayTwoSchedule: "*/30 * * * *"
   ```

| Field | Why it matters |
| --- | --- |
| `machineRef` | Which `Machine` to converge; must be in the same namespace |
| `source` | The flake repo, by branch (`ref`) or pinned commit (`rev`) |
| `flakeAttr` | The `nixosConfigurations.<attr>` to switch to |
| `fullInstall` | `true` runs `nixos-anywhere` **once**, then hands over to day-two |
| `dayTwoSchedule` | How often the host is reconciled back to the flake |

## Converge a cluster

A `NixCluster` selects Machines rather than naming them, so growing the cluster is
labelling a machine:

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata: {name: prod, namespace: apps}
spec:
  source: {gitRepo: https://github.com/acme/prod-cluster, ref: main}
  sshKeyRef: {name: cluster-ssh}
  ageKeyRef: {name: cluster-age}
  nodeGroups:
    - name: control-plane
      selector: {matchLabels: {role: server}}
      count: 3                      # stable, sticky subset
      values: {k3s: {role: server}} # opaque to NIO; becomes the member's attrset
    - name: workers
      selector: {matchLabels: {role: worker}}
      values: {k3s: {role: agent}}
```

Selection is **deterministic, stable and sticky**: candidates are sorted by name, a
Machine belongs to exactly one group (first match wins), and with a `count` the
already-selected members are kept while vacancies are topped up — adding a
lower-sorting Machine does not evict anyone. Under-fill surfaces as an
`Underprovisioned` condition instead of silently picking fewer.

```text
$ kubectl --namespace apps get nixclusters,nixstores,nixbuilders
NAME                                PHASE   CONVERGE   AGE
nixcluster.nio.homystack.com/prod                      3m28s

NAME                               PHASE   SUBSTITUTER   READY   AGE
nixstore.nio.homystack.com/store                                 3m28s

NAME                                         PHASE   ENDPOINT   READY   AGE
nixbuilder.nio.homystack.com/linux-builder                              3m28s
```

NIO renders one node file per selected Machine into the cluster's flake checkout and
owns a single converge `NixCronJob`. Everything about *how* the cluster comes up —
ordering, joining, secrets, post-ops — lives in that flake, via
[nixcluster](https://github.com/kitsunoff/nixcluster). NIO stays abstract: it does
not know what k3s is.

## Nix-native workloads

The workload kinds run a **flake attribute** instead of a container image. NIO
resolves the revision, pins the pod template to it, and rolls the workload when the
revision, the installable or the arguments change.

| NIO kind | Compiles to | Semantics |
| --- | --- | --- |
| `NixDeployment` | `apps/v1 Deployment` | Long-running service; rolling update on a new revision |
| `NixJob` | `batch/v1 Job` | Run-to-completion; re-run on a new revision |
| `NixCronJob` | `batch/v1 CronJob` | Scheduled run; optional immediate run on a new revision |
| `NixStatefulSet` | `apps/v1 StatefulSet` | Ordered, stable-identity pods |

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixCronJob
metadata: {name: nightly, namespace: apps}
spec:
  nix:
    source: {gitRepo: https://github.com/acme/web, ref: main}
    run: .#report
    storeRef: {name: store}
  cronJobTemplate:
    schedule: "0 2 * * *"
    concurrencyPolicy: Forbid
```

Pods build the flake in init-containers and `nix run` it. See [`examples/`](examples/)
for one manifest per kind, and
[`docs/design/nix-workloads.md`](docs/design/nix-workloads.md) for the authoritative
design.

## Build acceleration

The field documentation states what the code does, not what would be convenient —
`kubectl explain` is the contract:

```text
$ kubectl explain nixcluster.spec.builderRef
GROUP:      nio.homystack.com
KIND:       NixCluster
VERSION:    v1alpha1

FIELD: builderRef <Object>

DESCRIPTION:
    BuilderRef optionally delegates the converge build to a NixBuilder (same
    namespace) instead of building inside the converge pod. This is what makes
    day-two converges fast: the member closures are realized on the builder and
    stay in the builder's /nix — so give that NixBuilder `spec.storage`, or its
    /nix is an emptyDir and nothing survives the pod. The builder must be able
    to build the members' systems: a NixBuilder without `spec.systems` is
    advertised for both common Linux architectures, and because delegation sets
    `max-jobs = 0` there is no local fallback if it cannot.
```

Because there is no local fallback, NIO refuses a cluster whose builder **provably**
cannot build a member's architecture — the builder declares an explicit
`spec.systems` and a selected Machine reported an architecture it does not cover.
The cluster reports `Blocked` with reason `BuilderSystemMismatch`, naming the member
and the system it needs, and the converge child is suspended so it stops firing runs
that cannot succeed. Nothing is blocked on a guess: an unqualified builder, an absent
builder, or a Machine whose facts have not been collected all pass silently.

## What the status actually means

On an operator that touches real machines, a vague status is worse than none:

| Situation | What NIO reports |
| --- | --- |
| A referenced store/builder is missing or unready | Phase `Blocked`, `Stalled` naming the reference, and members keep their **last reported** status — a stalled converge applied nothing, so blaming the nodes would be a lie |
| The converge ran and its latest run failed | Phase `Degraded`, members `Failed`. A CronJob keeps scheduling whether its runs succeed or not, so the **runs** are what is observed, not the schedule |
| A host already carries a configuration but convergence is currently broken | `Applied=True` alongside `Degraded`. `Applied` describes the machine, not the latest run |
| Fewer Machines match than a group's `count` | `Underprovisioned=True`, and the members that do match are still converged |

## Development

```sh
make manifests generate          # CRDs, RBAC, deepcopy
make test                        # unit + envtest
make lint                        # golangci-lint, 0 issues expected
make test-e2e                    # Kind e2e (creates its own cluster; ~10 min)
```

E2E runs against a Kind cluster pinned to `kindest/node:v1.32.2` on purpose:
containerd 2.2.0+ rejects the `nixos/nix` image because of absolute symlinks in
`/etc/passwd`, so bumping it silently breaks every Nix workload pod.

> [!NOTE]
> A **failed** `make test-e2e` leaves its Kind cluster behind and the next run
> reuses it. Delete it first — `kind delete cluster --name go-operator-test-e2e` —
> or you will debug contamination instead of your change.

## Architecture

```text
Machine ─────────────► reachability + hardware facts (architecture)
   ▲
   │ machineRef                        selector + values
   │                                          │
NixosConfiguration                        NixCluster
   │  install (once) ──► NixJob                │
   │  day-two ────────► NixCronJob             │ renders modules/nodes/<m>.nix
   │  decommission ───► NixJob (orphan)        ▼
   │                                     NixCronJob "<cluster>-converge"
   ▼                                           │  runs nixcluster's converge
 one host                                      ▼
                                          the whole cluster
```

Every path funnels into the same primitive: **an idempotent script on a `Forbid`
cron**. `NixosConfiguration` switches one host; `NixCluster` reconciles many. Both
delegate the actual Nix work to the flake in the user's repository, which is why NIO
carries no opinion about k3s, Incus or anything else.

Resolved design decisions live in
[`docs/design/DECISIONS.md`](docs/design/DECISIONS.md); the cluster design is
[`docs/design/cluster-crd.md`](docs/design/cluster-crd.md).

## Known limitations

- **`v1.0.0` ships with 10 advisories reachable from NIO's own code**, disclosed in
  the release notes rather than left to be discovered. Fixing them needs a
  dependency bump, which forces a newer toolchain, which forces a newer
  golangci-lint, which surfaces 51 findings from linters this project already
  enables — tracked for `v1.0.1`. The `Security` workflow reports the findings on
  every run and is deliberately non-blocking until that chain lands.
- **Per-member converge status is coarse.** It is derived from the run's outcome; the
  per-member JSON that converge emits is not parsed yet.
- **A converge pod materialises every member closure in an unbounded `emptyDir`
  `/nix`**, even with a builder. `NixSpec.localStore` bounds it but is not reachable
  from a `NixCluster`.
- **`MemberStatusApplying` is unreachable on the cluster's default path**, because a
  one-off run triggered by a revision change never appears in `ActiveJobs`.
- **A failing converge reports a timestamp, not a pod.** You still have to find the
  failed Job yourself.

## License

Apache 2.0. See [LICENSE](LICENSE).
