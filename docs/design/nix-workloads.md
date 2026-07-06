# Design: Nix-native Workload Primitives

Status: **Draft / RFC**
Author: NIO maintainers
Applies to: `go-operator` (module `github.com/kitsunoff/nixos-operator`), API group `nio.homystack.com`, version `v1alpha1`

## 1. Motivation

Today NIO manages **NixOS hosts** (`Machine`) and pushes whole-system
configurations to them over SSH (`NixosConfiguration` → apply `Job` →
`nixos-rebuild` / `nixos-anywhere`). That layer is about *machines*.

This proposal adds a second, lower layer: **Nix-native workload primitives that
run inside the Kubernetes cluster**, mirroring the core Kubernetes workload
kinds but driven by a Nix flake attribute instead of a container image:

| Kubernetes kind | NIO kind          | Semantics                                            |
| --------------- | ----------------- | ---------------------------------------------------- |
| `Deployment`    | `NixDeployment`   | Long-running service, rolling update on new revision |
| `Job`           | `NixJob`          | Run-to-completion, re-run on new revision            |
| `CronJob`       | `NixCronJob`      | Scheduled run, optional immediate run on new revision |
| `StatefulSet`   | `NixStatefulSet`  | Ordered, stateful, rolling update on new revision    |

backed by two **infrastructure** kinds that the operator manages as
StatefulSets and that workloads merely reference:

| NIO infra kind | Backed by    | Role                                                   |
| -------------- | ------------ | ------------------------------------------------------ |
| `NixStore`     | StatefulSet  | A centralized Nix store / cache **server** (PVC-backed) |
| `NixBuilder`   | StatefulSet  | A **single builder worker** that runs `nix build`       |

The user story:

> I point at a git repo, a branch, and a flake attribute (e.g. `.#bebra`), and
> the thing runs. The operator watches the repo. On a new commit it rolls out. A
> `NixDeployment` rolls; a `NixJob` re-runs; a `NixCronJob` runs on schedule (and
> optionally immediately on a new commit); a `NixStatefulSet` rolls with ordering
> guarantees. Builds and the store are shared infrastructure I declare once.

### 1.1 Decided constraints

These were agreed up front and shape the whole design:

1. **Execution is in-cluster.** Each NIO workload **compiles down to a native
   Kubernetes workload** (`Deployment` / `Job` / `CronJob` / `StatefulSet`).
   The `Machine`/SSH layer is **not** involved in running these workloads.
2. **Revision tracking is poll-based** by default (the operator runs
   `git ls-remote` on an interval), with an **optional Flux source** (any
   artifact-producing kind — `GitRepository` / `OCIRepository` / `Bucket`): when
   referenced, the operator reads the resolved revision and artifact URL from the
   Flux object's status instead of polling itself.
3. **The store is a server, not a shared filesystem.** A `NixStore` runs a Nix
   store server (a StatefulSet on a PVC) that **owns its store database**.
   Clients (runner pods and builders) talk to it as ordinary nix
   `--substituters` / store clients over the network — so there is no shared-FS
   mounting, no concurrent-writer corruption, and multi-node "just works".
4. **The operator never builds; pods do.** The operator only resolves the
   revision and stamps it into a native workload. Building happens **in the pods'
   init-containers** — locally, or delegated to a single-worker `NixBuilder` that
   realizes into a `NixStore` for the other pods to substitute. A broken commit
   fails its pods in init and **stalls the rollout** rather than crash-looping a
   healthy service (the fail-fast trade-off, §2.1).

## 2. Core idea: the operator is a compiler

The single most important design principle:

> The NIO workload controller **does not build, and does not implement rollout,
> ordering, or scheduling**. It resolves a git ref into an immutable commit and
> **stamps that immutable revision into a native Kubernetes workload it owns**.
> Building the flake happens **inside the pods** (their init-containers, §4.5) —
> locally, or delegated to a `NixBuilder`. Kubernetes' own controllers do the
> rolling update / ordering / scheduling.

A reconcile is just two stages plus a feedback loop; the build is downstream, in
the pods:

```text
   poll / Flux                          pods build in init (local or NixBuilder)
Ref ───────────▶ resolvedRev ─────▶ K8s rollout ─────▶ init: nix build ─────▶ app: nix run
 ▲  ls-remote      stamp revision    (native ctrl)      (substitute from NixStore /
 │                 into pod template                      remote-build on NixBuilder)
 └──────────────────────────────────────────────────────────────────────────────────┘
                              requeue after pollInterval
```

1. **Resolve** — turn the mutable `ref` (branch) into an immutable commit SHA
   (or read it from a Flux source, §9).
2. **Project** — create/patch the owned native workload, stamping the resolved
   revision into its pod template. Kubernetes rolls it out. Each pod then builds
   the flake in its init-containers (substituting any already-realized paths from
   the `NixStore`, remote-building the rest on the `NixBuilder`) and `nix run`s it.

### 2.1 The operator never builds (deliberate simplification)

An earlier design had the operator *realize* (build) each revision before
projecting, to warm the store and fail fast. We dropped it: the build belongs in
the pods, and the two benefits are recovered elsewhere.

- **Store warming is automatic.** When a `NixBuilder` is referenced, the first
  pod's `instantiate` triggers the build **on the builder**, which realizes into
  its `storeRef` `NixStore`; every other pod then *substitutes* the result. Nix's
  per-output-path locking on the shared builder dedups N concurrent pods to **one**
  real build — the rest block on the lock and reuse it. No operator-side build,
  no redundant double build.
- **Fail-fast is recovered from Kubernetes.** A broken commit is still projected,
  but its pods fail in the `instantiate` init-container → they never become Ready
  → the native rolling update **stalls** with the old revision still serving. We
  do not get a pre-rollout rejection, but we also never crash-loop a healthy
  service. To keep capacity flat during a stalled rollout, NIO defaults the owned
  Deployment/StatefulSet to `maxUnavailable: 0` (surge-only) unless the user sets
  a strategy (§3.3). `NixJob`/`NixCronJob` need no fail-fast — a failed run-Job is
  normal.

**Build location is one of two, never the operator:**

- **In-pod local build** — no `NixBuilder` referenced: each pod builds in its own
  `/nix` during `instantiate`. With a `NixStore` referenced, already-realized
  paths are substituted, so usually little is actually built. With neither store
  nor builder (ephemeral fallback) every replica builds everything — dev only.
- **Delegated remote build** — a `NixBuilder` referenced: pods dispatch the build
  to the single worker via `builders`, which realizes into the shared `NixStore`;
  pods substitute from there. This is the deduped, prod path.

**Rollout trigger is the resolved revision (deliberate trade-off).** The
pod-template revision key is `hash(resolvedRevision+Run+Args)` — a new commit
always rolls out, even one that produces a byte-identical closure (e.g. a
docs-only change). We do **not** try to suppress that churn by diffing the
built closure: keying on "what was built" would require either content-addressed
derivations or output-NAR hashing plus source filtering, and the extra machinery
is not worth it. A commit is a deploy.

### 2.2 No per-build CRD, and no operator-side build Job either

A per-build `NixBuild` CRD keyed by `(repo, rev, run)` would sprawl etcd —
revisions × workloads — for no benefit, and the operator does not run build Jobs
of its own at all. Building is the pods' job (§4.5); the dedup index is the store,
not a Kubernetes object:

- **Nix already deduplicates builds.** The store is content-addressed at the
  output level: a second realization of the same revision finds every path
  already present in the `NixStore` and finishes as a near-instant noop. **The
  store is the dedup index — not a Kubernetes object.**
- **Concurrent pods don't stampede.** With a `NixBuilder`, the builder's
  per-output-path locking collapses N concurrent in-init builds to one (§10).

