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
COPY --chown=operator_user:operator_group main.py .
COPY --chown=operator_user:operator_group machine_handlers.py .
COPY --chown=operator_user:operator_group nixosconfiguration_handlers.py .
COPY --chown=operator_user:operator_group clients.py .
COPY --chown=operator_user:operator_group utils.py .
COPY --chown=operator_user:operator_group events.py .
COPY --chown=operator_user:operator_group scripts/ ./scripts/
COPY --chown=operator_user:operator_group crds/ ./crds/

USER operator_user
ENV KUBECONFIG=/app/.kube/config PYTHONUNBUFFERED=1 PYTHONPATH=/app
CMD ["python", "main.py"]
