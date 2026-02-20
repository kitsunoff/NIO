# NixOS Operator - Analysis for Kubebuilder Migration

This document contains a comprehensive analysis of the current nixos-operator implementation for migration to kubebuilder.

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Machine addressing** | Single `host` field (IP or hostname) | Operator doesn't care how to connect - user provides working address |
| **Namespace isolation** | Machine and NixosConfiguration must be in same namespace | Simplifies RBAC, projected volumes, owner references |
| **Secret references** | Same-namespace only (no cross-namespace) | Projected volumes only work within namespace |
| **Job secrets** | Projected volumes (not RBAC) | Principle of least privilege - Jobs only see needed secrets |
| **Concurrency limit** | Global (not per-namespace) | Simpler implementation, prevents cluster-wide resource exhaustion |
| **HardwareFacts storage** | `runtime.RawExtension` | Flexible schema, data changes frequently |
| **HardwareFacts → reconcile** | Does NOT trigger reconcile | Updated only on next reconcile, avoids unnecessary re-deployments |
| **onRemoveFlake failure** | Keep retrying + emit events | User must be notified, but deletion shouldn't be blocked forever |
| **Admission webhooks** | Not required | Validation via CRD OpenAPI schema and controller-side checks |

## 1. Project Structure

```
nixos-operator/
├── crds/                              # CRD definitions (YAML)
│   ├── machine.yaml                   # Machine CRD (v1alpha1)
│   └── nixosconfiguration.yaml        # NixosConfiguration CRD (v1alpha1)
│
├── main.py                            # Main operator file (kopf handlers)
├── machine_handlers.py                # Machine resource handlers
├── nixosconfiguration_handlers.py     # NixosConfiguration handlers
├── reconcile_helpers.py               # Reconciliation helper functions
├── clients.py                         # Kubernetes API client
├── config.py                          # Operator configuration
│
├── ssh_utils.py                       # SSH utilities
├── utils.py                           # General utilities (Git, hashing)
├── input_validation.py                # Input validation
├── retry_utils.py                     # Retry logic with backoff
├── events.py                          # Kubernetes events
├── known_hosts_manager.py             # SSH known_hosts management
├── health.py                          # Health check server
├── metrics.py                         # Prometheus metrics
│
├── scripts/
│   ├── hardware_scanner.sh            # Hardware scanning script
│   └── facts_parser.py                # Scan results parser
│
└── tests/                             # Unit and integration tests
```

## 2. Current Framework

**Framework**: KOPF (Kubernetes Operator Pythonic Framework)

KOPF uses decorators for event handling:

```python
# Machine handlers
@kopf.on.create()                  # On Machine creation
@kopf.timer()                       # Periodic availability check
@kopf.timer()                       # Periodic hardware scan

# NixosConfiguration handlers
@kopf.on.create()
@kopf.on.update()                   # On change
@kopf.on.resume()                   # On operator restart
@kopf.on.delete()                   # On deletion
@kopf.timer()                       # Periodic reconcile

# Lifecycle handlers
@kopf.on.startup()                  # On operator start
@kopf.on.cleanup()                  # On operator shutdown
```

## 3. CRD Definitions

### 3.1 Machine CRD

**API Group**: `nio.homystack.com/v1alpha1`

#### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `host` | string | Yes | Target machine address (hostname or IP) for SSH connection |
| `sshUser` | string | No | SSH user for connection (default: "root") |
| `sshKeySecretRef.name` | string | No | Secret name with SSH private key |
| `sshKeySecretRef.namespace` | string | No | Secret namespace |
| `sshPasswordSecretRef.name` | string | No | Secret name with SSH password |
| `sshPasswordSecretRef.namespace` | string | No | Secret namespace |
| `sshPasswordSecretRef.key` | string | No | Key in secret (default: "password") |

#### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `discoverable` | boolean | Machine is reachable via SSH |
| `hasConfiguration` | boolean | Configuration is applied |
| `appliedConfiguration` | string | Name of applied NixosConfiguration |
| `appliedCommit` | string | Git commit hash of applied config |
| `nixFacterResult` | object | Result from nix facter command |
| `hardwareFacts` | object | Collected hardware facts |
| `lastAppliedTime` | date-time | Last successful application timestamp |
| `lastHardwareScanTime` | date-time | Last hardware scan timestamp |
| `conditions` | array | Kubernetes conditions |

#### Additional Printer Columns

```yaml
additionalPrinterColumns:
  - name: Host            | jsonPath: .spec.host
  - name: Discoverable    | jsonPath: .status.discoverable
  - name: Has Config      | jsonPath: .status.hasConfiguration
  - name: Applied Config  | jsonPath: .status.appliedConfiguration
  # Age is built-in kubectl column, no need to define
```

### 3.2 NixosConfiguration CRD

**API Group**: `nio.homystack.com/v1alpha1`

#### Spec Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `gitRepo` | string | No | Git repository URL with NixOS config |
| `ref` | string | No | Git ref (branch/tag/commit), default: "main" |
| `credentialsRef.name` | string | No | Secret for private repo access |
| `flake` | string | No | Flake reference (e.g., "#worker") |
| `onRemoveFlake` | string | No | Flake to apply on resource deletion |
| `configurationSubdir` | string | No | Subdirectory with Nix config |
| `fullInstall` | boolean | No | Use nixos-anywhere (true) or nixos-rebuild (false) |
| `machineRef.name` | string | Yes | Reference to Machine resource |
| `additionalFiles` | array | No | Files to inject into repo |
| `additionalFiles[].path` | string | Yes | Path relative to repo root |
| `additionalFiles[].valueType` | enum | Yes | Inline, SecretRef, or NixosFacter |
| `additionalFiles[].inline` | string | No | Inline content |
| `additionalFiles[].secretRef.name` | string | No | Secret name |
| `additionalFiles[].secretRef.key` | string | No | Key in secret (required for SecretRef) |
| `additionalFiles[].nixosFacter` | boolean | No | Generate from machine facts |
| `jobTemplate` | object | No | Customization for apply Job pods |
| `jobTemplate.image` | string | No | Custom container image for apply jobs |
| `jobTemplate.nodeSelector` | map[string]string | No | Node selector for job pods |
| `jobTemplate.tolerations` | array | No | Tolerations for job pods |
| `jobTemplate.resources` | ResourceRequirements | No | Resource limits/requests for job container |
| `jobTemplate.serviceAccountName` | string | No | Custom ServiceAccount for jobs |

#### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `fullDiskInstallCompleted` | boolean | Full disk install completed |
| `appliedCommit` | string | Applied git commit hash |
| `lastAppliedTime` | date-time | Last successful application timestamp |
| `targetMachine` | string | Target Machine resource name |
| `conditions` | array | Kubernetes conditions |

#### Additional Printer Columns

```yaml
additionalPrinterColumns:
  - name: Git Repo        | jsonPath: .spec.gitRepo
  - name: Flake           | jsonPath: .spec.flake
  - name: Target Machine  | jsonPath: .spec.machineRef.name
  - name: Full Install    | jsonPath: .spec.fullInstall
  - name: Applied Commit  | jsonPath: .status.appliedCommit
  # Age is built-in kubectl column, no need to define
```

## 4. Reconciliation Logic

### 4.1 Machine Reconciliation

```
Machine Created
├── Start discovery timer (60s interval)
│   └── check_machine_discoverable()
│       ├── Get SSH credentials from Secret
│       ├── Try SSH connection
│       └── Update status.discoverable
│
└── Start hardware scan timer (300s interval)
    └── scan_machine_hardware()
        ├── Upload hardware_scanner.sh via SSH
        ├── Execute script remotely
        ├── Parse results
        └── Update status.hardwareFacts
```

### 4.2 NixosConfiguration Reconciliation

```
reconcile_nixos_configuration()
├── 1. check_machine_availability()
│   ├── Get Machine resource
│   ├── Verify SSH connectivity
│   └── Update conditions if not available
│
├── 2. prepare_git_repository() [with retry]
│   ├── Get remote commit hash
│   ├── Calculate workdir path
│   └── Clone repository
│
├── 3. detect_configuration_changes()
│   ├── Compare appliedCommit vs current
│   ├── Compare additionalFilesHash
│   └── Check deletion timestamp
│
├── 4. inject_additional_files()
│   ├── Process Inline files
│   ├── Process SecretRef files
│   └── Process NixosFacter files
│
├── 5. apply_nixos_configuration()
│   ├── Setup SSH key in /dev/shm
│   ├── For fullInstall: run nixos-anywhere
│   └── For update: run nixos-rebuild switch
│
├── 6. apply_and_update_status()
│   ├── Update Machine status
│   └── Update NixosConfiguration status
│
└── 7. cleanup_repository()
    └── Garbage collect old versions
```

## 5. Current Conditions Implementation

### Current Condition Structure

```yaml
conditions:
  - type: "Applied"
    status: "True" | "False"
    lastTransitionTime: "2024-11-06T12:34:56Z"
    reason: "Success" | "MissingCredentials" | "Removed" | "TemporaryError"
    message: "Description of current state"
```

### Current Reasons Used

| Reason | Status | Description |
|--------|--------|-------------|
| `Success` | True | Configuration successfully applied |
| `MissingCredentials` | False | SSH credentials not available |
| `Removed` | True | Configuration successfully removed |
| `TemporaryError` | False | Temporary error, will retry |

## 6. kstatus Compliance Issues

### 6.1 Missing observedGeneration

**Problem**: Neither CRD has `observedGeneration` field in status.

**Impact**: Tools like ArgoCD, Flux, kpt cannot determine if controller has processed latest changes.

**Required Fix**:
```yaml
status:
  observedGeneration: <metadata.generation>
```

Controller must update this on every reconciliation.

### 6.2 Missing Reconciling Condition

**Problem**: No `Reconciling` condition type exists.

**Impact**: Cannot distinguish between "fully reconciled" and "still processing".

**Required Fix**:
```yaml
conditions:
  - type: Reconciling
    status: "True" | "False"
    reason: "Progressing" | "Completed"
    message: "Controller is reconciling resource"
```

### 6.3 Missing Stalled Condition

**Problem**: No `Stalled` condition type exists.

**Impact**: Cannot signal that reconciliation is blocked.

**Required Fix**:
```yaml
conditions:
  - type: Stalled
    status: "True" | "False"
    reason: "MachineUnreachable" | "GitCloneFailed" | "ApplyFailed"
    message: "Description of blocking issue"
```

### 6.4 Missing Ready Condition

**Problem**: Uses custom `Applied` condition instead of standard `Ready`.

**Impact**: Generic tools expect `Ready` condition.

**Recommendation**: Add `Ready` condition in addition to `Applied`:
```yaml
conditions:
  - type: Ready
    status: "True" | "False"
    reason: "ConfigurationApplied" | "NotApplied"
```

### 6.5 Conditions Missing observedGeneration

**Problem**: Individual conditions don't include `observedGeneration`.

**Impact**: Cannot determine if condition reflects current generation.

**Required Fix**:
```yaml
conditions:
  - type: Ready
    status: "True"
    observedGeneration: 5  # Must match metadata.generation
```

## 7. Recommended Status Schema for Kubebuilder

### 7.1 Machine Spec and Status

```go
type MachineSpec struct {
    // Host is the target machine address (hostname or IP) for SSH connection.
    // +kubebuilder:validation:MinLength=1
    Host string `json:"host"`

    // SSHUser is the SSH username for connection.
    // +kubebuilder:default="root"
    // +optional
    SSHUser string `json:"sshUser,omitempty"`

    // SSHKeySecretRef references a Secret containing SSH private key.
    // +optional
    SSHKeySecretRef *SecretReference `json:"sshKeySecretRef,omitempty"`

    // SSHPasswordSecretRef references a Secret containing SSH password.
    // +optional
    SSHPasswordSecretRef *SSHPasswordSecretRef `json:"sshPasswordSecretRef,omitempty"`
}

// SecretReference references a Secret in the same namespace.
// Cross-namespace references are not supported by design.
type SecretReference struct {
    // Name is the Secret name (must be in the same namespace as the referencing resource).
    Name string `json:"name"`
}

// SSHPasswordSecretRef references a specific key in a Secret for SSH password.
// Must be in the same namespace as the Machine resource.
type SSHPasswordSecretRef struct {
    // Name is the Secret name (must be in the same namespace as the Machine).
    Name string `json:"name"`

    // Key is the key in the Secret containing the password.
    // +kubebuilder:default="password"
    // +optional
    Key string `json:"key,omitempty"`
}

type MachineStatus struct {
    // ObservedGeneration is the most recent generation observed by the controller.
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`

    // Discoverable indicates if machine is reachable via SSH.
    // +optional
    Discoverable bool `json:"discoverable,omitempty"`

    // HasConfiguration indicates if a NixOS configuration is applied.
    // +optional
    HasConfiguration bool `json:"hasConfiguration,omitempty"`

    // AppliedConfiguration is the name of applied NixosConfiguration.
    // +optional
    AppliedConfiguration string `json:"appliedConfiguration,omitempty"`

    // AppliedCommit is the git commit hash of applied configuration.
    // +optional
    AppliedCommit string `json:"appliedCommit,omitempty"`

    // LastAppliedTime is the timestamp of last successful application.
    // +optional
    LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

    // LastHardwareScanTime is the timestamp of last hardware scan.
    // +optional
    LastHardwareScanTime *metav1.Time `json:"lastHardwareScanTime,omitempty"`

    // HardwareFacts contains collected hardware information.
    // +optional
    HardwareFacts *HardwareFacts `json:"hardwareFacts,omitempty"`

    // NixFacterResult contains nix facter command output.
    // +optional
    // +kubebuilder:pruning:PreserveUnknownFields
    NixFacterResult runtime.RawExtension `json:"nixFacterResult,omitempty"`

    // Conditions represent the latest available observations.
    // +optional
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

### 7.2 NixosConfiguration Spec (with JobTemplate)

```go
type NixosConfigurationSpec struct {
    // MachineRef is a reference to the target Machine resource.
    // Machine must be in the same namespace as NixosConfiguration (by design).
    MachineRef MachineReference `json:"machineRef"`

    // GitRepo is the URL of the git repository containing NixOS configuration.
    // +optional
    GitRepo string `json:"gitRepo,omitempty"`

    // Ref is the git reference (branch, tag, or commit) to checkout.
    // +kubebuilder:default="main"
    // +optional
    Ref string `json:"ref,omitempty"`

    // CredentialsRef references a Secret for private repository access.
    // +optional
    CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`

    // Flake is the flake reference (e.g., "#worker").
    // +optional
    Flake string `json:"flake,omitempty"`

    // OnRemoveFlake is the flake to apply when this resource is deleted.
    // +optional
    OnRemoveFlake string `json:"onRemoveFlake,omitempty"`

    // ConfigurationSubdir is the subdirectory containing Nix configuration.
    // +optional
    ConfigurationSubdir string `json:"configurationSubdir,omitempty"`

    // FullInstall enables nixos-anywhere for full disk installation.
    // +optional
    FullInstall bool `json:"fullInstall,omitempty"`

    // AdditionalFiles are files to inject into the repository before apply.
    // +optional
    AdditionalFiles []AdditionalFile `json:"additionalFiles,omitempty"`

    // JobTemplate customizes the apply Job pods.
    // +optional
    JobTemplate *JobTemplate `json:"jobTemplate,omitempty"`
}