So the principle is: **building is in-pod (or on the shared `NixBuilder`); stable
infrastructure is a CRD (`NixStore`, `NixBuilder`).** Old store paths stay in the
`NixStore`, so rollback is just re-pinning the workload to an earlier revision
whose closure is still present.

## 3. API types

Every workload spec has two top-level fields: `nix` (a `NixSpec`, §3.2, which
embeds `NixSource`, §3.1) and a native `<kind>Template` (§3.3). The two infra
kinds are separate top-level CRDs (§3.5, §3.6). Shared types live in
`api/v1alpha1/nixworkload_common_types.go`.

### 3.1 `NixSource` — where the flake is and which revision

```go
// NixSource describes the flake's git origin and how the revision is tracked.
type NixSource struct {
	// GitRepo is the flake repository URL (https or ssh form).
	// Ignored when FluxSourceRef is set.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	GitRepo string `json:"gitRepo,omitempty"`

	// Ref is the mutable git ref (branch or tag) the operator tracks.
	// Polling re-resolves this to a commit SHA on PollInterval.
	// +kubebuilder:default="main"
	// +optional
	Ref string `json:"ref,omitempty"`

	// Rev pins an exact commit SHA. When set, polling is disabled and this
	// revision is used verbatim (GitOps-pinned / immutable deployments).
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{7,40}$`
	// +optional
	Rev string `json:"rev,omitempty"`

	// Dir is an optional subdirectory holding the flake (flake-in-subdir).
	// +optional
	Dir string `json:"dir,omitempty"`

	// CredentialsRef references a Secret (same namespace) for private repo
	// access. Recognised keys: "username"+"password", or "ssh-privatekey".
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`

	// FluxSourceRef, when set, makes the operator consume a Flux source object
	// (source.toolkit.fluxcd.io) in the SAME namespace instead of polling git
	// itself. Any artifact-producing Flux kind is accepted — GitRepository,
	// OCIRepository, or Bucket — because they share one status.artifact contract.
	// The operator reads status.artifact.revision (the rollout key) and
	// status.artifact.url (the tarball the pod fetches, §4.5). GitRepo / Ref /
	// PollInterval / CredentialsRef are then ignored: fetching, auth, and
	// verification are Flux's job.
	// +optional
	FluxSourceRef *FluxSourceRef `json:"fluxSourceRef,omitempty"`

	// PollInterval controls how often Ref is re-resolved via `git ls-remote`.
	// Ignored when Rev or FluxSourceRef is set.
	// +kubebuilder:default="1m"
	// +optional
	PollInterval *metav1.Duration `json:"pollInterval,omitempty"`
}

// FluxSourceRef points at a Flux source.toolkit.fluxcd.io object whose
// status.artifact (url + revision) the operator consumes. All three accepted
// kinds expose the same artifact contract, so the operator treats them
// uniformly: it never inspects the source-type-specific spec.
type FluxSourceRef struct {
	// Kind of the referenced Flux source.
	// +kubebuilder:validation:Enum=GitRepository;OCIRepository;Bucket
	Kind string `json:"kind"`

	// Name of the Flux source object (same namespace as this workload).
	Name string `json:"name"`

	// APIVersion of the Flux source group/version. Defaults to the
	// source.toolkit.fluxcd.io version the operator is built against.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}
```

### 3.2 `NixSpec` — the consolidated `nix:` block

