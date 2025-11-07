FROM python:3.11-slim

# Install system dependencies
RUN apt-get update && apt-get install -y \
    git xz-utils openssh-client curl wget vim kubectl \
    && rm -rf /var/lib/apt/lists/* \
    && apt-get clean

# Download Nix installer with checksum verification

# Install kind
ARG KIND_VERSION=v0.20.0
RUN curl -Lo /usr/local/bin/kind https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64 \
    && chmod +x /usr/local/bin/kind

# Create necessary directories
RUN mkdir -p /app/.kube

ADD https://install.determinate.systems/nix /tmp/nix-installer

# Install Nix (will be cached if installer doesn't change)
# SECURITY: Enable sandbox for build isolation
RUN chmod +x /tmp/nix-installer \
    && /tmp/nix-installer install linux \
        --extra-conf "sandbox = false" \
        --extra-conf "filter-syscalls = false" \
        --init none \
        --no-confirm \
    && rm -f /tmp/nix-installer

ENV PATH="${PATH}:/nix/var/nix/profiles/default/bin"

WORKDIR /app

# Copy only runtime-required files
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy only essential runtime files
COPY main.py .
COPY config.py .
COPY machine_handlers.py .
COPY nixosconfiguration_handlers.py .
COPY reconcile_helpers.py .
COPY retry_utils.py .
COPY metrics.py .
COPY clients.py .
COPY utils.py .
COPY events.py .
COPY ssh_utils.py .
COPY known_hosts_manager.py .
COPY input_validation.py .
COPY health.py .
COPY scripts/ ./scripts/
COPY crds/ ./crds/

# Expose metrics port
EXPOSE 8000

ENV KUBECONFIG=/app/.kube/config PYTHONUNBUFFERED=1 PYTHONPATH=/app
CMD ["python", "main.py"]