// JobTemplate defines customization for apply Job pods.
type JobTemplate struct {
    // Image is the container image for apply jobs.
    // If not specified, uses the operator's default image.
    // +optional
    Image string `json:"image,omitempty"`

    // NodeSelector is a selector for job pod assignment.
    // +optional
    NodeSelector map[string]string `json:"nodeSelector,omitempty"`

    // Tolerations are tolerations for job pods.
    // +optional
    Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

    // Resources are resource requirements for the job container.
    // +optional
    Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

    // ServiceAccountName is the ServiceAccount for job pods.
    // If not specified, uses the default job ServiceAccount.
    // +optional
    ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// AdditionalFile defines a file to inject into the repository.
type AdditionalFile struct {
    // Path is the file path relative to repository root.
    Path string `json:"path"`

    // ValueType specifies how to obtain the file content.
    // +kubebuilder:validation:Enum=Inline;SecretRef;NixosFacter
    ValueType string `json:"valueType"`

    // Inline is the literal file content (for ValueType=Inline).
    // +optional
    Inline string `json:"inline,omitempty"`

    // SecretRef references a Secret key (for ValueType=SecretRef).
    // +optional
    SecretRef *SecretKeyReference `json:"secretRef,omitempty"`

    // NixosFacter generates content from Machine facts (for ValueType=NixosFacter).
    // +optional
    NixosFacter bool `json:"nixosFacter,omitempty"`
}

// SecretKeyReference references a specific key in a Secret.
type SecretKeyReference struct {
    // Name is the Secret name.
    Name string `json:"name"`

    // Key is the key in the Secret.
    Key string `json:"key"`
}
```

### 7.3 NixosConfiguration Status

```go
type NixosConfigurationStatus struct {
    // ObservedGeneration is the most recent generation observed by the controller.
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`

    // FullDiskInstallCompleted indicates if nixos-anywhere was run.
    // +optional
    FullDiskInstallCompleted bool `json:"fullDiskInstallCompleted,omitempty"`

    // AppliedCommit is the git commit hash that was applied.
    // +optional
    AppliedCommit string `json:"appliedCommit,omitempty"`

    // LastAppliedTime is the timestamp of last successful application.
    // +optional
    LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

    // TargetMachine is the Machine resource name this config applies to.
    // +optional
    TargetMachine string `json:"targetMachine,omitempty"`

    // ConfigurationHash is the hash of applied configuration.
    // +optional
    ConfigurationHash string `json:"configurationHash,omitempty"`

    // AdditionalFilesHash is the hash of injected files.
    // +optional
    AdditionalFilesHash string `json:"additionalFilesHash,omitempty"`

    // Conditions represent the latest available observations.
    // +optional
    // +patchMergeKey=type
    // +patchStrategy=merge
    // +listType=map
    // +listMapKey=type
    Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
```

### 7.4 Standard Condition Types

```go
const (
    // ConditionReady indicates the resource has reached a fully reconciled state.
    ConditionReady = "Ready"

    // ConditionReconciling indicates the controller is actively processing changes.
    ConditionReconciling = "Reconciling"

    // ConditionStalled indicates the controller cannot make progress.
    ConditionStalled = "Stalled"
)

// Machine-specific condition types
const (
    // ConditionDiscoverable indicates SSH connectivity to the machine.
    ConditionDiscoverable = "Discoverable"

    // ConditionHardwareScanned indicates hardware facts were collected.
    ConditionHardwareScanned = "HardwareScanned"
)

// NixosConfiguration-specific condition types
const (
    // ConditionApplied indicates configuration was applied to the machine.
    ConditionApplied = "Applied"

    // ConditionGitSynced indicates git repository was successfully cloned.
    ConditionGitSynced = "GitSynced"
)
```

### 7.5 Condition Reasons

```go
// Generic reasons
const (
    ReasonSucceeded        = "Succeeded"
    ReasonFailed           = "Failed"
    ReasonProgressing      = "Progressing"
    ReasonWaiting          = "Waiting"
)

// Machine-specific reasons
const (
    ReasonSSHConnected     = "SSHConnected"
    ReasonSSHFailed        = "SSHFailed"
    ReasonCredentialsMissing = "CredentialsMissing"
    ReasonHardwareScanSucceeded = "HardwareScanSucceeded"
    ReasonHardwareScanFailed = "HardwareScanFailed"
)

// NixosConfiguration-specific reasons
const (
    ReasonConfigApplied    = "ConfigurationApplied"
    ReasonConfigRemoved    = "ConfigurationRemoved"
    ReasonApplyFailed      = "ApplyFailed"
    ReasonGitCloneSucceeded = "GitCloneSucceeded"
    ReasonGitCloneFailed   = "GitCloneFailed"
    ReasonMachineNotReady  = "MachineNotReady"
)
```

## 8. Recommended Additional Printer Columns

### 8.1 Machine

```yaml
additionalPrinterColumns:
  - name: Host
    type: string
    jsonPath: .spec.host
  - name: Ready
    type: string
    jsonPath: .status.conditions[?(@.type=="Ready")].status
  - name: Discoverable
    type: string
    jsonPath: .status.conditions[?(@.type=="Discoverable")].status
  - name: Config
    type: string
    jsonPath: .status.appliedConfiguration
  # Age is built-in kubectl column
```

### 8.2 NixosConfiguration

```yaml
additionalPrinterColumns:
  - name: Ready
    type: string
    jsonPath: .status.conditions[?(@.type=="Ready")].status
  - name: Target
    type: string
    jsonPath: .spec.machineRef.name
  - name: Flake
    type: string
    jsonPath: .spec.flake
  - name: Commit
    type: string
    jsonPath: .status.appliedCommit
    priority: 1
  - name: Age
    type: date
    jsonPath: .metadata.creationTimestamp
```

## 9. Controller Design Patterns for Kubebuilder

### 9.1 Reconciler Structure

```go
type MachineReconciler struct {
    client.Client
    Scheme     *runtime.Scheme
    SSHClient  *ssh.Client
    Metrics    *metrics.Metrics
}

func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // 1. Fetch the Machine instance
    var machine niov1alpha1.Machine
    if err := r.Get(ctx, req.NamespacedName, &machine); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // 2. Set observedGeneration immediately
    machine.Status.ObservedGeneration = machine.Generation

    // 3. Set Reconciling condition to True
    meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
        Type:               ConditionReconciling,
        Status:             metav1.ConditionTrue,
        ObservedGeneration: machine.Generation,
        Reason:             ReasonProgressing,
        Message:            "Reconciliation in progress",
    })

    // 4. Update status early
    if err := r.Status().Update(ctx, &machine); err != nil {
        return ctrl.Result{}, err
    }

    // 5. Perform reconciliation logic
    result, err := r.reconcile(ctx, &machine)

    // 6. Set final conditions based on result
    if err != nil {
        meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
            Type:               ConditionStalled,
            Status:             metav1.ConditionTrue,
            ObservedGeneration: machine.Generation,
            Reason:             ReasonFailed,
            Message:            err.Error(),
        })
    } else {
        meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
            Type:               ConditionReconciling,
            Status:             metav1.ConditionFalse,
            ObservedGeneration: machine.Generation,
            Reason:             ReasonSucceeded,
            Message:            "Reconciliation completed",
        })
        meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
            Type:               ConditionReady,
            Status:             metav1.ConditionTrue,
            ObservedGeneration: machine.Generation,
            Reason:             ReasonSucceeded,
            Message:            "Machine is ready",
        })
    }

    // 7. Final status update
    if statusErr := r.Status().Update(ctx, &machine); statusErr != nil {
        return ctrl.Result{}, statusErr
    }

    return result, err
}
```

### 9.2 Periodic Reconciliation

Instead of kopf timers, use kubebuilder's `RequeueAfter`:

```go
func (r *MachineReconciler) reconcile(ctx context.Context, machine *niov1alpha1.Machine) (ctrl.Result, error) {
    // Check SSH connectivity
    discoverable, err := r.checkDiscoverable(ctx, machine)
    if err != nil {
        return ctrl.Result{RequeueAfter: 30 * time.Second}, err
    }

    machine.Status.Discoverable = discoverable

    // Requeue for periodic check
    return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}
```

### 9.3 Finalizers for Cleanup

```go
const finalizerName = "nio.homystack.com/finalizer"

func (r *NixosConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var config niov1alpha1.NixosConfiguration
    if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Handle deletion
    if !config.DeletionTimestamp.IsZero() {
        if controllerutil.ContainsFinalizer(&config, finalizerName) {
            // Run finalization logic (apply onRemoveFlake if set)
            if err := r.finalizeConfig(ctx, &config); err != nil {
                return ctrl.Result{}, err
            }
            // Remove finalizer
            controllerutil.RemoveFinalizer(&config, finalizerName)
            if err := r.Update(ctx, &config); err != nil {
                return ctrl.Result{}, err
            }
        }
        return ctrl.Result{}, nil
    }

    // Add finalizer if not present
    if !controllerutil.ContainsFinalizer(&config, finalizerName) {
        controllerutil.AddFinalizer(&config, finalizerName)
        if err := r.Update(ctx, &config); err != nil {
            return ctrl.Result{}, err
        }
    }

    // Normal reconciliation...
    return r.reconcile(ctx, &config)
}
```

## 10. Configuration Variables

Current environment variables from `config.py`:

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `NIO_BASE_CONFIG_PATH` | string | `/tmp/nixos-config` | Cloned repos directory |
| `NIO_KNOWN_HOSTS_PATH` | string | `/tmp/nio-ssh-known-hosts` | SSH known_hosts path |
| `NIO_REMOTE_HARDWARE_SCRIPT_PATH` | string | `/tmp/hardware_scanner.sh` | Remote script path |
| `NIO_MACHINE_DISCOVERY_INTERVAL` | float | `60.0` | Discovery interval (sec) |
| `NIO_HARDWARE_SCAN_INTERVAL` | float | `300.0` | Hardware scan interval (sec) |
| `NIO_CONFIG_RECONCILE_INTERVAL` | float | `120.0` | Config reconcile interval (sec) |
| `NIO_NIXOS_APPLY_TIMEOUT` | int | `3600` | Apply timeout (sec) |
| `NIO_RETRY_MAX_ATTEMPTS` | int | `3` | Max retry attempts |
| `NIO_RETRY_INITIAL_DELAY` | float | `2.0` | Initial retry delay (sec) |
| `NIO_RETRY_MAX_DELAY` | float | `30.0` | Max retry delay (sec) |
| `NIO_RETRY_EXPONENTIAL_BASE` | float | `2.0` | Exponential backoff base |
| `METRICS_PORT` | int | `8000` | Prometheus metrics port |
| `HEALTH_CHECK_PORT` | int | `8080` | Health check port |

## 11. Prometheus Metrics

### Gauges (current state)
- `nio_machines_total`
- `nio_machines_discoverable`
- `nio_machines_with_configuration`
- `nio_configurations_total`

### Counters (accumulated)
- `nio_configurations_applied_total`
- `nio_configurations_failed_total`
- `nio_ssh_connections_total`
- `nio_git_clones_total`
- `nio_nixos_builds_total`
- `nio_retries_total`
- `nio_errors_total`

### Histograms (duration)
- `nio_reconcile_duration_seconds`
- `nio_ssh_connection_duration_seconds`
- `nio_git_clone_duration_seconds`
- `nio_nixos_build_duration_seconds`

## 12. RBAC Requirements

```yaml
ClusterRole:
  rules:
  # CRDs
  - apiGroups: ["nio.homystack.com"]
    resources: [machines, nixosconfigurations]
    verbs: [get, list, watch, create, update, patch, delete]
  - apiGroups: ["nio.homystack.com"]
    resources: [machines/status, nixosconfigurations/status]
    verbs: [get, update, patch]
  - apiGroups: ["nio.homystack.com"]
    resources: [machines/finalizers, nixosconfigurations/finalizers]
    verbs: [update]
  # Secrets
  - apiGroups: [""]
    resources: [secrets]
    verbs: [get, list, watch]
  # Events
  - apiGroups: [""]
    resources: [events]
    verbs: [create, patch]
```

## 13. Migration Checklist

- [ ] Initialize kubebuilder project with `kubebuilder init`
- [ ] Create API types with `kubebuilder create api`
- [ ] Implement Machine types matching current spec
- [ ] Implement NixosConfiguration types matching current spec
- [ ] Add observedGeneration to all status structs
- [ ] Add standard conditions (Ready, Reconciling, Stalled)
- [ ] Implement MachineReconciler with SSH logic
- [ ] Implement NixosConfigurationReconciler with Git/Nix logic
- [ ] Add finalizers for cleanup logic
- [ ] Implement periodic requeue for discovery/scans
- [ ] Add Prometheus metrics using controller-runtime metrics
- [ ] Add health/readiness probes
- [ ] Add comprehensive tests
- [ ] Generate CRD manifests with proper printer columns
- [ ] Test kstatus compatibility with ArgoCD/Flux

## 14. SSH Connection Implementation

### 14.1 Connection Flow

```
establish_ssh_connection()
├── Validate hostname (prevent command injection)
├── Validate SSH username
├── Get known_hosts manager (TOFU policy)
├── Try SSH key authentication
│   ├── Get secret with key "ssh-privatekey"
│   ├── Write key to /dev/shm/nio-ssh-keys/ (tmpfs, mode 0400)
│   └── Use asyncssh with client_keys
├── Try password authentication (fallback)
│   ├── Get secret with configurable key (default: "password")
│   └── Use asyncssh with password
├── Try no authentication (final fallback)
└── Return (connection, temp_key_path)
```

### 14.2 SSH Security Features

| Feature | Implementation |
|---------|----------------|
| Keys in memory only | `/dev/shm/nio-ssh-keys/` (tmpfs, never on disk) |
| Key permissions | `0o400` (owner read-only) |
| Directory permissions | `0o700` for key directory |
| Host verification | TOFU via `known_hosts_manager` |
| Input validation | Hostname, username validated against injection |
| Cleanup | Keys deleted after use via `cleanup_ssh_key()` |

### 14.3 Secret Formats

**SSH Key Secret:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: machine-ssh-key
type: kubernetes.io/ssh-auth
data:
  ssh-privatekey: <base64-encoded-private-key>
```

**SSH Password Secret:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: machine-ssh-password
type: Opaque
data:
  password: <base64-encoded-password>  # Key name configurable via sshPasswordSecretRef.key
```

**Git Credentials Secret:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: git-credentials
type: Opaque
data:
  # Either SSH key for git@... URLs
  ssh-privatekey: <base64-encoded-key>
  # Or token for https://... URLs (inserted as https://token:{token}@host/path)
  token: <base64-encoded-token>
```

## 15. additionalFiles Processing

### 15.1 Value Types

| Type | Source | Processing |
|------|--------|------------|
| `Inline` | `spec.additionalFiles[].inline` | Write content directly to file |
| `SecretRef` | Secret referenced by name and key | Get specified key from secret, write value |
| `NixosFacter` | Machine spec + hardwareFacts | Generate JSON with machine info |

### 15.2 NixosFacter Output Format

```json
{
  "host": "<spec.host>",
  // All fields from status.hardwareFacts merged in:
  "os": { "name": "NixOS", "id": "nixos" },
  "cpu": { "model": "...", "cores": "4" },
  "disk": { "sda": "500GB" },
  // etc.
}
```

**Important:** Changes to `Machine.Status.HardwareFacts` do NOT automatically trigger
NixosConfiguration reconciliation. The NixosFacter file content is updated only when
the NixosConfiguration reconciles for other reasons (spec change, periodic reconcile,
git commit change). This is by design to avoid unnecessary re-deployments when
hardware facts change slightly (e.g., uptime, memory usage).

### 15.3 File Injection Path

Files are written to: `{repo_path}/{configurationSubdir}/{additionalFiles[].path}`

After injection, files are added to git index with `--intent-to-add` (tracked but not committed).

## 16. NixOS Apply Commands

### 16.1 Full Install (nixos-anywhere)

Used when: `spec.fullInstall=true` AND `status.fullDiskInstallCompleted=false`

```bash
nix --extra-experimental-features 'nix-command flakes' \
  run github:nix-community/nixos-anywhere -- \
  --target-host {sshUser}@{targetHost} \
  --flake {configPath}{flake} \
  -i {sshKeyPath}
```

### 16.2 Update (nixos-rebuild)

Used when: `fullDiskInstallCompleted=true` OR `fullInstall=false`

```bash
NIX_SSHOPTS="-i {sshKeyPath}" \
nix --extra-experimental-features 'nix-command flakes' \
  shell nixpkgs#nixos-rebuild --command \
  nixos-rebuild switch \
  --flake {configPath}{flake} \
  --target-host {sshUser}@{targetHost}
```

### 16.3 Deletion (onRemoveFlake)

If `spec.onRemoveFlake` is set, applies that flake before resource deletion:
- Uses `nixos-rebuild switch` with `onRemoveFlake` instead of `flake`
- Machine status reset: `hasConfiguration=false`, `appliedConfiguration=null`

## 17. Hardware Scanner Output

### 17.1 Collected Facts

| Category | Fields |
|----------|--------|
| **OS** | `os.name`, `os.id`, `kernel.version`, `architecture`, `hostname`, `uptime.days` |
| **CPU** | `cpu.model`, `cpu.cores` |
| **Memory** | `memory.mb` |
| **Virtualization** | `virtualization.type` (physical/vm/docker/etc), `container.engine` |
| **System ID** | `system.serial`, `system.uuid`, `system.timezone` |
| **Software** | `system.glibc_version`, `system.gcc_version`, `nix.version` |
| **User** | `user.current`, `user.has_sudo` |
| **Storage** | `storage.filesystems` (array), `disk.<name>` (dynamic, e.g., `disk.sda=500GB`) |
| **Network** | `network.dns_servers` (array), `interface.<name>` (dynamic, e.g., `interface.eth0=192.168.1.10`) |
| **Security** | `security.apparmor`, `security.selinux` |

### 17.2 Parsed Structure

Raw `key=value` format is parsed into nested JSON:

```json
{
  "os": { "name": "NixOS", "id": "nixos" },
  "kernel": { "version": "6.1.0" },
  "cpu": { "model": "Intel...", "cores": "4" },
  "memory": { "mb": "16384" },
  "disk": { "sda": "500GB", "nvme0n1": "1TB" },
  "interface": { "eth0": "192.168.1.10", "wlan0": "192.168.1.20" },
  "storage": { "filesystems": ["ext4", "btrfs", "vfat"] },
  "network": { "dns_servers": ["8.8.8.8", "8.8.4.4"] }
}
```

## 18. Input Validation Rules

### 18.1 Validation Functions

| Function | Max Length | Allowed Characters | Blocked Patterns |
|----------|------------|-------------------|------------------|
| `validate_host()` | 253 | `[a-zA-Z0-9\-\.:\[\]]` | `;$\`|&><(){}` newlines |
| `validate_git_url()` | 2048 | Valid URL, schemes: `https/http/git/ssh` | `;$\`|&` newlines |
| `validate_ssh_username()` | 32 | `[a-zA-Z0-9_\-]` | Everything else |
| `validate_path()` | 4096 | Most chars except dangerous | null bytes, `;$\`|&` newlines |

### 18.2 Kubebuilder Implementation

In Go, implement via:
1. **CRD validation** (OpenAPI schema patterns in kubebuilder markers)
2. **Admission webhooks** (for complex validation)
3. **Runtime validation** (in reconciler before external calls)

```go
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9][a-zA-Z0-9\-\.]*[a-zA-Z0-9]$`
Host string `json:"host"`
```

## 19. Kubernetes Events

### 19.1 Event Types

| Function | Level | Reasons |
|----------|-------|---------|
| `emit_missing_credentials_event()` | Warning | `MissingSSHKey`, `SecretNotFound`, `MissingPassword` |
| `emit_configuration_applied_event()` | Normal | Custom reason/message |
| `emit_error_event()` | Warning | Custom reason/message |

### 19.2 Kubebuilder Events

```go
// In reconciler
r.Recorder.Event(&machine, corev1.EventTypeWarning, "MissingSSHKey",
    "Secret does not contain 'ssh-privatekey'")

r.Recorder.Eventf(&config, corev1.EventTypeNormal, "ConfigurationApplied",
    "Successfully applied commit %s", commitHash[:8])
```

## 20. Example Resource Manifests

### 20.1 Machine

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata:
  name: worker-01
  namespace: default
spec:
  host: worker-01.example.com  # hostname or IP address
  sshUser: root
  sshKeySecretRef:
    name: worker-ssh-key
```

### 20.2 NixosConfiguration

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixosConfiguration
metadata:
  name: worker-01-config
  namespace: default
spec:
  machineRef:
    name: worker-01
  gitRepo: https://github.com/example/nixos-configs.git
  ref: main
  flake: "#worker"
  fullInstall: true
  onRemoveFlake: "#minimal"
  configurationSubdir: hosts/worker
  additionalFiles:
    - path: hardware-configuration.nix
      valueType: NixosFacter
    - path: secrets/api-key.txt
      valueType: SecretRef
      secretRef:
        name: worker-api-key
        key: api-key
    - path: local.nix
      valueType: Inline
      inline: |
        { config, ... }: {
          networking.hostName = "worker-01";
        }
  jobTemplate:
    image: ghcr.io/homystack/nixos-operator:v1.0.0
    nodeSelector:
      kubernetes.io/arch: amd64
      node-role.kubernetes.io/builder: "true"
    tolerations:
      - key: "dedicated"
        operator: "Equal"
        value: "nixos-builder"
        effect: "NoSchedule"
    resources:
      requests:
        cpu: 500m
        memory: 512Mi
      limits:
        cpu: 4
        memory: 4Gi
```

## 21. Error Handling Strategy

### 21.1 Transient vs Permanent Errors

| Error Type | Action | KOPF/Kubebuilder |
|------------|--------|------------------|
| SSH connection failed | Retry with backoff | `kopf.TemporaryError` / `RequeueAfter` |
| Git clone failed | Retry with backoff | `kopf.TemporaryError` / `RequeueAfter` |
| Secret not found | Retry (may appear later) | `kopf.TemporaryError` / `RequeueAfter` |
| Invalid spec (validation) | Don't retry, set Stalled | Return error, set condition |
| nixos-rebuild failed | Retry once, then Stalled | Condition `Applied=False` |
| Machine not discoverable | Continue periodic checks | Condition `Discoverable=False` |

### 21.2 Retry Configuration

```go
// Kubebuilder equivalent of current retry logic
const (
    MaxRetryAttempts    = 3
    InitialRetryDelay   = 2 * time.Second
    MaxRetryDelay       = 30 * time.Second
    ExponentialBase     = 2.0
)

func calculateBackoff(attempt int) time.Duration {
    delay := float64(InitialRetryDelay) * math.Pow(ExponentialBase, float64(attempt))
    if delay > float64(MaxRetryDelay) {
        delay = float64(MaxRetryDelay)
    }
    return time.Duration(delay)
}
```

## 22. Long-Running Operations

### 22.1 Problem Statement

NixOS operations can run for extended periods:

| Operation | Typical Duration | Max Duration |
|-----------|------------------|--------------|
| `nixos-rebuild switch` | 5-15 min | 30 min |
| `nixos-anywhere` (full install) | 15-45 min | 60+ min |
| Git clone (large repo) | 1-5 min | 10 min |
| Hardware scan | 10-30 sec | 2 min |

**Challenges:**
- Reconciler blocking for 30+ minutes is unacceptable
- Operator restart loses in-progress operation state
- No visibility into operation progress
- Resource contention with multiple concurrent operations

### 22.2 Architecture: Kubernetes Jobs

All configuration apply operations MUST run as Kubernetes Jobs. This provides:
- **Restartability**: Jobs survive operator restarts
- **Isolation**: Separate resource limits per operation
- **Observability**: Native pod logs and metrics
- **Garbage collection**: TTL-based cleanup via owner references

### 22.3 Status Schema for Operation Tracking

```go
type NixosConfigurationStatus struct {
    // ... existing fields ...

    // OperationState tracks long-running operation progress
    // +optional
    OperationState *OperationState `json:"operationState,omitempty"`
}

type OperationState struct {
    // Type of operation in progress
    // +kubebuilder:validation:Enum=NixosRebuild;FullInstall
    Type string `json:"type"`

    // StartedAt is when the operation began
    StartedAt metav1.Time `json:"startedAt"`

    // Phase describes current operation phase
    // +optional
    Phase string `json:"phase,omitempty"`

    // JobName is the name of the Kubernetes Job running this operation
    JobName string `json:"jobName"`

    // LastLogLine contains last line of job output for quick status
    // +optional
    LastLogLine string `json:"lastLogLine,omitempty"`
}
```

### 22.4 Job Creation

```go
func (r *NixosConfigurationReconciler) createApplyJob(ctx context.Context, config *niov1alpha1.NixosConfiguration, opType string) (*batchv1.Job, error) {
    jobName := fmt.Sprintf("%s-apply-%s", config.Name, randomSuffix(5))

    // Determine timeout based on operation type
    var timeout int64
    if opType == "FullInstall" {
        timeout = 3600 // 1 hour for nixos-anywhere
    } else {
        timeout = 1800 // 30 min for nixos-rebuild
    }

    // Apply jobTemplate customizations
    image := r.DefaultJobImage
    nodeSelector := map[string]string{}
    var tolerations []corev1.Toleration
    resources := corev1.ResourceRequirements{
        Requests: corev1.ResourceList{
            corev1.ResourceCPU:    resource.MustParse("100m"),
            corev1.ResourceMemory: resource.MustParse("256Mi"),
        },
        Limits: corev1.ResourceList{
            corev1.ResourceCPU:    resource.MustParse("2"),
            corev1.ResourceMemory: resource.MustParse("2Gi"),
        },
    }
    serviceAccountName := "nixos-operator-job"

    if jt := config.Spec.JobTemplate; jt != nil {
        if jt.Image != "" {
            image = jt.Image
        }
        if jt.NodeSelector != nil {
            nodeSelector = jt.NodeSelector
        }
        if jt.Tolerations != nil {
            tolerations = jt.Tolerations
        }
        if jt.Resources != nil {
            resources = *jt.Resources
        }
        if jt.ServiceAccountName != "" {
            serviceAccountName = jt.ServiceAccountName
        }
    }

    job := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      jobName,
            Namespace: config.Namespace,
            Labels: map[string]string{
                "app.kubernetes.io/name":       "nixos-operator",
                "app.kubernetes.io/component":  "apply-job",
                "nio.homystack.com/config":     config.Name,
                "nio.homystack.com/machine":    config.Spec.MachineRef.Name,
                "nio.homystack.com/operation":  opType,
            },
            Annotations: map[string]string{
                "nio.homystack.com/config-generation": fmt.Sprintf("%d", config.Generation),
            },
        },
        Spec: batchv1.JobSpec{
            TTLSecondsAfterFinished: ptr.To(int32(3600)),  // Cleanup after 1h
            BackoffLimit:            ptr.To(int32(0)),     // No retries, operator handles retry logic
            ActiveDeadlineSeconds:   ptr.To(timeout),
            Template: corev1.PodTemplateSpec{
                ObjectMeta: metav1.ObjectMeta{
                    Labels: map[string]string{
                        "app.kubernetes.io/name":      "nixos-operator",
                        "app.kubernetes.io/component": "apply-job",
                        "nio.homystack.com/config":    config.Name,
                    },
                },
                Spec: corev1.PodSpec{
                    RestartPolicy:      corev1.RestartPolicyNever,
                    ServiceAccountName: serviceAccountName,
                    NodeSelector:       nodeSelector,
                    Tolerations:        tolerations,
                    SecurityContext: &corev1.PodSecurityContext{
                        RunAsNonRoot: ptr.To(true),
                        RunAsUser:    ptr.To(int64(1000)),
                        FSGroup:      ptr.To(int64(1000)),
                        SeccompProfile: &corev1.SeccompProfile{
                            Type: corev1.SeccompProfileTypeRuntimeDefault,
                        },
                    },
                    Containers: []corev1.Container{{
                        Name:  "nixos-apply",
                        Image: image,
                        Args: []string{
                            "apply",
                            "--config-name=" + config.Name,
                            "--config-namespace=" + config.Namespace,
                            "--operation=" + opType,
                        },
                        Env: r.buildJobEnv(config),
                        VolumeMounts: []corev1.VolumeMount{
                            {
                                Name:      "ssh-key",
                                MountPath: "/secrets/ssh",
                                ReadOnly:  true,
                            },
                            {
                                Name:      "workdir",
                                MountPath: "/work",
                            },
                        },
                        Resources: resources,
                        SecurityContext: &corev1.SecurityContext{
                            AllowPrivilegeEscalation: ptr.To(false),
                            ReadOnlyRootFilesystem:   ptr.To(true),
                            Capabilities: &corev1.Capabilities{
                                Drop: []corev1.Capability{"ALL"},
                            },
                        },
                    }},
                    Volumes: r.buildJobVolumes(ctx, config),
                },
            },
        },
    }

    // Set owner reference for garbage collection
    if err := ctrl.SetControllerReference(config, job, r.Scheme); err != nil {
        return nil, fmt.Errorf("set controller reference: %w", err)
    }

    if err := r.Create(ctx, job); err != nil {
        return nil, fmt.Errorf("create job: %w", err)
    }

    return job, nil
}

func (r *NixosConfigurationReconciler) buildJobEnv(config *niov1alpha1.NixosConfiguration) []corev1.EnvVar {
    return []corev1.EnvVar{
        {Name: "GIT_REPO", Value: config.Spec.GitRepo},
        {Name: "GIT_REF", Value: config.Spec.Ref},
        {Name: "FLAKE", Value: config.Spec.Flake},
        {Name: "CONFIG_SUBDIR", Value: config.Spec.ConfigurationSubdir},
        {Name: "SSH_KEY_PATH", Value: "/secrets/ssh/ssh-privatekey"},
    }
}

// buildJobVolumes creates projected volumes for the Job.
// Uses projected volumes to mount only the specific secrets needed,
// avoiding broad RBAC permissions.
func (r *NixosConfigurationReconciler) buildJobVolumes(ctx context.Context, config *niov1alpha1.NixosConfiguration) []corev1.Volume {
    volumes := []corev1.Volume{
        {
            Name: "workdir",
            VolumeSource: corev1.VolumeSource{
                EmptyDir: &corev1.EmptyDirVolumeSource{},
            },
        },
    }

    // Build projected volume sources for secrets
    var projectedSources []corev1.VolumeProjection

    // Get Machine to find SSH secret reference
    machine, err := r.getMachine(ctx, config)
    if err == nil && machine.Spec.SSHKeySecretRef != nil {
        projectedSources = append(projectedSources, corev1.VolumeProjection{
            Secret: &corev1.SecretProjection{
                LocalObjectReference: corev1.LocalObjectReference{
                    Name: machine.Spec.SSHKeySecretRef.Name,
                },
                Items: []corev1.KeyToPath{
                    {Key: "ssh-privatekey", Path: "ssh-privatekey"},
                },
            },
        })
    }

    // Add git credentials if specified
    if config.Spec.CredentialsRef != nil {
        projectedSources = append(projectedSources, corev1.VolumeProjection{
            Secret: &corev1.SecretProjection{
                LocalObjectReference: corev1.LocalObjectReference{
                    Name: config.Spec.CredentialsRef.Name,
                },
                Items: []corev1.KeyToPath{
                    {Key: "token", Path: "git-token", Mode: ptr.To(int32(0400))},
                },
                Optional: ptr.To(true), // token may not exist if using SSH
            },
        })
    }

    // Add additionalFiles secrets
    for i, f := range config.Spec.AdditionalFiles {
        if f.ValueType == "SecretRef" && f.SecretRef != nil {
            projectedSources = append(projectedSources, corev1.VolumeProjection{
                Secret: &corev1.SecretProjection{
                    LocalObjectReference: corev1.LocalObjectReference{
                        Name: f.SecretRef.Name,
                    },
                    Items: []corev1.KeyToPath{
                        {Key: f.SecretRef.Key, Path: fmt.Sprintf("additional-%d", i)},
                    },
                },
            })
        }
    }

    if len(projectedSources) > 0 {
        volumes = append(volumes, corev1.Volume{
            Name: "secrets",
            VolumeSource: corev1.VolumeSource{
                Projected: &corev1.ProjectedVolumeSource{
                    DefaultMode: ptr.To(int32(0400)),
                    Sources:     projectedSources,
                },
            },
        })
    }

    return volumes
}
```

### 22.5 Reconciler with Job Watching

```go
func (r *NixosConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&niov1alpha1.NixosConfiguration{}).
        Owns(&batchv1.Job{}).  // Watch owned Jobs - triggers reconcile on Job status change
        Complete(r)
}

func (r *NixosConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    var config niov1alpha1.NixosConfiguration
    if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Check if operation is already in progress
    if config.Status.OperationState != nil {
        return r.checkJobProgress(ctx, &config)
    }

    // Check if apply is needed
    if !r.needsApply(&config) {
        return ctrl.Result{RequeueAfter: r.ReconcileInterval}, nil
    }

    // Check per-machine concurrency (prevent parallel jobs on same machine)
    hasActiveJob, err := r.hasActiveJobForMachine(ctx, config.Spec.MachineRef.Name, config.Namespace)
    if err != nil {
        return ctrl.Result{}, err
    }
    if hasActiveJob {
        log.Info("Machine already has active job, requeuing", "machine", config.Spec.MachineRef.Name)
        meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
            Type:               ConditionReconciling,
            Status:             metav1.ConditionTrue,
            ObservedGeneration: config.Generation,
            Reason:             "MachineInUse",
            Message:            fmt.Sprintf("Machine %s has another job in progress", config.Spec.MachineRef.Name),
        })
        if err := r.Status().Update(ctx, &config); err != nil {
            return ctrl.Result{}, err
        }
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    // Check global concurrency limit
    activeJobs, err := r.countActiveJobs(ctx)
    if err != nil {
        return ctrl.Result{}, err
    }
    if activeJobs >= r.MaxConcurrentJobs {
        log.Info("Max concurrent jobs reached, requeuing", "active", activeJobs, "max", r.MaxConcurrentJobs)
        meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
            Type:               ConditionReconciling,
            Status:             metav1.ConditionTrue,
            ObservedGeneration: config.Generation,
            Reason:             "Queued",
            Message:            fmt.Sprintf("Waiting for job slot (%d/%d active)", activeJobs, r.MaxConcurrentJobs),
        })
        if err := r.Status().Update(ctx, &config); err != nil {
            return ctrl.Result{}, err
        }
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    // Create apply job
    opType := "NixosRebuild"
    if config.Spec.FullInstall && !config.Status.FullDiskInstallCompleted {
        opType = "FullInstall"
    }

    job, err := r.createApplyJob(ctx, &config, opType)
    if err != nil {
        return ctrl.Result{}, fmt.Errorf("create apply job: %w", err)
    }

    // Update status with operation state
    config.Status.OperationState = &niov1alpha1.OperationState{
        Type:      opType,
        StartedAt: metav1.Now(),
        Phase:     "JobCreated",
        JobName:   job.Name,
    }
    meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
        Type:               ConditionReconciling,
        Status:             metav1.ConditionTrue,
        ObservedGeneration: config.Generation,
        Reason:             "ApplyStarted",
        Message:            fmt.Sprintf("Started %s job: %s", opType, job.Name),
    })

    if err := r.Status().Update(ctx, &config); err != nil {
        return ctrl.Result{}, err
    }

    r.Recorder.Eventf(&config, corev1.EventTypeNormal, "ApplyStarted",
        "Created %s job %s", opType, job.Name)

    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}
```

### 22.6 Job Progress Monitoring

```go
func (r *NixosConfigurationReconciler) checkJobProgress(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    if config.Status.OperationState == nil {
        return ctrl.Result{}, nil
    }

    jobName := config.Status.OperationState.JobName
    var job batchv1.Job
    if err := r.Get(ctx, types.NamespacedName{
        Name:      jobName,
        Namespace: config.Namespace,
    }, &job); err != nil {
        if apierrors.IsNotFound(err) {
            // Job was deleted - mark as failed
            log.Error(err, "Job not found, marking operation as failed", "job", jobName)
            return r.markOperationFailed(ctx, config, "Job was deleted")
        }
        return ctrl.Result{}, err
    }

    // Check job status
    if job.Status.Succeeded > 0 {
        return r.handleJobSuccess(ctx, config, &job)
    }

    if job.Status.Failed > 0 {
        return r.handleJobFailure(ctx, config, &job)
    }

    // Job still running - update progress from logs
    if job.Status.Active > 0 {
        if err := r.updateProgressFromLogs(ctx, config, &job); err != nil {
            log.Error(err, "Failed to update progress from logs")
        }
        return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
    }

    // Job pending - check timeout
    age := time.Since(config.Status.OperationState.StartedAt.Time)
    if age > 5*time.Minute && job.Status.Active == 0 {
        log.Info("Job stuck in pending state", "job", jobName, "age", age)
        config.Status.OperationState.Phase = "Pending"
        meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
            Type:               ConditionStalled,
            Status:             metav1.ConditionTrue,
            ObservedGeneration: config.Generation,
            Reason:             "JobPending",
            Message:            fmt.Sprintf("Job %s stuck in pending state for %s", jobName, age.Round(time.Second)),
        })
        r.Status().Update(ctx, config)
    }

    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *NixosConfigurationReconciler) handleJobSuccess(ctx context.Context, config *niov1alpha1.NixosConfiguration, job *batchv1.Job) (ctrl.Result, error) {
    log := log.FromContext(ctx)
    log.Info("Job completed successfully", "job", job.Name)

    // Get result from job annotations or configmap
    result, err := r.getJobResult(ctx, job)
    if err != nil {
        log.Error(err, "Failed to get job result, assuming success")
    }

    // Update configuration status
    config.Status.AppliedCommit = result.Commit
    config.Status.LastAppliedTime = &metav1.Time{Time: time.Now()}
    config.Status.OperationState = nil

    if config.Status.OperationState != nil && config.Status.OperationState.Type == "FullInstall" {
        config.Status.FullDiskInstallCompleted = true
    }

    meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
        Type:               ConditionReady,
        Status:             metav1.ConditionTrue,
        ObservedGeneration: config.Generation,
        Reason:             ReasonConfigApplied,
        Message:            fmt.Sprintf("Configuration applied successfully (commit: %s)", truncate(result.Commit, 8)),
    })
    meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
        Type:               ConditionReconciling,
        Status:             metav1.ConditionFalse,
        ObservedGeneration: config.Generation,
        Reason:             ReasonSucceeded,
        Message:            "Reconciliation completed",
    })
    meta.RemoveStatusCondition(&config.Status.Conditions, ConditionStalled)

    if err := r.Status().Update(ctx, config); err != nil {
        return ctrl.Result{}, err
    }

    // Update Machine status
    if err := r.updateMachineStatus(ctx, config); err != nil {
        log.Error(err, "Failed to update Machine status")
    }

    r.Recorder.Eventf(config, corev1.EventTypeNormal, "ConfigurationApplied",
        "Successfully applied configuration (commit: %s)", truncate(result.Commit, 8))

    return ctrl.Result{RequeueAfter: r.ReconcileInterval}, nil
}

func (r *NixosConfigurationReconciler) handleJobFailure(ctx context.Context, config *niov1alpha1.NixosConfiguration, job *batchv1.Job) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // Get failure reason from pod logs
    failureReason := r.getJobFailureReason(ctx, job)
    log.Error(nil, "Job failed", "job", job.Name, "reason", failureReason)

    config.Status.OperationState = nil
    meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
        Type:               ConditionReady,
        Status:             metav1.ConditionFalse,
        ObservedGeneration: config.Generation,
        Reason:             ReasonApplyFailed,
        Message:            fmt.Sprintf("Apply failed: %s", failureReason),
    })
    meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
        Type:               ConditionStalled,
        Status:             metav1.ConditionTrue,
        ObservedGeneration: config.Generation,
        Reason:             ReasonApplyFailed,
        Message:            failureReason,
    })

    if err := r.Status().Update(ctx, config); err != nil {
        return ctrl.Result{}, err
    }

    r.Recorder.Eventf(config, corev1.EventTypeWarning, "ApplyFailed",
        "Configuration apply failed: %s", failureReason)

    // Requeue with backoff for retry
    return ctrl.Result{RequeueAfter: r.calculateBackoff(config)}, nil
}

func (r *NixosConfigurationReconciler) markOperationFailed(ctx context.Context, config *niov1alpha1.NixosConfiguration, reason string) (ctrl.Result, error) {
    config.Status.OperationState = nil
    meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
        Type:               ConditionStalled,
        Status:             metav1.ConditionTrue,
        ObservedGeneration: config.Generation,
        Reason:             "OperationFailed",
        Message:            reason,
    })

    if err := r.Status().Update(ctx, config); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{RequeueAfter: r.calculateBackoff(config)}, nil
}
```

### 22.7 Progress Updates from Job Logs

```go
func (r *NixosConfigurationReconciler) updateProgressFromLogs(ctx context.Context, config *niov1alpha1.NixosConfiguration, job *batchv1.Job) error {
    // Get pod for this job
    var pods corev1.PodList
    if err := r.List(ctx, &pods,
        client.InNamespace(job.Namespace),
        client.MatchingLabels{"job-name": job.Name},
    ); err != nil {
        return err
    }

    if len(pods.Items) == 0 {
        return nil
    }

    pod := &pods.Items[0]

    // Get last few lines of logs
    req := r.Clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
        TailLines: ptr.To(int64(5)),
    })
    logs, err := req.DoRaw(ctx)
    if err != nil {
        return err
    }

    // Parse progress from logs (look for patterns like "building X of Y" or percentage)
    phase, lastLine := parseProgressFromLogs(string(logs))

    config.Status.OperationState.Phase = phase
    config.Status.OperationState.LastLogLine = lastLine

    meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
        Type:               ConditionReconciling,
        Status:             metav1.ConditionTrue,
        ObservedGeneration: config.Generation,
        Reason:             "ApplyInProgress",
        Message:            fmt.Sprintf("%s: %s", phase, truncate(lastLine, 100)),
    })

    return r.Status().Update(ctx, config)
}

