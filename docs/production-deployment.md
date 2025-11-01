# Production Deployment Guide

This guide covers deploying the NixOS Infrastructure Operator in a production Kubernetes environment with full observability and security.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Deployment Steps](#deployment-steps)
- [Monitoring Setup](#monitoring-setup)
- [Security Considerations](#security-considerations)
- [Resource Planning](#resource-planning)
- [Troubleshooting](#troubleshooting)
- [Operational Best Practices](#operational-best-practices)

## Prerequisites

### Kubernetes Cluster Requirements

- Kubernetes 1.24+
- CNI plugin configured (Calico, Cilium, or similar)
- StorageClass for persistent volumes (if needed for config storage)
- Network connectivity to target machines via SSH

### Required Cluster Components

1. **Prometheus Operator** (recommended for monitoring)
   ```bash
   kubectl apply --filename https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/main/bundle.yaml
   ```

2. **Grafana** (recommended for dashboards)
   ```bash
   kubectl apply --filename https://raw.githubusercontent.com/grafana/grafana/main/deploy/kubernetes/grafana.yaml
   ```

### External Dependencies

- **SSH Access**: Operator needs SSH access to target NixOS machines
- **Git Repositories**: Access to configuration Git repositories (GitHub, GitLab, etc.)
- **Container Registry Access**: Access to `ghcr.io/homystack/nio` images

## Deployment Steps

### Step 1: Create Namespace

```bash
kubectl create namespace nixos-operator-system
```

### Step 2: Configure SSH Credentials

Create a Secret with SSH private key for accessing target machines:

```bash
kubectl create secret generic nixos-ssh-key \
  --from-file=ssh-privatekey=/path/to/ssh/key \
  --namespace nixos-operator-system
```

Mount this secret in the operator Deployment by adding to `deployment.yaml`:

```yaml
spec:
  template:
    spec:
      volumes:
      - name: ssh-key
        secret:
          secretName: nixos-ssh-key
          defaultMode: 0600
      containers:
      - name: operator
        volumeMounts:
        - name: ssh-key
          mountPath: /etc/ssh-key
          readOnly: true
        env:
        - name: SSH_KEY_PATH
          value: /etc/ssh-key/ssh-privatekey
```

### Step 3: Apply Custom Resource Definitions

```bash
kubectl apply --filename crds/
```

Verify CRDs are created:

```bash
kubectl get crds | grep nio.homystack.com
```

Expected output:

```text
machines.nio.homystack.com
nixosconfigurations.nio.homystack.com
```

### Step 4: Deploy Operator

```bash
kubectl apply --filename deployment.yaml
```

This creates:

- ServiceAccount with RBAC permissions
- Deployment with operator pod
- Service for Prometheus metrics (port 8000)
- Service for health checks (port 8080)

### Step 5: Verify Deployment

Check operator pod is running:

```bash
kubectl get pods --namespace nixos-operator-system
```

Check operator logs:

```bash
kubectl logs --namespace nixos-operator-system deployment/nixos-operator --follow
```

Look for these initialization messages:

```console
INFO:__main__:NixOS Infrastructure Operator starting
INFO:__main__:Prometheus metrics server started on port 8000
INFO:__main__:Health check server started on port 8080
INFO:kopf.objects:Handler 'configure' succeeded.
```

### Step 6: Verify Health Endpoints

Test health endpoints:

```bash
# Port-forward health service
kubectl port-forward --namespace nixos-operator-system service/nixos-operator-health 8080:8080

# In another terminal, test endpoints
curl http://localhost:8080/health   # Should return {"status": "healthy"}
curl http://localhost:8080/ready    # Should return {"status": "ready"}
curl http://localhost:8080/live     # Should return {"status": "alive"}
```

## Monitoring Setup

### Step 1: Deploy ServiceMonitor

For Prometheus Operator integration:

```bash
kubectl apply --filename monitoring/service-monitor.yaml
```

This configures Prometheus to scrape metrics from the operator automatically.

### Step 2: Deploy Alerting Rules

```bash
kubectl apply --filename monitoring/prometheus-rules.yaml
```

This creates PrometheusRule with alerts for:

- Operator health and readiness
- High reconciliation failure rates
- SSH connection failures
- NixOS build failures
- High error rates

### Step 3: Import Grafana Dashboard

1. Access Grafana UI
2. Navigate to **Dashboards > Import**
3. Upload `monitoring/grafana/nio-operator-dashboard.json`
4. Select Prometheus datasource
5. Click **Import**

The dashboard provides:
- Overview of managed machines and configurations
- Reconciliation performance metrics
- SSH and Git operation metrics
- NixOS build duration and success rates
- Error and retry tracking

### Step 4: Configure Alertmanager

Add AlertManager route for operator alerts:

```yaml
route:
  receiver: 'default'
  routes:
  - match:
      alertname: NixOSOperatorDown
    receiver: 'critical-alerts'
    continue: true
  - match:
      component: operator
    receiver: 'operator-team'
```

## Security Considerations

### 1. SSH Key Management

**Best Practices:**
- Use dedicated SSH keys per cluster/environment
- Rotate SSH keys periodically (recommended: every 90 days)
- Use read-only keys where possible
- Enable SSH key passphrase protection if operator supports it

**Avoid:**
- Sharing SSH keys across environments
- Using personal SSH keys in production
- Storing unencrypted keys in version control

### 2. Network Security

**Firewall Rules:**
```bash
# Allow operator to SSH to target machines
iptables -A OUTPUT -p tcp --dport 22 -j ACCEPT

# Allow Prometheus to scrape metrics
iptables -A INPUT -p tcp --dport 8000 -j ACCEPT

# Allow health check probes
iptables -A INPUT -p tcp --dport 8080 -j ACCEPT
```

**NetworkPolicy (recommended):**
```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nixos-operator-netpol
  namespace: nixos-operator-system
spec:
  podSelector:
    matchLabels:
      app: nixos-operator
  policyTypes:
  - Ingress
  - Egress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          name: monitoring
    ports:
    - protocol: TCP
      port: 8000  # Metrics
    - protocol: TCP
      port: 8080  # Health checks
  egress:
  - to:
    - namespaceSelector: {}
    ports:
    - protocol: TCP
      port: 443  # Kubernetes API
  - to: []  # Allow all egress for SSH to external machines
    ports:
    - protocol: TCP
      port: 22
```

### 3. RBAC Permissions

The operator requires these minimum permissions:

```yaml
rules:
- apiGroups: ["nio.homystack.com"]
  resources: ["machines", "nixosconfigurations"]
  verbs: ["get", "list", "watch", "update", "patch"]
- apiGroups: ["nio.homystack.com"]
  resources: ["machines/status", "nixosconfigurations/status"]
  verbs: ["update", "patch"]
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list"]  # For SSH keys
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create"]  # For Kubernetes events
```

**Security hardening:**
- Do not grant cluster-admin permissions
- Use namespaced Roles instead of ClusterRoles if possible
- Audit RBAC permissions regularly

### 4. Pod Security

The deployment includes security context:

```yaml
securityContext:
  runAsUser: 1000
  runAsGroup: 1000
  runAsNonRoot: true
  allowPrivilegeEscalation: false
  capabilities:
    drop:
    - ALL
  readOnlyRootFilesystem: false  # Required for /tmp writes
```

**Pod Security Standards:**
- Enable **Restricted** Pod Security Standard where possible
- Consider **Baseline** if operator requires specific capabilities

## Resource Planning

### CPU and Memory Requirements

**Minimum (for testing/development):**
```yaml
resources:
  requests:
    memory: "128Mi"
    cpu: "100m"
  limits:
    memory: "512Mi"
    cpu: "500m"
```

**Recommended (production):**
```yaml
resources:
  requests:
    memory: "256Mi"
    cpu: "250m"
  limits:
    memory: "1Gi"
    cpu: "1000m"
```

**Sizing Guidelines:**
- Base memory: 128Mi
- Per managed machine: +20Mi
- Per active reconciliation: +50Mi
- NixOS build operations can spike CPU usage significantly

Example: Managing 20 machines with 5 concurrent reconciliations:
- Memory: 128 + (20 × 20) + (5 × 50) = 778Mi → round to 1Gi limit
- CPU: 250m base + bursting to 1000m for builds

### High Availability

For production HA setup:

```yaml
spec:
  replicas: 1  # Operator uses leader election, multiple replicas supported
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
```

**Note:** Kopf framework handles leader election automatically. Multiple replicas can be deployed for HA, but only one will be active (leader) at a time.

### Storage Considerations

The operator uses `/tmp` for:
- Git repository clones
- SSH known_hosts storage
- Temporary configuration files

**Recommendations:**
- Mount emptyDir volume for `/tmp` with size limit
- Use tmpfs for better performance (ephemeral data)

```yaml
volumes:
- name: tmp
  emptyDir:
    sizeLimit: 1Gi
    medium: Memory  # Optional: use tmpfs
containers:
- name: operator
  volumeMounts:
  - name: tmp
    mountPath: /tmp
```

## Troubleshooting

### Common Issues

#### 1. Operator Pod CrashLoopBackOff

**Symptoms:**
```bash
kubectl get pods -n nixos-operator-system
# NAME                              READY   STATUS             RESTARTS
# nixos-operator-7d8f9c5b6d-abc12   0/1     CrashLoopBackOff   5
```

**Diagnosis:**
```bash
kubectl logs -n nixos-operator-system nixos-operator-7d8f9c5b6d-abc12 --previous
```

**Common causes:**
- Missing SSH key secret
- Invalid Kubernetes API permissions
- Python dependency errors

**Resolution:**
- Verify SSH secret exists: `kubectl get secret nixos-ssh-key -n nixos-operator-system`
- Check RBAC: `kubectl auth can-i list machines.nio.homystack.com --as=system:serviceaccount:nixos-operator-system:nixos-operator`
- Review image version and dependencies

#### 2. Machines Not Discoverable

**Symptoms:**
- Metric `nio_machines_discoverable == 0`
- Logs show SSH connection failures

**Diagnosis:**
```bash
# Check SSH connectivity from operator pod
kubectl exec -n nixos-operator-system deployment/nixos-operator -- ssh -i /etc/ssh-key/ssh-privatekey user@target-machine
```

**Common causes:**
- Network firewall blocking port 22
- SSH key not authorized on target machine
- Target machine powered off or unreachable

**Resolution:**
- Verify network connectivity
- Add SSH public key to `~/.ssh/authorized_keys` on target
- Check target machine status and network configuration

#### 3. High Reconciliation Failure Rate

**Symptoms:**
- Alert: `HighReconciliationFailureRate`
- Metrics show `nio_configurations_failed_total` increasing

**Diagnosis:**
```bash
# Check reconciliation error metrics
kubectl port-forward -n nixos-operator-system service/nixos-operator-metrics 8000:8000
curl http://localhost:8000/metrics | grep nio_reconcile_errors_total
```

**Common causes:**
- Invalid NixOS configurations
- Git repository access issues
- NixOS build failures on target machines

**Resolution:**
- Review operator logs for specific error messages
- Validate NixOS configuration syntax
- Check Git repository access and credentials
- Verify target machine has sufficient resources for builds

#### 4. Prometheus Not Scraping Metrics

**Symptoms:**
- Grafana dashboard shows "No data"
- Prometheus targets page shows operator as "Down"

**Diagnosis:**
```bash
# Verify metrics endpoint is accessible
kubectl port-forward -n nixos-operator-system service/nixos-operator-metrics 8000:8000
curl http://localhost:8000/metrics
```

**Common causes:**
- ServiceMonitor not created or misconfigured
- Prometheus not configured to discover ServiceMonitors
- Network policy blocking Prometheus

**Resolution:**
- Verify ServiceMonitor: `kubectl get servicemonitor -n nixos-operator-system`
- Check Prometheus operator logs
- Review NetworkPolicy configuration

### Debug Mode

Enable verbose logging:

```yaml
env:
- name: LOG_LEVEL
  value: "DEBUG"
```

This will log detailed information about:
- SSH connection attempts
- Git clone operations
- NixOS build commands
- Reconciliation decisions

## Operational Best Practices

### 1. Backup and Disaster Recovery

**What to back up:**
- Custom Resource definitions (CRDs)
- Machine and NixOSConfiguration resources
- SSH keys (encrypted)
- Monitoring configuration

**Backup commands:**
```bash
# Backup all custom resources
kubectl get machines,nixosconfigurations -A -o yaml > nio-resources-backup.yaml

# Backup monitoring configuration
kubectl get servicemonitor,prometheusrule -n nixos-operator-system -o yaml > nio-monitoring-backup.yaml
```

### 2. Rolling Updates

When updating the operator:

```bash
# Apply new deployment
kubectl apply --filename deployment.yaml

# Watch rollout
kubectl rollout status deployment/nixos-operator -n nixos-operator-system

# Rollback if needed
kubectl rollout undo deployment/nixos-operator -n nixos-operator-system
```

**Zero-downtime updates:**
- Operator uses leader election (multiple replicas safe)
- Active reconciliations complete before pod termination
- 5-second grace period configured in cleanup handler

### 3. Monitoring and Alerting

**Key metrics to monitor:**
- `nio_machines_total` - Total managed machines
- `nio_machines_discoverable` - Reachable machines
- `rate(nio_configurations_applied_total[5m])` - Success rate
- `rate(nio_configurations_failed_total[5m])` - Failure rate
- `nio_reconcile_duration_seconds` - Performance

**Critical alerts:**
- `NixOSOperatorDown` - Operator unavailable
- `HighReconciliationFailureRate` - Configuration issues
- `SSHConnectionsCompletelyFailing` - Network/access problems

### 4. Scaling Considerations

**Horizontal scaling:**
- Operator supports multiple replicas with leader election
- Only active leader performs reconciliations
- Standby replicas ready for failover

**Vertical scaling:**
- Increase CPU limits for faster NixOS builds
- Increase memory for managing more machines
- Monitor resource usage and adjust

**Performance tuning:**
```yaml
env:
- name: NIO_MACHINE_DISCOVERY_INTERVAL
  value: "60"  # Seconds between discovery checks
- name: NIO_CONFIG_RECONCILE_INTERVAL
  value: "120"  # Seconds between config reconciliation
- name: NIO_RETRY_MAX_ATTEMPTS
  value: "3"
```

### 5. Logging and Auditing

**Centralized logging:**
- Ship logs to Loki, Elasticsearch, or CloudWatch
- Retain logs for compliance requirements
- Enable structured logging for parsing

**Audit trail:**
- Kubernetes Events created for major operations
- Metrics track all operations with labels
- Git commits provide configuration change history

### 6. Security Auditing

**Regular security tasks:**
- Rotate SSH keys every 90 days
- Review RBAC permissions quarterly
- Scan container images for vulnerabilities
- Update dependencies regularly

**Security scanning:**
```bash
# Scan operator image for vulnerabilities
trivy image ghcr.io/homystack/nio:main

# Check for outdated Python dependencies
kubectl exec -n nixos-operator-system deployment/nixos-operator -- pip list --outdated
```

## Configuration Reference

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `METRICS_PORT` | `8000` | Prometheus metrics HTTP port |
| `HEALTH_CHECK_PORT` | `8080` | Health check endpoints HTTP port |
| `NIO_BASE_CONFIG_PATH` | `/tmp/nixos-config` | Base path for configuration storage |
| `NIO_KNOWN_HOSTS_PATH` | `/tmp/nio-ssh-known-hosts` | SSH known_hosts file path |
| `NIO_MACHINE_DISCOVERY_INTERVAL` | `60.0` | Machine discovery interval (seconds) |
| `NIO_HARDWARE_SCAN_INTERVAL` | `300.0` | Hardware scan interval (seconds) |
| `NIO_CONFIG_RECONCILE_INTERVAL` | `120.0` | Configuration reconciliation interval (seconds) |
| `NIO_NIXOS_APPLY_TIMEOUT` | `3600` | NixOS apply operation timeout (seconds) |
| `NIO_RETRY_MAX_ATTEMPTS` | `3` | Maximum retry attempts for operations |
| `NIO_RETRY_INITIAL_DELAY` | `2.0` | Initial retry delay (seconds) |
| `NIO_RETRY_MAX_DELAY` | `30.0` | Maximum retry delay (seconds) |
| `NIO_RETRY_EXPONENTIAL_BASE` | `2.0` | Exponential backoff base multiplier |

### Service Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 8000 | HTTP | Prometheus metrics endpoint (`/metrics`) |
| 8080 | HTTP | Health check endpoints (`/health`, `/ready`, `/live`) |

## Support and Resources

- **Documentation**: https://github.com/homystack/NIO/tree/main/docs
- **Issues**: https://github.com/homystack/NIO/issues
- **Source Code**: https://github.com/homystack/NIO

For production support, please open an issue with:
- Operator version
- Kubernetes version and platform
- Relevant logs and metrics
- Steps to reproduce the issue
