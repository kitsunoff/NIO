# NixOS Infrastructure Operator - Usage Guide

## Overview

NixOS Infrastructure Operator (NIO) provides a GitOps approach to managing bare-metal and virtual machines running NixOS.

## Quick Start

### 1. Install the Operator

```bash
./install.sh
```

### 2. Create SSH Key Secret for Machine

```bash
# Create Secret with SSH key
kubectl create secret generic worker-ssh-key \
  --from-file=ssh-privatekey=~/.ssh/id_rsa \
  --namespace=default
```

### 3. Create Machine Resource

```bash
kubectl apply -f examples/machine-example.yaml
```

### 4. Create Git Credentials (if needed)

```bash
# For private repositories
kubectl create secret generic git-credentials \
  --from-literal=token=your-github-token \
  --namespace=default
```

### 5. Apply Configuration

```bash
kubectl apply -f examples/nixosconfiguration-example.yaml
```

## Monitoring Status

### Check Machine Status

```bash
kubectl get machine worker-01 -o yaml
```

Expected status after successful application:
```yaml
status:
  hasConfiguration: true
  appliedConfiguration: "worker-config"
  appliedCommit: "a1b2c3d4e5f6..."
  lastAppliedTime: "2025-01-21T08:30:00Z"
```

### Check NixosConfiguration Status

```bash
kubectl get nixosconfiguration worker-config -o yaml
```

## Key Features

### Commit Tracking

The operator always tracks the Git commit hash from which the configuration was applied. This ensures:

- **Reproducibility**: Any engineer can run `git show <commit>` to exactly reproduce the system state
- **Auditability**: Complete transparency of which configuration version is running on the machine
- **Reliability**: The hash is only saved after the entire operation completes successfully

### Two Application Modes

1. **Full Installation** (`fullInstall: true`):
   - Uses `nixos-anywhere --kexec`
   - Suitable for initial OS installation
   - Performs complete system reinstallation

2. **Update** (`fullInstall: false`):
   - Uses `nixos-rebuild switch --flake`
   - Suitable for updating existing systems
   - Preserves state and data

### Safe Deletion

When deleting NixosConfiguration:
- Machine status is cleaned
- Removal configuration is applied if specified (`onRemoveFlake`)
- Ensures clean state removal

## Advanced Scenarios

### Using Flakes

```yaml
spec:
  flake: ".#worker"
  fullInstall: false
```

### Additional Files

```yaml
additionalFiles:
- path: "secrets/database-password"
  value:
    secretRef:
      name: db-secret
- path: "config/custom.nix" 
  value:
    inline: |
      { config, pkgs, ... }:
      {
        services.postgresql.enable = true;
      }
- path: "facts/system-info"
  value:
    nixosFacter: true
```

### Removal Configuration

```yaml
spec:
  onRemoveFlake: "hosts/minimal.nix"
```

## Troubleshooting

### Check Operator Logs

```bash
kubectl logs -l app=nixos-operator -n nixos-operator-system
```

### Check Events

```bash
kubectl get events --field-selector involvedObject.name=worker-config
```

### Debug SSH Connection

Ensure that:
- Machine IP address is accessible from the Kubernetes cluster
- SSH key is correctly configured in the Secret
- SSH user has permission to execute commands

## Best Practices

1. **Use Git Tags**: For production, use tags instead of branches
2. **Secure Secrets**: Use external secret management systems
3. **Monitor Status**: Regularly check appliedCommit status
4. **Test Configurations**: Apply first in test environments
5. **Use Backup**: Configure safe removal procedures

## Example Workflow

1. Developer pushes changes to Git
2. Operator automatically detects changes
3. Configuration is applied to target machine
4. Commit hash is recorded in Machine status
5. Engineers can verify appliedCommit for audit purposes

## Resource Reference

### Machine Resource

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata:
  name: worker-01
spec:
  hostname: worker-01
  ipAddress: 192.168.1.100
  sshUser: root
  sshKeySecretRef:
    name: worker-ssh-key
```

### NixosConfiguration Resource

```yaml
apiVersion: nio.homystack.com/v1alpha1
kind: NixosConfiguration
metadata:
  name: worker-config
spec:
  gitRepo: "https://github.com/your-org/nixos-configs.git"
  flake: ".#worker"
  fullInstall: false
  machineRef:
    name: worker-01
  credentialsRef:
    name: git-credentials
```

## Common Issues

### Machine Not Discoverable

- Check network connectivity between Kubernetes cluster and target machine
- Verify SSH key is correctly configured
- Ensure SSH service is running on target machine

### Configuration Not Applied

- Check Git repository accessibility
- Verify credentials for private repositories
- Review operator logs for specific error messages

### Permission Denied

- Ensure SSH user has sudo privileges if required
- Check file permissions in target directories
- Verify SSH key permissions on the operator side