func parseProgressFromLogs(logs string) (phase, lastLine string) {
    lines := strings.Split(strings.TrimSpace(logs), "\n")
    if len(lines) == 0 {
        return "Running", ""
    }

    lastLine = lines[len(lines)-1]

    // Detect phase from log patterns
    switch {
    case strings.Contains(logs, "cloning"):
        phase = "CloningRepository"
    case strings.Contains(logs, "building"):
        phase = "Building"
    case strings.Contains(logs, "copying"):
        phase = "CopyingToTarget"
    case strings.Contains(logs, "activating"):
        phase = "Activating"
    case strings.Contains(logs, "nixos-anywhere"):
        phase = "FullInstall"
    default:
        phase = "Running"
    }

    return phase, lastLine
}
```

### 22.8 Job Cleanup and Concurrency

```go
func (r *NixosConfigurationReconciler) countActiveJobs(ctx context.Context) (int, error) {
    var jobList batchv1.JobList
    if err := r.List(ctx, &jobList,
        client.MatchingLabels{"app.kubernetes.io/name": "nixos-operator", "app.kubernetes.io/component": "apply-job"},
    ); err != nil {
        return 0, err
    }

    active := 0
    for _, job := range jobList.Items {
        if job.Status.Active > 0 {
            active++
        }
    }
    return active, nil
}

// hasActiveJobForMachine checks if there's already an active job targeting the specified machine.
// This prevents concurrent operations on the same machine which could cause corruption.
func (r *NixosConfigurationReconciler) hasActiveJobForMachine(ctx context.Context, machineName, namespace string) (bool, error) {
    var jobList batchv1.JobList
    if err := r.List(ctx, &jobList,
        client.InNamespace(namespace),
        client.MatchingLabels{
            "app.kubernetes.io/name":      "nixos-operator",
            "app.kubernetes.io/component": "apply-job",
            "nio.homystack.com/machine":   machineName,
        },
    ); err != nil {
        return false, err
    }

    for _, job := range jobList.Items {
        if job.Status.Active > 0 {
            return true, nil
        }
    }
    return false, nil
}

// Cleanup stale jobs that lost their parent NixosConfiguration
func (r *NixosConfigurationReconciler) cleanupOrphanedJobs(ctx context.Context) error {
    var jobList batchv1.JobList
    if err := r.List(ctx, &jobList,
        client.MatchingLabels{"app.kubernetes.io/name": "nixos-operator"},
    ); err != nil {
        return err
    }

    for _, job := range jobList.Items {
        // Jobs with owner references will be garbage collected automatically
        // This handles edge cases where owner reference was not set
        if len(job.OwnerReferences) == 0 {
            age := time.Since(job.CreationTimestamp.Time)
            if age > 2*time.Hour {
                if err := r.Delete(ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
                    if !apierrors.IsNotFound(err) {
                        return err
                    }
                }
            }
        }
    }
    return nil
}
```

### 22.9 Job RBAC Requirements

```yaml
# Additional RBAC for Job management (for the operator)
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: nixos-operator-job-manager
rules:
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["get", "list", "watch", "create", "delete"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
---
# ServiceAccount for Jobs themselves (minimal permissions)
# Jobs do NOT need RBAC to read secrets - secrets are mounted as projected volumes
apiVersion: v1
kind: ServiceAccount
metadata:
  name: nixos-operator-job
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: nixos-operator-job
rules:
  # Jobs need to read Machine for target info (host address)
  - apiGroups: ["nio.homystack.com"]
    resources: ["machines"]
    verbs: ["get"]
  # Jobs may optionally update NixosConfiguration status with progress
  # (alternatively, operator can poll job logs)
  - apiGroups: ["nio.homystack.com"]
    resources: ["nixosconfigurations/status"]
    verbs: ["get", "patch"]
```

**Note:** Jobs receive secrets via projected volumes (see `buildJobVolumes()`), not via RBAC.
This follows the principle of least privilege - Jobs only have access to the specific
secrets they need, not all secrets in the namespace.

### 22.10 Implementation Checklist

- [ ] Add `OperationState` to NixosConfigurationStatus schema
- [ ] Implement `createApplyJob()` with proper security context
- [ ] Add Job watch to reconciler (`Owns(&batchv1.Job{})`)
- [ ] Implement `checkJobProgress()` with all job states
- [ ] Implement `handleJobSuccess()` with status updates
- [ ] Implement `handleJobFailure()` with error extraction from logs
- [ ] Add `updateProgressFromLogs()` for real-time progress
- [ ] Implement concurrency limiting (`countActiveJobs()`)
- [ ] Add RBAC for Job management and Job ServiceAccount
- [ ] Build separate container image or binary mode for jobs
- [ ] Add metrics: `nio_jobs_active`, `nio_job_duration_seconds`, `nio_jobs_failed_total`
- [ ] Set operation timeouts (1h for full install, 30m for rebuild)
- [ ] Implement `cleanupOrphanedJobs()` for edge cases

## 23. Secret Watches with Field Indexes

### 23.1 Problem Statement

When a Secret referenced by Machine or NixosConfiguration is created or updated, the controller must react immediately:

| Resource | Secret References |
|----------|-------------------|
| Machine | `spec.sshKeySecretRef`, `spec.sshPasswordSecretRef` |
| NixosConfiguration | `spec.credentialsRef`, `spec.additionalFiles[].secretRef` |

**Without Secret watches:**
1. User creates Machine with `sshKeySecretRef: my-key`
2. Secret `my-key` doesn't exist yet
3. Machine stuck in `Discoverable=False`
4. User creates Secret `my-key`
5. **Nothing happens** until next periodic reconcile (60-120 seconds)

### 23.2 Solution: Field Indexes + Filtered Watch

Use kubebuilder field indexes to efficiently map Secrets to dependent resources.

### 23.3 Index Registration

```go
const (
    // Index field names
    IndexMachineBySSHKeySecret      = "spec.sshKeySecretRef.name"
    IndexMachineBySSHPasswordSecret = "spec.sshPasswordSecretRef.name"
    IndexConfigByCredentialsSecret  = "spec.credentialsRef.name"
    IndexConfigByAdditionalFiles    = "spec.additionalFiles.secretRef"
)

func SetupIndexes(ctx context.Context, mgr ctrl.Manager) error {
    // Machine indexes
    if err := mgr.GetFieldIndexer().IndexField(ctx, &niov1alpha1.Machine{},
        IndexMachineBySSHKeySecret,
        func(obj client.Object) []string {
            machine := obj.(*niov1alpha1.Machine)
            if machine.Spec.SSHKeySecretRef == nil {
                return nil
            }
            return []string{machine.Spec.SSHKeySecretRef.Name}
        },
    ); err != nil {
        return fmt.Errorf("index %s: %w", IndexMachineBySSHKeySecret, err)
    }

    if err := mgr.GetFieldIndexer().IndexField(ctx, &niov1alpha1.Machine{},
        IndexMachineBySSHPasswordSecret,
        func(obj client.Object) []string {
            machine := obj.(*niov1alpha1.Machine)
            if machine.Spec.SSHPasswordSecretRef == nil {
                return nil
            }
            return []string{machine.Spec.SSHPasswordSecretRef.Name}
        },
    ); err != nil {
        return fmt.Errorf("index %s: %w", IndexMachineBySSHPasswordSecret, err)
    }

    // NixosConfiguration indexes
    if err := mgr.GetFieldIndexer().IndexField(ctx, &niov1alpha1.NixosConfiguration{},
        IndexConfigByCredentialsSecret,
        func(obj client.Object) []string {
            config := obj.(*niov1alpha1.NixosConfiguration)
            if config.Spec.CredentialsRef == nil {
                return nil
            }
            return []string{config.Spec.CredentialsRef.Name}
        },
    ); err != nil {
        return fmt.Errorf("index %s: %w", IndexConfigByCredentialsSecret, err)
    }

    if err := mgr.GetFieldIndexer().IndexField(ctx, &niov1alpha1.NixosConfiguration{},
        IndexConfigByAdditionalFiles,
        func(obj client.Object) []string {
            config := obj.(*niov1alpha1.NixosConfiguration)
            var secrets []string
            for _, f := range config.Spec.AdditionalFiles {
                if f.ValueType == "SecretRef" && f.SecretRef != nil {
                    secrets = append(secrets, f.SecretRef.Name)
                }
            }
            return secrets
        },
    ); err != nil {
        return fmt.Errorf("index %s: %w", IndexConfigByAdditionalFiles, err)
    }

    return nil
}
```

### 23.4 Machine Controller Setup

```go
func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&niov1alpha1.Machine{}).
        Watches(
            &corev1.Secret{},
            handler.EnqueueRequestsFromMapFunc(r.findMachinesForSecret),
            builder.WithPredicates(r.secretChangePredicate()),
        ).
        Complete(r)
}

// findMachinesForSecret returns reconcile requests for all Machines that reference this Secret
func (r *MachineReconciler) findMachinesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
    secret := obj.(*corev1.Secret)
    log := log.FromContext(ctx).WithValues("secret", secret.Name, "namespace", secret.Namespace)

    var requests []reconcile.Request

    // Find Machines referencing this Secret as SSH key
    var machinesByKey niov1alpha1.MachineList
    if err := r.List(ctx, &machinesByKey,
        client.InNamespace(secret.Namespace),
        client.MatchingFields{IndexMachineBySSHKeySecret: secret.Name},
    ); err != nil {
        log.Error(err, "Failed to list Machines by SSH key secret")
        return nil
    }

    for _, m := range machinesByKey.Items {
        requests = append(requests, reconcile.Request{
            NamespacedName: types.NamespacedName{
                Name:      m.Name,
                Namespace: m.Namespace,
            },
        })
    }

    // Find Machines referencing this Secret as SSH password
    var machinesByPassword niov1alpha1.MachineList
    if err := r.List(ctx, &machinesByPassword,
        client.InNamespace(secret.Namespace),
        client.MatchingFields{IndexMachineBySSHPasswordSecret: secret.Name},
    ); err != nil {
        log.Error(err, "Failed to list Machines by SSH password secret")
        return requests
    }

    for _, m := range machinesByPassword.Items {
        // Avoid duplicates
        found := false
        for _, req := range requests {
            if req.Name == m.Name && req.Namespace == m.Namespace {
                found = true
                break
            }
        }
        if !found {
            requests = append(requests, reconcile.Request{
                NamespacedName: types.NamespacedName{
                    Name:      m.Name,
                    Namespace: m.Namespace,
                },
            })
        }
    }

    if len(requests) > 0 {
        log.Info("Secret change triggered Machine reconciliation", "machines", len(requests))
    }

    return requests
}

// secretChangePredicate filters Secret events to reduce noise
func (r *MachineReconciler) secretChangePredicate() predicate.Predicate {
    return predicate.Funcs{
        CreateFunc: func(e event.CreateEvent) bool {
            // Always process newly created Secrets
            return true
        },
        UpdateFunc: func(e event.UpdateEvent) bool {
            // Only process if data changed (not just metadata)
            oldSecret := e.ObjectOld.(*corev1.Secret)
            newSecret := e.ObjectNew.(*corev1.Secret)
            return !reflect.DeepEqual(oldSecret.Data, newSecret.Data)
        },
        DeleteFunc: func(e event.DeleteEvent) bool {
            // Process deletions to update status
            return true
        },
        GenericFunc: func(e event.GenericEvent) bool {
            return false
        },
    }
}
```

### 23.5 NixosConfiguration Controller Setup

```go
func (r *NixosConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&niov1alpha1.NixosConfiguration{}).
        Owns(&batchv1.Job{}).
        Watches(
            &corev1.Secret{},
            handler.EnqueueRequestsFromMapFunc(r.findConfigsForSecret),
            builder.WithPredicates(r.secretChangePredicate()),
        ).
        Watches(
            &niov1alpha1.Machine{},
            handler.EnqueueRequestsFromMapFunc(r.findConfigsForMachine),
        ).
        Complete(r)
}

func (r *NixosConfigurationReconciler) findConfigsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
    secret := obj.(*corev1.Secret)
    log := log.FromContext(ctx).WithValues("secret", secret.Name, "namespace", secret.Namespace)

    var requests []reconcile.Request
    seen := make(map[string]bool)

    // Find configs using this Secret for Git credentials
    var configsByCreds niov1alpha1.NixosConfigurationList
    if err := r.List(ctx, &configsByCreds,
        client.InNamespace(secret.Namespace),
        client.MatchingFields{IndexConfigByCredentialsSecret: secret.Name},
    ); err != nil {
        log.Error(err, "Failed to list configs by credentials secret")
        return nil
    }

    for _, c := range configsByCreds.Items {
        key := c.Namespace + "/" + c.Name
        if !seen[key] {
            seen[key] = true
            requests = append(requests, reconcile.Request{
                NamespacedName: types.NamespacedName{Name: c.Name, Namespace: c.Namespace},
            })
        }
    }

    // Find configs using this Secret in additionalFiles
    var configsByFiles niov1alpha1.NixosConfigurationList
    if err := r.List(ctx, &configsByFiles,
        client.InNamespace(secret.Namespace),
        client.MatchingFields{IndexConfigByAdditionalFiles: secret.Name},
    ); err != nil {
        log.Error(err, "Failed to list configs by additional files secret")
        return requests
    }

    for _, c := range configsByFiles.Items {
        key := c.Namespace + "/" + c.Name
        if !seen[key] {
            seen[key] = true
            requests = append(requests, reconcile.Request{
                NamespacedName: types.NamespacedName{Name: c.Name, Namespace: c.Namespace},
            })
        }
    }

    if len(requests) > 0 {
        log.Info("Secret change triggered NixosConfiguration reconciliation", "configs", len(requests))
    }

    return requests
}

// Also watch Machine changes to trigger config reconciliation
func (r *NixosConfigurationReconciler) findConfigsForMachine(ctx context.Context, obj client.Object) []reconcile.Request {
    machine := obj.(*niov1alpha1.Machine)

    var configs niov1alpha1.NixosConfigurationList
    if err := r.List(ctx, &configs, client.InNamespace(machine.Namespace)); err != nil {
        return nil
    }

    var requests []reconcile.Request
    for _, c := range configs.Items {
        if c.Spec.MachineRef.Name == machine.Name {
            requests = append(requests, reconcile.Request{
                NamespacedName: types.NamespacedName{Name: c.Name, Namespace: c.Namespace},
            })
        }
    }

    return requests
}
```

### 23.6 Same-Namespace Secret References

**Design Decision:** All secret references are same-namespace only.

This simplifies the implementation:
- No cross-namespace RBAC complexity
- Simple field indexes (just secret name, no namespace)
- Projected volumes work naturally (same namespace)

The `SecretReference` type has only `Name` field (no `Namespace`):

```go
type SecretReference struct {
    // Name is the Secret name (must be in the same namespace as the referencing resource).
    Name string `json:"name"`
}
```

This makes the mapper function straightforward:

```go
func (r *MachineReconciler) findMachinesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
    secret := obj.(*corev1.Secret)

    // Only look for Machines in the same namespace as the Secret
    var machinesByKey niov1alpha1.MachineList
    if err := r.List(ctx, &machinesByKey,
        client.InNamespace(secret.Namespace),
        client.MatchingFields{IndexMachineBySSHKeySecret: secret.Name},
    ); err != nil {
        return nil
    }

    var requests []reconcile.Request
    for _, m := range machinesByKey.Items {
        requests = append(requests, reconcile.Request{
            NamespacedName: types.NamespacedName{Name: m.Name, Namespace: m.Namespace},
        })
    }

    return requests
}
```

### 23.7 Manager Setup

```go
func main() {
    // ... manager setup ...

    // Register indexes before starting controllers
    if err := SetupIndexes(ctx, mgr); err != nil {
        setupLog.Error(err, "unable to setup indexes")
        os.Exit(1)
    }

    // Setup controllers
    if err := (&MachineReconciler{
        Client:   mgr.GetClient(),
        Scheme:   mgr.GetScheme(),
        Recorder: mgr.GetEventRecorderFor("machine-controller"),
    }).SetupWithManager(mgr); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "Machine")
        os.Exit(1)
    }

    // ... start manager ...
}
```

### 23.8 Testing Secret Watches

```go
func TestMachineReconciler_SecretWatch(t *testing.T) {
    ctx := context.Background()

    // Create Machine with non-existent Secret reference
    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "test-machine",
            Namespace: "default",
        },
        Spec: niov1alpha1.MachineSpec{
            Host: "192.168.1.100",
            SSHKeySecretRef: &niov1alpha1.SecretReference{
                Name: "ssh-key",
            },
        },
    }
    require.NoError(t, k8sClient.Create(ctx, machine))

    // Wait for initial reconcile - should be not discoverable (missing secret)
    eventually(t, func() bool {
        var m niov1alpha1.Machine
        k8sClient.Get(ctx, client.ObjectKeyFromObject(machine), &m)
        cond := meta.FindStatusCondition(m.Status.Conditions, ConditionDiscoverable)
        return cond != nil && cond.Status == metav1.ConditionFalse &&
            cond.Reason == ReasonCredentialsMissing
    }, 5*time.Second)

    // Create the Secret
    secret := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "ssh-key",
            Namespace: "default",
        },
        Type: corev1.SecretTypeSSHAuth,
        Data: map[string][]byte{
            "ssh-privatekey": []byte(testSSHKey),
        },
    }
    require.NoError(t, k8sClient.Create(ctx, secret))

    // Machine should be reconciled automatically (Secret watch triggered)
    eventually(t, func() bool {
        var m niov1alpha1.Machine
        k8sClient.Get(ctx, client.ObjectKeyFromObject(machine), &m)
        // Should attempt SSH connection now that Secret exists
        cond := meta.FindStatusCondition(m.Status.Conditions, ConditionDiscoverable)
        return cond != nil && cond.Reason != ReasonCredentialsMissing
    }, 5*time.Second)
}
```

### 23.9 Implementation Checklist

- [ ] Register field indexes in manager setup (`SetupIndexes`)
- [ ] Add `Watches(&corev1.Secret{}, ...)` to MachineReconciler
- [ ] Add `Watches(&corev1.Secret{}, ...)` to NixosConfigurationReconciler
- [ ] Implement `findMachinesForSecret()` mapper function
- [ ] Implement `findConfigsForSecret()` mapper function
- [ ] Add `secretChangePredicate()` to filter noise (only data changes)
- [ ] Add `Watches(&niov1alpha1.Machine{}, ...)` to NixosConfigurationReconciler
- [ ] Add integration tests for Secret watch behavior
- [ ] Add metrics: `nio_secret_watch_triggers_total`

## 24. Testing Strategy

### 24.1 Testing Philosophy

**Tests MUST be written BEFORE implementation (TDD):**

1. Write failing unit tests for each scenario
2. Implement minimal code to pass tests
3. Refactor while keeping tests green

**Test pyramid:**

```
         /\
        /  \       E2E Tests (few)
       /----\      - Real cluster, real SSH
      /      \
     /--------\    Integration Tests (some)
    /          \   - envtest, fake SSH
   /------------\
  /              \ Unit Tests (many)
 /----------------\- Pure Go, mocked interfaces
```

### 24.2 Test Framework Setup

```go
// internal/controller/suite_test.go
package controller

import (
    "context"
    "path/filepath"
    "testing"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    "k8s.io/client-go/kubernetes/scheme"
    "k8s.io/client-go/rest"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/envtest"
    logf "sigs.k8s.io/controller-runtime/pkg/log"
    "sigs.k8s.io/controller-runtime/pkg/log/zap"

    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
)

var (
    cfg       *rest.Config
    k8sClient client.Client
    testEnv   *envtest.Environment
    ctx       context.Context
    cancel    context.CancelFunc
)

func TestControllers(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
    logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

    ctx, cancel = context.WithCancel(context.Background())

    By("bootstrapping test environment")
    testEnv = &envtest.Environment{
        CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
        ErrorIfCRDPathMissing: true,
    }

    var err error
    cfg, err = testEnv.Start()
    Expect(err).NotTo(HaveOccurred())
    Expect(cfg).NotTo(BeNil())

    err = niov1alpha1.AddToScheme(scheme.Scheme)
    Expect(err).NotTo(HaveOccurred())

    k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
    Expect(err).NotTo(HaveOccurred())
    Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
    cancel()
    By("tearing down the test environment")
    err := testEnv.Stop()
    Expect(err).NotTo(HaveOccurred())
})
```

### 24.3 Mocking Interfaces

```go
// internal/ssh/interface.go
package ssh

import "context"

// Client defines SSH operations interface for testing
type Client interface {
    // Connect establishes SSH connection to the target
    Connect(ctx context.Context, host string, user string, auth AuthMethod) (Connection, error)
}

// Connection represents an established SSH connection
type Connection interface {
    // Execute runs a command and returns output
    Execute(ctx context.Context, cmd string) (stdout, stderr string, exitCode int, err error)
    // Upload copies a file to the remote host
    Upload(ctx context.Context, localPath, remotePath string) error
    // Close terminates the connection
    Close() error
}

// AuthMethod represents SSH authentication
type AuthMethod interface {
    isAuthMethod()
}

type KeyAuth struct {
    PrivateKey []byte
}

func (KeyAuth) isAuthMethod() {}

type PasswordAuth struct {
    Password string
}

func (PasswordAuth) isAuthMethod() {}
```

```go
// internal/ssh/mock.go
package ssh

import (
    "context"
    "fmt"
)

// MockClient implements Client for testing
type MockClient struct {
    // ConnectFunc allows customizing Connect behavior per test
    ConnectFunc func(ctx context.Context, host, user string, auth AuthMethod) (Connection, error)

    // Default behaviors
    Reachable     map[string]bool // host -> reachable
    ExecuteOutput map[string]string // cmd -> output
    ExecuteErrors map[string]error  // cmd -> error
}

func NewMockClient() *MockClient {
    return &MockClient{
        Reachable:     make(map[string]bool),
        ExecuteOutput: make(map[string]string),
        ExecuteErrors: make(map[string]error),
    }
}

func (m *MockClient) Connect(ctx context.Context, host, user string, auth AuthMethod) (Connection, error) {
    if m.ConnectFunc != nil {
        return m.ConnectFunc(ctx, host, user, auth)
    }

    if !m.Reachable[host] {
        return nil, fmt.Errorf("connection refused: %s", host)
    }

    return &MockConnection{
        host:          host,
        executeOutput: m.ExecuteOutput,
        executeErrors: m.ExecuteErrors,
    }, nil
}

type MockConnection struct {
    host          string
    executeOutput map[string]string
    executeErrors map[string]error
    closed        bool
}

func (c *MockConnection) Execute(ctx context.Context, cmd string) (string, string, int, error) {
    if err := c.executeErrors[cmd]; err != nil {
        return "", err.Error(), 1, err
    }
    return c.executeOutput[cmd], "", 0, nil
}

func (c *MockConnection) Upload(ctx context.Context, local, remote string) error {
    return nil
}

func (c *MockConnection) Close() error {
    c.closed = true
    return nil
}
```

### 24.4 Unit Tests: Machine Not Reachable

```go
// internal/controller/machine_controller_test.go
package controller

import (
    "context"
    "errors"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/tools/record"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client/fake"

    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
    "github.com/homystack/nixos-operator/internal/ssh"
)

func TestMachineReconciler_MachineNotReachable_ConnectionRefused(t *testing.T) {
    // Arrange
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))

    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "test-machine",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.MachineSpec{
            Host:    "192.168.1.100",
            SSHUser: "root",
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(machine).
        WithStatusSubresource(machine).
        Build()

    mockSSH := ssh.NewMockClient()
    mockSSH.Reachable["192.168.1.100"] = false // Machine is NOT reachable

    recorder := record.NewFakeRecorder(10)

    reconciler := &MachineReconciler{
        Client:    fakeClient,
        Scheme:    scheme,
        SSHClient: mockSSH,
        Recorder:  recorder,
    }

    // Act
    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{
            Name:      "test-machine",
            Namespace: "default",
        },
    })

    // Assert
    require.NoError(t, err) // Reconcile should not return error for unreachable machine

    // Should requeue to retry later
    assert.True(t, result.RequeueAfter > 0, "should requeue for retry")
    assert.LessOrEqual(t, result.RequeueAfter, 60*time.Second)

    // Check status was updated
    var updated niov1alpha1.Machine
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "test-machine", Namespace: "default"}, &updated))

    // ObservedGeneration should be set
    assert.Equal(t, int64(1), updated.Status.ObservedGeneration)

    // Discoverable should be false
    assert.False(t, updated.Status.Discoverable)

    // Discoverable condition should exist with correct reason
    cond := findCondition(updated.Status.Conditions, ConditionDiscoverable)
    require.NotNil(t, cond, "Discoverable condition should exist")
    assert.Equal(t, metav1.ConditionFalse, cond.Status)
    assert.Equal(t, ReasonSSHFailed, cond.Reason)
    assert.Contains(t, cond.Message, "connection refused")

    // Ready condition should be false
    readyCond := findCondition(updated.Status.Conditions, ConditionReady)
    require.NotNil(t, readyCond)
    assert.Equal(t, metav1.ConditionFalse, readyCond.Status)

    // Should NOT be stalled (transient error, will retry)
    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    if stalledCond != nil {
        assert.Equal(t, metav1.ConditionFalse, stalledCond.Status)
    }

    // Check event was emitted
    select {
    case event := <-recorder.Events:
        assert.Contains(t, event, "Warning")
        assert.Contains(t, event, "SSHConnectionFailed")
    default:
        t.Error("expected warning event for connection failure")
    }
}

func TestMachineReconciler_MachineNotReachable_Timeout(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))

    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "timeout-machine",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.MachineSpec{
            Host:    "192.168.1.200",
            SSHUser: "root",
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(machine).
        WithStatusSubresource(machine).
        Build()

    mockSSH := &ssh.MockClient{
        ConnectFunc: func(ctx context.Context, host, user string, auth ssh.AuthMethod) (ssh.Connection, error) {
            // Simulate timeout
            return nil, context.DeadlineExceeded
        },
    }

    reconciler := &MachineReconciler{
        Client:    fakeClient,
        Scheme:    scheme,
        SSHClient: mockSSH,
        Recorder:  record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "timeout-machine", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.Machine
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "timeout-machine", Namespace: "default"}, &updated))

    cond := findCondition(updated.Status.Conditions, ConditionDiscoverable)
    require.NotNil(t, cond)
    assert.Equal(t, metav1.ConditionFalse, cond.Status)
    assert.Equal(t, ReasonSSHFailed, cond.Reason)
    assert.Contains(t, cond.Message, "timeout")
}

func TestMachineReconciler_MachineNotReachable_AuthenticationFailed(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))

    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "auth-fail-machine",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.MachineSpec{
            Host:    "192.168.1.50",
            SSHUser: "root",
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(machine).
        WithStatusSubresource(machine).
        Build()

    mockSSH := &ssh.MockClient{
        ConnectFunc: func(ctx context.Context, host, user string, auth ssh.AuthMethod) (ssh.Connection, error) {
            return nil, errors.New("ssh: handshake failed: ssh: unable to authenticate")
        },
    }

    reconciler := &MachineReconciler{
        Client:    fakeClient,
        Scheme:    scheme,
        SSHClient: mockSSH,
        Recorder:  record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "auth-fail-machine", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.Machine
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "auth-fail-machine", Namespace: "default"}, &updated))

    cond := findCondition(updated.Status.Conditions, ConditionDiscoverable)
    require.NotNil(t, cond)
    assert.Equal(t, metav1.ConditionFalse, cond.Status)
    assert.Equal(t, ReasonSSHFailed, cond.Reason)
    assert.Contains(t, cond.Message, "authenticate")
}

