---
title: Install the operator
sidebar_label: Installation
sidebar_position: 1
description: Install NIO into a Kubernetes cluster, and why the install needs a server-side apply.
---

# Install the operator

## Requirements

- A Kubernetes cluster you can create CRDs in (cluster-admin).
- `kubectl` new enough to support `--server-side` (v1.18+; effectively any
  supported version).
- Outbound network from the operator's pods, and SSH reachability from those
  pods to whatever hosts you intend to manage.

## Install

```sh
kubectl apply --server-side \
  --filename https://github.com/kitsunoff/NIO/releases/download/v1.0.0/install.yaml
```

That single artifact contains the namespace, the nine CRDs, RBAC, and the
controller `Deployment`.

For the newest release, take the corresponding `install.yaml` from the
[releases page](https://github.com/kitsunoff/NIO/releases) rather than pinning
`v1.0.0` indefinitely.

## `--server-side` is required, not a preference

Leave `--server-side` off and the install fails. A client-side `kubectl apply`
records the entire object it sent in the
`kubectl.kubernetes.io/last-applied-configuration` annotation, and annotations
are capped at 256 KB (262144 bytes) in total per object.

Six of NIO's nine CRD schemas are larger than that cap on their own, because
they embed complete native Kubernetes workload schemas (`batch/v1 JobSpec`,
`apps/v1 DeploymentSpec`, and so on) so that you can write a native pod template
inline:

| CRD | Schema size | Over the 256 KB annotation cap? |
| --- | ---: | --- |
| `machines.nio.homystack.com` | 11 787 B | no |
| `nixclusters.nio.homystack.com` | 20 621 B | no |
| `nixosconfigurations.nio.homystack.com` | 21 151 B | no |
| `nixbuilders.nio.homystack.com` | 612 084 B | **yes** |
| `nixstores.nio.homystack.com` | 612 465 B | **yes** |
| `nixjobs.nio.homystack.com` | 704 901 B | **yes** |
| `nixdeployments.nio.homystack.com` | 705 061 B | **yes** |
| `nixstatefulsets.nio.homystack.com` | 705 227 B | **yes** |
| `nixcronjobs.nio.homystack.com` | 705 610 B | **yes** |

So a plain apply fails, once per oversized CRD:

```text
The CustomResourceDefinition "nixjobs.nio.homystack.com" is invalid:
metadata.annotations: Too long: may not be more than 262144 bytes
```

A server-side apply does not write that annotation at all — the apiserver tracks
ownership in `metadata.managedFields` instead — so the size cap never comes into
play.

:::tip Recovering from a partial client-side install
A plain apply is not atomic: the three small CRDs and all the RBAC **do** get
created before the six big ones fail. Re-running with `--server-side` over that
partial state works as-is. If your apiserver does report field-manager
conflicts, add `--force-conflicts` so it takes over the fields the client-side
apply claimed:

```sh
kubectl apply --server-side --force-conflicts \
  --filename https://github.com/kitsunoff/NIO/releases/download/v1.0.0/install.yaml
```
:::

## Verify the install

All nine CRDs should be established:

```sh
kubectl get crd --output name | grep nio.homystack.com
```

```text
customresourcedefinition.apiextensions.k8s.io/machines.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixbuilders.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixclusters.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixcronjobs.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixdeployments.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixjobs.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixosconfigurations.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixstatefulsets.nio.homystack.com
customresourcedefinition.apiextensions.k8s.io/nixstores.nio.homystack.com
```

The controller lands in the namespace **`go-operator-system`**, not `nio-system`
— the manifest still carries the kubebuilder scaffold's name prefix, and so does
the Deployment:

```sh
kubectl rollout status deployment/go-operator-controller-manager \
  --namespace go-operator-system --timeout=180s
```

```text
NAME                             IMAGE
go-operator-controller-manager   ghcr.io/kitsunoff/nio:v1.0.0
```

The API group is `nio.homystack.com`, version `v1alpha1`. It is unchanged from
the operator's original home and is **not** derived from the GitHub repository
name; do not expect a `kitsunoff.io` group.

Six of the nine kinds have short names:

```text
NAME                  SHORTNAMES   APIVERSION                   NAMESPACED   KIND
machines                           nio.homystack.com/v1alpha1   true         Machine
nixbuilders           nbuilder     nio.homystack.com/v1alpha1   true         NixBuilder
nixclusters                        nio.homystack.com/v1alpha1   true         NixCluster
nixcronjobs           nixcron      nio.homystack.com/v1alpha1   true         NixCronJob
nixdeployments        nixdeploy    nio.homystack.com/v1alpha1   true         NixDeployment
nixjobs               nixj         nio.homystack.com/v1alpha1   true         NixJob
nixosconfigurations                nio.homystack.com/v1alpha1   true         NixosConfiguration
nixstatefulsets       nixsts       nio.homystack.com/v1alpha1   true         NixStatefulSet
nixstores             nstore       nio.homystack.com/v1alpha1   true         NixStore
```

Note that `Machine`, `NixCluster` and `NixosConfiguration` have **no** short
name, and that the kind is `NixosConfiguration` — lowercase `os`, not
`NixOSConfiguration`.

## Security state

:::warning
`v1.0.0` ships with **10 known security advisories reachable from code NIO
actually executes** — in `google.golang.org/grpc`, `golang.org/x/text`,
`golang.org/x/net`, `golang.org/x/crypto` (five separate advisories) and
`go.opentelemetry.io/otel/sdk`, plus a tail in the Go standard library. They are
disclosed in that release's own notes rather than left to be discovered.

Check the [releases page](https://github.com/kitsunoff/NIO/releases) for a newer
release before deploying, and treat the operator's namespace as privileged
regardless: it holds SSH private keys for every host you manage.
:::

## Uninstall

Deleting the CRDs deletes every NIO custom resource in the cluster. Delete your
`NixosConfiguration` objects **first** and let their finalizers run, otherwise
the decommission step described in
[the reference](../reference/nixosconfiguration.md#deletion-and-onremoveflake)
never happens and your hosts keep the configuration they were last given.

## Next

[Converge your first machine](./first-machine.md).
