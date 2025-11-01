# NixOS Operator - Development Guide

This document describes tools and processes for developing and debugging the nixos-operator.

> **Note on Container Tools:** This project uses Podman by default for building OCI images, but you can use any OCI-compatible tool (Docker, Buildah, etc.). All `podman` commands can be replaced with `docker` if preferred. The `docker-compose.yml` file works with both `docker compose` and `podman compose`.

## 📁 Created Files

### 1. Kind Cluster Setup Script (`kind-setup.sh`)
Automates creation and configuration of a Kind cluster for operator testing.

**Usage:**
```bash
chmod +x kind-setup.sh
./kind-setup.sh
```

### 2. Compose for Development (`docker-compose.yml`)
Starts a complete development environment with Kind cluster and operator in debug mode.

**Usage:**
```bash
# Start the entire environment
docker-compose up -d

# Stop the environment
docker-compose down

# View operator logs
docker-compose logs -f nixos-operator-dev
```

### 3. VS Code Debug Configuration (`.vscode/`)
- `launch.json` - configurations for Python debugging
- `tasks.json` - tasks for development automation

## 🔧 Debugging Modes

### Local Debugging
1. Open the project in VS Code
2. Go to "Run and Debug" panel (Ctrl+Shift+D)
3. Select "NixOS Operator: Local Debug"
4. Click "Start Debugging"

### Remote Debugging in Container
1. Start docker-compose: `docker-compose up -d`
2. In VS Code select "NixOS Operator: Remote Debug (Container)"
3. Click "Start Debugging"

### Debugging with Kind Cluster
1. Select "NixOS Operator: Debug with Kind"
2. VS Code will automatically create Kind cluster and configure the environment

## 🚀 Quick Start

### Option 1: Simple Setup
```bash
# Create Kind cluster and start operator
./kind-setup.sh

# Apply examples for testing
kubectl apply -f examples/machine-example.yaml
kubectl apply -f examples/nixosconfiguration-example.yaml
```

### Option 2: Development with Compose
```bash
# Start complete development environment
docker-compose up -d

# Operator will be available for debugging on port 5678
# Kind cluster will be configured automatically
```

### Option 3: Local Development
```bash
# Install dependencies
pip install -r requirements.txt

# Run operator locally
python main.py
```

## 🐛 Debugging

### Breakpoints
Set breakpoints in `main.py` at the following locations:
- `on_nixosconfiguration_create` - configuration creation
- `on_machine_create` - machine creation
- `apply_nixos_configuration` - configuration application
- `check_machine_discoverable` - machine availability check

### Useful Debugging Commands
```bash
# View operator logs
kubectl logs -f deployment/nixos-operator -n nixos-operator-system

# Check CRD status
kubectl get machines,nixosconfigurations --all-namespaces

# Describe resources for diagnostics
kubectl describe machine <name>
kubectl describe nixosconfiguration <name>
```

## 📋 VS Code Tasks

Available tasks (Ctrl+Shift+P → "Tasks: Run Task"):
- `setup-k8s-environment` - configure Kubernetes environment
- `setup-kind-cluster` - create Kind cluster
- `start-compose-dev` - start Compose
- `build-operator-image` - build container image
- `install-dependencies` - install Python dependencies

## 🔍 Monitoring

After starting the operator, you can monitor its operation:

```bash
# View operator events
kubectl get events -n nixos-operator-system --sort-by='.lastTimestamp'

# Check pod status
kubectl get pods -n nixos-operator-system -w

# View filtered logs
kubectl logs -f deployment/nixos-operator -n nixos-operator-system | grep -E "(ERROR|WARNING|INFO)"
```

## 🛠️ Troubleshooting

### Issue: Kind Cluster Not Creating
**Solution:** Ensure your container runtime (Podman/Docker) is running and you have permissions to create containers.

### Issue: Operator Not Connecting to Kubernetes
**Solution:** Check KUBECONFIG configuration and ensure Kind cluster is running.

### Issue: Debugger Not Connecting
**Solution:** Ensure port 5678 is not occupied and Compose is running.

### Issue: CRDs Not Applying
**Solution:** Check access permissions and ensure you're in the correct namespace.

## 🏗️ Project Structure

```
nixos-operator/
├── main.py                    # Main operator entry point
├── machine_handlers.py        # Machine resource handlers
├── nixosconfiguration_handlers.py    # Configuration handlers
├── nixosconfiguration_job_handlers.py # Job-based configuration handlers
├── clients.py                 # Kubernetes client utilities
├── utils.py                   # Utility functions
├── events.py                  # Event handling
├── crds/                      # Custom Resource Definitions
│   ├── machine.yaml
│   └── nixosconfiguration.yaml
├── examples/                  # Example configurations
│   ├── machine-example.yaml
│   └── nixosconfiguration-example.yaml
├── scripts/                   # Helper scripts
│   ├── hardware_scanner.sh
│   └── facts_parser.py
└── .vscode/                   # VS Code configuration
    ├── launch.json
    └── tasks.json
```

## 🔄 Development Workflow

### 1. Set Up Development Environment
```bash
# Clone the repository
git clone <repository-url>
cd nixos-operator

# Set up development environment
./kind-setup.sh
```

### 2. Make Code Changes
- Modify Python files in the root directory
- Update CRDs in `crds/` if API changes are needed
- Add examples in `examples/` for new features

### 3. Test Changes
```bash
# Apply test resources
kubectl apply -f examples/machine-example.yaml
kubectl apply -f examples/nixosconfiguration-example.yaml

# Monitor operator behavior
kubectl logs -f deployment/nixos-operator -n nixos-operator-system
```

### 4. Build and Deploy
```bash
# Build container image (use podman or docker)
podman build -t nixos-operator:latest .

# Update deployment
kubectl rollout restart deployment/nixos-operator -n nixos-operator-system
```

## 🧪 Testing

### Unit Tests
[Add unit testing instructions here]

### Integration Tests
[Add integration testing instructions here]

### End-to-End Tests
[Add E2E testing instructions here]

## 📝 Code Style

- Follow PEP 8 for Python code
- Use type hints where possible
- Document public functions and classes
- Keep functions focused and single-purpose

## 🔧 Dependencies

Key dependencies:
- `kopf` - Kubernetes operator framework
- `kubernetes` - Kubernetes Python client
- `paramiko` - SSH client library
- `pyyaml` - YAML parsing

Install all dependencies:
```bash
pip install -r requirements.txt
```

## 🚀 Deployment

### Production Deployment
```bash
# Build production image (use podman or docker)
podman build -t nixos-operator:latest .

# Apply to production cluster
kubectl apply -f crds/
kubectl apply -f deployment.yaml
```

### Development Deployment
```bash
# Use development setup (use podman compose or docker compose)
podman compose up -d
```

## 📚 Additional Resources

- [Kopf Documentation](https://kopf.readthedocs.io/)
- [Kubernetes Operators](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [NixOS Documentation](https://nixos.org/learn/)
- [Kind Documentation](https://kind.sigs.k8s.io/)