func TestMachineReconciler_MachineNotReachable_DNSResolutionFailed(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))

    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "dns-fail-machine",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.MachineSpec{
            Host:    "nonexistent.invalid",
            SSHUser: "root",
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(machine).
        WithStatusSubresource(machine).
        Build()

    mockSSH := &ssh.MockClient{
        ConnectFunc: func(ctx context.Context, host, user string, auth ssh.AuthMethod) (ssh.Connection, error) {
            return nil, errors.New("dial tcp: lookup nonexistent.invalid: no such host")
        },
    }

    reconciler := &MachineReconciler{
        Client:    fakeClient,
        Scheme:    scheme,
        SSHClient: mockSSH,
        Recorder:  record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "dns-fail-machine", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.Machine
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "dns-fail-machine", Namespace: "default"}, &updated))

    cond := findCondition(updated.Status.Conditions, ConditionDiscoverable)
    require.NotNil(t, cond)
    assert.Equal(t, metav1.ConditionFalse, cond.Status)
    assert.Contains(t, cond.Message, "no such host")
}

// Helper function
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
    for i := range conditions {
        if conditions[i].Type == condType {
            return &conditions[i]
        }
    }
    return nil
}
```

### 24.5 Unit Tests: Configuration Apply Failed

```go
// internal/controller/nixosconfiguration_controller_test.go
package controller

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    batchv1 "k8s.io/api/batch/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/tools/record"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client/fake"

    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
)

func TestNixosConfigurationReconciler_ApplyFailed_NixosBuildError(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "test-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
                JobName:   "test-config-apply-abc12",
            },
        },
    }

    // Job that has failed
    failedJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "test-config-apply-abc12",
            Namespace: "default",
            Labels: map[string]string{
                "nio.homystack.com/config": "test-config",
            },
        },
        Status: batchv1.JobStatus{
            Failed:     1,
            Succeeded:  0,
            Active:     0,
            Conditions: []batchv1.JobCondition{
                {
                    Type:    batchv1.JobFailed,
                    Status:  corev1.ConditionTrue,
                    Reason:  "BackoffLimitExceeded",
                    Message: "Job has reached the specified backoff limit",
                },
            },
        },
    }

    // Pod with failure logs
    failedPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "test-config-apply-abc12-xyz",
            Namespace: "default",
            Labels: map[string]string{
                "job-name": "test-config-apply-abc12",
            },
        },
        Status: corev1.PodStatus{
            Phase: corev1.PodFailed,
            ContainerStatuses: []corev1.ContainerStatus{
                {
                    Name: "nixos-apply",
                    State: corev1.ContainerState{
                        Terminated: &corev1.ContainerStateTerminated{
                            ExitCode: 1,
                            Reason:   "Error",
                            Message:  "error: builder for '/nix/store/...-nixos-system.drv' failed",
                        },
                    },
                },
            },
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, failedJob, failedPod).
        WithStatusSubresource(config).
        Build()

    recorder := record.NewFakeRecorder(10)

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: recorder,
    }

    // Act
    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "test-config", Namespace: "default"},
    })

    // Assert
    require.NoError(t, err)

    // Should requeue with backoff for retry
    assert.True(t, result.RequeueAfter > 0)
    assert.GreaterOrEqual(t, result.RequeueAfter, 30*time.Second) // Backoff should be significant

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "test-config", Namespace: "default"}, &updated))

    // OperationState should be cleared after failure
    assert.Nil(t, updated.Status.OperationState)

    // Ready condition should be False
    readyCond := findCondition(updated.Status.Conditions, ConditionReady)
    require.NotNil(t, readyCond)
    assert.Equal(t, metav1.ConditionFalse, readyCond.Status)
    assert.Equal(t, ReasonApplyFailed, readyCond.Reason)

    // Stalled condition should be True
    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Equal(t, metav1.ConditionTrue, stalledCond.Status)
    assert.Contains(t, stalledCond.Message, "builder")

    // Check warning event
    select {
    case event := <-recorder.Events:
        assert.Contains(t, event, "Warning")
        assert.Contains(t, event, "ApplyFailed")
    default:
        t.Error("expected ApplyFailed event")
    }
}

func TestNixosConfigurationReconciler_ApplyFailed_GitCloneError(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "git-fail-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/nonexistent/repo.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
                JobName:   "git-fail-config-apply-def34",
            },
        },
    }

    failedJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "git-fail-config-apply-def34",
            Namespace: "default",
            Labels:    map[string]string{"nio.homystack.com/config": "git-fail-config"},
        },
        Status: batchv1.JobStatus{Failed: 1},
    }

    failedPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "git-fail-config-apply-def34-pod",
            Namespace: "default",
            Labels:    map[string]string{"job-name": "git-fail-config-apply-def34"},
        },
        Status: corev1.PodStatus{
            Phase: corev1.PodFailed,
            ContainerStatuses: []corev1.ContainerStatus{{
                Name: "nixos-apply",
                State: corev1.ContainerState{
                    Terminated: &corev1.ContainerStateTerminated{
                        ExitCode: 128,
                        Message:  "fatal: repository 'https://github.com/nonexistent/repo.git' not found",
                    },
                },
            }},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, failedJob, failedPod).
        WithStatusSubresource(config).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "git-fail-config", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "git-fail-config", Namespace: "default"}, &updated))

    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Equal(t, metav1.ConditionTrue, stalledCond.Status)
    assert.Contains(t, stalledCond.Message, "repository")
}

func TestNixosConfigurationReconciler_ApplyFailed_SSHConnectionLost(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "ssh-lost-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
                JobName:   "ssh-lost-config-apply-ghi56",
            },
        },
    }

    failedJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "ssh-lost-config-apply-ghi56",
            Namespace: "default",
            Labels:    map[string]string{"nio.homystack.com/config": "ssh-lost-config"},
        },
        Status: batchv1.JobStatus{Failed: 1},
    }

    failedPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "ssh-lost-config-apply-ghi56-pod",
            Namespace: "default",
            Labels:    map[string]string{"job-name": "ssh-lost-config-apply-ghi56"},
        },
        Status: corev1.PodStatus{
            Phase: corev1.PodFailed,
            ContainerStatuses: []corev1.ContainerStatus{{
                Name: "nixos-apply",
                State: corev1.ContainerState{
                    Terminated: &corev1.ContainerStateTerminated{
                        ExitCode: 255,
                        Message:  "ssh: connect to host 192.168.1.100 port 22: Connection timed out",
                    },
                },
            }},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, failedJob, failedPod).
        WithStatusSubresource(config).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "ssh-lost-config", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "ssh-lost-config", Namespace: "default"}, &updated))

    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Contains(t, stalledCond.Message, "Connection timed out")
}

func TestNixosConfigurationReconciler_ApplyFailed_Timeout(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "timeout-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-35 * time.Minute)),
                JobName:   "timeout-config-apply-jkl78",
            },
        },
    }

    // Job failed due to ActiveDeadlineSeconds
    failedJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "timeout-config-apply-jkl78",
            Namespace: "default",
            Labels:    map[string]string{"nio.homystack.com/config": "timeout-config"},
        },
        Status: batchv1.JobStatus{
            Failed: 1,
            Conditions: []batchv1.JobCondition{{
                Type:    batchv1.JobFailed,
                Status:  corev1.ConditionTrue,
                Reason:  "DeadlineExceeded",
                Message: "Job was active longer than specified deadline",
            }},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, failedJob).
        WithStatusSubresource(config).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "timeout-config", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "timeout-config", Namespace: "default"}, &updated))

    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Contains(t, stalledCond.Message, "deadline")
}
```

### 24.6 Unit Tests: Job Cannot Start (Scheduler Issues)

```go
func TestNixosConfigurationReconciler_JobPending_InsufficientResources(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "resource-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-10 * time.Minute)), // 10 min ago
                JobName:   "resource-config-apply-mno90",
            },
        },
    }

    // Job exists but pod cannot be scheduled
    pendingJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "resource-config-apply-mno90",
            Namespace: "default",
            Labels:    map[string]string{"nio.homystack.com/config": "resource-config"},
        },
        Status: batchv1.JobStatus{
            Active:    0, // No active pods
            Succeeded: 0,
            Failed:    0,
        },
    }

    // Pod stuck in Pending with scheduling error
    pendingPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "resource-config-apply-mno90-pod",
            Namespace: "default",
            Labels:    map[string]string{"job-name": "resource-config-apply-mno90"},
        },
        Status: corev1.PodStatus{
            Phase: corev1.PodPending,
            Conditions: []corev1.PodCondition{{
                Type:    corev1.PodScheduled,
                Status:  corev1.ConditionFalse,
                Reason:  "Unschedulable",
                Message: "0/3 nodes are available: 3 Insufficient memory.",
            }},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, pendingJob, pendingPod).
        WithStatusSubresource(config).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "resource-config", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "resource-config", Namespace: "default"}, &updated))

    // OperationState should still exist (job not finished)
    require.NotNil(t, updated.Status.OperationState)
    assert.Equal(t, "Pending", updated.Status.OperationState.Phase)

    // Stalled because stuck in pending for > 5 minutes
    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Equal(t, metav1.ConditionTrue, stalledCond.Status)
    assert.Equal(t, "JobPending", stalledCond.Reason)
    assert.Contains(t, stalledCond.Message, "Insufficient memory")
}

func TestNixosConfigurationReconciler_JobPending_ImagePullError(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "image-pull-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-6 * time.Minute)),
                JobName:   "image-pull-config-apply-pqr12",
            },
        },
    }

    pendingJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "image-pull-config-apply-pqr12",
            Namespace: "default",
            Labels:    map[string]string{"nio.homystack.com/config": "image-pull-config"},
        },
        Status: batchv1.JobStatus{Active: 0},
    }

    // Pod stuck with ImagePullBackOff
    pendingPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "image-pull-config-apply-pqr12-pod",
            Namespace: "default",
            Labels:    map[string]string{"job-name": "image-pull-config-apply-pqr12"},
        },
        Status: corev1.PodStatus{
            Phase: corev1.PodPending,
            ContainerStatuses: []corev1.ContainerStatus{{
                Name: "nixos-apply",
                State: corev1.ContainerState{
                    Waiting: &corev1.ContainerStateWaiting{
                        Reason:  "ImagePullBackOff",
                        Message: "Back-off pulling image \"ghcr.io/homystack/nixos-operator:v0.0.1\"",
                    },
                },
            }},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, pendingJob, pendingPod).
        WithStatusSubresource(config).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "image-pull-config", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "image-pull-config", Namespace: "default"}, &updated))

    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Equal(t, metav1.ConditionTrue, stalledCond.Status)
    assert.Contains(t, stalledCond.Message, "ImagePullBackOff")
}

func TestNixosConfigurationReconciler_JobPending_SecretNotFound(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "secret-missing-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
                JobName:   "secret-missing-config-apply-stu34",
            },
        },
    }

    pendingJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "secret-missing-config-apply-stu34",
            Namespace: "default",
            Labels:    map[string]string{"nio.homystack.com/config": "secret-missing-config"},
        },
        Status: batchv1.JobStatus{Active: 0},
    }

    // Pod cannot start because secret volume mount fails
    pendingPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "secret-missing-config-apply-stu34-pod",
            Namespace: "default",
            Labels:    map[string]string{"job-name": "secret-missing-config-apply-stu34"},
        },
        Status: corev1.PodStatus{
            Phase: corev1.PodPending,
            ContainerStatuses: []corev1.ContainerStatus{{
                Name: "nixos-apply",
                State: corev1.ContainerState{
                    Waiting: &corev1.ContainerStateWaiting{
                        Reason:  "CreateContainerConfigError",
                        Message: "secret \"ssh-key\" not found",
                    },
                },
            }},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, pendingJob, pendingPod).
        WithStatusSubresource(config).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "secret-missing-config", Namespace: "default"},
    })

    require.NoError(t, err)

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "secret-missing-config", Namespace: "default"}, &updated))

    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Contains(t, stalledCond.Message, "secret")
    assert.Contains(t, stalledCond.Message, "not found")
}

func TestNixosConfigurationReconciler_JobPending_NodeSelectorMismatch(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "nodeselector-config",
            Namespace:  "default",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:      "NixosRebuild",
                StartedAt: metav1.NewTime(time.Now().Add(-8 * time.Minute)),
                JobName:   "nodeselector-config-apply-vwx56",
            },
        },
    }

    pendingJob := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "nodeselector-config-apply-vwx56",
            Namespace: "default",
            Labels:    map[string]string{"nio.homystack.com/config": "nodeselector-config"},
        },
        Status: batchv1.JobStatus{Active: 0},
    }

    pendingPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "nodeselector-config-apply-vwx56-pod",
            Namespace: "default",
            Labels:    map[string]string{"job-name": "nodeselector-config-apply-vwx56"},
        },
        Status: corev1.PodStatus{
            Phase: corev1.PodPending,
            Conditions: []corev1.PodCondition{{
                Type:    corev1.PodScheduled,
                Status:  corev1.ConditionFalse,
                Reason:  "Unschedulable",
                Message: "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.",
            }},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, pendingJob, pendingPod).
        WithStatusSubresource(config).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "nodeselector-config", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0)

    var updated niov1alpha1.NixosConfiguration
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "nodeselector-config", Namespace: "default"}, &updated))

    stalledCond := findCondition(updated.Status.Conditions, ConditionStalled)
    require.NotNil(t, stalledCond)
    assert.Contains(t, stalledCond.Message, "node affinity")
}
```

### 24.7 Test Helpers and Fixtures

```go
// internal/controller/testutil/fixtures.go
package testutil

import (
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
)

// NewMachine creates a Machine for testing
func NewMachine(name, namespace string, opts ...MachineOption) *niov1alpha1.Machine {
    m := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:       name,
            Namespace:  namespace,
            Generation: 1,
        },
        Spec: niov1alpha1.MachineSpec{
            Host:    "192.168.1.100",
            SSHUser: "root",
        },
    }
    for _, opt := range opts {
        opt(m)
    }
    return m
}

type MachineOption func(*niov1alpha1.Machine)

func WithSSHKeySecret(name string) MachineOption {
    return func(m *niov1alpha1.Machine) {
        m.Spec.SSHKeySecretRef = &niov1alpha1.SecretReference{Name: name}
    }
}

func WithHost(host string) MachineOption {
    return func(m *niov1alpha1.Machine) {
        m.Spec.Host = host
    }
}

// NewNixosConfiguration creates a NixosConfiguration for testing
func NewNixosConfiguration(name, namespace, machineName string, opts ...ConfigOption) *niov1alpha1.NixosConfiguration {
    c := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       name,
            Namespace:  namespace,
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: machineName},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Ref:        "main",
            Flake:      "#worker",
        },
    }
    for _, opt := range opts {
        opt(c)
    }
    return c
}

type ConfigOption func(*niov1alpha1.NixosConfiguration)

func WithFullInstall(enabled bool) ConfigOption {
    return func(c *niov1alpha1.NixosConfiguration) {
        c.Spec.FullInstall = enabled
    }
}

func WithOperationInProgress(opType, jobName string) ConfigOption {
    return func(c *niov1alpha1.NixosConfiguration) {
        c.Status.OperationState = &niov1alpha1.OperationState{
            Type:      opType,
            StartedAt: metav1.Now(),
            JobName:   jobName,
        }
    }
}
```

### 24.8 Running Tests

```bash
# Run all unit tests
go test ./internal/controller/... -v

# Run specific test
go test ./internal/controller/... -v -run TestMachineReconciler_MachineNotReachable

# Run with coverage
go test ./internal/controller/... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html

# Run integration tests (requires envtest binaries)
KUBEBUILDER_ASSETS=$(setup-envtest use -p path) go test ./internal/controller/... -v -tags=integration
```

### 24.9 Test Coverage Requirements

| Component | Min Coverage | Critical Paths |
|-----------|-------------|----------------|
| MachineReconciler | 80% | SSH connection, status updates |
| NixosConfigurationReconciler | 80% | Job lifecycle, error handling |
| SSH Client | 70% | Connection, execution |
| Git Operations | 70% | Clone, checkout |

### 24.10 Implementation Checklist

- [ ] Setup test suite with envtest (`suite_test.go`)
- [ ] Create SSH mock interface and implementation
- [ ] Write unit tests: Machine not reachable (connection refused)
- [ ] Write unit tests: Machine not reachable (timeout)
- [ ] Write unit tests: Machine not reachable (auth failed)
- [ ] Write unit tests: Machine not reachable (DNS failed)
- [ ] Write unit tests: Apply failed (nix build error)
- [ ] Write unit tests: Apply failed (git clone error)
- [ ] Write unit tests: Apply failed (SSH lost during apply)
- [ ] Write unit tests: Apply failed (timeout/deadline)
- [ ] Write unit tests: Job pending (insufficient resources)
- [ ] Write unit tests: Job pending (image pull error)
- [ ] Write unit tests: Job pending (secret not found)
- [ ] Write unit tests: Job pending (node selector mismatch)
- [ ] Create test fixtures and helpers
- [ ] Add integration tests with envtest
- [ ] Setup CI pipeline to run tests
- [ ] Add coverage reporting to CI

## 25. Owner References and Garbage Collection

### 25.1 Resource Relationships

```
┌─────────────────────────────────────────────────────────────┐
│                        Cluster                               │
│                                                              │
│  ┌──────────────┐         ┌─────────────────────────┐       │
│  │   Machine    │◄────────│   NixosConfiguration    │       │
│  │              │  refs   │                         │       │
│  └──────────────┘         └───────────┬─────────────┘       │
│         │                             │                      │
│         │ reads                       │ owns                 │
│         ▼                             ▼                      │
│  ┌──────────────┐         ┌─────────────────────────┐       │
│  │    Secret    │         │          Job            │       │
│  │  (SSH key)   │         │    (apply operation)    │       │
│  └──────────────┘         └─────────────────────────┘       │
│                                                              │
└─────────────────────────────────────────────────────────────┘

Legend:
  ────► owns (owner reference, garbage collected)
  ----► refs (soft reference, no GC)
```

### 25.2 Ownership Model

| Owner | Owned Resource | Relationship | GC Behavior |
|-------|---------------|--------------|-------------|
| NixosConfiguration | Job | Owner Reference | Jobs deleted when config deleted |
| Machine | - | None | Machines are top-level |
| NixosConfiguration | Machine | Soft Reference | Machine NOT deleted with config |

**Key principle:** NixosConfiguration references Machine but does NOT own it. Multiple configs could reference the same machine, and deleting one config should not affect others.

### 25.3 Setting Owner References

```go
// internal/controller/nixosconfiguration_controller.go

import (
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *NixosConfigurationReconciler) createApplyJob(
    ctx context.Context,
    config *niov1alpha1.NixosConfiguration,
    opType string,
) (*batchv1.Job, error) {
    job := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-apply-%s", config.Name, randomSuffix(5)),
            Namespace: config.Namespace,
            Labels: map[string]string{
                "app.kubernetes.io/name":      "nixos-operator",
                "app.kubernetes.io/component": "apply-job",
                "app.kubernetes.io/instance":  config.Name,
                "nio.homystack.com/config":    config.Name,
                "nio.homystack.com/machine":   config.Spec.MachineRef.Name,
                "nio.homystack.com/operation": opType,
            },
        },
        Spec: batchv1.JobSpec{
            // ... job spec ...
        },
    }

    // Set NixosConfiguration as owner of the Job
    // This ensures:
    // 1. Job is deleted when NixosConfiguration is deleted
    // 2. Job changes trigger NixosConfiguration reconciliation (via Owns())
    if err := ctrl.SetControllerReference(config, job, r.Scheme); err != nil {
        return nil, fmt.Errorf("set controller reference: %w", err)
    }

    if err := r.Create(ctx, job); err != nil {
        return nil, fmt.Errorf("create job: %w", err)
    }

    return job, nil
}
```

### 25.4 Owner Reference Structure

```yaml
# Job created by NixosConfiguration
apiVersion: batch/v1
kind: Job
metadata:
  name: worker-01-config-apply-abc12
  namespace: default
  ownerReferences:
    - apiVersion: nio.homystack.com/v1alpha1
      kind: NixosConfiguration
      name: worker-01-config
      uid: 12345678-1234-1234-1234-123456789abc
      controller: true        # This controller manages the Job
      blockOwnerDeletion: true  # Wait for Job to be deleted before owner
```

### 25.5 Garbage Collection Policies

```go
// Different deletion propagation policies

// Foreground: Wait for dependents to be deleted first
// Use when: You need to ensure Jobs complete or are cleaned up
err := r.Delete(ctx, config, client.PropagationPolicy(metav1.DeletePropagationForeground))

// Background: Delete owner immediately, GC cleans up dependents async
// Use when: Quick deletion, don't care about dependent cleanup timing
err := r.Delete(ctx, config, client.PropagationPolicy(metav1.DeletePropagationBackground))

// Orphan: Delete owner, leave dependents running
// Use when: You want Jobs to finish even after config is deleted
err := r.Delete(ctx, config, client.PropagationPolicy(metav1.DeletePropagationOrphan))
```

### 25.6 Handling NixosConfiguration Deletion

```go
const finalizerName = "nio.homystack.com/finalizer"

func (r *NixosConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    var config niov1alpha1.NixosConfiguration
    if err := r.Get(ctx, req.NamespacedName, &config); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Handle deletion
    if !config.DeletionTimestamp.IsZero() {
        return r.handleDeletion(ctx, &config)
    }

    // Add finalizer if not present
    if !controllerutil.ContainsFinalizer(&config, finalizerName) {
        log.Info("Adding finalizer")
        controllerutil.AddFinalizer(&config, finalizerName)
        if err := r.Update(ctx, &config); err != nil {
            return ctrl.Result{}, err
        }
        // Requeue to continue with reconciliation
        return ctrl.Result{Requeue: true}, nil
    }

    // Normal reconciliation...
    return r.reconcile(ctx, &config)
}

func (r *NixosConfigurationReconciler) handleDeletion(ctx context.Context, config *niov1alpha1.NixosConfiguration) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    if !controllerutil.ContainsFinalizer(config, finalizerName) {
        // Finalizer already removed, nothing to do
        return ctrl.Result{}, nil
    }

    // Step 1: Cancel any in-progress operation
    if config.Status.OperationState != nil {
        log.Info("Cancelling in-progress operation", "job", config.Status.OperationState.JobName)
        if err := r.cancelOperation(ctx, config); err != nil {
            log.Error(err, "Failed to cancel operation, continuing with deletion")
        }
    }

    // Step 2: Apply onRemoveFlake if specified
    if config.Spec.OnRemoveFlake != "" && config.Status.FullDiskInstallCompleted {
        log.Info("Applying removal configuration", "flake", config.Spec.OnRemoveFlake)

        // Check if removal already done (idempotency)
        if !r.isRemovalApplied(config) {
            result, err := r.applyRemovalConfiguration(ctx, config)
            if err != nil {
                // Set condition and emit event, but keep trying
                meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
                    Type:               "RemovalApplied",
                    Status:             metav1.ConditionFalse,
                    ObservedGeneration: config.Generation,
                    Reason:             "RemovalFailed",
                    Message:            err.Error(),
                })
                r.Status().Update(ctx, config)

                // Emit warning event so user knows removal is failing
                r.Recorder.Eventf(config, corev1.EventTypeWarning, "RemovalFailed",
                    "Failed to apply onRemoveFlake %s: %v. Will keep retrying.",
                    config.Spec.OnRemoveFlake, err)

                // Keep retrying - onRemoveFlake runs as a Job, so we just requeue
                return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
            } else if result.RequeueAfter > 0 {
                // Removal Job in progress, wait for it
                return result, nil
            }
        }
    }

    // Step 3: Update Machine status (clear applied configuration)
    if err := r.clearMachineStatus(ctx, config); err != nil {
        log.Error(err, "Failed to clear Machine status")
        // Don't block deletion for this
    }

    // Step 4: Remove finalizer
    log.Info("Removing finalizer")
    controllerutil.RemoveFinalizer(config, finalizerName)
    if err := r.Update(ctx, config); err != nil {
        return ctrl.Result{}, err
    }

    // Jobs will be garbage collected automatically due to owner references

    r.Recorder.Event(config, corev1.EventTypeNormal, "Deleted", "Configuration deleted successfully")

    return ctrl.Result{}, nil
}

func (r *NixosConfigurationReconciler) cancelOperation(ctx context.Context, config *niov1alpha1.NixosConfiguration) error {
    if config.Status.OperationState == nil {
        return nil
    }

    // Delete the Job - this will also terminate the Pod
    job := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      config.Status.OperationState.JobName,
            Namespace: config.Namespace,
        },
    }

    // Use Foreground propagation to wait for Pod termination
    return client.IgnoreNotFound(r.Delete(ctx, job,
        client.PropagationPolicy(metav1.DeletePropagationForeground)))
}

func (r *NixosConfigurationReconciler) clearMachineStatus(ctx context.Context, config *niov1alpha1.NixosConfiguration) error {
    var machine niov1alpha1.Machine
    if err := r.Get(ctx, types.NamespacedName{
        Name:      config.Spec.MachineRef.Name,
        Namespace: config.Namespace,
    }, &machine); err != nil {
        return client.IgnoreNotFound(err)
    }

    // Only clear if this config was the applied one
    if machine.Status.AppliedConfiguration != config.Name {
        return nil
    }

    machine.Status.HasConfiguration = false
    machine.Status.AppliedConfiguration = ""
    machine.Status.AppliedCommit = ""

    return r.Status().Update(ctx, &machine)
}
```

### 25.7 Machine Deletion Handling

Machine deletion is more complex because NixosConfiguration references it:

```go
func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    var machine niov1alpha1.Machine
    if err := r.Get(ctx, req.NamespacedName, &machine); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    // Handle deletion
    if !machine.DeletionTimestamp.IsZero() {
        return r.handleDeletion(ctx, &machine)
    }

    // Add finalizer
    if !controllerutil.ContainsFinalizer(&machine, finalizerName) {
        controllerutil.AddFinalizer(&machine, finalizerName)
        if err := r.Update(ctx, &machine); err != nil {
            return ctrl.Result{}, err
        }
        return ctrl.Result{Requeue: true}, nil
    }

    return r.reconcile(ctx, &machine)
}

func (r *MachineReconciler) handleDeletion(ctx context.Context, machine *niov1alpha1.Machine) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    if !controllerutil.ContainsFinalizer(machine, finalizerName) {
        return ctrl.Result{}, nil
    }

    // Check for NixosConfigurations referencing this Machine
    var configs niov1alpha1.NixosConfigurationList
    if err := r.List(ctx, &configs, client.InNamespace(machine.Namespace)); err != nil {
        return ctrl.Result{}, err
    }

    var referencingConfigs []string
    for _, c := range configs.Items {
        if c.Spec.MachineRef.Name == machine.Name {
            referencingConfigs = append(referencingConfigs, c.Name)
        }
    }

    if len(referencingConfigs) > 0 {
        // Block deletion until configs are removed
        log.Info("Machine has referencing NixosConfigurations, blocking deletion",
            "configs", referencingConfigs)

        meta.SetStatusCondition(&machine.Status.Conditions, metav1.Condition{
            Type:               "DeletionBlocked",
            Status:             metav1.ConditionTrue,
            ObservedGeneration: machine.Generation,
            Reason:             "HasDependents",
            Message:            fmt.Sprintf("Cannot delete: referenced by NixosConfigurations: %v", referencingConfigs),
        })
        r.Status().Update(ctx, machine)

        r.Recorder.Eventf(machine, corev1.EventTypeWarning, "DeletionBlocked",
            "Cannot delete Machine: referenced by %d NixosConfiguration(s)", len(referencingConfigs))

        // Requeue to check again later
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    // No references, safe to delete
    log.Info("Removing finalizer")
    controllerutil.RemoveFinalizer(machine, finalizerName)
    if err := r.Update(ctx, machine); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{}, nil
}
```

### 25.8 Cross-Namespace References

**Design Decision:** Machine and NixosConfiguration must be in the same namespace.

Cross-namespace references are NOT supported because:
1. Simplifies RBAC (no need for cross-namespace permissions)
2. Jobs use projected volumes which only work within same namespace
3. Owner references don't work cross-namespace
4. Easier to reason about resource relationships

```go
// MachineReference is a same-namespace reference to a Machine resource.
type MachineReference struct {
    // Name of the Machine resource (must be in the same namespace).
    Name string `json:"name"`
}