```go
// NixSpec groups everything Nix- and git-specific. The Kubernetes workload
// shape lives in the sibling <kind>Template field, never here. WHERE things
// build and run is just two references — a NixStore and (optionally) a
// NixBuilder — never inline store/cache config.
type NixSpec struct {
	// Source is the flake's git origin and revision tracking (§3.1).
	Source NixSource `json:"source"`

	// Run is the installable to execute, exactly as typed after `nix run`.
	// The source is checked out into the working directory at the resolved
	// revision, so "." refers to it:
	//   Run: ".#bebra"  → nix run .#bebra                  (attr from source)
	// You may also run an external flake and pass the source as data via Args:
	//   Run: "github:serokell/deploy-rs#deploy-rs", Args: [".#main"]
	// Defaults to "." (the source flake's default app).
	// +kubebuilder:default="."
	// +optional
	Run string `json:"run,omitempty"`

	// Args follow the `--` separator: runtime data, NOT built. "." here also
	// refers to the checked-out source.
	// +optional
	Args []string `json:"args,omitempty"`

	// Prebuild lists flake installables to materialize BEFORE the app runs, in
	// addition to Run. Each entry is a flake installable — local (".#dep") or
	// external ("github:owner/repo#x"). An `instantiate` init-container (§4.5)
	// runs `nix build <Run> <Prebuild...>` (substituting from the NixStore when
	// referenced, building on the NixBuilder or locally otherwise) into the
	// pod-local store; if any fails to build, the init fails and the pod does not
	// start (per-pod fail-fast). The
	// app's `nix run` then only evaluates + runs — never builds at runtime.
	// Use it for runtime deps Run's own closure doesn't cover, e.g. deploy-rs:
	//   Run: "github:serokell/deploy-rs#deploy-rs", Prebuild: [".#main"].
	// +optional
	Prebuild []string `json:"prebuild,omitempty"`

	// Image is the nix-bearing container image for the runner pods (it provides
	// the nix CLI seeded by the bootstrap init, §4.5, and runs the build in the
	// instantiate init). Defaults to the official upstream nix image.
	// +kubebuilder:default="nixos/nix:latest"
	// +optional
	Image string `json:"image,omitempty"`

	// ContainerName is the container in <kind>Template the operator OWNS: it
	// sets that container's .image and .command and wires the local /nix +
	// workdir. If absent in the template, it is synthesized.
	// +kubebuilder:default=app
	// +optional
	ContainerName string `json:"containerName,omitempty"`

	// NixFlags are extra nix CLI flags for build and run. nix-command/flakes
	// are always enabled.
	// +optional
	NixFlags []string `json:"nixFlags,omitempty"`

	// StoreRef points the workload's nix at a NixStore (§3.5) in the SAME
	// namespace: runner pods use it as a `--substituters` source — their
	// instantiate init pulls already-built paths from it instead of rebuilding.
	// Cross-namespace references are out of scope for v1alpha1 (a future
	// cluster-scoped ClusterNixStore will cover the shared-infra case — see §6).
	// When ABSENT (and no BuilderRef), build+run is ephemeral and pod-local (each
	// replica rebuilds) — a dev fallback only.
	// +optional
	StoreRef *LocalObjectReference `json:"storeRef,omitempty"`

	// BuilderRef offloads the build to a SHARED, single-worker NixBuilder (§3.6)
	// in the SAME namespace (cross-namespace is out of scope for v1alpha1 — see
	// §6). Mutually exclusive with BuilderTemplate.
	// +optional
	BuilderRef *LocalObjectReference `json:"builderRef,omitempty"`

	// BuilderTemplate constructs a DEDICATED single-worker NixBuilder owned by
	// this workload (its StoreRef defaults to this workload's StoreRef).
	// Mutually exclusive with BuilderRef. When neither is set, each pod builds
	// locally in its own init-container (no remote builder, no operator build).
	// +optional
	BuilderTemplate *NixBuilderSpec `json:"builderTemplate,omitempty"`

	// LocalStore configures the pod-local /nix the realized closure is
	// substituted into. Defaults to a node-local emptyDir (§4.5).
	// +optional
	LocalStore *NixLocalStore `json:"localStore,omitempty"`

	// TriggerOnChange controls the reaction to a NEW resolved revision.
	// Per-kind meaning (and per-kind default):
	//   NixDeployment/NixStatefulSet: roll out the new revision   (default true)
	//   NixJob:                       create a fresh run-Job       (default true)
	//   NixCronJob:                   also fire an immediate Job    (default false)
	// +optional
	TriggerOnChange *bool `json:"triggerOnChange,omitempty"`

	// Suspend pauses NIO reconciliation (no new builds / rollouts / runs).
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// NixLocalStore configures the pod-local /nix that the realized closure is
// substituted into. The content is reproducible and lives in the NixStore, so
// this volume need NOT survive a reschedule — emptyDir vs a per-pod generic
// ephemeral volume is purely a size/perf choice.
type NixLocalStore struct {
	// Medium:
	//   "Disk"   — emptyDir on node disk (DEFAULT): fast, node-local. Counts
	//              against node ephemeral-storage — large closures risk eviction.
	//   "Memory" — emptyDir tmpfs: fastest, RAM-bound; tiny closures only.
	//   "PodPVC" — a per-pod PVC (provisioned as a generic ephemeral volume,
	//              deleted with the pod) on StorageClassName: for large closures /
	//              to avoid node ephemeral-storage pressure; speed depends on the
	//              class. Each pod gets its own volume.
	// +kubebuilder:validation:Enum=Disk;Memory;PodPVC
	// +kubebuilder:default=Disk
	// +optional
	Medium string `json:"medium,omitempty"`

	// SizeLimit caps the emptyDir (Disk/Memory) or requests the size for a
	// PodPVC volume.
	// +optional
	SizeLimit *resource.Quantity `json:"sizeLimit,omitempty"`

	// StorageClassName selects the storage class for Medium=PodPVC
	// (defaults to the cluster default class).
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`
}
```

### 3.3 The native `<kind>Template` block

The second top-level field of every workload spec is the **native Kubernetes
workload spec, verbatim** — no custom re-modelling of replicas/strategy/etc.:

| Kind             | Field                 | Go type                     |
| ---------------- | --------------------- | --------------------------- |
| `NixDeployment`  | `deploymentTemplate`  | `appsv1.DeploymentSpec`     |
| `NixJob`         | `jobTemplate`         | `batchv1.JobSpec`           |
| `NixCronJob`     | `cronJobTemplate`     | `batchv1.CronJobSpec`       |
| `NixStatefulSet` | `statefulSetTemplate` | `appsv1.StatefulSetSpec`    |

All native knobs live where users expect them: `replicas`, `strategy` (Deploy);
`schedule`, `concurrencyPolicy` (CronJob); `serviceName`, `volumeClaimTemplates`
(StatefulSet); `completions`, `backoffLimit` (Job). NIO adds no duplicates.

**What the operator owns inside the template** (everything else passes through —
sidecars, volumes, scheduling, securityContext, probes, resources):

1. The `nix.containerName` (default `app`) container's `.image` (= `nix.image`),
   `.command` (`nix run <Run> -- <Args>`), `NIX_CONFIG`, plus the `/nix` and
   `/workspace` mounts. If that container is absent, it is synthesized.
2. Three prepended init-containers: `bootstrap` (seed nix into `/nix`),
   `fetch-source` (check out the source), and `instantiate` (build `Run` +
   `prebuild` into the local store; fail-fast) (§4.5).
3. The pod-local `nix-store` (emptyDir/PVC) and `workspace` (emptyDir) volumes.
4. The pod-template **revision annotation** (composite hash) and managed pod
   **labels** (§8).
5. The `.selector` (Deployment/StatefulSet) when the user omits it.
6. For **Deployment** only, `strategy.rollingUpdate.maxUnavailable` defaults to
   `0` (surge-only) when the user leaves it unset, so a broken revision whose pods
   fail in `instantiate` (§4.5) stalls the rollout **without** shedding capacity
   from the healthy old revision (§2.1). A `StatefulSet` needs no such default —
   its ordered RollingUpdate already halts on the first unready pod, keeping
   lower-ordinal old pods running (and `maxUnavailable` there is feature-gated, so
   NIO does not set it). A user-supplied strategy is respected verbatim.

The `<kind>Template` field is **optional**; a defaulting webhook fills the
fields the operator owns so native required-field validation passes. A minimal
workload is therefore just a `nix:` block (examples §1).

### 3.4 Shared status: `NixWorkloadStatus`

```go
// NixWorkloadStatus is embedded by every NIO workload status.
type NixWorkloadStatus struct {
	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse, human-facing lifecycle state (see §5).
	// +kubebuilder:validation:Enum=Pending;Resolving;Building;Progressing;Ready;Degraded;Failed;Suspended
	// +optional
	Phase string `json:"phase,omitempty"`

	// ResolvedRevision is the commit SHA that Ref currently points to.
	// +optional
	ResolvedRevision string `json:"resolvedRevision,omitempty"`

	// LastPolledTime is when Ref was last resolved.
	// +optional
	LastPolledTime *metav1.Time `json:"lastPolledTime,omitempty"`

	// RolledOutRevision is the commit SHA currently stamped into the owned
	// workload's pod template — i.e. the revision its pods build and run. It
	// trails ResolvedRevision while a rollout is in progress and equals it once
	// the workload is Ready. The operator does not build, so there is no
	// "realized store path / drv hash" recorded here; the build happens in the
	// pods (§4.5) and rollback is re-pinning to an earlier commit (§2.2).
	// +optional
	RolledOutRevision string `json:"rolledOutRevision,omitempty"`

	// WorkloadRef is the name of the owned native workload object.
	// +optional
	WorkloadRef string `json:"workloadRef,omitempty"`

	// Conditions: Ready, Reconciling, Stalled (kstatus) + GitSynced,
	// Progressing (NIO-specific). See §5.2.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

### 3.5 `NixStore` — the centralized store/cache server

```go
// NixStore manages a StatefulSet running a centralized Nix store / binary-cache
// SERVER backed by a PVC. The server owns the store database; clients (runner
// pods, builders) reach it over the network as ordinary nix substituter/store
// clients. There is no shared filesystem and no concurrent-writer problem.
// A handful of these exist per namespace — they are infrastructure, not
// per-build. NixStore is namespace-scoped and only consumable by workloads in
// the same namespace; a cluster-wide ClusterNixStore is a future addition (§6).
type NixStoreSpec struct {
	// Replicas of the store-server StatefulSet.
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Storage is the volumeClaimTemplate backing the store (the real /nix).
	Storage corev1.PersistentVolumeClaimSpec `json:"storage"`

	// Image of the store server (a nix daemon + substituter server such as
	// harmonia / nix-serve). Defaults to an operator-provided image.
	// +optional
	Image string `json:"image,omitempty"`

	// SigningKeySecretRef holds the Nix signing key pair used to sign served
	// paths. If absent, the operator generates one and publishes the public key
	// in status.
	// +optional
	SigningKeySecretRef *SecretReference `json:"signingKeySecretRef,omitempty"`

	// UpstreamSubstituters the store falls through to on a miss (so builds layer
	// on top of cache.nixos.org). cache.nixos.org is trusted by default.
	// +optional
	UpstreamSubstituters []string `json:"upstreamSubstituters,omitempty"`

	// Template overrides for the server pods (resources, scheduling, …).
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`
}

type NixStoreStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Ready;Degraded
	// +optional
	Phase string `json:"phase,omitempty"`
	// SubstituterURL is the READ endpoint clients pass to `--substituters`.
	// +optional
	SubstituterURL string `json:"substituterURL,omitempty"`
	// StoreURI is the realize/push endpoint (e.g. ssh-ng://svc or the daemon).
	// +optional
	StoreURI string `json:"storeURI,omitempty"`
	// PublicKey is the trusted-public-key entry clients must trust.
	// +optional
	PublicKey string `json:"publicKey,omitempty"`
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

The `NixStore` controller creates: the StatefulSet (server + `volumeClaimTemplate`
from `Storage`), a headless `Service`, and the signing-key `Secret` (generated
when absent), then publishes `SubstituterURL` / `StoreURI` / `PublicKey` to
status for clients to consume.

### 3.6 `NixBuilder` — the single builder worker

