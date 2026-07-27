# Examples

Sample manifests for the NIO Nix-native workload kinds. They assume a namespace
`apps` and a `NixStore` named `store` (see `nixstore.yaml`). Apply the store (and
optionally the builder) first, then any workload.

```sh
kubectl create namespace apps
kubectl apply -f examples/nixstore.yaml
kubectl apply -f examples/nixdeployment.yaml
```

| File                     | Kind             |
| ------------------------ | ---------------- |
| `nixstore.yaml`          | `NixStore`       |
| `nixbuilder.yaml`        | `NixBuilder`     |
| `nixdeployment.yaml`     | `NixDeployment`  |
| `nixjob.yaml`            | `NixJob`         |
| `nixcronjob.yaml`        | `NixCronJob`     |
| `nixstatefulset.yaml`    | `NixStatefulSet` |
| `nixcluster.yaml`        | `NixCluster`     |

Replace `gitRepo` / `ref` / `run` with your own flake. Every workload references
the `NixStore`, so its pods **substitute** already-built paths from it instead of
rebuilding them. A store is not a cache the pods fill: paths are pushed into it
only when a `builderRef` is set as well, and then only the paths the pod builds
directly.