func (r *NixosConfigurationReconciler) getMachine(ctx context.Context, config *niov1alpha1.NixosConfiguration) (*niov1alpha1.Machine, error) {
    var machine niov1alpha1.Machine
    if err := r.Get(ctx, types.NamespacedName{
        Name:      config.Spec.MachineRef.Name,
        Namespace: config.Namespace,  // Always same namespace
    }, &machine); err != nil {
        return nil, err
    }

    return &machine, nil
}
```

**Note:** Cross-namespace owner references are NOT allowed by Kubernetes. Jobs must be in the same namespace as NixosConfiguration.

### 25.9 Preventing Orphaned Resources

```go
// Periodic cleanup of orphaned Jobs (edge cases)
func (r *NixosConfigurationReconciler) cleanupOrphanedJobs(ctx context.Context) error {
    log := log.FromContext(ctx)

    var jobs batchv1.JobList
    if err := r.List(ctx, &jobs,
        client.MatchingLabels{
            "app.kubernetes.io/name":      "nixos-operator",
            "app.kubernetes.io/component": "apply-job",
        },
    ); err != nil {
        return err
    }

    for _, job := range jobs.Items {
        // Jobs should have owner references
        if len(job.OwnerReferences) > 0 {
            continue
        }

        // Orphaned job found - check age before deleting
        age := time.Since(job.CreationTimestamp.Time)
        if age < 1*time.Hour {
            // Give time for owner reference to be set
            continue
        }

        log.Info("Deleting orphaned Job", "job", job.Name, "namespace", job.Namespace, "age", age)

        if err := r.Delete(ctx, &job,
            client.PropagationPolicy(metav1.DeletePropagationBackground),
        ); err != nil && !apierrors.IsNotFound(err) {
            log.Error(err, "Failed to delete orphaned Job", "job", job.Name)
        }
    }

    return nil
}

// Run cleanup periodically via manager
func (r *NixosConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
    // Start periodic cleanup
    if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
        ticker := time.NewTicker(1 * time.Hour)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                return nil
            case <-ticker.C:
                if err := r.cleanupOrphanedJobs(ctx); err != nil {
                    log.FromContext(ctx).Error(err, "Failed to cleanup orphaned jobs")
                }
            }
        }
    })); err != nil {
        return err
    }

    return ctrl.NewControllerManagedBy(mgr).
        For(&niov1alpha1.NixosConfiguration{}).
        Owns(&batchv1.Job{}).
        // ... other watches ...
        Complete(r)
}
```

### 25.10 Unit Tests for Owner References

```go
func TestNixosConfigurationReconciler_JobOwnerReference(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))
    require.NoError(t, corev1.AddToScheme(scheme))

    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:       "test-config",
            Namespace:  "default",
            UID:        "config-uid-12345",
            Generation: 1,
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
            GitRepo:    "https://github.com/example/nixos-config.git",
            Flake:      "#worker",
        },
    }

    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "test-machine",
            Namespace: "default",
        },
        Status: niov1alpha1.MachineStatus{
            Discoverable: true,
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, machine).
        WithStatusSubresource(config, machine).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
        JobImage: "ghcr.io/homystack/nixos-operator:latest",
    }

    // Trigger reconciliation
    _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "test-config", Namespace: "default"},
    })
    require.NoError(t, err)

    // Find created Job
    var jobs batchv1.JobList
    require.NoError(t, fakeClient.List(context.Background(), &jobs,
        client.InNamespace("default"),
        client.MatchingLabels{"nio.homystack.com/config": "test-config"},
    ))

    require.Len(t, jobs.Items, 1, "expected exactly one Job")
    job := jobs.Items[0]

    // Verify owner reference
    require.Len(t, job.OwnerReferences, 1, "Job should have one owner reference")

    ownerRef := job.OwnerReferences[0]
    assert.Equal(t, "NixosConfiguration", ownerRef.Kind)
    assert.Equal(t, "test-config", ownerRef.Name)
    assert.Equal(t, types.UID("config-uid-12345"), ownerRef.UID)
    assert.True(t, *ownerRef.Controller, "should be controller reference")
    assert.True(t, *ownerRef.BlockOwnerDeletion, "should block owner deletion")
}

func TestMachineReconciler_DeletionBlockedByConfig(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))

    now := metav1.Now()
    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:              "test-machine",
            Namespace:         "default",
            DeletionTimestamp: &now,
            Finalizers:        []string{finalizerName},
        },
        Spec: niov1alpha1.MachineSpec{
            Host: "test.example.com",
        },
    }

    // Config referencing this machine
    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "test-config",
            Namespace: "default",
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(machine, config).
        WithStatusSubresource(machine).
        Build()

    reconciler := &MachineReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "test-machine", Namespace: "default"},
    })

    require.NoError(t, err)
    assert.True(t, result.RequeueAfter > 0, "should requeue while blocked")

    // Machine should still have finalizer (deletion blocked)
    var updated niov1alpha1.Machine
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "test-machine", Namespace: "default"}, &updated))

    assert.Contains(t, updated.Finalizers, finalizerName)

    // Should have blocking condition
    cond := findCondition(updated.Status.Conditions, "DeletionBlocked")
    require.NotNil(t, cond)
    assert.Equal(t, metav1.ConditionTrue, cond.Status)
    assert.Contains(t, cond.Message, "test-config")
}

func TestNixosConfigurationReconciler_DeletionClearsOperation(t *testing.T) {
    scheme := runtime.NewScheme()
    require.NoError(t, niov1alpha1.AddToScheme(scheme))
    require.NoError(t, batchv1.AddToScheme(scheme))

    now := metav1.Now()
    config := &niov1alpha1.NixosConfiguration{
        ObjectMeta: metav1.ObjectMeta{
            Name:              "test-config",
            Namespace:         "default",
            DeletionTimestamp: &now,
            Finalizers:        []string{finalizerName},
        },
        Spec: niov1alpha1.NixosConfigurationSpec{
            MachineRef: niov1alpha1.MachineReference{Name: "test-machine"},
        },
        Status: niov1alpha1.NixosConfigurationStatus{
            OperationState: &niov1alpha1.OperationState{
                Type:    "NixosRebuild",
                JobName: "test-config-apply-abc12",
            },
        },
    }

    // Running Job that should be deleted
    job := &batchv1.Job{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "test-config-apply-abc12",
            Namespace: "default",
            OwnerReferences: []metav1.OwnerReference{{
                APIVersion: "nio.homystack.com/v1alpha1",
                Kind:       "NixosConfiguration",
                Name:       "test-config",
            }},
        },
        Status: batchv1.JobStatus{Active: 1},
    }

    machine := &niov1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "test-machine",
            Namespace: "default",
        },
        Status: niov1alpha1.MachineStatus{
            HasConfiguration:     true,
            AppliedConfiguration: "test-config",
        },
    }

    fakeClient := fake.NewClientBuilder().
        WithScheme(scheme).
        WithObjects(config, job, machine).
        WithStatusSubresource(config, machine).
        Build()

    reconciler := &NixosConfigurationReconciler{
        Client:   fakeClient,
        Scheme:   scheme,
        Recorder: record.NewFakeRecorder(10),
    }

    _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
        NamespacedName: types.NamespacedName{Name: "test-config", Namespace: "default"},
    })
    require.NoError(t, err)

    // Job should be deleted
    var updatedJob batchv1.Job
    err = fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "test-config-apply-abc12", Namespace: "default"}, &updatedJob)
    assert.True(t, apierrors.IsNotFound(err), "Job should be deleted")

    // Machine status should be cleared
    var updatedMachine niov1alpha1.Machine
    require.NoError(t, fakeClient.Get(context.Background(),
        types.NamespacedName{Name: "test-machine", Namespace: "default"}, &updatedMachine))

    assert.False(t, updatedMachine.Status.HasConfiguration)
    assert.Empty(t, updatedMachine.Status.AppliedConfiguration)
}
```

### 25.11 Implementation Checklist

- [ ] Add finalizer constant and logic to NixosConfigurationReconciler
- [ ] Add finalizer constant and logic to MachineReconciler
- [ ] Implement `ctrl.SetControllerReference()` for Jobs
- [ ] Implement `handleDeletion()` for NixosConfiguration
- [ ] Implement `handleDeletion()` for Machine (check referencing configs)
- [ ] Implement `cancelOperation()` to delete running Jobs
- [ ] Implement `clearMachineStatus()` on config deletion
- [ ] Implement `applyRemovalConfiguration()` for onRemoveFlake
- [ ] Add periodic `cleanupOrphanedJobs()` runnable
- [ ] Add unit tests for owner references
- [ ] Add unit tests for deletion blocking
- [ ] Add unit tests for cascading deletion
- [ ] Add integration tests for garbage collection behavior

## 26. State Machines and Lifecycle

### 26.1 Machine State Machine

```
                                    ┌─────────────────────────────────────────┐
                                    │                                         │
                                    ▼                                         │
┌─────────┐    create    ┌──────────────────┐    SSH OK    ┌──────────────┐   │
│         │─────────────►│                  │─────────────►│              │   │
│ (none)  │              │  Undiscoverable  │              │ Discoverable │   │
│         │              │                  │◄─────────────│              │   │
└─────────┘              └──────────────────┘   SSH fail   └──────────────┘   │
                                │                                │            │
                                │                                │            │
                                │ delete                         │ delete     │
                                │ (no configs)                   │ (no cfgs)  │
                                ▼                                ▼            │
                         ┌──────────────┐                 ┌──────────────┐    │
                         │   Deleting   │                 │   Deleting   │    │
                         │  (finalize)  │                 │  (finalize)  │    │
                         └──────┬───────┘                 └──────┬───────┘    │
                                │                                │            │
                                │ finalizer                      │ finalizer  │
                                │ removed                        │ removed    │
                                ▼                                ▼            │
                         ┌──────────────┐                 ┌──────────────┐    │
                         │   Deleted    │                 │   Deleted    │    │
                         └──────────────┘                 └──────────────┘    │
                                                                              │
                         ┌──────────────┐                                     │
                         │  Deletion    │◄────────────────────────────────────┘
                         │   Blocked    │  delete (has referencing configs)
                         │              │
                         └──────┬───────┘
                                │
                                │ configs deleted
                                ▼
                         ┌──────────────┐
                         │   Deleting   │
                         └──────────────┘
```

**Machine States (via Conditions):**

| State | Ready | Discoverable | Stalled | DeletionBlocked |
|-------|-------|--------------|---------|-----------------|
| Undiscoverable | False | False | False | - |
| Discoverable | True | True | False | - |
| DeletionBlocked | - | - | - | True |

**Transitions:**

| From | To | Trigger | Action |
|------|-----|---------|--------|
| (none) | Undiscoverable | Machine created | Add finalizer, start SSH check |
| Undiscoverable | Discoverable | SSH connection succeeds | Update status, start hardware scan |
| Discoverable | Undiscoverable | SSH connection fails | Update status, emit event |
| Discoverable | DeletionBlocked | Delete + has configs | Set condition, block finalizer removal |
| DeletionBlocked | Deleting | All configs deleted | Remove finalizer |
| * | Deleting | Delete + no configs | Remove finalizer |

### 26.2 NixosConfiguration State Machine

```
┌─────────┐
│ (none)  │
└────┬────┘
     │ create
     ▼
┌─────────────────┐
│    Pending      │◄──────────────────────────────────────────────┐
│                 │                                                │
│ - Waiting for   │                                                │
│   Machine       │                                                │
└────────┬────────┘                                                │
         │                                                         │
         │ Machine.Discoverable=True                               │
         ▼                                                         │
┌─────────────────┐     Job failed      ┌─────────────────┐       │
│   Reconciling   │────────────────────►│     Stalled     │       │
│                 │                      │                 │       │
│ - Creating Job  │                      │ - Build error   │       │
│ - Job running   │                      │ - Git error     │       │
└────────┬────────┘                      │ - SSH lost      │       │
         │                               └────────┬────────┘       │
         │ Job succeeded                          │                │
         ▼                                        │ spec changed   │
┌─────────────────┐                               │ or retry       │
│     Applied     │◄──────────────────────────────┘                │
│                 │                                                │
│ - Ready=True    │                                                │
│ - Config active │                                                │
└────────┬────────┘                                                │
         │                                                         │
         │ spec changed (git commit, flake, etc.)                  │
         └─────────────────────────────────────────────────────────┘
         │
         │ delete
         ▼
┌─────────────────┐     onRemoveFlake     ┌─────────────────┐
│    Deleting     │──────────────────────►│ ApplyingRemoval │
│                 │      specified        │                 │
│ - Cancel jobs   │                       │ - Job running   │
└────────┬────────┘                       └────────┬────────┘
         │                                         │
         │ no onRemoveFlake                        │ Job done
         │ or removal done                         │
         ▼                                         │
┌─────────────────┐◄───────────────────────────────┘
│ ClearingMachine │
│                 │
│ - Clear Machine │
│   status        │
└────────┬────────┘
         │
         │ Machine updated
         ▼
┌─────────────────┐
│    Deleted      │
│                 │
│ - Finalizer     │
│   removed       │
└─────────────────┘
```

**NixosConfiguration States (via Conditions):**

| State | Ready | Reconciling | Stalled | Applied |
|-------|-------|-------------|---------|---------|
| Pending | False | True | False | False |
| Reconciling | False | True | False | False |
| Applied | True | False | False | True |
| Stalled | False | False | True | False |
| Deleting | - | - | - | - |

**Transitions:**

| From | To | Trigger | Action |
|------|-----|---------|--------|
| (none) | Pending | Config created | Add finalizer, check Machine |
| Pending | Pending | Machine not discoverable | Requeue, wait |
| Pending | Reconciling | Machine discoverable | Create apply Job |
| Reconciling | Applied | Job succeeded | Update status, update Machine |
| Reconciling | Stalled | Job failed | Set error condition |
| Applied | Reconciling | Spec changed | Create new Job |
| Stalled | Reconciling | Spec changed or retry | Create new Job |
| * | Deleting | Delete requested | Cancel Job, start cleanup |
| Deleting | ApplyingRemoval | onRemoveFlake set | Create removal Job |
| ApplyingRemoval | ClearingMachine | Removal Job done | Clear Machine status |
| Deleting | ClearingMachine | No onRemoveFlake | Clear Machine status |
| ClearingMachine | Deleted | Machine cleared | Remove finalizer |

### 26.3 Interaction: NixosConfiguration Created

```
User                    K8s API                NixosConfig           Machine
  │                        │                   Controller           Controller
  │                        │                        │                    │
  │ kubectl apply          │                        │                    │
  │ NixosConfiguration     │                        │                    │
  │───────────────────────►│                        │                    │
  │                        │                        │                    │
  │                        │  Reconcile triggered   │                    │
  │                        │───────────────────────►│                    │
  │                        │                        │                    │
  │                        │                        │ Get Machine        │
  │                        │                        │───────────────────►│
  │                        │                        │◄───────────────────│
  │                        │                        │                    │
  │                        │                        │                    │
  │                        │     ┌──────────────────┴──────────────────┐ │
  │                        │     │ Machine.Discoverable == false?      │ │
  │                        │     └──────────────────┬──────────────────┘ │
  │                        │                        │                    │
  │                        │                    YES │                    │
  │                        │                        ▼                    │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Set conditions: │            │
  │                        │              │ Ready=False     │            │
  │                        │              │ Reason=Machine  │            │
  │                        │              │   NotReady      │            │
  │                        │              │                 │            │
  │                        │              │ RequeueAfter:   │            │
  │                        │              │   30s           │            │
  │                        │              └─────────────────┘            │
  │                        │                        │                    │
  │                        │                        │                    │
  │                        │                     NO │ (Machine ready)    │
  │                        │                        ▼                    │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Check if apply  │            │
  │                        │              │ needed:         │            │
  │                        │              │ - New config    │            │
  │                        │              │ - Commit changed│            │
  │                        │              │ - Spec changed  │            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Check concurrency│           │
  │                        │              │ limit            │           │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       ▼                     │
  │                        │  Create Job  ┌─────────────────┐            │
  │                        │◄─────────────│ Create apply    │            │
  │                        │              │ Job with owner  │            │
  │                        │              │ reference       │            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Update status:  │            │
  │                        │◄─────────────│ OperationState  │            │
  │                        │              │ Reconciling=True│            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       │ RequeueAfter: 10s   │
  │                        │                       ▼                     │
  │                        │                                             │
  │                        │              ... Job executes ...           │
  │                        │                                             │
  │                        │                       │                     │
  │                        │  Job status changed   │                     │
  │                        │───────────────────────►                     │
  │                        │                       │                     │
  │                        │     ┌─────────────────┴───────────────────┐ │
  │                        │     │ Job.Succeeded > 0?                  │ │
  │                        │     └─────────────────┬───────────────────┘ │
  │                        │                       │                     │
  │                        │                   YES │                     │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Update config:  │            │
  │                        │◄─────────────│ AppliedCommit   │            │
  │                        │              │ Ready=True      │            │
  │                        │              │ Applied=True    │            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Update Machine: │            │
  │                        │◄─────────────│ HasConfig=True  │───────────►│
  │                        │              │ AppliedConfig=  │            │
  │                        │              │   this config   │            │
  │                        │              │ AppliedCommit   │            │
  │                        │              └─────────────────┘            │
  │                        │                                             │
```

### 26.4 Interaction: NixosConfiguration Deleted

```
User                    K8s API                NixosConfig           Machine
  │                        │                   Controller           Controller
  │                        │                        │                    │
  │ kubectl delete         │                        │                    │
  │ NixosConfiguration     │                        │                    │
  │───────────────────────►│                        │                    │
  │                        │                        │                    │
  │                        │ DeletionTimestamp set  │                    │
  │                        │───────────────────────►│                    │
  │                        │                        │                    │
  │                        │     ┌──────────────────┴──────────────────┐ │
  │                        │     │ OperationState != nil?              │ │
  │                        │     │ (Job in progress)                   │ │
  │                        │     └──────────────────┬──────────────────┘ │
  │                        │                        │                    │
  │                        │                    YES │                    │
  │                        │                        ▼                    │
  │                        │              ┌─────────────────┐            │
  │                        │  Delete Job  │ Cancel running  │            │
  │                        │◄─────────────│ Job (Foreground │            │
  │                        │              │ propagation)    │            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       │ Wait for Job       │
  │                        │                       │ termination        │
  │                        │                       ▼                     │
  │                        │     ┌──────────────────┴──────────────────┐ │
  │                        │     │ spec.onRemoveFlake != ""?           │ │
  │                        │     └──────────────────┬──────────────────┘ │
  │                        │                        │                    │
  │                        │                    YES │                    │
  │                        │                        ▼                    │
  │                        │              ┌─────────────────┐            │
  │                        │  Create Job  │ Create removal  │            │
  │                        │◄─────────────│ Job with        │            │
  │                        │              │ onRemoveFlake   │            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       │ RequeueAfter: 10s   │
  │                        │                       │                     │
  │                        │              ... Removal Job runs ...       │
  │                        │                       │                     │
  │                        │  Job succeeded        │                     │
  │                        │───────────────────────►                     │
  │                        │                       │                     │
  │                        │                    NO │ (no onRemoveFlake   │
  │                        │                       │  or removal done)   │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Clear Machine   │            │
  │                        │              │ status:         │            │
  │                        │◄─────────────│ HasConfig=False │───────────►│
  │                        │              │ AppliedConfig=""│            │
  │                        │              │ AppliedCommit=""│            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │ Remove       │ Remove finalizer│            │
  │                        │ finalizer    │                 │            │
  │                        │◄─────────────│                 │            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Config deleted  │            │
  │                        │              │ from etcd       │            │
  │                        │              └─────────────────┘            │
  │                        │                                             │
  │                        │                                             │
  │◄───────────────────────│ Deletion confirmed                          │
  │                        │                                             │
```

### 26.5 Interaction: Machine Deleted (Blocked)

```
User                    K8s API                Machine              NixosConfig
  │                        │                   Controller           Controller
  │                        │                        │                    │
  │ kubectl delete         │                        │                    │
  │ Machine                │                        │                    │
  │───────────────────────►│                        │                    │
  │                        │                        │                    │
  │                        │ DeletionTimestamp set  │                    │
  │                        │───────────────────────►│                    │
  │                        │                        │                    │
  │                        │                        │ List NixosConfigs  │
  │                        │                        │ referencing this   │
  │                        │                        │ Machine            │
  │                        │                        │───────────────────►│
  │                        │                        │◄───────────────────│
  │                        │                        │ Found: [config-1]  │
  │                        │                        │                    │
  │                        │              ┌─────────┴─────────┐          │
  │                        │              │ Referencing       │          │
  │                        │              │ configs exist!    │          │
  │                        │              └─────────┬─────────┘          │
  │                        │                        │                    │
  │                        │                        ▼                    │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Set condition:  │            │
  │                        │◄─────────────│ DeletionBlocked │            │
  │                        │              │ =True           │            │
  │                        │              │                 │            │
  │                        │              │ Keep finalizer  │            │
  │                        │              │                 │            │
  │                        │              │ RequeueAfter:   │            │
  │                        │              │   30s           │            │
  │                        │              └─────────────────┘            │
  │                        │                        │                    │
  │                        │                        │                    │
  │◄───────────────────────│ Machine in             │                    │
  │                        │ "Terminating" state    │                    │
  │                        │                        │                    │
  │                        │                        │                    │
  │ ... User must delete   │                        │                    │
  │     NixosConfiguration │                        │                    │
  │     first ...          │                        │                    │
  │                        │                        │                    │
  │ kubectl delete         │                        │                    │
  │ NixosConfiguration     │                        │                    │
  │───────────────────────►│                        │                    │
  │                        │                        │                    │
  │                        │ Config deleted ────────┼───────────────────►│
  │                        │                        │                    │
  │                        │                        │ (config reconcile  │
  │                        │                        │  clears Machine    │
  │                        │                        │  status)           │
  │                        │                        │                    │
  │                        │ Machine reconcile      │                    │
  │                        │ triggered (periodic)   │                    │
  │                        │───────────────────────►│                    │
  │                        │                        │                    │
  │                        │                        │ List NixosConfigs  │
  │                        │                        │───────────────────►│
  │                        │                        │◄───────────────────│
  │                        │                        │ Found: [] (empty)  │
  │                        │                        │                    │
  │                        │              ┌─────────┴─────────┐          │
  │                        │              │ No referencing    │          │
  │                        │              │ configs           │          │
  │                        │              └─────────┬─────────┘          │
  │                        │                        │                    │
  │                        │                        ▼                    │
  │                        │              ┌─────────────────┐            │
  │                        │ Remove       │ Remove finalizer│            │
  │                        │ finalizer    │                 │            │
  │                        │◄─────────────│                 │            │
  │                        │              └────────┬────────┘            │
  │                        │                       │                     │
  │                        │                       ▼                     │
  │                        │              ┌─────────────────┐            │
  │                        │              │ Machine deleted │            │
  │                        │              │ from etcd       │            │
  │                        │              └─────────────────┘            │
  │                        │                                             │
  │◄───────────────────────│ Deletion confirmed                          │
  │                        │                                             │
```

### 26.6 Interaction: Machine Becomes Discoverable

```
                        K8s API                Machine              NixosConfig
                           │                   Controller           Controller
                           │                        │                    │
    SSH becomes            │                        │                    │
    available              │                        │                    │
         │                 │                        │                    │
         │                 │  Periodic reconcile    │                    │
         │                 │───────────────────────►│                    │
         │                 │                        │                    │
         │                 │                        │ Try SSH connection │
         │                 │                        │──────────────────► │
         │                 │                        │ ◄───────────────── │
         │                 │                        │ SUCCESS            │
         │                 │                        │                    │
         │                 │              ┌─────────┴─────────┐          │
         │                 │              │ Was Discoverable  │          │
         │                 │              │ == false?         │          │
         │                 │              └─────────┬─────────┘          │
         │                 │                        │                    │
         │                 │                    YES │                    │
         │                 │                        ▼                    │
         │                 │              ┌─────────────────┐            │
         │                 │              │ Update status:  │            │
         │                 │◄─────────────│ Discoverable=   │            │
         │                 │              │   True          │            │
         │                 │              │ Ready=True      │            │
         │                 │              └─────────────────┘            │
         │                 │                        │                    │
         │                 │                        │                    │
         │                 │  Machine status        │                    │
         │                 │  changed event         │                    │
         │                 │  (via Watch)           │                    │
         │                 │────────────────────────┼───────────────────►│
         │                 │                        │                    │
         │                 │                        │              ┌─────┴─────┐
         │                 │                        │              │ Find      │
         │                 │                        │              │ NixosConf │
         │                 │                        │              │ for this  │
         │                 │                        │              │ Machine   │
         │                 │                        │              └─────┬─────┘
         │                 │                        │                    │
         │                 │                        │                    │
         │                 │                        │              ┌─────┴─────┐
         │                 │                        │              │ Was       │
         │                 │                        │              │ waiting   │
         │                 │                        │              │ for       │
         │                 │                        │              │ Machine?  │
         │                 │                        │              └─────┬─────┘
         │                 │                        │                    │
         │                 │                        │                YES │
         │                 │                        │                    ▼
         │                 │                        │              ┌───────────┐
         │                 │                        │              │ Start     │
         │                 │                        │              │ apply Job │
         │                 │◄───────────────────────┼──────────────│           │
         │                 │  Create Job            │              └───────────┘
         │                 │                        │                    │
```

### 26.7 State Transition Table: NixosConfiguration

| Current State | Event | Next State | Actions |
|---------------|-------|------------|---------|
| - | Created | Pending | Add finalizer, check Machine |
| Pending | Machine not ready | Pending | Set condition MachineNotReady, requeue 30s |
| Pending | Machine ready | Reconciling | Check concurrency, create Job |
| Pending | Concurrency limit | Pending | Set condition Queued, requeue 30s |
| Reconciling | Job running | Reconciling | Update progress from logs, requeue 10s |
| Reconciling | Job succeeded | Applied | Update status, update Machine, emit event |
| Reconciling | Job failed | Stalled | Set error condition, calculate backoff |
| Applied | Spec changed | Reconciling | Create new Job |
| Applied | Git commit changed | Reconciling | Create new Job |
| Stalled | Spec changed | Reconciling | Clear stalled, create new Job |
| Stalled | Backoff elapsed | Reconciling | Retry with new Job |
| * | Delete requested | Deleting | Cancel Job if running |
| Deleting | Has onRemoveFlake | ApplyingRemoval | Create removal Job |
| Deleting | No onRemoveFlake | Finalizing | Clear Machine status |
| ApplyingRemoval | Removal succeeded | Finalizing | Clear Machine status |
| ApplyingRemoval | Removal failed (3x) | Finalizing | Log error, proceed anyway |
| Finalizing | Machine cleared | Deleted | Remove finalizer |

### 26.8 State Transition Table: Machine

| Current State | Event | Next State | Actions |
|---------------|-------|------------|---------|
| - | Created | Undiscoverable | Add finalizer, try SSH |
| Undiscoverable | SSH succeeded | Discoverable | Update status, start hardware scan |
| Discoverable | SSH failed | Undiscoverable | Update status, emit event |
| Discoverable | Hardware scan done | Discoverable | Update hardwareFacts |
| * | Delete + has configs | DeletionBlocked | Set condition, keep finalizer |
| DeletionBlocked | Configs deleted | Deleting | Proceed with deletion |
| * | Delete + no configs | Deleting | Remove finalizer |
| Deleting | Finalizer removed | Deleted | - |

### 26.9 Implementation Checklist

- [ ] Implement Machine state transitions in reconciler
- [ ] Implement NixosConfiguration state transitions in reconciler
- [ ] Add Machine watch to NixosConfiguration controller
- [ ] Implement deletion blocking for Machine
- [ ] Implement onRemoveFlake application on deletion
- [ ] Add comprehensive logging for state transitions
- [ ] Add metrics for state distribution (`nio_machines_by_state`, `nio_configs_by_state`)
- [ ] Add state transition events for observability
- [ ] Write integration tests for full lifecycle scenarios

## 27. Leader Election and High Availability

### 27.1 Why Leader Election is Required

In production Kubernetes clusters, operators typically run with multiple replicas for high availability. Without leader election:

| Problem | Impact |
|---------|--------|
| Multiple reconciles | Same resource reconciled by multiple instances simultaneously |
| Race conditions | Status updates conflict, Jobs created multiple times |
| Resource corruption | Inconsistent state due to concurrent modifications |
| Wasted resources | Duplicate SSH connections, git clones, NixOS applies |

**Leader election ensures only ONE replica actively reconciles resources at any time.**

### 27.2 How Leader Election Works

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Kubernetes Cluster                           │
│                                                                      │
│  ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐  │
│  │   Replica 1     │    │   Replica 2     │    │   Replica 3     │  │
│  │   (LEADER)      │    │   (STANDBY)     │    │   (STANDBY)     │  │
│  │                 │    │                 │    │                 │  │
│  │ ✓ Reconciling   │    │ ✗ Waiting       │    │ ✗ Waiting       │  │
│  │ ✓ Creating Jobs │    │ ✗ Health only   │    │ ✗ Health only   │  │
│  │ ✓ SSH connects  │    │                 │    │                 │  │
│  └────────┬────────┘    └────────┬────────┘    └────────┬────────┘  │
│           │                      │                      │           │
│           │         Lease Object (coordination.k8s.io)  │           │
│           │         ┌─────────────────────────────┐     │           │
│           └────────►│ nixos-operator-leader-lock  │◄────┘           │
│                     │                             │                 │
│                     │ holderIdentity: replica-1   │                 │
│                     │ leaseDuration: 15s          │                 │
│                     │ renewTime: 2024-01-15T...   │                 │
│                     └─────────────────────────────┘                 │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘

Leader Failover:
1. Replica 1 stops renewing lease (crash, network partition)
2. Lease expires after leaseDuration + renewDeadline
3. Replica 2 or 3 acquires lease, becomes new leader
4. New leader starts reconciliation
```