```go
// NixBuilder manages a StatefulSet with a SINGLE builder worker that runs
// `nix build`. MVP-deliberate: one worker, no pool, no routing/balancing.
// Builds offload here via nix remote-build; the builder realizes INTO its
// StoreRef NixStore, so outputs are immediately available to that store's
// substituter clients. Get one of two ways: reference a shared NixBuilder
// (workload's nix.builderRef), or construct a dedicated one inline
// (nix.builderTemplate). Infrastructure, not per-build.
type NixBuilderSpec struct {
	// StoreRef is the NixStore this builder realizes into. For a
	// nix.builderTemplate it defaults to the workload's nix.storeRef.
	// +optional
	StoreRef *LocalObjectReference `json:"storeRef,omitempty"`

	// Image of the builder pod (nix + toolchain). Defaults to the nix image.
	// +optional
	Image string `json:"image,omitempty"`

	// Systems this builder can build (e.g. ["x86_64-linux","aarch64-linux"]).
	// +optional
	Systems []string `json:"systems,omitempty"`

	// MaxJobs is nix `max-jobs` on the single worker (how many builds it runs in
	// parallel). This is the only concurrency knob — there is no horizontal pool.
	// +optional
	MaxJobs *int32 `json:"maxJobs,omitempty"`

	// Storage is the builder's own persistent /nix (the StatefulSet's
	// volumeClaimTemplate), so its build cache survives restarts.
	// +optional
	Storage *corev1.PersistentVolumeClaimSpec `json:"storage,omitempty"`

	// Template overrides for the builder pod (big resources, taints, …).
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`
}

type NixBuilderStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +kubebuilder:validation:Enum=Pending;Ready;Degraded
	// +optional
	Phase string `json:"phase,omitempty"`
	// BuilderEndpoint is the remote-build endpoint (e.g. ssh-ng://svc) that
	// builds dispatch to.
	// +optional
	BuilderEndpoint string `json:"builderEndpoint,omitempty"`
	// Ready is 0 or 1 — the single worker.
	// +optional
	Ready bool `json:"ready,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

The `NixBuilder` controller creates the single-worker StatefulSet (one pod
running a nix daemon / sshd that accepts remote builds), wires it to `StoreRef`
so realized paths land in that `NixStore`, and publishes `BuilderEndpoint` to
status. A workload's `nix.builderTemplate` produces such a `NixBuilder` owned by
the workload (GC'd with it); `nix.builderRef` reuses a shared one.

## 4. The four workload kinds

Every workload spec is `nix` (`NixSpec`, §3.2) plus the native `<kind>Template`
(§3.3). Each owns one native object, into whose pod template the operator stamps
(uniformly — see §4.5):

- three init-containers: `bootstrap` (seed the nix CLI into `/nix`),
  `fetch-source` (check out the source into `/workspace`), and `instantiate`
  (build `Run` + `prebuild` into the local store — the per-pod fail-fast gate);
- `NIX_CONFIG` wiring nix to the `NixStore` (substituters + trusted-public-keys)
  and the `NixBuilder` (builders) — each when referenced;
- the pod-template annotation
  `nio.homystack.com/revision: hash(resolvedRevision+Run+Args)`
  (changing it triggers the native rolling update — a new commit always rolls,
  see §2.1);
- the `app` container command `nix run <Run> -- <Args...>` from `/workspace`.

### 4.1 `NixDeployment` → `apps/v1 Deployment`

```go
type NixDeploymentSpec struct {
	Nix NixSpec `json:"nix"`
	// +optional
	DeploymentTemplate *appsv1.DeploymentSpec `json:"deploymentTemplate,omitempty"`
}

