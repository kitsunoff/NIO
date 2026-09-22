# Operator Runtime Specification

## Purpose

Describes what NIO is as a deployable process: the API surface it serves, the
cross-cutting rules every kind obeys, how the manager is configured and observed,
and how the operator and its CRDs are delivered to a cluster.

## Requirements

### Requirement: API surface

The operator SHALL serve one API group, `nio.homystack.com`, version `v1alpha1`,
containing nine namespaced kinds: `Machine`, `NixosConfiguration`, `NixCluster`,
`NixStore`, `NixBuilder`, `NixDeployment`, `NixJob`, `NixCronJob`, `NixStatefulSet`.
Every kind exposes a status subresource and print columns summarising its state.

#### Scenario: Kinds are servable

- **WHEN** the CRDs are installed
- **THEN** all nine kinds are creatable in a namespace and their status is writable
  only through the status subresource

#### Scenario: Listing shows meaningful columns

- **WHEN** a user lists any NIO kind
- **THEN** the output shows that kind's key state, such as phase, readiness, target or
  resolved revision

### Requirement: References are namespace-local

Every reference between NIO objects, and to Secrets, ConfigMaps and Flux sources,
SHALL resolve in the referencing object's own namespace. Cross-namespace references
are out of scope by design.

#### Scenario: Reference resolution

- **WHEN** any NIO object references another object by name
- **THEN** it is looked up in the referencing object's namespace and nowhere else

### Requirement: Controller registration

The manager SHALL register a controller for each of the nine kinds, each watching its
own kind, the objects it owns, and the external objects its behaviour depends on. A
failure to register any controller SHALL abort startup.

#### Scenario: Startup failure is fatal

- **WHEN** a controller cannot be registered — for example a field index cannot be
  created
- **THEN** the process logs the error and exits non-zero instead of running partially

### Requirement: Manager configuration

The operator SHALL accept flags for the metrics bind address, the health probe bind
address, leader election, metrics security, HTTP/2, and certificate paths for the
metrics and webhook servers. Metrics are disabled unless an address is given, the
probe endpoint defaults to port 8081, leader election is off by default, metrics are
served securely by default, and HTTP/2 is disabled by default.

#### Scenario: Default invocation

- **WHEN** the manager is started with no flags
- **THEN** no metrics endpoint is served, health probes listen on the default port,
  leader election is disabled, and HTTP/2 is off

#### Scenario: Secure metrics

- **WHEN** metrics are enabled with the default security setting
- **THEN** the endpoint is served over HTTPS and requests are authenticated and
  authorized

#### Scenario: Leader election

- **WHEN** leader election is enabled
- **THEN** only one manager instance reconciles at a time

#### Scenario: Version subcommand

- **WHEN** the binary is invoked with the `version` argument
- **THEN** it prints its version and exits without starting the manager

### Requirement: Health probes

The manager SHALL serve liveness and readiness probes so a Kubernetes deployment can
restart or gate traffic on it.

#### Scenario: Probes answer

- **WHEN** the manager is running
- **THEN** its health and readiness endpoints respond successfully

### Requirement: Operational metrics

The operator SHALL expose Prometheus metrics for fleet state and operations: counts
of machines and configurations and their states, SSH connection outcomes and
durations, git, build and job outcomes and durations, retries, and errors by type.

#### Scenario: SSH probe is measured

- **WHEN** a machine's reachability is probed
- **THEN** the attempt's outcome and duration are recorded

#### Scenario: Errors are attributed

- **WHEN** an operation fails
- **THEN** an error counter for that error type is incremented

### Requirement: Events explain state changes

Controllers SHALL record Kubernetes events for the transitions an operator would want
to see — reachability changes, install outcomes and decommission progress — so
`kubectl describe` explains what happened without reading operator logs.

#### Scenario: Install failure is visible

- **WHEN** a full-disk install exhausts its retries
- **THEN** a warning event naming the failure is recorded on the configuration

### Requirement: Least-privilege RBAC is generated from the code

The cluster role the operator ships SHALL be generated from the permissions the
controllers declare, covering the NIO kinds and their statuses and finalizers, plus
the Kubernetes objects the controllers create and observe: Deployments, StatefulSets,
Jobs, CronJobs, Services, Secrets, ConfigMaps, Pods, Events, and the Flux source kinds
it reads.

#### Scenario: Permissions match the code

- **WHEN** the manifests are regenerated
- **THEN** the cluster role reflects exactly the permissions declared alongside the
  controllers

### Requirement: CRDs require server-side apply

The CRD schemas are larger than the annotation limit a client-side apply writes, so
installation SHALL use server-side apply. The project's install targets do so, and the
documentation states the requirement.

#### Scenario: Client-side apply

- **WHEN** a user applies the CRDs without server-side apply
- **THEN** the API server rejects them because the annotation is too long

#### Scenario: Project install targets

- **WHEN** CRDs or the controller are installed through the project's make targets
- **THEN** they are applied server-side with conflicts forced

### Requirement: Installation artifacts

The project SHALL offer three installation paths: the make targets against a
kubeconfig, a consolidated manifest built with a pinned image, and a released
consolidated manifest attached to a tagged release.

#### Scenario: Consolidated manifest

- **WHEN** the installer manifest is built with an image reference
- **THEN** it contains the CRDs and the controller deployment with that image pinned

#### Scenario: Release asset

- **WHEN** a version tag is pushed
- **THEN** a release is published carrying the consolidated install manifest

### Requirement: Container image publication

The controller image SHALL be published for both common Linux architectures. Pushes
to the default branch publish a branch tag and a short-commit tag; a version tag
publishes the full, minor and patch semantic version tags.

#### Scenario: Branch build

- **WHEN** a commit lands on the default branch
- **THEN** a multi-architecture image is published under the branch tag and a
  short-commit tag

#### Scenario: Tagged release

- **WHEN** a version tag is pushed
- **THEN** the image is published under the tag and its semantic version variants

### Requirement: Quality gates

The project SHALL be verifiable locally and in CI with generated manifests in sync,
unit and envtest suites, linting with zero findings, and an end-to-end suite against a
local cluster.

#### Scenario: Generated artifacts are current

- **WHEN** manifests and deepcopy code are regenerated
- **THEN** the working tree shows no drift from what is committed

#### Scenario: End-to-end environment is pinned

- **WHEN** the end-to-end suite runs
- **THEN** it uses the pinned node image, because newer container runtimes reject the
  nix image the workload pods depend on