### 27.3 Controller-Runtime Leader Election

Controller-runtime provides built-in leader election via the Manager:

```go
// cmd/main.go
package main

import (
    "flag"
    "os"
    "time"

    "k8s.io/apimachinery/pkg/runtime"
    utilruntime "k8s.io/apimachinery/pkg/util/runtime"
    clientgoscheme "k8s.io/client-go/kubernetes/scheme"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/healthz"
    "sigs.k8s.io/controller-runtime/pkg/log/zap"
    metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
    "github.com/homystack/nixos-operator/internal/controller"
)

var (
    scheme   = runtime.NewScheme()
    setupLog = ctrl.Log.WithName("setup")
)

func init() {
    utilruntime.Must(clientgoscheme.AddToScheme(scheme))
    utilruntime.Must(niov1alpha1.AddToScheme(scheme))
}

func main() {
    var (
        metricsAddr          string
        probeAddr            string
        enableLeaderElection bool
        leaderElectionID     string
        leaseDuration        time.Duration
        renewDeadline        time.Duration
        retryPeriod          time.Duration
    )

    flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
        "The address the metric endpoint binds to.")
    flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
        "The address the probe endpoint binds to.")
    flag.BoolVar(&enableLeaderElection, "leader-elect", false,
        "Enable leader election for controller manager. "+
            "Enabling this will ensure there is only one active controller manager.")
    flag.StringVar(&leaderElectionID, "leader-election-id", "nixos-operator-leader-lock",
        "The name of the leader election resource.")
    flag.DurationVar(&leaseDuration, "leader-election-lease-duration", 15*time.Second,
        "The duration that non-leader candidates will wait after observing a leadership "+
            "renewal until attempting to acquire leadership.")
    flag.DurationVar(&renewDeadline, "leader-election-renew-deadline", 10*time.Second,
        "The interval between attempts by the acting leader to renew the leadership.")
    flag.DurationVar(&retryPeriod, "leader-election-retry-period", 2*time.Second,
        "The duration the clients should wait between attempting acquisition and renewal.")

    opts := zap.Options{Development: true}
    opts.BindFlags(flag.CommandLine)
    flag.Parse()

    ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

    mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
        Scheme: scheme,
        Metrics: metricsserver.Options{
            BindAddress: metricsAddr,
        },
        HealthProbeBindAddress: probeAddr,

        // Leader Election Configuration
        LeaderElection:          enableLeaderElection,
        LeaderElectionID:        leaderElectionID,
        LeaderElectionNamespace: getLeaderElectionNamespace(),
        LeaseDuration:           &leaseDuration,
        RenewDeadline:           &renewDeadline,
        RetryPeriod:             &retryPeriod,

        // Graceful shutdown
        GracefulShutdownTimeout: ptr.To(30 * time.Second),
    })
    if err != nil {
        setupLog.Error(err, "unable to start manager")
        os.Exit(1)
    }

    // Register indexes BEFORE controllers
    ctx := ctrl.SetupSignalHandler()
    if err := controller.SetupIndexes(ctx, mgr); err != nil {
        setupLog.Error(err, "unable to setup indexes")
        os.Exit(1)
    }

    // Setup controllers
    if err := (&controller.MachineReconciler{
        Client:   mgr.GetClient(),
        Scheme:   mgr.GetScheme(),
        Recorder: mgr.GetEventRecorderFor("machine-controller"),
    }).SetupWithManager(mgr); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "Machine")
        os.Exit(1)
    }

    if err := (&controller.NixosConfigurationReconciler{
        Client:   mgr.GetClient(),
        Scheme:   mgr.GetScheme(),
        Recorder: mgr.GetEventRecorderFor("nixosconfiguration-controller"),
    }).SetupWithManager(mgr); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "NixosConfiguration")
        os.Exit(1)
    }

    // Health checks
    if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
        setupLog.Error(err, "unable to set up health check")
        os.Exit(1)
    }
    if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
        setupLog.Error(err, "unable to set up ready check")
        os.Exit(1)
    }

    setupLog.Info("starting manager",
        "leaderElection", enableLeaderElection,
        "leaderElectionID", leaderElectionID,
    )

    if err := mgr.Start(ctx); err != nil {
        setupLog.Error(err, "problem running manager")
        os.Exit(1)
    }
}

// getLeaderElectionNamespace returns the namespace for leader election.
// In-cluster: uses downward API or falls back to service account namespace.
// Out-of-cluster: uses "default" namespace.
func getLeaderElectionNamespace() string {
    // Check POD_NAMESPACE env var (set via downward API)
    if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
        return ns
    }

    // Try to read from service account
    if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
        return string(data)
    }

    // Fallback for local development
    return "nixos-operator-system"
}
```

### 27.4 Leader Election Parameters

| Parameter | Default | Description | Recommendation |
|-----------|---------|-------------|----------------|
| `LeaseDuration` | 15s | Time non-leaders wait before trying to acquire | 15-30s for most cases |
| `RenewDeadline` | 10s | Time leader has to renew before losing leadership | < LeaseDuration |
| `RetryPeriod` | 2s | Time between acquisition/renewal attempts | 2-5s |

**Constraints:**
- `RenewDeadline` < `LeaseDuration` (leader must renew before lease expires)
- `RetryPeriod` < `RenewDeadline` (must retry before deadline)

**Tuning for different scenarios:**

```go
// Fast failover (more API server load)
LeaseDuration: 10 * time.Second,
RenewDeadline: 8 * time.Second,
RetryPeriod:   2 * time.Second,
// Failover time: ~10-12 seconds

// Slow failover (less API server load, good for edge/resource-constrained)
LeaseDuration: 60 * time.Second,
RenewDeadline: 45 * time.Second,
RetryPeriod:   10 * time.Second,
// Failover time: ~60-70 seconds

// Balanced (recommended for most production)
LeaseDuration: 15 * time.Second,
RenewDeadline: 10 * time.Second,
RetryPeriod:   2 * time.Second,
// Failover time: ~15-17 seconds
```

### 27.5 RBAC for Leader Election

Leader election requires permissions to create and update Lease objects:

```yaml
# config/rbac/leader_election_role.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: nixos-operator-leader-election
  namespace: nixos-operator-system
rules:
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: nixos-operator-leader-election
  namespace: nixos-operator-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: nixos-operator-leader-election
subjects:
  - kind: ServiceAccount
    name: nixos-operator-controller-manager
    namespace: nixos-operator-system
```

### 27.6 Deployment Configuration

```yaml
# config/manager/manager.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nixos-operator-controller-manager
  namespace: nixos-operator-system
  labels:
    app.kubernetes.io/name: nixos-operator
    app.kubernetes.io/component: controller-manager
spec:
  replicas: 2  # Multiple replicas for HA
  selector:
    matchLabels:
      app.kubernetes.io/name: nixos-operator
      app.kubernetes.io/component: controller-manager
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 1
  template:
    metadata:
      labels:
        app.kubernetes.io/name: nixos-operator
        app.kubernetes.io/component: controller-manager
      annotations:
        kubectl.kubernetes.io/default-container: manager
    spec:
      serviceAccountName: nixos-operator-controller-manager
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      terminationGracePeriodSeconds: 30
      containers:
        - name: manager
          image: ghcr.io/homystack/nixos-operator:latest
          args:
            - --leader-elect=true
            - --leader-election-id=nixos-operator-leader-lock
            - --leader-election-lease-duration=15s
            - --leader-election-renew-deadline=10s
            - --leader-election-retry-period=2s
            - --health-probe-bind-address=:8081
            - --metrics-bind-address=:8080
          env:
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
          ports:
            - name: metrics
              containerPort: 8080
              protocol: TCP
            - name: health
              containerPort: 8081
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: health
            initialDelaySeconds: 15
            periodSeconds: 20
            timeoutSeconds: 5
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /readyz
              port: health
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 3
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 512Mi
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchLabels:
                    app.kubernetes.io/name: nixos-operator
                    app.kubernetes.io/component: controller-manager
                topologyKey: kubernetes.io/hostname
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: nixos-operator
              app.kubernetes.io/component: controller-manager
```

### 27.7 Pod Disruption Budget

```yaml
# config/manager/pdb.yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: nixos-operator-controller-manager
  namespace: nixos-operator-system
spec:
  minAvailable: 1  # At least one replica must be available during disruption
  selector:
    matchLabels:
      app.kubernetes.io/name: nixos-operator
      app.kubernetes.io/component: controller-manager
```

### 27.8 Leader Election Metrics

Controller-runtime exposes leader election metrics:

```go
// Metrics available out of the box:
// - leader_election_master_status: 1 if this instance is the leader, 0 otherwise
// - leader_election_slow_path_total: Number of slow path leader elections

// Custom metrics for monitoring
var (
    leaderElectionTransitions = prometheus.NewCounter(prometheus.CounterOpts{
        Name: "nio_leader_election_transitions_total",
        Help: "Total number of leader election transitions (both gaining and losing)",
    })

    isLeader = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "nio_is_leader",
        Help: "1 if this instance is currently the leader, 0 otherwise",
    })
)

func init() {
    metrics.Registry.MustRegister(leaderElectionTransitions, isLeader)
}
```

### 27.9 Graceful Leadership Transition

Handle leadership changes gracefully:

```go
// Manager runs callbacks on leader election events
mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
    // ... other options ...

    // Leader election callbacks
    LeaderElectionReleaseOnCancel: true,  // Release lease on context cancel
})

// Controllers automatically stop reconciling when leadership is lost
// Jobs in progress will continue running (they're independent pods)
// No special handling needed for most cases
```

### 27.10 Testing Leader Election

```go
// internal/controller/leaderelection_test.go
package controller

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    coordinationv1 "k8s.io/api/coordination/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes/fake"
    "k8s.io/client-go/tools/leaderelection"
    "k8s.io/client-go/tools/leaderelection/resourcelock"
)

func TestLeaderElection_SingleInstance(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    client := fake.NewSimpleClientset()

    lock := &resourcelock.LeaseLock{
        LeaseMeta: metav1.ObjectMeta{
            Name:      "test-leader-lock",
            Namespace: "default",
        },
        Client: client.CoordinationV1(),
        LockConfig: resourcelock.ResourceLockConfig{
            Identity: "test-instance-1",
        },
    }

    leaderElected := make(chan struct{})
    leaderLost := make(chan struct{})

    go leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
        Lock:            lock,
        LeaseDuration:   5 * time.Second,
        RenewDeadline:   3 * time.Second,
        RetryPeriod:     1 * time.Second,
        ReleaseOnCancel: true,
        Callbacks: leaderelection.LeaderCallbacks{
            OnStartedLeading: func(ctx context.Context) {
                close(leaderElected)
            },
            OnStoppedLeading: func() {
                close(leaderLost)
            },
        },
    })

    select {
    case <-leaderElected:
        // Success - we became leader
    case <-time.After(5 * time.Second):
        t.Fatal("timed out waiting for leader election")
    }

    // Verify lease was created
    lease, err := client.CoordinationV1().Leases("default").Get(ctx, "test-leader-lock", metav1.GetOptions{})
    require.NoError(t, err)
    assert.Equal(t, "test-instance-1", *lease.Spec.HolderIdentity)
}

func TestLeaderElection_Failover(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    client := fake.NewSimpleClientset()

    // Instance 1 becomes leader
    lock1 := &resourcelock.LeaseLock{
        LeaseMeta: metav1.ObjectMeta{
            Name:      "test-leader-lock",
            Namespace: "default",
        },
        Client: client.CoordinationV1(),
        LockConfig: resourcelock.ResourceLockConfig{
            Identity: "instance-1",
        },
    }

    ctx1, cancel1 := context.WithCancel(ctx)
    leader1Elected := make(chan struct{})

    go leaderelection.RunOrDie(ctx1, leaderelection.LeaderElectionConfig{
        Lock:            lock1,
        LeaseDuration:   5 * time.Second,
        RenewDeadline:   3 * time.Second,
        RetryPeriod:     1 * time.Second,
        ReleaseOnCancel: true,
        Callbacks: leaderelection.LeaderCallbacks{
            OnStartedLeading: func(ctx context.Context) {
                close(leader1Elected)
            },
        },
    })

    <-leader1Elected

    // Instance 2 waiting
    lock2 := &resourcelock.LeaseLock{
        LeaseMeta: metav1.ObjectMeta{
            Name:      "test-leader-lock",
            Namespace: "default",
        },
        Client: client.CoordinationV1(),
        LockConfig: resourcelock.ResourceLockConfig{
            Identity: "instance-2",
        },
    }

    leader2Elected := make(chan struct{})

    go leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
        Lock:            lock2,
        LeaseDuration:   5 * time.Second,
        RenewDeadline:   3 * time.Second,
        RetryPeriod:     1 * time.Second,
        ReleaseOnCancel: true,
        Callbacks: leaderelection.LeaderCallbacks{
            OnStartedLeading: func(ctx context.Context) {
                close(leader2Elected)
            },
        },
    })

    // Kill instance 1
    cancel1()

    // Instance 2 should become leader
    select {
    case <-leader2Elected:
        // Success - failover worked
    case <-time.After(15 * time.Second):
        t.Fatal("timed out waiting for failover")
    }

    // Verify lease holder changed
    lease, err := client.CoordinationV1().Leases("default").Get(ctx, "test-leader-lock", metav1.GetOptions{})
    require.NoError(t, err)
    assert.Equal(t, "instance-2", *lease.Spec.HolderIdentity)
}
```

### 27.11 Monitoring and Alerting

```yaml
# Prometheus alert rules
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: nixos-operator-alerts
  namespace: nixos-operator-system
spec:
  groups:
    - name: nixos-operator.leader-election
      rules:
        - alert: NixosOperatorNoLeader
          expr: |
            sum(leader_election_master_status{job="nixos-operator"}) == 0
          for: 2m
          labels:
            severity: critical
          annotations:
            summary: "No leader elected for nixos-operator"
            description: "nixos-operator has no active leader for more than 2 minutes. Reconciliation is stopped."

        - alert: NixosOperatorMultipleLeaders
          expr: |
            sum(leader_election_master_status{job="nixos-operator"}) > 1
          for: 30s
          labels:
            severity: critical
          annotations:
            summary: "Multiple leaders detected for nixos-operator"
            description: "Multiple instances claim leadership. This should never happen and indicates a serious issue."

        - alert: NixosOperatorLeaderElectionSlowPath
          expr: |
            increase(leader_election_slow_path_total{job="nixos-operator"}[5m]) > 0
          for: 0m
          labels:
            severity: warning
          annotations:
            summary: "Leader election using slow path"
            description: "Leader election fell back to slow path. This may indicate API server issues."

        - alert: NixosOperatorFrequentLeaderChanges
          expr: |
            increase(nio_leader_election_transitions_total[10m]) > 3
          for: 0m
          labels:
            severity: warning
          annotations:
            summary: "Frequent leader election changes"
            description: "Leader changed more than 3 times in 10 minutes. Check for network issues or pod restarts."
```

### 27.12 Troubleshooting Leader Election

**Check current leader:**

```bash
kubectl get lease nixos-operator-leader-lock -n nixos-operator-system -o yaml
```

**Expected output:**

```yaml
apiVersion: coordination.k8s.io/v1
kind: Lease
metadata:
  name: nixos-operator-leader-lock
  namespace: nixos-operator-system
spec:
  acquireTime: "2024-01-15T10:30:00.000000Z"
  holderIdentity: nixos-operator-controller-manager-7d8f9b6c4d-abc12
  leaseDurationSeconds: 15
  leaseTransitions: 3
  renewTime: "2024-01-15T12:45:30.123456Z"
```

**Common issues:**

| Symptom | Cause | Solution |
|---------|-------|----------|
| No leader for extended time | All pods crashed | Check pod logs, events |
| Frequent leader changes | Network instability | Increase lease duration |
| Multiple leaders (very rare) | Clock skew | Sync clocks, check NTP |
| Lease not created | RBAC missing | Add coordination.k8s.io permissions |

### 27.13 Implementation Checklist

- [ ] Add leader election flags to main.go
- [ ] Configure Manager with LeaderElection options
- [ ] Create RBAC for Lease objects
- [ ] Update Deployment with `--leader-elect=true`
- [ ] Set replicas > 1 for HA
- [ ] Add PodDisruptionBudget
- [ ] Configure pod anti-affinity
- [ ] Add leader election metrics
- [ ] Create Prometheus alerting rules
- [ ] Write leader election tests
- [ ] Document failover procedure in runbook

## 28. Helm Chart

### 28.1 Chart Structure

```
charts/nixos-operator/
├── Chart.yaml
├── values.yaml
├── templates/
│   ├── _helpers.tpl
│   ├── crds/
│   │   ├── machine.yaml
│   │   └── nixosconfiguration.yaml
│   ├── deployment.yaml
│   ├── serviceaccount.yaml
│   ├── clusterrole.yaml
│   ├── clusterrolebinding.yaml
│   ├── role.yaml
│   ├── rolebinding.yaml
│   ├── service.yaml
│   ├── servicemonitor.yaml
│   └── pdb.yaml
└── README.md
```

### 28.2 Chart.yaml

```yaml
apiVersion: v2
name: nixos-operator
description: Kubernetes operator for managing NixOS machines
type: application
version: 0.1.0
appVersion: "0.1.0"
kubeVersion: ">=1.26.0-0"
home: https://github.com/homystack/nixos-operator
sources:
  - https://github.com/homystack/nixos-operator
maintainers:
  - name: homystack
    url: https://github.com/homystack
keywords:
  - nixos
  - operator
  - infrastructure
  - configuration-management
annotations:
  artifacthub.io/category: integration-delivery
  artifacthub.io/license: Apache-2.0
  artifacthub.io/operator: "true"
  artifacthub.io/operatorCapabilities: Full Lifecycle
```

### 28.3 values.yaml

```yaml
# Default values for nixos-operator

# -- Number of replicas (use 2+ for HA with leader election)
replicaCount: 2

image:
  # -- Container image repository
  repository: ghcr.io/homystack/nixos-operator
  # -- Image pull policy
  pullPolicy: IfNotPresent
  # -- Overrides the image tag (default: Chart appVersion)
  tag: ""

# -- Image pull secrets for private registries
imagePullSecrets: []

# -- Override chart name
nameOverride: ""

# -- Override full release name
fullnameOverride: ""

serviceAccount:
  # -- Create ServiceAccount
  create: true
  # -- Annotations for ServiceAccount
  annotations: {}
  # -- ServiceAccount name (generated if not set)
  name: ""

# -- Pod annotations
podAnnotations: {}

# -- Pod labels
podLabels: {}

# -- Pod security context
podSecurityContext:
  runAsNonRoot: true
  seccompProfile:
    type: RuntimeDefault

# -- Container security context
securityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop:
      - ALL

# -- Resource requests and limits
resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 512Mi

# -- Node selector
nodeSelector: {}

# -- Tolerations
tolerations: []

# -- Affinity rules
affinity: {}

# -- Topology spread constraints
topologySpreadConstraints: []

# Leader election configuration
leaderElection:
  # -- Enable leader election (required for replicas > 1)
  enabled: true
  # -- Lease resource name
  resourceName: nixos-operator-leader-lock
  # -- Lease duration
  leaseDuration: 15s
  # -- Renew deadline
  renewDeadline: 10s
  # -- Retry period
  retryPeriod: 2s

# Metrics configuration
metrics:
  # -- Enable metrics endpoint
  enabled: true
  # -- Metrics service port
  port: 8080
  service:
    # -- Metrics service type
    type: ClusterIP
    # -- Metrics service annotations
    annotations: {}

# ServiceMonitor for Prometheus Operator
serviceMonitor:
  # -- Create ServiceMonitor
  enabled: false
  # -- ServiceMonitor namespace (defaults to release namespace)
  namespace: ""
  # -- Additional labels for ServiceMonitor
  labels: {}
  # -- Scrape interval
  interval: 30s
  # -- Scrape timeout
  scrapeTimeout: 10s
  # -- Metric relabelings
  metricRelabelings: []
  # -- Target relabelings
  relabelings: []

# Health probes configuration
probes:
  # -- Health probe port
  port: 8081
  liveness:
    # -- Initial delay for liveness probe
    initialDelaySeconds: 15
    # -- Period for liveness probe
    periodSeconds: 20
    # -- Timeout for liveness probe
    timeoutSeconds: 5
    # -- Failure threshold for liveness probe
    failureThreshold: 3
  readiness:
    # -- Initial delay for readiness probe
    initialDelaySeconds: 5
    # -- Period for readiness probe
    periodSeconds: 10
    # -- Timeout for readiness probe
    timeoutSeconds: 5
    # -- Failure threshold for readiness probe
    failureThreshold: 3

# Pod Disruption Budget
podDisruptionBudget:
  # -- Create PodDisruptionBudget
  enabled: true
  # -- Minimum available pods
  minAvailable: 1
  # -- Maximum unavailable pods (mutually exclusive with minAvailable)
  # maxUnavailable: 1

# CRD management
crds:
  # -- Install CRDs
  install: true
  # -- Keep CRDs on chart uninstall
  keep: true

# Apply Jobs configuration
applyJobs:
  # -- Default image for apply jobs (defaults to operator image)
  image: ""
  # -- Default resources for apply jobs
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      cpu: "2"
      memory: 2Gi
  # -- TTL for completed jobs (seconds)
  ttlSecondsAfterFinished: 3600

# Operator configuration
config:
  # -- Machine discovery interval
  machineDiscoveryInterval: 60s
  # -- Hardware scan interval
  hardwareScanInterval: 300s
  # -- Configuration reconcile interval
  configReconcileInterval: 120s
  # -- NixOS apply timeout
  nixosApplyTimeout: 3600s
  # -- Max concurrent apply jobs
  maxConcurrentJobs: 5
  # -- Log level (debug, info, warn, error)
  logLevel: info
  # -- Log format (json, console)
  logFormat: json
```

### 28.4 templates/_helpers.tpl

```yaml
{{/*
Expand the name of the chart.
*/}}
{{- define "nixos-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "nixos-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nixos-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nixos-operator.labels" -}}
helm.sh/chart: {{ include "nixos-operator.chart" . }}
{{ include "nixos-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nixos-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nixos-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: controller-manager
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "nixos-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "nixos-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Container image
*/}}
{{- define "nixos-operator.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
Apply jobs image (defaults to operator image)
*/}}
{{- define "nixos-operator.applyJobsImage" -}}
{{- if .Values.applyJobs.image }}
{{- .Values.applyJobs.image }}
{{- else }}
{{- include "nixos-operator.image" . }}
{{- end }}
{{- end }}

{{/*
Leader election namespace
*/}}
{{- define "nixos-operator.leaderElectionNamespace" -}}
{{- .Release.Namespace }}
{{- end }}
```

### 28.5 templates/serviceaccount.yaml

```yaml
{{- if .Values.serviceAccount.create -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "nixos-operator.serviceAccountName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
  {{- with .Values.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
automountServiceAccountToken: true
{{- end }}
```