type NixDeploymentStatus struct {
	NixWorkloadStatus `json:",inline"`
	ReadyReplicas     int32 `json:"readyReplicas,omitempty"`
	UpdatedReplicas   int32 `json:"updatedReplicas,omitempty"`
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`
}
```

**Rollout:** patch the owned Deployment's pod-template revision annotation →
the Deployment controller does a `RollingUpdate` per `deploymentTemplate.strategy`.
NIO mirrors `.status.*Replicas` and sets `Progressing`/`Ready` from the
Deployment's own conditions. NIO never deletes pods itself.

### 4.2 `NixJob` → `batch/v1 Job`

```go
type NixJobSpec struct {
	Nix NixSpec `json:"nix"`
	// +optional
	JobTemplate *batchv1.JobSpec `json:"jobTemplate,omitempty"`
}

type NixJobStatus struct {
	NixWorkloadStatus `json:",inline"`
	ActiveJob   string       `json:"activeJob,omitempty"`
	LastRunTime *metav1.Time `json:"lastRunTime,omitempty"`
	Succeeded   int32        `json:"succeeded,omitempty"`
	Failed      int32        `json:"failed,omitempty"`
}
```

**Re-run semantics:** Jobs are immutable in their pod template, so a new revision
(when `nix.triggerOnChange` is true) means a **new Job object** `<name>-<hash>`.
NIO keeps ownership, GCs old completed run-Jobs beyond a history limit, never
mutates an existing Job. With `triggerOnChange: false` the Job runs once.

> Programs that invoke nix themselves (`deploy-rs`, `nixos-rebuild`, `colmena`)
> are common as `NixJob`s and need no special handling: the uniform pod model
> (§4.5) already gives every container a real writable nix via `nix run`.

### 4.3 `NixCronJob` → `batch/v1 CronJob`

```go
type NixCronJobSpec struct {
	Nix NixSpec `json:"nix"`
	// schedule, concurrencyPolicy, suspend, *JobsHistoryLimit and the embedded
	// jobTemplate all live here natively. Required (schedule has no default).
	CronJobTemplate batchv1.CronJobSpec `json:"cronJobTemplate"`
}

type NixCronJobStatus struct {
	NixWorkloadStatus `json:",inline"`
	LastScheduleTime   *metav1.Time `json:"lastScheduleTime,omitempty"`
	LastSuccessfulTime *metav1.Time `json:"lastSuccessfulTime,omitempty"`
	ActiveJobs         []string     `json:"activeJobs,omitempty"`
}
```

**Scheduling is delegated** to the native CronJob. NIO keeps
`cronJobTemplate.jobTemplate` pinned to the latest resolved revision. When
`nix.triggerOnChange` is true and the revision changes, NIO additionally creates
a one-off Job from the same template, respecting the native `concurrencyPolicy`.

### 4.4 `NixStatefulSet` → `apps/v1 StatefulSet`

```go
type NixStatefulSetSpec struct {
	Nix NixSpec `json:"nix"`
	// serviceName, volumeClaimTemplates, updateStrategy, podManagementPolicy,
	// replicas all live here natively. Required (serviceName has no default).
	StatefulSetTemplate appsv1.StatefulSetSpec `json:"statefulSetTemplate"`
}

type NixStatefulSetStatus struct {
	NixWorkloadStatus `json:",inline"`
	ReadyReplicas   int32  `json:"readyReplicas,omitempty"`
	UpdatedReplicas int32  `json:"updatedReplicas,omitempty"`
	CurrentRevision string `json:"currentRevision,omitempty"`
	UpdateRevision  string `json:"updateRevision,omitempty"`
}
```

**Ordering & PVCs** are entirely the StatefulSet controller's job. NIO stamps the
revision into the pod template; partitioned/canary rollouts use the native
`statefulSetTemplate.updateStrategy`.

### 4.5 Generated pod anatomy

Every pod follows the **same** shape — no exec-vs-`nix run` branching, no
conditional source checkout, no per-kind special case. nix is configured (via
`NIX_CONFIG`) to know where to substitute from (the `NixStore`) and where to
build (the `NixBuilder`), so the app container can simply always `nix run`, and
nix does the right thing: substitute the prebuilt closure when it is in the
store, or build it (on the `NixBuilder`, or locally) when it is not.

**Volumes (always):**

| Volume      | Mount        | Type                          | Notes                                          |
| ----------- | ------------ | ----------------------------- | ---------------------------------------------- |
| `nix-store` | `/nix`       | `emptyDir` or per-pod PVC     | writable, pod-local; per `nix.localStore` (§3.2) |
| `workspace` | `/workspace` | `emptyDir`                    | the source checkout                            |

The `nix-store` volume need not survive a reschedule — its content is
reproducible and held in the `NixStore`, so a fresh pod just re-substitutes.
Default `emptyDir`; switch to `PodPVC` for large closures (`nix.localStore`).

**Init-containers (always, all three):**

1. `bootstrap` — seeds the nix CLI **and its closure** from the image into the
   shared volume. The trick: the `nix-store` volume is mounted here at
   **`/nix-vol`** (NOT `/nix`), so the image's own `/nix/store` is **not**
   shadowed and can be copied from. If the volume is already populated (pod
   restart, reused volume), it is left untouched:

   ```sh
   [ -e /nix-vol/store ] || cp --archive /nix/. /nix-vol/
   ```

   Every later container mounts the **same** volume at `/nix`, so they all see a
   working, writable nix. This works with the stock `nixos/nix` image — no
   relocated store copy in the image is required (Nix store paths are not
   relocatable, so the copy lands back at the canonical `/nix/store`).

2. `fetch-source` — populates `/workspace` with the source at the resolved
   revision, using git from nixpkgs (`nix shell nixpkgs#gitMinimal`) on the
   `nix.image`. **Always present**, and **always leaves `/workspace` as a clean
   git tree**, so `.` resolves identically whether it appears in `Run` or in
   `Args`, and `nix run` gets the git-level hermeticity flakes require. Two modes:

   - **Direct git (default).** Shallow-fetch the `source.gitRepo` at the resolved
     commit SHA and check it out; creds from `source.credentialsRef`:

     ```sh
     git init /workspace && cd /workspace
     git remote add origin "$NIO_GIT_REPO"
     git fetch --depth 1 origin "$NIO_REVISION"
     git checkout --detach FETCH_HEAD
     ```

   - **Flux (`source.fluxSourceRef`).** No direct git I/O: download the tarball
     from the Flux artifact URL the operator resolved (`status.artifact.url`,
     served in-cluster by source-controller — works for GitRepository,
     OCIRepository, and Bucket alike), extract it, and — because the artifact has
     **no `.git`** — synthesize one so the tree is a hermetic flake input:

     ```sh
     curl --location --fail "$NIO_ARTIFACT_URL" | tar --extract --gzip --directory /workspace
     cd /workspace
     [ -e .git ] || (git init && git add --all && git -c user.email=nio@homystack.com \
       -c user.name=nio commit --quiet --message "flux artifact $NIO_REVISION")
     ```

     Both modes end with a committed git tree at `/workspace`, so the downstream
     `instantiate` and `app` steps are byte-for-byte identical regardless of
     revision source.

3. `instantiate` — **materializes** everything the app needs into the pod-local
   `/nix` (substituting from the `NixStore`, building on the `NixBuilder` or
   locally — see below):

   ```sh
   nix build .#server <nix.prebuild...>     # Run + every prebuild entry
   # NIX_CONFIG: substituters=<NixStore>, builders=<NixBuilder>, builders-use-substitutes
   ```

   Each path is substituted from the `NixStore` if already present (warmed by an
   earlier pod's build, or by the `NixBuilder` realizing into it), or otherwise
   built — **on the `NixBuilder`** when one is referenced (deduped across pods by
   the builder's per-output-path lock, §10), or **locally in this pod's `/nix`**
   when none is. **If any build fails, this init fails and the pod does not
   start** — per-pod fail-fast, which is what stalls a broken rollout (§2.1).
   `nix.prebuild` extends the set beyond `Run` (e.g. deploy-rs's `.#main`).

**App container (always):**

- `image: nix.image`; `workingDir: /workspace`; mounts `/nix` (rw) + `/workspace`;
- the same `NIX_CONFIG` (built from the referenced `NixStore`/`NixBuilder`
  status) as the `instantiate` init, so any lazy runtime resolution still works:

  ```ini
  experimental-features     = nix-command flakes
  substituters              = http://<NixStore.substituterURL> https://cache.nixos.org
  trusted-public-keys       = <NixStore.publicKey> cache.nixos.org-1:...
  builders                  = ssh-ng://<NixBuilder.builderEndpoint> x86_64-linux
  builders-use-substitutes  = true
  ```

- command: `nix run <Run> -- <Args...>`.

Because `instantiate` already put `Run` (and `prebuild`) in the local store,
the app's `nix run` only **evaluates** `.` from `/workspace` and **runs** — no
build at app start. Each pod has its own writable `/nix`, so pods never share a
store filesystem; the `NixStore` server is the only shared thing. nix-invoking
programs (`deploy-rs`, `nixos-rebuild`, `colmena`) need no special mode — they
get a real writable nix, with their `prebuild` deps already present.

**No operator-side build.** The operator never builds; the entire build/realize
happens in the pods (§2.1). The first pod's `instantiate` warms the `NixStore`
(directly, or via the `NixBuilder` it realizes into); every later pod
substitutes. There is no operator build Job, no `nix copy` plumbing, and no
`realizedStorePath`/`drvHash` computed operator-side.

**Why the pod runs a plain `nix build`, not a pinned store path.** Even though
the build result is a concrete store path, `instantiate` deliberately runs plain
`nix build .#server` (full evaluation of `.` from `/workspace`), exactly as a
user would on their own machine — not a `nix copy` of some pre-resolved path.
This keeps semantics familiar and self-consistent (what runs in-cluster is what
`nix run .#server` runs locally), and it is what nix-invoking tools like
`deploy-rs` need anyway: they require the full evaluation context, not a single
output path. The cost is that each replica re-evaluates the flake (an eval, not a
rebuild — the build itself is cache-warmed); for an N-replica Deployment that is
N evaluations. We accept that cost in exchange for unsurprising, plain-nix
behavior.

**Ephemeral fallback (no `storeRef`, no `builderRef`):** identical anatomy, but
`NIX_CONFIG` gets no extra substituters/builders — so `instantiate` just builds
everything locally in the pod's own `/nix`, every replica independently. Same
code path, slower; dev/throwaway only.

## 5. State machine & conditions

### 5.1 Phase machine (same for all four kinds)

The operator only **resolves** and **projects**; everything after the stamp is
observed from the owned native workload's pods. There is no operator-driven build
phase — `Building` is just "the projected revision's pods are compiling in their
init-containers", read from init-container status.

```text
                    spec created / suspend=false
                              │
                              ▼
   ┌───────────────────────────────────────────────────────────┐
   │                        Resolving                            │  resolve Ref→SHA
   │   (git ls-remote / read Flux source status.artifact)        │
   └───────────────┬──────────────────────────┬─────────────────┘
                   │ resolved == rolledOut     │ resolved != rolledOut
                   │ & healthy                 ▼
                   │                  project: stamp revision into pod template
                   │                           │
                   │                           ▼
                   │                  ┌───────────────────┐  new pods running
                   │                  │     Building      │  `instantiate`
                   │                  │ (pods compile in   │  (substitute / build
                   │                  │  init-containers)  │   on NixBuilder)
                   │                  └─────┬───────┬──────┘
                   │           init ok,     │       │  init build fails, OR
                   │          pods starting │       │  store/builder not Ready
                   │                        ▼       ▼
                   │                ┌────────────┐ ┌──────────┐
                   │                │Progressing │ │ Degraded │  rollout STALLS;
                   │                │  (native   │ │ +Stalled │  old revision keeps
                   │                │  rollout)  │ └────┬─────┘  serving (§2.1)
                   │                └─────┬──────┘      │
                   │     workload healthy │             │
                   │                      ▼             │
                   │                  ┌──────┐          │
                   └─────────────────▶│Ready │          │
                                      └──┬───┘          │
                                         └──────┬───────┘
                                                ▼
                                     requeue after PollInterval → Resolving
```

- `Suspended` is a terminal-until-resumed state entered when `spec.suspend=true`.
- **Broken commit ⇒ stalled rollout, never an outage.** A bad revision is still
  projected, but its new pods fail in the `instantiate` init-container, so they
  never become Ready and the native rolling update halts with the previous
  revision still serving. For a `NixDeployment` this is kept capacity-flat by the
  `maxUnavailable: 0` default (§3.3); a `NixStatefulSet` already halts on an
  unready pod by its ordered update semantics. NIO reports `Degraded`/`Stalled`
  with the init failure surfaced as the reason. This is the fail-fast trade-off
  of §2.1 — no pre-rollout rejection, no crash-loop of a healthy service.
- `Degraded` = the owned native controller reports unhealthy (e.g. Deployment
  `Progressing=False` / `ReplicaFailure`) **or** new-revision pods are stuck
  failing init.
- `Failed` is reached only by `NixJob`/`NixCronJob`, when a run-Job exhausts its
  `backoffLimit` — a genuinely terminal run. A `NixDeployment`/`NixStatefulSet`
  never goes `Failed`; a bad revision leaves it `Degraded`/`Stalled` with the old
  revision serving.
- A `NixStore`/`NixBuilder` that is not `Ready` means new pods cannot build:
  they stall in init, and NIO sets `Stalled` with a reason pointing at the
  missing infra.

### 5.2 Conditions (kstatus-compatible)

| Condition     | Meaning                                                              |
| ------------- | ------------------------------------------------------------------- |
| `Ready`       | `RolledOutRevision == ResolvedRevision` and the workload is healthy. |
| `Reconciling` | A reconcile is in progress (resolve / project / observe).           |
| `Stalled`     | Progress is blocked and will not self-resolve: bad ref, new pods failing their init build, or store/builder not ready. |
| `GitSynced`   | `ResolvedRevision` is current w.r.t. `Ref`/Flux.                     |
| `Progressing` | The native workload is mid-rollout toward the resolved revision (pods building in init or becoming ready). |

`Ready` is the kstatus aggregate consumers (ArgoCD, `kubectl wait`) key on.
`NixStore`/`NixBuilder` carry their own `Ready`/`Stalled` from StatefulSet health.

## 6. Storage & build infrastructure (NixStore + NixBuilder)

This replaces the earlier per-workload `SharedPVC` / `BinaryCache` / `PerPod`
store modes — those are subsumed by a single concept: **a `NixStore` is the
store server, and workloads reference it.**

```text
                 builds realize into            runners substitute from
NixBuilder ─────────────────────────▶  NixStore  ◀──────────────────────  runner pods
 (1 STS worker;                        (StatefulSet                        (NixDeployment/
  ref or template)                      + PVC, owns                         Job/Cron/STS)
                                        the store db)
```

- **`NixStore`** — one (or a few) per namespace, consumable only by workloads in
  that same namespace (v1alpha1 has no cross-namespace references; see the
  Cluster-scoped note below). A served store means pods on **any node** are
  network substituter clients; the server owns the store DB, so the old "shared
  RWX PVC + concurrent writers" hazard simply does not exist. Single-node and
  multi-node are the same model.
- **`NixBuilder`** — optional, and deliberately a **single STS worker** (no
  pool, no balancing — MVP). Reference a shared one (`nix.builderRef`) or
  construct a dedicated one inline (`nix.builderTemplate`); either way it is one
  worker. Without any builder, each pod builds locally in its own init-container
  (the operator never runs a build Job). When the builder's `storeRef` is the
  same `NixStore` the workload references, the build→run handoff is automatic:
  the pods dispatch the build to the builder, it realizes into the store, and the
  pods substitute from it — no `nix copy` plumbing. Scale by giving heavy
  workloads their own dedicated builder, not by pooling.
- **Ephemeral fallback** — a workload with neither `storeRef` nor `builderRef`
  builds and runs entirely in the pod (each replica rebuilds independently).
  Dev/throwaway only; not for multi-replica prod.

**Why this is both simpler and more powerful:** the workload spec drops all
store/cache/PVC/signing config down to two references (`storeRef`, `builderRef`);
all the store/cache mechanics live once in the `NixStore`/`NixBuilder` objects;
and remote-builder support (a dedicated single-worker builder per heavy
workload) and multi-node serving come for free from the server model.

**Namespace scope (v1alpha1).** `NixStore`, `NixBuilder`, and every workload are
all namespace-scoped, and `storeRef`/`builderRef` are same-namespace
`LocalObjectReference`s. A workload can only use store/builder infrastructure in
its **own** namespace; there is no cross-namespace reference. To share one store
across many namespaces today, deploy a `NixStore` per namespace. A cluster-wide
`ClusterNixStore` / `ClusterNixBuilder` (cluster-scoped CRDs referenced from any
namespace) is a planned future addition and is **out of scope** for this
iteration (§12).

## 7. Reconcile workflow (workload controllers)

Common skeleton; the `project()` step differs per kind.

```text
Reconcile(obj):
  status.observedGeneration = obj.generation
  setCondition(Reconciling=True)

  if obj.deletionTimestamp != nil:
    return reconcileDelete(obj)        # drop store ref; ownerRef GCs workload
  ensureFinalizer(obj)

  if obj.nix.suspend:
    setPhase(Suspended); patchOwnedWorkload(suspend); return done

  # --- 1. Resolve ------------------------------------------------------
  rev, err = resolveRevision(obj)      # Rev > Flux status.artifact > git ls-remote(Ref)
  if err: setCondition(GitSynced=False, Stalled=True); requeue(short)
  status.resolvedRevision = rev; status.lastPolledTime = now
  setCondition(GitSynced=True)

  # --- 2. Infra preflight (no operator build; pods build in init) ------
  store   = resolveStore(obj.nix.storeRef)       # nil => ephemeral mode
  builder = resolveBuilder(obj)                  # ref/template/nil
  if (store != nil && !store.Ready) || (builder != nil && !builder.Ready):
    # new pods could not build/substitute — surface it, but do NOT tear down a
    # healthy running workload; just don't advance the rollout.
    setCondition(Stalled=True, reason=InfraNotReady); requeue

  # --- 3. Project ------------------------------------------------------
  # Stamp the resolved revision into the pod template. The build itself happens
  # in the pods' init-containers (§4.5); the operator computes no store path.
  desired = renderNativeWorkload(obj, rev, store, builder)
  setControllerReference(obj, desired); serverSideApply(desired)

  # --- 4. Observe ------------------------------------------------------
  observeNativeWorkload(obj, status)   # reads pods: init-build state + health
  switch:
    new pods compiling in init: setPhase(Building);   setCondition(Progressing=True)
    rollout advancing:          setPhase(Progressing); setCondition(Progressing=True)
    healthy & rev rolled out:   setPhase(Ready);       setCondition(Ready=True, Progressing=False)
                                status.rolledOutRevision = rev
    new pods failing init / unhealthy:
                                setPhase(Degraded); setCondition(Ready=False, Stalled=True)
                                # old revision keeps serving (maxUnavailable:0) — §2.1/§5

  patchStatus(obj); requeue(after = pollInterval)
```

### 7.1 Per-kind `project()` differences

- **NixDeployment / NixStatefulSet:** server-side apply the owned object with the
  revision annotation in the pod template; the native controller rolls. Observe
  `.status` (and new-pod init-container state) for health.
- **NixJob:** if no run-Job exists for the composite revision hash (and not
  suspended), create `<name>-<hash>`. Never mutate an existing run-Job; GC old.
  A broken commit just makes the run-Job fail in init — no special handling.
- **NixCronJob:** apply the owned CronJob with its `jobTemplate` pinned to the
  resolved revision. On a new revision with `triggerOnChange`, also create a
  one-off Job honoring `concurrencyPolicy`.

## 8. Interaction with other Kubernetes objects

| Interaction                | Mechanism                                                                 |
| -------------------------- | ------------------------------------------------------------------------- |
| Owned native workload      | `ownerReference` (controller=true) → cascading delete + GC                |
| `NixStore` (referenced)    | `Watches(...)`; read `status.substituterURL/storeURI/publicKey` into pod `NIX_CONFIG`; not-Ready ⇒ `Stalled` |
| `NixBuilder` (referenced)  | `Watches(...)`; read `status.builderEndpoint` into pod `NIX_CONFIG` (`builders=`); pods dispatch builds to it |
| `NixStore` controller owns | StatefulSet + headless Service + signing-key Secret                        |
| `NixBuilder` controller owns | StatefulSet (+ per-builder PVCs)                                         |
| Watches own native kind    | `Owns(&appsv1.Deployment{})` etc. → re-reconcile on rollout status change |
| Flux source (Git/OCI/Bucket) | `Watches(...)` each referenced kind with an enqueue mapper when `FluxSourceRef` is set; read `status.artifact.{revision,url}` |
| Secrets (git creds, keys)  | field-indexed `Watches`, mirrors the existing Machine secret-watch pattern |
| Services / Ingress         | **out of scope** — user creates a Service selecting NIO pod labels        |

**Pod labels** stamped on every generated pod template:

```yaml
nio.homystack.com/workload-kind: NixDeployment
nio.homystack.com/workload-name: bebra
nio.homystack.com/revision: <compositeHash>
app.kubernetes.io/managed-by: nio
```

## 9. Polling vs Flux (revision source)

- **Poll (default).** Reconcile requeues after `PollInterval`; each tick runs
  `git ls-remote <repo> <ref>` (cheap, no clone) for the SHA, cached in
  `status.resolvedRevision`. A global rate limiter / dedup by `(repo,ref)` avoids
  hammering the git host when many objects share a repo.
- **Flux.** When `source.fluxSourceRef` is set, the operator does **no** git I/O:
  it reads `status.artifact.revision` (the rollout key) and `status.artifact.url`
  from the referenced Flux source and watches that object, offloading
  fetching/auth/known-hosts/verification to Flux. The same path works for
  `GitRepository`, `OCIRepository`, and `Bucket` because all three expose the
  identical `status.artifact` contract. The pod's `fetch-source` then downloads
  that artifact tarball and, since it carries no `.git`, initializes a git tree
  for flake hermeticity (§4.5).

## 10. Concurrency, limits, safety

- **Store concurrency is the server's job.** The `NixStore` nix daemon serializes
  its own DB; clients are plain substituter/store clients. No operator-side store
  lock, no single-writer rule, no shared-FS hazard.
- **No build thundering-herd when a Deployment scales up.** When a `NixBuilder`
  is referenced, all N pods dispatch their `instantiate` build to the **same**
  single worker; nix's per-output-path locking there collapses them to **one**
  real build — the others block on the lock and reuse the result, then substitute
  from the shared `NixStore`. The herd in the normal case is therefore N
  concurrent *downloads* of one closure from the store, not N builds — mitigable
  with a node-local cache (O10). Only the storeless/builderless ephemeral
  fallback builds per-pod (dev only). The operator runs **no** build of its own,
  so it is never part of this path.
- **Remote build queueing:** builds dispatched by pods run on the single
  `NixBuilder` worker, bounded by its `maxJobs`; extra builds queue (no
  pool/balancing — give a heavy workload its own dedicated builder via
  `builderTemplate`).
- **Immutability of run-Jobs:** never mutate an existing Job; only create/GC.
- **Store GC:** a `NixStore` maintenance task (server-side) handles `nix-collect-garbage`;
  never from workload pods.
- **Finalizer** `nio.homystack.com/finalizer`: on delete, the native workload is
  removed by ownerReference GC. There are no operator-owned build Jobs to clean up.
- **RBAC additions:** `deployments`, `statefulsets`, `jobs`, `cronjobs`,
  `services`, `secrets` (create/get/list/watch/update/patch/delete as needed);
  the new `nixstores`/`nixbuilders` resources; `get`/`watch` on the referenced
  `source.toolkit.fluxcd.io` kinds (`gitrepositories`, `ocirepositories`,
  `buckets`) when Flux mode is used.

## 11. Open questions

- ~~**O1** — Default store mode.~~ **Resolved:** there are no inline store
  modes. A workload references a `NixStore` (server) via `nix.storeRef`; no ref
  means ephemeral pod-local build+run. §6.
- **O2** — Do we expose a Service automatically for `NixDeployment`/
  `NixStatefulSet`, or keep networking strictly out of scope (current
  assumption: out of scope)?
- **O3** — One controller per workload kind, or a shared generic reconciler with
  a per-kind `project()` strategy (less code, recommended)? `NixStore` and
  `NixBuilder` get their own controllers regardless.
- **O4** — Build-an-OCI-image-and-run-that as an additional mode (rollout = image
  tag; pods need no nix and no `bootstrap`/`nix run` at startup). Worth a v2 mode
  for latency-sensitive services that want a plain image at runtime?
- **O5** — Beyond `apps.X`, do we need an explicit `packages.X`+command form, or
  does the free-form `Run` string already cover it?
- **O6** — Health gating: readiness/liveness probes live on the `app` container
  in `<kind>Template`. Gate NIO `Ready` purely on native workload health, or add
  app-level signals?
- **O7** — Embedding native specs makes `selector` / `template.spec.containers`
  `required` upstream. Resolve via a defaulting webhook or by relaxing those two
  fields in the generated CRD schema. Which for v1alpha1?
- **O8** — `NixStore`/`NixBuilder` each warrant their own mini design doc
  (server image choice — harmonia vs nix-serve vs custom; remote-build transport
  — `ssh-ng` vs daemon; signing-key rotation; HA/replicas semantics). **Key
  detail to pin down:** with plain nix `builders=`, a remote build's outputs are
  copied back to the *requesting pod's* store, not automatically into the shared
  `NixStore`. The "builder realizes into the store, others substitute" model
  (§2.1, §6 "no `nix copy` plumbing") therefore needs a concrete mechanism —
  e.g. a post-build-hook on the builder that pushes to the store's `StoreURI`, or
  configuring the builder to use the `NixStore` as its own remote store. Until
  this is chosen, "no plumbing" is aspirational.
- ~~**O9** — writable `nix run` vs exec-prebuilt-path per kind.~~ **Resolved:**
  the pod model is uniform — every app container runs `nix run` with a writable
  pod-local `/nix`, so nix-invoking tools need no special mode (§4.5).
- **O10** — `bootstrap` seeds nix into the pod-local `/nix` volume on every pod
  start (copying nix's closure from the image, §4.5). Acceptable, or worth a
  node-local cache / pre-baked volume optimization to avoid the per-pod copy?

## 12. Out of scope (this iteration)

- Cross-cluster / multi-tenant build farms.
- **Cross-namespace store/builder sharing.** All references are same-namespace
  (§6); cluster-scoped `ClusterNixStore` / `ClusterNixBuilder` CRDs referenced
  from any namespace are a planned future addition, not part of v1alpha1.
- Automatic Service/Ingress/HPA generation.
- `NixBuilder` targeting `Machine` hosts over SSH (builders are in-cluster pods
  here; reusing the `Machine`/SSH layer as a builder backend is a later option).
- Running these workloads on `Machine` hosts (explicitly an in-cluster design;
  the SSH/`Machine` layer remains for OS-level config).

## Appendix A — Worked example: `NixDeployment` → pods

### A.1 What the author declares

```yaml
# infra, once
kind: NixStore
metadata: { name: store, namespace: apps }
spec:
  storage: { accessModes: [ReadWriteOnce], resources: { requests: { storage: 200Gi } } }
# status (filled by the NixStore controller):
#   substituterURL: http://store.apps.svc
#   storeURI:       ssh-ng://store.apps.svc
#   publicKey:      store:AbC123==
---
kind: NixBuilder                         # one worker (no pool)
metadata: { name: linux-builder, namespace: apps }
spec:
  storeRef: { name: store }      # builds land straight in the store
  storage: { accessModes: [ReadWriteOnce], resources: { requests: { storage: 100Gi } } }
# status: builderEndpoint: ssh-ng://linux-builder.apps.svc
---
kind: NixDeployment
metadata: { name: web, namespace: apps }
spec:
  nix:
    source: { gitRepo: https://github.com/acme/web, ref: main }
    run: .#server
    args: ["--port", "8080"]
    image: nixos/nix:2.24.0
    storeRef:   { name: store }   # pull closure from here
    builderRef: { name: linux-builder }    # build here (optional)
  deploymentTemplate:
    replicas: 3
    template:
      spec:
        containers:
          - name: app                     # only user bits; image+cmd are owned
            readinessProbe: { httpGet: { path: /healthz, port: 8080 } }
```

### A.2 How it is configured & unfolds

```text
 AUTHOR                OPERATOR (NixDeployment ctrl)              KUBERNETES
 ──────                ─────────────────────────────             ──────────
 NixStore ───────────▶ read status: substituterURL,
 NixBuilder ─────────▶   storeURI, publicKey, builderEndpoint
 NixDeployment ──┐       (→ baked into pod NIX_CONFIG; operator never builds)
                 │
                 ▼
        1. RESOLVE  git ls-remote main ───────────────▶ rev = abc123
                    revKey = hash(abc123+".#server"+args) = r-7f3a9c
                 │
                 ▼
        2. PROJECT  server-side apply apps/v1 Deployment "web"  ──▶ Deployment
                    (ownerRef: NixDeployment/web; pod template          │
                     stamped with revKey; app cmd = nix run .#server;    ▼
                     NIX_CONFIG → store + linux-builder)            ReplicaSet ──▶ 3 Pods
                                                                                   │
                 ┌── observe Deployment .status ◀──────────────────────────────────┘
                 ▼                                  each Pod (uniform):
        3. Building/Progressing/        init `bootstrap`   : seed nix → /nix-vol ⇒ /nix
           Ready/Degraded               init `fetch-source`: checkout abc123 → /workspace
                                        init `instantiate` : nix build .#server (+prebuild)
                                           → 1st pod builds on linux-builder → store;
                                             the rest substitute. fail ⇒ pod fails ⇒
                                             rollout STALLS, old rev keeps serving
                                        container `app`    : nix run .#server -- --port 8080
                                           (eval + run; nothing to build)
```

### A.3 The rendered native `Deployment` pod spec

The `app` container's `image`/`command`/`NIX_CONFIG`, the three init-containers,
and the volumes are operator-owned; the `readinessProbe` is the user's, preserved:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: apps
  ownerReferences:
    - { apiVersion: nio.homystack.com/v1alpha1, kind: NixDeployment, name: web, controller: true }
spec:
  replicas: 3
  selector:
    matchLabels:
      nio.homystack.com/workload-kind: NixDeployment
      nio.homystack.com/workload-name: web
  template:
    metadata:
      labels:
        nio.homystack.com/workload-kind: NixDeployment
        nio.homystack.com/workload-name: web
        nio.homystack.com/revision: r-7f3a9c
        app.kubernetes.io/managed-by: nio
      annotations:
        nio.homystack.com/revision: r-7f3a9c       # change here ⇒ rolling update
    spec:
      volumes:
        - name: nix-store                          # localStore: Disk (default)
          emptyDir: {}
        - name: workspace
          emptyDir: {}
      initContainers:
        - name: bootstrap                          # OWNED: seed nix into the volume
          image: nixos/nix:2.24.0
          # volume at /nix-vol so the image's own /nix is NOT shadowed; copy from it.
          command: ["sh", "-c", "[ -e /nix-vol/store ] || cp --archive /nix/. /nix-vol/"]
          volumeMounts:
            - { name: nix-store, mountPath: /nix-vol }
        - name: fetch-source                       # OWNED: checkout @abc123 → /workspace
          image: nixos/nix:2.24.0
          command: ["sh", "-c"]
          args:
            - |
              nix shell nixpkgs#gitMinimal --command sh -c '
                git init /workspace && cd /workspace
                git remote add origin "$NIO_GIT_REPO"
                git fetch --depth 1 origin "$NIO_REVISION"
                git checkout --detach FETCH_HEAD'
          env:
            - { name: NIO_GIT_REPO, value: "https://github.com/acme/web" }
            - { name: NIO_REVISION, value: "abc123" }
          volumeMounts:
            - { name: nix-store, mountPath: /nix }
            - { name: workspace, mountPath: /workspace }
        - name: instantiate                        # OWNED: materialize Run+prebuild
          image: nixos/nix:2.24.0
          workingDir: /workspace
          command: ["nix", "build", ".#server"]    # + any nix.prebuild entries
          env:
            - { name: NIX_CONFIG, value: "experimental-features = nix-command flakes\nsubstituters = http://store.apps.svc\ntrusted-public-keys = store:AbC123==\nbuilders = ssh-ng://linux-builder.apps.svc x86_64-linux\nbuilders-use-substitutes = true" }
          volumeMounts:
            - { name: nix-store, mountPath: /nix }
            - { name: workspace, mountPath: /workspace }
      containers:
        - name: app                                # OWNED: image + command + NIX_CONFIG
          image: nixos/nix:2.24.0
          workingDir: /workspace
          command: ["nix", "run", ".#server", "--", "--port", "8080"]
          env:
            - name: NIX_CONFIG
              value: |
                experimental-features = nix-command flakes
                substituters = http://store.apps.svc https://cache.nixos.org
                trusted-public-keys = store:AbC123== cache.nixos.org-1:...
                builders = ssh-ng://linux-builder.apps.svc x86_64-linux
                builders-use-substitutes = true
          readinessProbe:                          # USER's, preserved verbatim
            httpGet: { path: /healthz, port: 8080 }
          volumeMounts:
            - { name: nix-store, mountPath: /nix }
            - { name: workspace, mountPath: /workspace }
  strategy:                                        # defaulted by NIO (§3.3):
    type: RollingUpdate                            # surge-only so a broken
    rollingUpdate: { maxUnavailable: 0 }           # revision can't shed capacity
```

The `instantiate` init materialized `…-server` into the pod-local `/nix` (the
first pod built it on `linux-builder` → `store`; the rest substituted it; a build
failure fails the pod and stalls the rollout). So the app's `nix run .#server`
only evaluates `.` from `/workspace` and runs — **no build, nothing to fetch** at
app start.

### A.4 On the next `git push`

```text
ls-remote main → def456 (≠ abc123)            (operator does NOT build)
  → revKey' = hash(def456+…) = r-91b2e0       (changed: new commit)
  → server-side apply Deployment with annotation nio.homystack.com/revision: r-91b2e0
    and fetch-source env NIO_REVISION=def456
  → Deployment controller sees pod-template change → native RollingUpdate
  → new pods: instantiate builds .#server@def456 (1st on linux-builder → store,
    rest substitute), then nix run .#server; old pods drain once new are Ready
```

Every new commit rolls out, even one whose closure is byte-identical (e.g. a
docs-only change): the revision key is the commit, not the `drvHash`. This is the
deliberate trade-off from §2.1 — a commit is a deploy, and we accept the churn
rather than carry closure-diffing machinery to suppress it.
