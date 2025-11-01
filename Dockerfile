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

# Create user
ARG USER_UID=1000
ARG USER_GID=1000
RUN groupadd -g ${USER_GID} operator_group \
    && useradd -u ${USER_UID} -g operator_group -m -s /bin/bash operator_user \
    && mkdir -p /home/operator_user/.kube /app/.kube \
    && chown -R operator_user:operator_group /home/operator_user /app


ADD https://install.determinate.systems/nix /tmp/nix-installer

# Install Nix (will be cached if installer doesn't change)
# SECURITY: Enable sandbox for build isolation
RUN chmod +x /tmp/nix-installer \
    && /tmp/nix-installer install linux \
        --extra-conf "sandbox = relaxed" \
        --extra-conf "filter-syscalls = true" \
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
COPY machine_handlers.py .
COPY nixosconfiguration_handlers.py .
COPY clients.py .
COPY utils.py .
COPY events.py .
COPY ssh_utils.py .
COPY known_hosts_manager.py .
COPY input_validation.py .
COPY scripts/ ./scripts/
COPY crds/ ./crds/

ENV KUBECONFIG=/app/.kube/config PYTHONUNBUFFERED=1 PYTHONPATH=/app
CMD ["python", "main.py"]