### 28.6 templates/clusterrole.yaml

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "nixos-operator.fullname" . }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
rules:
  # CRDs
  - apiGroups: ["nio.homystack.com"]
    resources: ["machines"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["nio.homystack.com"]
    resources: ["machines/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: ["nio.homystack.com"]
    resources: ["machines/finalizers"]
    verbs: ["update"]
  - apiGroups: ["nio.homystack.com"]
    resources: ["nixosconfigurations"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["nio.homystack.com"]
    resources: ["nixosconfigurations/status"]
    verbs: ["get", "update", "patch"]
  - apiGroups: ["nio.homystack.com"]
    resources: ["nixosconfigurations/finalizers"]
    verbs: ["update"]
  # Secrets (for SSH keys and git credentials)
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
  # Events
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
  # Jobs (for apply operations)
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["get", "list", "watch", "create", "delete"]
  # Pods (for job logs)
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
```

### 28.7 templates/clusterrolebinding.yaml

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "nixos-operator.fullname" . }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "nixos-operator.fullname" . }}
subjects:
  - kind: ServiceAccount
    name: {{ include "nixos-operator.serviceAccountName" . }}
    namespace: {{ .Release.Namespace }}
```

### 28.8 templates/role.yaml

```yaml
{{- if .Values.leaderElection.enabled }}
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ include "nixos-operator.fullname" . }}-leader-election
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
rules:
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
{{- end }}
```

### 28.9 templates/rolebinding.yaml

```yaml
{{- if .Values.leaderElection.enabled }}
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "nixos-operator.fullname" . }}-leader-election
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "nixos-operator.fullname" . }}-leader-election
subjects:
  - kind: ServiceAccount
    name: {{ include "nixos-operator.serviceAccountName" . }}
    namespace: {{ .Release.Namespace }}
{{- end }}
```

### 28.10 templates/deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "nixos-operator.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      {{- include "nixos-operator.selectorLabels" . | nindent 6 }}
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 1
  template:
    metadata:
      annotations:
        kubectl.kubernetes.io/default-container: manager
        {{- with .Values.podAnnotations }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      labels:
        {{- include "nixos-operator.labels" . | nindent 8 }}
        {{- with .Values.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "nixos-operator.serviceAccountName" . }}
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      terminationGracePeriodSeconds: 30
      containers:
        - name: manager
          image: {{ include "nixos-operator.image" . }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          args:
            {{- if .Values.leaderElection.enabled }}
            - --leader-elect=true
            - --leader-election-id={{ .Values.leaderElection.resourceName }}
            - --leader-election-lease-duration={{ .Values.leaderElection.leaseDuration }}
            - --leader-election-renew-deadline={{ .Values.leaderElection.renewDeadline }}
            - --leader-election-retry-period={{ .Values.leaderElection.retryPeriod }}
            {{- else }}
            - --leader-elect=false
            {{- end }}
            - --health-probe-bind-address=:{{ .Values.probes.port }}
            {{- if .Values.metrics.enabled }}
            - --metrics-bind-address=:{{ .Values.metrics.port }}
            {{- else }}
            - --metrics-bind-address=0
            {{- end }}
            - --machine-discovery-interval={{ .Values.config.machineDiscoveryInterval }}
            - --hardware-scan-interval={{ .Values.config.hardwareScanInterval }}
            - --config-reconcile-interval={{ .Values.config.configReconcileInterval }}
            - --nixos-apply-timeout={{ .Values.config.nixosApplyTimeout }}
            - --max-concurrent-jobs={{ .Values.config.maxConcurrentJobs }}
            - --log-level={{ .Values.config.logLevel }}
            - --log-format={{ .Values.config.logFormat }}
            - --apply-job-image={{ include "nixos-operator.applyJobsImage" . }}
            - --apply-job-ttl={{ .Values.applyJobs.ttlSecondsAfterFinished }}
          env:
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
          ports:
            {{- if .Values.metrics.enabled }}
            - name: metrics
              containerPort: {{ .Values.metrics.port }}
              protocol: TCP
            {{- end }}
            - name: health
              containerPort: {{ .Values.probes.port }}
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: health
            initialDelaySeconds: {{ .Values.probes.liveness.initialDelaySeconds }}
            periodSeconds: {{ .Values.probes.liveness.periodSeconds }}
            timeoutSeconds: {{ .Values.probes.liveness.timeoutSeconds }}
            failureThreshold: {{ .Values.probes.liveness.failureThreshold }}
          readinessProbe:
            httpGet:
              path: /readyz
              port: health
            initialDelaySeconds: {{ .Values.probes.readiness.initialDelaySeconds }}
            periodSeconds: {{ .Values.probes.readiness.periodSeconds }}
            timeoutSeconds: {{ .Values.probes.readiness.timeoutSeconds }}
            failureThreshold: {{ .Values.probes.readiness.failureThreshold }}
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- if or .Values.affinity (gt (int .Values.replicaCount) 1) }}
      affinity:
        {{- if .Values.affinity }}
        {{- toYaml .Values.affinity | nindent 8 }}
        {{- else }}
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchLabels:
                    {{- include "nixos-operator.selectorLabels" . | nindent 20 }}
                topologyKey: kubernetes.io/hostname
        {{- end }}
      {{- end }}
      {{- with .Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- toYaml . | nindent 8 }}
      {{- end }}
```

### 28.11 templates/service.yaml

```yaml
{{- if .Values.metrics.enabled }}
apiVersion: v1
kind: Service
metadata:
  name: {{ include "nixos-operator.fullname" . }}-metrics
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
  {{- with .Values.metrics.service.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  type: {{ .Values.metrics.service.type }}
  ports:
    - port: {{ .Values.metrics.port }}
      targetPort: metrics
      protocol: TCP
      name: metrics
  selector:
    {{- include "nixos-operator.selectorLabels" . | nindent 4 }}
{{- end }}
```

### 28.12 templates/servicemonitor.yaml

```yaml
{{- if and .Values.metrics.enabled .Values.serviceMonitor.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "nixos-operator.fullname" . }}
  namespace: {{ default .Release.Namespace .Values.serviceMonitor.namespace }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
    {{- with .Values.serviceMonitor.labels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  endpoints:
    - port: metrics
      interval: {{ .Values.serviceMonitor.interval }}
      scrapeTimeout: {{ .Values.serviceMonitor.scrapeTimeout }}
      {{- with .Values.serviceMonitor.metricRelabelings }}
      metricRelabelings:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.serviceMonitor.relabelings }}
      relabelings:
        {{- toYaml . | nindent 8 }}
      {{- end }}
  namespaceSelector:
    matchNames:
      - {{ .Release.Namespace }}
  selector:
    matchLabels:
      {{- include "nixos-operator.selectorLabels" . | nindent 6 }}
{{- end }}
```

### 28.13 templates/pdb.yaml

```yaml
{{- if .Values.podDisruptionBudget.enabled }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "nixos-operator.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
spec:
  {{- if .Values.podDisruptionBudget.minAvailable }}
  minAvailable: {{ .Values.podDisruptionBudget.minAvailable }}
  {{- else if .Values.podDisruptionBudget.maxUnavailable }}
  maxUnavailable: {{ .Values.podDisruptionBudget.maxUnavailable }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "nixos-operator.selectorLabels" . | nindent 6 }}
{{- end }}
```

### 28.14 templates/crds/machine.yaml

```yaml
{{- if .Values.crds.install }}
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: machines.nio.homystack.com
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
  annotations:
    controller-gen.kubebuilder.io/version: v0.14.0
    {{- if .Values.crds.keep }}
    "helm.sh/resource-policy": keep
    {{- end }}
spec:
  group: nio.homystack.com
  names:
    kind: Machine
    listKind: MachineList
    plural: machines
    singular: machine
    shortNames:
      - mc
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      additionalPrinterColumns:
        - name: Host
          type: string
          jsonPath: .spec.host
        - name: Ready
          type: string
          jsonPath: .status.conditions[?(@.type=="Ready")].status
        - name: Discoverable
          type: string
          jsonPath: .status.conditions[?(@.type=="Discoverable")].status
        - name: Config
          type: string
          jsonPath: .status.appliedConfiguration
        - name: Age
          type: date
          jsonPath: .metadata.creationTimestamp
      schema:
        openAPIV3Schema:
          type: object
          required: ["spec"]
          properties:
            apiVersion:
              type: string
            kind:
              type: string
            metadata:
              type: object
            spec:
              type: object
              required: ["host"]
              properties:
                host:
                  type: string
                  minLength: 1
                  maxLength: 253
                  description: Target machine address (hostname or IP) for SSH connection
                sshUser:
                  type: string
                  default: root
                  maxLength: 32
                  description: SSH username for connection
                sshKeySecretRef:
                  type: object
                  properties:
                    name:
                      type: string
                    namespace:
                      type: string
                  required: ["name"]
                sshPasswordSecretRef:
                  type: object
                  properties:
                    name:
                      type: string
                    namespace:
                      type: string
                    key:
                      type: string
                      default: password
                  required: ["name"]
            status:
              type: object
              properties:
                observedGeneration:
                  type: integer
                  format: int64
                discoverable:
                  type: boolean
                hasConfiguration:
                  type: boolean
                appliedConfiguration:
                  type: string
                appliedCommit:
                  type: string
                lastAppliedTime:
                  type: string
                  format: date-time
                lastHardwareScanTime:
                  type: string
                  format: date-time
                hardwareFacts:
                  type: object
                  x-kubernetes-preserve-unknown-fields: true
                nixFacterResult:
                  type: object
                  x-kubernetes-preserve-unknown-fields: true
                conditions:
                  type: array
                  items:
                    type: object
                    required: ["type", "status", "lastTransitionTime", "reason", "message"]
                    properties:
                      type:
                        type: string
                      status:
                        type: string
                        enum: ["True", "False", "Unknown"]
                      lastTransitionTime:
                        type: string
                        format: date-time
                      reason:
                        type: string
                      message:
                        type: string
                      observedGeneration:
                        type: integer
                        format: int64
{{- end }}
```

### 28.15 templates/crds/nixosconfiguration.yaml

```yaml
{{- if .Values.crds.install }}
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: nixosconfigurations.nio.homystack.com
  labels:
    {{- include "nixos-operator.labels" . | nindent 4 }}
  annotations:
    controller-gen.kubebuilder.io/version: v0.14.0
    {{- if .Values.crds.keep }}
    "helm.sh/resource-policy": keep
    {{- end }}
spec:
  group: nio.homystack.com
  names:
    kind: NixosConfiguration
    listKind: NixosConfigurationList
    plural: nixosconfigurations
    singular: nixosconfiguration
    shortNames:
      - nc
      - nixcfg
  scope: Namespaced
  versions:
    - name: v1alpha1
      served: true
      storage: true
      subresources:
        status: {}
      additionalPrinterColumns:
        - name: Ready
          type: string
          jsonPath: .status.conditions[?(@.type=="Ready")].status
        - name: Target
          type: string
          jsonPath: .spec.machineRef.name
        - name: Flake
          type: string
          jsonPath: .spec.flake
        - name: Commit
          type: string
          jsonPath: .status.appliedCommit
          priority: 1
        - name: Age
          type: date
          jsonPath: .metadata.creationTimestamp
      schema:
        openAPIV3Schema:
          type: object
          required: ["spec"]
          properties:
            apiVersion:
              type: string
            kind:
              type: string
            metadata:
              type: object
            spec:
              type: object
              required: ["machineRef"]
              properties:
                machineRef:
                  type: object
                  required: ["name"]
                  properties:
                    name:
                      type: string
                    namespace:
                      type: string
                gitRepo:
                  type: string
                  maxLength: 2048
                ref:
                  type: string
                  default: main
                credentialsRef:
                  type: object
                  properties:
                    name:
                      type: string
                    namespace:
                      type: string
                  required: ["name"]
                flake:
                  type: string
                onRemoveFlake:
                  type: string
                configurationSubdir:
                  type: string
                fullInstall:
                  type: boolean
                  default: false
                additionalFiles:
                  type: array
                  items:
                    type: object
                    required: ["path", "valueType"]
                    properties:
                      path:
                        type: string
                      valueType:
                        type: string
                        enum: ["Inline", "SecretRef", "NixosFacter"]
                      inline:
                        type: string
                      secretRef:
                        type: object
                        required: ["name", "key"]
                        properties:
                          name:
                            type: string
                          key:
                            type: string
                      nixosFacter:
                        type: boolean
                jobTemplate:
                  type: object
                  properties:
                    image:
                      type: string
                    nodeSelector:
                      type: object
                      additionalProperties:
                        type: string
                    tolerations:
                      type: array
                      items:
                        type: object
                        properties:
                          key:
                            type: string
                          operator:
                            type: string
                          value:
                            type: string
                          effect:
                            type: string
                          tolerationSeconds:
                            type: integer
                            format: int64
                    resources:
                      type: object
                      properties:
                        requests:
                          type: object
                          additionalProperties:
                            anyOf:
                              - type: integer
                              - type: string
                            x-kubernetes-int-or-string: true
                        limits:
                          type: object
                          additionalProperties:
                            anyOf:
                              - type: integer
                              - type: string
                            x-kubernetes-int-or-string: true
                    serviceAccountName:
                      type: string
            status:
              type: object
              properties:
                observedGeneration:
                  type: integer
                  format: int64
                fullDiskInstallCompleted:
                  type: boolean
                appliedCommit:
                  type: string
                lastAppliedTime:
                  type: string
                  format: date-time
                targetMachine:
                  type: string
                configurationHash:
                  type: string
                additionalFilesHash:
                  type: string
                operationState:
                  type: object
                  properties:
                    type:
                      type: string
                      enum: ["NixosRebuild", "FullInstall"]
                    startedAt:
                      type: string
                      format: date-time
                    phase:
                      type: string
                    jobName:
                      type: string
                    lastLogLine:
                      type: string
                conditions:
                  type: array
                  items:
                    type: object
                    required: ["type", "status", "lastTransitionTime", "reason", "message"]
                    properties:
                      type:
                        type: string
                      status:
                        type: string
                        enum: ["True", "False", "Unknown"]
                      lastTransitionTime:
                        type: string
                        format: date-time
                      reason:
                        type: string
                      message:
                        type: string
                      observedGeneration:
                        type: integer
                        format: int64
{{- end }}
```

### 28.16 Installation Examples

```bash
# Install with default values
helm install nixos-operator ./charts/nixos-operator \
  --namespace nixos-operator-system \
  --create-namespace

# Install with custom values
helm install nixos-operator ./charts/nixos-operator \
  --namespace nixos-operator-system \
  --create-namespace \
  --set replicaCount=3 \
  --set serviceMonitor.enabled=true \
  --set config.logLevel=debug

# Install from OCI registry
helm install nixos-operator oci://ghcr.io/homystack/charts/nixos-operator \
  --version 0.1.0 \
  --namespace nixos-operator-system \
  --create-namespace

# Upgrade
helm upgrade nixos-operator ./charts/nixos-operator \
  --namespace nixos-operator-system \
  --reuse-values \
  --set image.tag=v0.2.0

# Uninstall (CRDs kept by default)
helm uninstall nixos-operator --namespace nixos-operator-system

# Uninstall including CRDs
helm uninstall nixos-operator --namespace nixos-operator-system
kubectl delete crd machines.nio.homystack.com nixosconfigurations.nio.homystack.com
```

### 28.17 values.yaml for Production

```yaml
# values-production.yaml
replicaCount: 3

resources:
  requests:
    cpu: 200m
    memory: 256Mi
  limits:
    cpu: "1"
    memory: 1Gi

leaderElection:
  enabled: true
  leaseDuration: 15s
  renewDeadline: 10s
  retryPeriod: 2s

serviceMonitor:
  enabled: true
  interval: 15s

podDisruptionBudget:
  enabled: true
  minAvailable: 1

topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: ScheduleAnyway
    labelSelector:
      matchLabels:
        app.kubernetes.io/name: nixos-operator

config:
  logLevel: info
  logFormat: json
  maxConcurrentJobs: 10

applyJobs:
  resources:
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: "4"
      memory: 4Gi
```

### 28.18 Implementation Checklist

- [ ] Create `charts/nixos-operator/` directory structure
- [ ] Create `Chart.yaml` with metadata
- [ ] Create `values.yaml` with all configurable options
- [ ] Create `templates/_helpers.tpl` with helper templates
- [ ] Create `templates/serviceaccount.yaml`
- [ ] Create `templates/clusterrole.yaml` with RBAC rules
- [ ] Create `templates/clusterrolebinding.yaml`
- [ ] Create `templates/role.yaml` for leader election
- [ ] Create `templates/rolebinding.yaml` for leader election
- [ ] Create `templates/deployment.yaml`
- [ ] Create `templates/service.yaml` for metrics
- [ ] Create `templates/servicemonitor.yaml` for Prometheus
- [ ] Create `templates/pdb.yaml`
- [ ] Create `templates/crds/machine.yaml`
- [ ] Create `templates/crds/nixosconfiguration.yaml`
- [ ] Add `helm.sh/resource-policy: keep` annotation to CRDs
- [ ] Create `values-production.yaml` example
- [ ] Run `helm lint` to validate chart
- [ ] Run `helm template` to verify rendered manifests
- [ ] Test installation in dev cluster
- [ ] Publish chart to OCI registry

## 29. Container Image

### 29.1 Image Requirements

The nixos-operator uses a **single container image** for both:
1. **Controller** - runs as Deployment, manages CRDs
2. **Apply Jobs** - spawned to execute nixos-rebuild/nixos-anywhere

| Component | Required For | Notes |
|-----------|-------------|-------|
| Go binary | Both | Operator logic, job executor |
| Nix | Jobs | nixos-rebuild, nixos-anywhere, flake evaluation |
| Git | Jobs | Clone configuration repositories |
| OpenSSH client | Jobs | Connect to target machines |
| hardware_scanner.sh | Controller | Upload to machines for facts collection |

### 29.2 Image Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                    nixos-operator:v0.1.0                            │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                      /usr/local/bin/                         │   │
│  │  ┌──────────────────┐  ┌──────────────────────────────────┐ │   │
│  │  │ nixos-operator   │  │ hardware_scanner.sh              │ │   │
│  │  │ (Go binary)      │  │ (embedded script)                │ │   │
│  │  │                  │  │                                  │ │   │
│  │  │ Modes:           │  │ Uploaded to machines via SSH     │ │   │
│  │  │ - controller     │  │ for hardware facts collection    │ │   │
│  │  │ - apply          │  │                                  │ │   │
│  │  └──────────────────┘  └──────────────────────────────────┘ │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌─────────────────────────────────────────────────────────────┐   │
│  │                         Nix Store                            │   │
│  │  ┌────────────┐ ┌────────────┐ ┌────────────┐ ┌───────────┐ │   │
│  │  │ nix        │ │ git        │ │ openssh    │ │ coreutils │ │   │
│  │  │ 2.24+      │ │ 2.40+      │ │ 9.0+       │ │ bash      │ │   │
│  │  └────────────┘ └────────────┘ └────────────┘ └───────────┘ │   │
│  │                                                              │   │
│  │  ┌────────────────────────────────────────────────────────┐ │   │
│  │  │ nixos-anywhere (fetched at runtime via flake)          │ │   │
│  │  └────────────────────────────────────────────────────────┘ │   │
│  └─────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  User: nonroot (65532)                                              │
│  Workdir: /work                                                     │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### 29.3 Containerfile (Multi-stage Build)

```dockerfile
# syntax=docker/dockerfile:1

# ============================================================================
# Stage 1: Build Go binary
# ============================================================================
FROM docker.io/library/golang:1.23-alpine AS builder

WORKDIR /src

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build binary
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w \
        -X main.version=${VERSION} \
        -X main.commit=${COMMIT} \
        -X main.buildDate=${BUILD_DATE}" \
    -o /nixos-operator \
    ./cmd/

# ============================================================================
# Stage 2: Final image
# ============================================================================
FROM nixos/nix:2.24.10

LABEL org.opencontainers.image.title="NixOS Operator"
LABEL org.opencontainers.image.description="Kubernetes operator for managing NixOS machines"
LABEL org.opencontainers.image.source="https://github.com/homystack/nixos-operator"
LABEL org.opencontainers.image.licenses="Apache-2.0"

# Configure Nix and install required packages
RUN mkdir -p /etc/nix && \
    echo "experimental-features = nix-command flakes" >> /etc/nix/nix.conf && \
    echo "sandbox = false" >> /etc/nix/nix.conf && \
    echo "filter-syscalls = false" >> /etc/nix/nix.conf && \
    nix-env -iA \
        nixpkgs.git \
        nixpkgs.openssh \
        nixpkgs.coreutils \
        nixpkgs.bash \
        nixpkgs.cacert && \
    nix-collect-garbage -d

# Copy Go binary from builder
COPY --from=builder /nixos-operator /usr/local/bin/nixos-operator

# Copy hardware scanner script
COPY scripts/hardware_scanner.sh /usr/local/bin/hardware_scanner.sh
RUN chmod +x /usr/local/bin/hardware_scanner.sh

# Create non-root user
RUN addgroup -g 65532 -S nonroot && \
    adduser -u 65532 -S nonroot -G nonroot -h /home/nonroot

# Create working directories
RUN mkdir -p /work /home/nonroot/.ssh && \
    chown -R nonroot:nonroot /work /home/nonroot

# Configure SSH client
RUN mkdir -p /etc/ssh && \
    echo "Host *" >> /etc/ssh/ssh_config && \
    echo "  StrictHostKeyChecking accept-new" >> /etc/ssh/ssh_config && \
    echo "  UserKnownHostsFile /home/nonroot/.ssh/known_hosts" >> /etc/ssh/ssh_config

# Environment
ENV HOME=/home/nonroot
ENV NIX_SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
ENV GIT_SSL_CAINFO=/etc/ssl/certs/ca-certificates.crt

WORKDIR /work
USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/nixos-operator"]
CMD ["controller"]
```

### 29.4 Alternative: Nix Flake-based Build

For fully reproducible builds using Nix:

```nix
# flake.nix
{
  description = "NixOS Operator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        # Go binary
        nixos-operator = pkgs.buildGoModule {
          pname = "nixos-operator";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-XXXX..."; # Update after first build

          ldflags = [
            "-s" "-w"
            "-X main.version=${self.shortRev or "dev"}"
          ];
        };

        # Container image
        containerImage = pkgs.dockerTools.buildLayeredImage {
          name = "ghcr.io/homystack/nixos-operator";
          tag = "latest";

          contents = [
            nixos-operator
            pkgs.nix
            pkgs.git
            pkgs.openssh
            pkgs.coreutils
            pkgs.bash
            pkgs.cacert

            # Hardware scanner script
            (pkgs.writeScriptBin "hardware_scanner.sh"
              (builtins.readFile ./scripts/hardware_scanner.sh))
          ];

          config = {
            Entrypoint = [ "${nixos-operator}/bin/nixos-operator" ];
            Cmd = [ "controller" ];
            User = "65532:65532";
            WorkingDir = "/work";
            Env = [
              "HOME=/home/nonroot"
              "NIX_SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
            ];
          };

          extraCommands = ''
            mkdir -p work home/nonroot/.ssh etc/nix
            echo "experimental-features = nix-command flakes" > etc/nix/nix.conf
            echo "sandbox = false" >> etc/nix/nix.conf
          '';
        };
      in
      {
        packages = {
          default = nixos-operator;
          container = containerImage;
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go_1_23
            gopls
            golangci-lint
            kubebuilder
            kubectl
            kubernetes-helm
          ];
        };
      }
    );
}
```

Build with:
```bash
nix build .#container
docker load < result
```

### 29.5 Binary Modes

The single binary supports multiple modes via subcommands:

```go
// cmd/main.go
package main

import (
    "os"

    "github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
    Use:   "nixos-operator",
    Short: "Kubernetes operator for managing NixOS machines",
}

var controllerCmd = &cobra.Command{
    Use:   "controller",
    Short: "Run the operator controller",
    Run:   runController,
}

var applyCmd = &cobra.Command{
    Use:   "apply",
    Short: "Run NixOS apply operation (used by Jobs)",
    Run:   runApply,
}

var versionCmd = &cobra.Command{
    Use:   "version",
    Short: "Print version information",
    Run: func(cmd *cobra.Command, args []string) {
        fmt.Printf("nixos-operator %s (commit: %s, built: %s)\n",
            version, commit, buildDate)
    },
}

func init() {
    // Controller flags
    controllerCmd.Flags().Bool("leader-elect", false, "Enable leader election")
    controllerCmd.Flags().String("leader-election-id", "nixos-operator-leader-lock", "Leader election resource name")
    controllerCmd.Flags().String("metrics-bind-address", ":8080", "Metrics endpoint address")
    controllerCmd.Flags().String("health-probe-bind-address", ":8081", "Health probe address")
    controllerCmd.Flags().Duration("machine-discovery-interval", 60*time.Second, "Machine discovery interval")
    controllerCmd.Flags().Duration("hardware-scan-interval", 300*time.Second, "Hardware scan interval")
    controllerCmd.Flags().Int("max-concurrent-jobs", 5, "Maximum concurrent apply jobs")
    controllerCmd.Flags().String("apply-job-image", "", "Image for apply jobs (defaults to current image)")

    // Apply flags
    applyCmd.Flags().String("config-name", "", "NixosConfiguration resource name")
    applyCmd.Flags().String("config-namespace", "", "NixosConfiguration resource namespace")
    applyCmd.Flags().String("operation", "NixosRebuild", "Operation type: NixosRebuild or FullInstall")

    rootCmd.AddCommand(controllerCmd)
    rootCmd.AddCommand(applyCmd)
    rootCmd.AddCommand(versionCmd)
}

func main() {
    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

### 29.6 Hardware Scanner Embedding

Embed the script in the Go binary for easy access:

```go
// internal/scanner/embedded.go
package scanner

import (
    _ "embed"
)

//go:embed hardware_scanner.sh
var HardwareScannerScript string

// GetScript returns the hardware scanner script content
func GetScript() string {
    return HardwareScannerScript
}
```

Usage in controller:
```go
// internal/controller/machine_controller.go

func (r *MachineReconciler) uploadAndRunScanner(ctx context.Context, conn ssh.Connection) (map[string]interface{}, error) {
    // Upload script to target machine
    scriptContent := scanner.GetScript()
    remotePath := "/tmp/nio-hardware-scanner.sh"

    if err := conn.WriteFile(ctx, remotePath, []byte(scriptContent), 0755); err != nil {
        return nil, fmt.Errorf("upload scanner script: %w", err)
    }
    defer conn.Execute(ctx, "rm -f "+remotePath)

    // Execute and parse output
    stdout, stderr, exitCode, err := conn.Execute(ctx, remotePath)
    if err != nil || exitCode != 0 {
        return nil, fmt.Errorf("execute scanner: exit=%d stderr=%s: %w", exitCode, stderr, err)
    }

    return parseHardwareFacts(stdout), nil
}
```

### 29.7 Apply Job Execution

When running as apply job:

```go
// internal/apply/executor.go
package apply

import (
    "context"
    "fmt"
    "os"
    "os/exec"
)

type Executor struct {
    ConfigName      string
    ConfigNamespace string
    Operation       string
    GitRepo         string
    GitRef          string
    Flake           string
    TargetHost      string
    SSHUser         string
    SSHKeyPath      string
    ConfigSubdir    string
}

func (e *Executor) Run(ctx context.Context) error {
    // 1. Clone repository
    workDir, err := e.cloneRepository(ctx)
    if err != nil {
        return fmt.Errorf("clone repository: %w", err)
    }
    defer os.RemoveAll(workDir)

    // 2. Inject additional files (read from K8s API)
    if err := e.injectAdditionalFiles(ctx, workDir); err != nil {
        return fmt.Errorf("inject additional files: %w", err)
    }

    // 3. Execute NixOS command
    switch e.Operation {
    case "FullInstall":
        return e.runNixosAnywhere(ctx, workDir)
    case "NixosRebuild":
        return e.runNixosRebuild(ctx, workDir)
    default:
        return fmt.Errorf("unknown operation: %s", e.Operation)
    }
}

func (e *Executor) runNixosRebuild(ctx context.Context, workDir string) error {
    configPath := workDir
    if e.ConfigSubdir != "" {
        configPath = filepath.Join(workDir, e.ConfigSubdir)
    }

    flakeRef := configPath + e.Flake

    cmd := exec.CommandContext(ctx,
        "nix",
        "--extra-experimental-features", "nix-command flakes",
        "shell", "nixpkgs#nixos-rebuild",
        "--command", "nixos-rebuild", "switch",
        "--flake", flakeRef,
        "--target-host", fmt.Sprintf("%s@%s", e.SSHUser, e.TargetHost),
    )

    cmd.Env = append(os.Environ(),
        fmt.Sprintf("NIX_SSHOPTS=-i %s -o StrictHostKeyChecking=accept-new", e.SSHKeyPath),
    )

    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr

    return cmd.Run()
}

func (e *Executor) runNixosAnywhere(ctx context.Context, workDir string) error {
    configPath := workDir
    if e.ConfigSubdir != "" {
        configPath = filepath.Join(workDir, e.ConfigSubdir)
    }

    flakeRef := configPath + e.Flake

    cmd := exec.CommandContext(ctx,
        "nix",
        "--extra-experimental-features", "nix-command flakes",
        "run", "github:nix-community/nixos-anywhere", "--",
        "--flake", flakeRef,
        "--target-host", fmt.Sprintf("%s@%s", e.SSHUser, e.TargetHost),
        "-i", e.SSHKeyPath,
    )

    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr

    return cmd.Run()
}
```

### 29.8 Build Pipeline

```yaml
# .github/workflows/build.yaml
name: Build and Push

on:
  push:
    branches: [main]
    tags: ['v*']
  pull_request:
    branches: [main]

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write

    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to Container Registry
        if: github.event_name != 'pull_request'
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=ref,event=branch
            type=ref,event=pr
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=sha

      - name: Build and push
        uses: docker/build-push-action@v5
        with:
          context: .
          file: ./Containerfile
          platforms: linux/amd64,linux/arm64
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
          build-args: |
            VERSION=${{ github.ref_name }}
            COMMIT=${{ github.sha }}
            BUILD_DATE=${{ github.event.head_commit.timestamp }}
```

### 29.9 Image Size Optimization

Target image size: **~500MB** (Nix store is large but necessary)

| Component | Approximate Size |
|-----------|-----------------|
| Nix base | ~300MB |
| Git, OpenSSH, coreutils | ~50MB |
| Go binary | ~30MB |
| CA certificates | ~1MB |
| **Total** | **~400-500MB** |

Optimizations applied:
- Multi-stage build (Go build artifacts not in final image)
- `nix-collect-garbage -d` after package installation
- Static Go binary with `-s -w` flags
- Single binary for both controller and jobs

### 29.10 Security Considerations

| Aspect | Implementation |
|--------|---------------|
| Non-root user | UID 65532 (nonroot) |
| Read-only filesystem | Supported (workdir is tmpfs in Jobs) |
| No shell in PATH | bash available but not in PATH for controller |
| Minimal packages | Only what's needed for NixOS operations |
| CA certificates | Pinned from nixpkgs |
| SSH strict host checking | `accept-new` (TOFU model) |

### 29.11 Implementation Checklist

- [ ] Create `Containerfile` with multi-stage build
- [ ] Create `flake.nix` for Nix-based builds (optional)
- [ ] Implement `cmd/main.go` with subcommands (controller, apply, version)
- [ ] Embed `hardware_scanner.sh` using `//go:embed`
- [ ] Implement `internal/apply/executor.go` for job execution
- [ ] Create GitHub Actions workflow for multi-arch builds
- [ ] Test image locally with `docker build` / `nix build`
- [ ] Test controller mode in kind/k3d
- [ ] Test apply mode with mock SSH target
- [ ] Verify image size is reasonable (~500MB)
- [ ] Scan image for vulnerabilities (trivy/grype)
- [ ] Push to ghcr.io with proper tags

## 30. Rate Limiting and Work Queue

### 30.1 Controller-Runtime Defaults

Controller-runtime uses a rate-limited work queue with exponential backoff:

| Parameter | Default | Description |
|-----------|---------|-------------|
| Base delay | 5ms | Initial backoff delay |
| Max delay | 1000s (~16min) | Maximum backoff delay |
| Max concurrent reconciles | 1 | Parallel reconciliations per controller |

### 30.2 Configuration in main.go

```go
// cmd/main.go
import (
    "time"

    "golang.org/x/time/rate"
    "k8s.io/client-go/util/workqueue"
    ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
    var (
        // Rate limiting flags
        maxConcurrentReconciles int
        rateLimitBaseDelay      time.Duration
        rateLimitMaxDelay       time.Duration
        rateLimitBucketSize     int
        rateLimitQPS            float64
    )

    flag.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", 2,
        "Maximum number of concurrent reconciles per controller")
    flag.DurationVar(&rateLimitBaseDelay, "rate-limit-base-delay", 5*time.Millisecond,
        "Base delay for rate limiter exponential backoff")
    flag.DurationVar(&rateLimitMaxDelay, "rate-limit-max-delay", 5*time.Minute,
        "Maximum delay for rate limiter exponential backoff")
    flag.IntVar(&rateLimitBucketSize, "rate-limit-bucket-size", 100,
        "Bucket size for rate limiter")
    flag.Float64Var(&rateLimitQPS, "rate-limit-qps", 10.0,
        "QPS for rate limiter")

    // ... parse flags ...

    // Create custom rate limiter
    rateLimiter := workqueue.NewTypedMaxOfRateLimiter(
        // Exponential backoff for requeues
        workqueue.NewTypedItemExponentialFailureRateLimiter[ctrl.Request](
            rateLimitBaseDelay,
            rateLimitMaxDelay,
        ),
        // Overall rate limit
        &workqueue.TypedBucketRateLimiter[ctrl.Request]{
            Limiter: rate.NewLimiter(rate.Limit(rateLimitQPS), rateLimitBucketSize),
        },
    )

    // Setup controllers with rate limiter
    if err := (&controller.MachineReconciler{
        Client:   mgr.GetClient(),
        Scheme:   mgr.GetScheme(),
        Recorder: mgr.GetEventRecorderFor("machine-controller"),
    }).SetupWithManager(mgr, rateLimiter, maxConcurrentReconciles); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "Machine")
        os.Exit(1)
    }

    if err := (&controller.NixosConfigurationReconciler{
        Client:   mgr.GetClient(),
        Scheme:   mgr.GetScheme(),
        Recorder: mgr.GetEventRecorderFor("nixosconfiguration-controller"),
    }).SetupWithManager(mgr, rateLimiter, maxConcurrentReconciles); err != nil {
        setupLog.Error(err, "unable to create controller", "controller", "NixosConfiguration")
        os.Exit(1)
    }
}
```

### 30.3 Controller Setup with Options

```go
// internal/controller/machine_controller.go
func (r *MachineReconciler) SetupWithManager(
    mgr ctrl.Manager,
    rateLimiter workqueue.TypedRateLimiter[ctrl.Request],
    maxConcurrent int,
) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&niov1alpha1.Machine{}).
        Watches(
            &corev1.Secret{},
            handler.EnqueueRequestsFromMapFunc(r.findMachinesForSecret),
            builder.WithPredicates(r.secretChangePredicate()),
        ).
        WithOptions(ctrlcontroller.Options{
            MaxConcurrentReconciles: maxConcurrent,
            RateLimiter:             rateLimiter,
        }).
        Complete(r)
}

// internal/controller/nixosconfiguration_controller.go
func (r *NixosConfigurationReconciler) SetupWithManager(
    mgr ctrl.Manager,
    rateLimiter workqueue.TypedRateLimiter[ctrl.Request],
    maxConcurrent int,
) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&niov1alpha1.NixosConfiguration{}).
        Owns(&batchv1.Job{}).
        Watches(
            &corev1.Secret{},
            handler.EnqueueRequestsFromMapFunc(r.findConfigsForSecret),
            builder.WithPredicates(r.secretChangePredicate()),
        ).
        Watches(
            &niov1alpha1.Machine{},
            handler.EnqueueRequestsFromMapFunc(r.findConfigsForMachine),
        ).
        WithOptions(ctrlcontroller.Options{
            MaxConcurrentReconciles: maxConcurrent,
            RateLimiter:             rateLimiter,
        }).
        Complete(r)
}
```

### 30.4 Helm values.yaml Addition

```yaml
# values.yaml (add to config section)
config:
  # ... existing config ...

  # Rate limiting configuration
  rateLimit:
    # -- Maximum concurrent reconciles per controller
    maxConcurrentReconciles: 2
    # -- Base delay for exponential backoff
    baseDelay: 5ms
    # -- Maximum delay for exponential backoff
    maxDelay: 5m
    # -- Bucket size for rate limiter
    bucketSize: 100
    # -- Queries per second limit
    qps: 10
```

### 30.5 Deployment args Addition

```yaml
# templates/deployment.yaml (add to args)
args:
  # ... existing args ...
  - --max-concurrent-reconciles={{ .Values.config.rateLimit.maxConcurrentReconciles }}
  - --rate-limit-base-delay={{ .Values.config.rateLimit.baseDelay }}
  - --rate-limit-max-delay={{ .Values.config.rateLimit.maxDelay }}
  - --rate-limit-bucket-size={{ .Values.config.rateLimit.bucketSize }}
  - --rate-limit-qps={{ .Values.config.rateLimit.qps }}
```

### 30.6 Recommended Values

| Scenario | maxConcurrent | baseDelay | maxDelay | QPS |
|----------|---------------|-----------|----------|-----|
| Small cluster (<50 machines) | 2 | 5ms | 5m | 10 |
| Medium cluster (50-200) | 5 | 10ms | 5m | 20 |
| Large cluster (200+) | 10 | 20ms | 10m | 50 |
| Development | 1 | 1ms | 1m | 100 |

### 30.7 Implementation Checklist

- [ ] Add rate limiting flags to main.go
- [ ] Create custom rate limiter with exponential backoff
- [ ] Update controller SetupWithManager to accept options
- [ ] Add rate limiting config to values.yaml
- [ ] Update deployment template with new args
- [ ] Document recommended values for different cluster sizes

## 31. E2E Tests

### 31.1 E2E Test Strategy

```
┌─────────────────────────────────────────────────────────────────────┐
│                         E2E Test Environment                        │
│                                                                     │
│  ┌─────────────────┐         ┌─────────────────────────────────┐   │
│  │   Kind Cluster  │         │      Mock SSH Server            │   │
│  │                 │         │      (testcontainers)           │   │
│  │  ┌───────────┐  │         │                                 │   │
│  │  │ Operator  │  │  SSH    │  ┌─────────────────────────┐   │   │
│  │  │ Pod       │──┼────────►│  │  openssh-server         │   │   │
│  │  └───────────┘  │         │  │  + mock NixOS responses │   │   │
│  │                 │         │  └─────────────────────────┘   │   │
│  │  ┌───────────┐  │         │                                 │   │
│  │  │ CRDs      │  │         └─────────────────────────────────┘   │
│  │  │ Machine   │  │                                               │
│  │  │ NixosCfg  │  │         ┌─────────────────────────────────┐   │
│  │  └───────────┘  │         │      Mock Git Server            │   │
│  │                 │   Git   │      (gitea or gogs)            │   │
│  │  ┌───────────┐  │────────►│                                 │   │
│  │  │ Jobs      │  │         │  Contains test NixOS configs   │   │
│  │  └───────────┘  │         └─────────────────────────────────┘   │
│  └─────────────────┘                                               │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### 31.2 Test Framework Setup

```go
// test/e2e/e2e_suite_test.go
package e2e

import (
    "context"
    "os"
    "testing"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/wait"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/client/config"

    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
)

var (
    k8sClient    client.Client
    ctx          context.Context
    cancel       context.CancelFunc
    sshContainer testcontainers.Container
    sshHost      string
    sshPort      string
)

func TestE2E(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
    ctx, cancel = context.WithTimeout(context.Background(), 30*time.Minute)

    // Setup K8s client (assumes kind cluster is running)
    cfg, err := config.GetConfig()
    Expect(err).NotTo(HaveOccurred())

    k8sClient, err = client.New(cfg, client.Options{
        Scheme: scheme,
    })
    Expect(err).NotTo(HaveOccurred())

    // Start mock SSH server
    sshContainer, err = startMockSSHServer(ctx)
    Expect(err).NotTo(HaveOccurred())

    sshHost, err = sshContainer.Host(ctx)
    Expect(err).NotTo(HaveOccurred())

    mappedPort, err := sshContainer.MappedPort(ctx, "22")
    Expect(err).NotTo(HaveOccurred())
    sshPort = mappedPort.Port()
})

var _ = AfterSuite(func() {
    if sshContainer != nil {
        sshContainer.Terminate(ctx)
    }
    cancel()
})
```

### 31.3 Mock SSH Server

```go
// test/e2e/mock_ssh_test.go
package e2e

import (
    "context"
    "fmt"

    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/wait"
)

func startMockSSHServer(ctx context.Context) (testcontainers.Container, error) {
    req := testcontainers.ContainerRequest{
        Image:        "linuxserver/openssh-server:latest",
        ExposedPorts: []string{"22/tcp"},
        Env: map[string]string{
            "PUID":            "1000",
            "PGID":            "1000",
            "TZ":              "UTC",
            "PASSWORD_ACCESS": "true",
            "USER_PASSWORD":   "testpassword",
            "USER_NAME":       "testuser",
        },
        WaitingFor: wait.ForListeningPort("22/tcp").WithStartupTimeout(60 * time.Second),
        // Mount mock scripts that simulate NixOS responses
        Mounts: testcontainers.ContainerMounts{
            {
                Source: testcontainers.GenericBindMountSource{
                    HostPath: "./testdata/mock-scripts",
                },
                Target: "/config/custom-cont-init.d",
            },
        },
    }

    return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: req,
        Started:          true,
    })
}
```

### 31.4 Mock Scripts

```bash
# test/e2e/testdata/mock-scripts/setup-mock-nixos.sh
#!/bin/bash

# Create mock nixos-rebuild that succeeds
cat > /usr/local/bin/nixos-rebuild << 'EOF'
#!/bin/bash
echo "building the system configuration..."
sleep 2
echo "activating the configuration..."
echo "setting up /etc..."
echo "reloading systemd..."
echo "Done."
exit 0
EOF
chmod +x /usr/local/bin/nixos-rebuild

# Create mock nix command
cat > /usr/local/bin/nix << 'EOF'
#!/bin/bash
case "$*" in
    *"--version"*)
        echo "nix (Nix) 2.24.0"
        ;;
    *)
        echo "nix mock executed: $*"
        exit 0
        ;;
