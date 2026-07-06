# NIO

NIO is a Kubernetes operator for running **Nix** on and in your cluster. It has
two layers:

1. **NixOS host management** — `Machine` and `NixosConfiguration` manage NixOS
   hosts over SSH (connectivity, hardware discovery, and applying whole-system
   configurations via `nixos-rebuild` / `nixos-anywhere`).
2. **Nix-native workload primitives** — six CRDs that mirror the core Kubernetes
   workload kinds but are driven by a **Nix flake attribute** instead of a
   container image. You point at a git repo, a ref, and a flake installable
   (e.g. `.#server`), and the operator resolves the revision and compiles it into
   a native Kubernetes workload whose pods build the flake in init-containers and
   `nix run` it.

The authoritative design is [`docs/design/nix-workloads.md`](docs/design/nix-workloads.md);
resolved design decisions are in [`docs/design/DECISIONS.md`](docs/design/DECISIONS.md).

## The workload kinds

| NIO kind          | Compiles to            | Semantics                                              |
| ----------------- | ---------------------- | ------------------------------------------------------ |
| `NixDeployment`   | `apps/v1 Deployment`   | Long-running service; rolling update on a new revision |
| `NixJob`          | `batch/v1 Job`         | Run-to-completion; re-run on a new revision            |
| `NixCronJob`      | `batch/v1 CronJob`     | Scheduled run; optional immediate run on a new revision |
| `NixStatefulSet`  | `apps/v1 StatefulSet`  | Ordered, stateful; rolling update on a new revision    |

backed by two infrastructure kinds:

| NIO infra kind | Backed by     | Role                                                    |
| -------------- | ------------- | ------------------------------------------------------- |
| `NixStore`     | StatefulSet   | A centralized Nix binary-cache **server** (PVC-backed)  |
| `NixBuilder`   | StatefulSet   | A single **builder worker**                             |

### How a workload runs

The operator is a compiler, not a builder. On each reconcile it:

1. **Resolves** the mutable `ref` into an immutable commit — a pinned `rev`, a
   Flux source's `status.artifact`, or `git ls-remote` polling.
2. **Projects** a native workload, stamping the revision into the pod template.

Each pod then, in three init-containers, seeds Nix into a pod-local `/nix`
(`bootstrap`), checks out the source (`fetch-source`), and materializes the
flake (`instantiate` — substituting already-built paths from the `NixStore`,
building on the `NixBuilder` or locally otherwise). The app container then
`nix run`s the installable. A broken commit fails in `instantiate` and **stalls
the rollout** while the previous revision keeps serving.

## Quickstart

Declare a store, then a workload that references it:

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixStore
metadata:
  name: store
  namespace: apps
spec:
  storage:
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: 50Gi
---
apiVersion: nio.homystack.com/v1alpha1
kind: NixDeployment
metadata:
  name: web
  namespace: apps
spec:
  nix:
    source:
      gitRepo: https://github.com/acme/web
      ref: main
    run: .#server
    args: ["--port", "8080"]
    storeRef:
      name: store
  deploymentTemplate:
    replicas: 3
```

More manifests for every kind are in [`examples/`](examples/).

## Getting started (developers)

Prerequisites: Go (see `go.mod`), Docker (OrbStack/Colima/Docker), `kubectl`,
and `kind` for e2e. A pinned dev shell is provided:

```sh
nix develop        # go, kubectl, kind, golangci-lint, and build tooling
```

Common targets:

```sh
make build         # build the manager binary
make test          # unit + envtest
make lint          # golangci-lint (must be zero findings)
make docker-build  # build the manager image
make test-e2e      # end-to-end on a real Kind cluster (exercises all six kinds)
make install       # install CRDs into the current-context cluster
make deploy IMG=<image>   # deploy the operator
```

> **Kind note:** the e2e suite pins `kindest/node:v1.32.2` (containerd 2.0.3).
> containerd 2.2.0+ rejects the `nixos/nix` image's absolute `/etc/passwd`
> symlink ([containerd#12683](https://github.com/containerd/containerd/issues/12683)).

## License

Apache License 2.0.