esac
EOF
chmod +x /usr/local/bin/nix

# Create /etc/os-release for NixOS
cat > /etc/os-release << 'EOF'
NAME="NixOS"
ID=nixos
VERSION="24.05"
VERSION_ID="24.05"
EOF
```

### 31.5 E2E Test Cases

```go
// test/e2e/machine_test.go
package e2e

import (
    "fmt"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/types"

    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
)

var _ = Describe("Machine E2E", func() {
    const (
        timeout  = 2 * time.Minute
        interval = 5 * time.Second
    )

    Context("When creating a Machine with valid SSH credentials", func() {
        var (
            machine *niov1alpha1.Machine
            secret  *corev1.Secret
        )

        BeforeEach(func() {
            // Create SSH password secret
            secret = &corev1.Secret{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "e2e-ssh-password",
                    Namespace: "default",
                },
                Type: corev1.SecretTypeOpaque,
                StringData: map[string]string{
                    "password": "testpassword",
                },
            }
            Expect(k8sClient.Create(ctx, secret)).To(Succeed())

            // Create Machine pointing to mock SSH server
            machine = &niov1alpha1.Machine{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "e2e-test-machine",
                    Namespace: "default",
                },
                Spec: niov1alpha1.MachineSpec{
                    Host:    fmt.Sprintf("%s:%s", sshHost, sshPort),
                    SSHUser: "testuser",
                    SSHPasswordSecretRef: &niov1alpha1.SSHPasswordSecretRef{
                        Name: "e2e-ssh-password",
                        Key:  "password",
                    },
                },
            }
            Expect(k8sClient.Create(ctx, machine)).To(Succeed())
        })

        AfterEach(func() {
            // Cleanup
            k8sClient.Delete(ctx, machine)
            k8sClient.Delete(ctx, secret)
        })

        It("Should become Discoverable", func() {
            Eventually(func() bool {
                var m niov1alpha1.Machine
                if err := k8sClient.Get(ctx, types.NamespacedName{
                    Name:      machine.Name,
                    Namespace: machine.Namespace,
                }, &m); err != nil {
                    return false
                }
                return m.Status.Discoverable
            }, timeout, interval).Should(BeTrue())
        })

        It("Should have Ready condition True", func() {
            Eventually(func() string {
                var m niov1alpha1.Machine
                if err := k8sClient.Get(ctx, types.NamespacedName{
                    Name:      machine.Name,
                    Namespace: machine.Namespace,
                }, &m); err != nil {
                    return ""
                }
                for _, c := range m.Status.Conditions {
                    if c.Type == "Ready" {
                        return string(c.Status)
                    }
                }
                return ""
            }, timeout, interval).Should(Equal("True"))
        })

        It("Should collect hardware facts", func() {
            Eventually(func() bool {
                var m niov1alpha1.Machine
                if err := k8sClient.Get(ctx, types.NamespacedName{
                    Name:      machine.Name,
                    Namespace: machine.Namespace,
                }, &m); err != nil {
                    return false
                }
                return m.Status.HardwareFacts != nil
            }, timeout, interval).Should(BeTrue())
        })
    })

    Context("When creating a Machine with invalid SSH credentials", func() {
        var machine *niov1alpha1.Machine

        BeforeEach(func() {
            machine = &niov1alpha1.Machine{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "e2e-invalid-machine",
                    Namespace: "default",
                },
                Spec: niov1alpha1.MachineSpec{
                    Host:    "192.168.255.255", // Non-routable IP
                    SSHUser: "root",
                },
            }
            Expect(k8sClient.Create(ctx, machine)).To(Succeed())
        })

        AfterEach(func() {
            k8sClient.Delete(ctx, machine)
        })

        It("Should have Discoverable condition False", func() {
            Consistently(func() bool {
                var m niov1alpha1.Machine
                if err := k8sClient.Get(ctx, types.NamespacedName{
                    Name:      machine.Name,
                    Namespace: machine.Namespace,
                }, &m); err != nil {
                    return true // Error, keep checking
                }
                return m.Status.Discoverable
            }, 30*time.Second, interval).Should(BeFalse())
        })
    })
})
```

### 31.6 NixosConfiguration E2E Tests

```go
// test/e2e/nixosconfiguration_test.go
package e2e

import (
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    batchv1 "k8s.io/api/batch/v1"
    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/types"
    "sigs.k8s.io/controller-runtime/pkg/client"

    niov1alpha1 "github.com/homystack/nixos-operator/api/v1alpha1"
)

var _ = Describe("NixosConfiguration E2E", func() {
    const (
        timeout  = 5 * time.Minute
        interval = 5 * time.Second
    )

    Context("When creating NixosConfiguration for discoverable Machine", func() {
        var (
            machine *niov1alpha1.Machine
            config  *niov1alpha1.NixosConfiguration
            secret  *corev1.Secret
        )

        BeforeEach(func() {
            // Create secret
            secret = &corev1.Secret{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "e2e-config-ssh",
                    Namespace: "default",
                },
                Type: corev1.SecretTypeOpaque,
                StringData: map[string]string{
                    "password": "testpassword",
                },
            }
            Expect(k8sClient.Create(ctx, secret)).To(Succeed())

            // Create Machine
            machine = &niov1alpha1.Machine{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "e2e-config-machine",
                    Namespace: "default",
                },
                Spec: niov1alpha1.MachineSpec{
                    Host:    fmt.Sprintf("%s:%s", sshHost, sshPort),
                    SSHUser: "testuser",
                    SSHPasswordSecretRef: &niov1alpha1.SSHPasswordSecretRef{
                        Name: "e2e-config-ssh",
                        Key:  "password",
                    },
                },
            }
            Expect(k8sClient.Create(ctx, machine)).To(Succeed())

            // Wait for Machine to be discoverable
            Eventually(func() bool {
                var m niov1alpha1.Machine
                k8sClient.Get(ctx, types.NamespacedName{
                    Name: machine.Name, Namespace: machine.Namespace,
                }, &m)
                return m.Status.Discoverable
            }, 2*time.Minute, interval).Should(BeTrue())

            // Create NixosConfiguration
            config = &niov1alpha1.NixosConfiguration{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "e2e-test-config",
                    Namespace: "default",
                },
                Spec: niov1alpha1.NixosConfigurationSpec{
                    MachineRef: niov1alpha1.MachineReference{
                        Name: machine.Name,
                    },
                    GitRepo: "https://github.com/homystack/nixos-test-config.git",
                    Ref:     "main",
                    Flake:   "#test",
                },
            }
            Expect(k8sClient.Create(ctx, config)).To(Succeed())
        })

        AfterEach(func() {
            // Cleanup in reverse order
            k8sClient.Delete(ctx, config)
            k8sClient.Delete(ctx, machine)
            k8sClient.Delete(ctx, secret)

            // Wait for Jobs to be cleaned up
            Eventually(func() int {
                var jobs batchv1.JobList
                k8sClient.List(ctx, &jobs,
                    client.InNamespace("default"),
                    client.MatchingLabels{"nio.homystack.com/config": "e2e-test-config"},
                )
                return len(jobs.Items)
            }, timeout, interval).Should(Equal(0))
        })

        It("Should create an apply Job", func() {
            Eventually(func() int {
                var jobs batchv1.JobList
                k8sClient.List(ctx, &jobs,
                    client.InNamespace("default"),
                    client.MatchingLabels{"nio.homystack.com/config": config.Name},
                )
                return len(jobs.Items)
            }, timeout, interval).Should(BeNumerically(">=", 1))
        })

        It("Should have OperationState while Job is running", func() {
            Eventually(func() bool {
                var c niov1alpha1.NixosConfiguration
                k8sClient.Get(ctx, types.NamespacedName{
                    Name: config.Name, Namespace: config.Namespace,
                }, &c)
                return c.Status.OperationState != nil
            }, timeout, interval).Should(BeTrue())
        })

        It("Should set Reconciling condition to True", func() {
            Eventually(func() string {
                var c niov1alpha1.NixosConfiguration
                k8sClient.Get(ctx, types.NamespacedName{
                    Name: config.Name, Namespace: config.Namespace,
                }, &c)
                for _, cond := range c.Status.Conditions {
                    if cond.Type == "Reconciling" {
                        return string(cond.Status)
                    }
                }
                return ""
            }, timeout, interval).Should(Equal("True"))
        })
    })

    Context("When NixosConfiguration references non-existent Machine", func() {
        var config *niov1alpha1.NixosConfiguration

        BeforeEach(func() {
            config = &niov1alpha1.NixosConfiguration{
                ObjectMeta: metav1.ObjectMeta{
                    Name:      "e2e-orphan-config",
                    Namespace: "default",
                },
                Spec: niov1alpha1.NixosConfigurationSpec{
                    MachineRef: niov1alpha1.MachineReference{
                        Name: "nonexistent-machine",
                    },
                    GitRepo: "https://github.com/example/config.git",
                    Flake:   "#test",
                },
            }
            Expect(k8sClient.Create(ctx, config)).To(Succeed())
        })

        AfterEach(func() {
            k8sClient.Delete(ctx, config)
        })

        It("Should have Ready condition False with MachineNotFound reason", func() {
            Eventually(func() string {
                var c niov1alpha1.NixosConfiguration
                k8sClient.Get(ctx, types.NamespacedName{
                    Name: config.Name, Namespace: config.Namespace,
                }, &c)
                for _, cond := range c.Status.Conditions {
                    if cond.Type == "Ready" && cond.Status == "False" {
                        return cond.Reason
                    }
                }
                return ""
            }, timeout, interval).Should(Equal("MachineNotFound"))
        })

        It("Should NOT create any Jobs", func() {
            Consistently(func() int {
                var jobs batchv1.JobList
                k8sClient.List(ctx, &jobs,
                    client.InNamespace("default"),
                    client.MatchingLabels{"nio.homystack.com/config": config.Name},
                )
                return len(jobs.Items)
            }, 30*time.Second, interval).Should(Equal(0))
        })
    })
})
```

### 31.7 Running E2E Tests

```bash
# Setup kind cluster with operator deployed
kind create cluster --name nixos-operator-e2e

# Install CRDs
kubectl apply -f config/crd/bases/

# Deploy operator
helm install nixos-operator ./charts/nixos-operator \
  --namespace nixos-operator-system \
  --create-namespace \
  --set image.tag=dev \
  --set replicaCount=1

# Run E2E tests
go test ./test/e2e/... -v -timeout 30m

# Cleanup
kind delete cluster --name nixos-operator-e2e
```

### 31.8 GitHub Actions E2E Workflow

```yaml
# .github/workflows/e2e.yaml
name: E2E Tests

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Setup Kind
        uses: helm/kind-action@v1
        with:
          cluster_name: e2e

      - name: Build and load image
        run: |
          docker build -t ghcr.io/homystack/nixos-operator:e2e .
          kind load docker-image ghcr.io/homystack/nixos-operator:e2e --name e2e

      - name: Install CRDs
        run: kubectl apply -f config/crd/bases/

      - name: Deploy operator
        run: |
          helm install nixos-operator ./charts/nixos-operator \
            --namespace nixos-operator-system \
            --create-namespace \
            --set image.tag=e2e \
            --set image.pullPolicy=Never \
            --set replicaCount=1 \
            --wait --timeout 5m

      - name: Wait for operator ready
        run: |
          kubectl wait --for=condition=available deployment/nixos-operator \
            -n nixos-operator-system --timeout=120s

      - name: Run E2E tests
        run: go test ./test/e2e/... -v -timeout 20m

      - name: Collect logs on failure
        if: failure()
        run: |
          kubectl logs -n nixos-operator-system -l app.kubernetes.io/name=nixos-operator --tail=100
          kubectl get machines,nixosconfigurations -A -o yaml
```

### 31.9 Implementation Checklist

- [ ] Create `test/e2e/` directory structure
- [ ] Setup Ginkgo test suite (`e2e_suite_test.go`)
- [ ] Implement mock SSH server with testcontainers
- [ ] Create mock scripts for NixOS commands
- [ ] Write Machine E2E tests (discoverable, unreachable)
- [ ] Write NixosConfiguration E2E tests (apply, orphan)
- [ ] Create GitHub Actions workflow for E2E
- [ ] Add Makefile targets for E2E (`make e2e`, `make e2e-setup`)
- [ ] Document E2E test requirements in CONTRIBUTING.md

## 32. Graceful Shutdown

### 32.1 Design Principle: Stateless Operator

The operator is designed to be **maximally stateless**:

| Aspect | Approach |
|--------|----------|
| In-progress reconciles | Let them complete or timeout |
| Running Jobs | Jobs are independent pods, continue running |
| Cached data | None - all state in K8s resources |
| Local files | None - workdir is ephemeral |

**On shutdown:**
1. Stop accepting new reconciliations
2. Wait for in-flight reconciles (with timeout)
3. Exit

Jobs survive operator restart because they are independent pods with owner references.

### 32.2 Controller-Runtime Built-in Handling

Controller-runtime handles graceful shutdown automatically:

```go
// cmd/main.go
func main() {
    // ... setup ...

    // SetupSignalHandler creates a context that is cancelled on SIGINT/SIGTERM
    ctx := ctrl.SetupSignalHandler()

    // Manager.Start blocks until context is cancelled
    // Then it:
    // 1. Stops all controllers (no new reconciles)
    // 2. Waits for in-flight reconciles (up to GracefulShutdownTimeout)
    // 3. Stops leader election (releases lease)
    // 4. Returns
    if err := mgr.Start(ctx); err != nil {
        setupLog.Error(err, "problem running manager")
        os.Exit(1)
    }
}
```

### 32.3 Manager Configuration

```go
mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
    // ... other options ...

    // Graceful shutdown timeout
    // In-flight reconciles have this long to complete
    GracefulShutdownTimeout: ptr.To(30 * time.Second),

    // Release leader election lease on shutdown
    // Allows another replica to take over immediately
    LeaderElectionReleaseOnCancel: true,
})
```

### 32.4 Reconciler Timeout Handling

Reconcilers should respect context cancellation:

```go
func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx)

    // Context is cancelled on shutdown - check early
    if ctx.Err() != nil {
        log.Info("Context cancelled, skipping reconcile")
        return ctrl.Result{}, nil
    }

    // ... reconciliation logic ...

    // For long operations, check context periodically
    if err := r.checkSSHConnection(ctx, machine); err != nil {
        if ctx.Err() != nil {
            // Shutdown in progress, don't update status
            return ctrl.Result{}, nil
        }
        // Handle actual error
        return ctrl.Result{}, err
    }

    return ctrl.Result{}, nil
}
```

### 32.5 SSH Operations with Context

```go
func (r *MachineReconciler) checkSSHConnection(ctx context.Context, machine *niov1alpha1.Machine) error {
    // Create timeout context for SSH operation
    sshCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    conn, err := r.SSHClient.Connect(sshCtx, machine.Spec.Host, machine.Spec.SSHUser, auth)
    if err != nil {
        // Check if it's a context cancellation (shutdown)
        if sshCtx.Err() != nil {
            return sshCtx.Err()
        }
        return fmt.Errorf("ssh connect: %w", err)
    }
    defer conn.Close()

    // Execute command with context
    _, _, _, err = conn.Execute(sshCtx, "echo ok")
    return err
}
```

### 32.6 Jobs Are Independent

Jobs don't need special shutdown handling because:

```yaml
# Job is an independent resource
apiVersion: batch/v1
kind: Job
metadata:
  name: config-apply-abc12
  ownerReferences:
    - apiVersion: nio.homystack.com/v1alpha1
      kind: NixosConfiguration
      name: my-config
      # Job survives NixosConfiguration changes
      # Only deleted when NixosConfiguration is deleted
spec:
  # Job has its own timeout
  activeDeadlineSeconds: 3600
  template:
    spec:
      # Pod restarts independently
      restartPolicy: Never
```

When operator restarts:
1. Job continues running
2. On next reconcile, operator checks Job status
3. If Job finished, operator updates NixosConfiguration status
4. If Job still running, operator waits

### 32.7 Helm Configuration

```yaml
# values.yaml
config:
  # Graceful shutdown timeout (time for in-flight reconciles to complete)
  gracefulShutdownTimeout: 30s

# Deployment
terminationGracePeriodSeconds: 35  # Slightly longer than gracefulShutdownTimeout
```

```yaml
# templates/deployment.yaml
spec:
  template:
    spec:
      terminationGracePeriodSeconds: 35
      containers:
        - name: manager
          args:
            - --graceful-shutdown-timeout={{ .Values.config.gracefulShutdownTimeout }}
```

### 32.8 Startup/Shutdown Sequence

```
STARTUP:
1. Manager starts
2. Leader election (if enabled)
3. Controllers start watching
4. Reconcilers begin processing queue

SHUTDOWN (SIGTERM received):
1. Context cancelled
2. Controllers stop accepting new work
3. In-flight reconciles continue (up to gracefulShutdownTimeout)
4. Leader election lease released
5. Manager.Start() returns
6. Process exits

JOBS DURING SHUTDOWN:
- Continue running (independent pods)
- On next operator startup, status is reconciled
- No data loss, no orphaned state
```

### 32.9 Implementation Checklist

- [ ] Configure `GracefulShutdownTimeout` in Manager options
- [ ] Set `LeaderElectionReleaseOnCancel: true`
- [ ] Add context cancellation checks in reconcilers
- [ ] Ensure SSH operations respect context
- [ ] Set `terminationGracePeriodSeconds` in Deployment
- [ ] Add `--graceful-shutdown-timeout` flag
- [ ] Add graceful shutdown config to values.yaml
- [ ] Test shutdown behavior (kill pod, verify Jobs continue)

## References

- [kstatus README](https://github.com/kubernetes-sigs/cli-utils/blob/master/pkg/kstatus/README.md)
- [CRD Status Convention](https://kpt.dev/reference/schema/crd-status-convention/)
- [Implementing observedGeneration](https://alenkacz.medium.com/kubernetes-operator-best-practices-implementing-observedgeneration-250728868792)
- [Status and Conditions Explained](https://superorbital.io/blog/status-and-conditions/)
- [Kubebuilder: Watching Externally Managed Resources](https://book.kubebuilder.io/reference/watching-resources/externally-managed)
- [Kubebuilder: Writing Controller Tests](https://book.kubebuilder.io/cronjob-tutorial/writing-tests)
- [envtest Documentation](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest)
- [Kubernetes: Garbage Collection](https://kubernetes.io/docs/concepts/architecture/garbage-collection/)
- [Kubernetes: Owner References](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/)
- [Helm Chart Best Practices](https://helm.sh/docs/chart_best_practices/)
- [Helm: CRD Management](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/)
