#!/bin/bash

set -e

echo "Installing NixOS Infrastructure Operator..."

# Создание namespace
kubectl create namespace nixos-operator-system --dry-run=client -o yaml | kubectl apply -f -

# Применение CRD
echo "Applying CRDs..."
kubectl apply -f crds/

# Сборка и загрузка образа оператора
echo "Building operator image..."
podman build -t nixos-operator:latest .

# Если используется kind или minikube, загрузить образ в кластер
if command -v kind &> /dev/null; then
    echo "Loading image into kind cluster..."
    podman save nixos-operator:latest | kind load image-archive /dev/stdin
elif command -v minikube &> /dev/null; then
    echo "Loading image into minikube cluster..."
    minikube image load nixos-operator:latest
fi

# Применение развертывания
echo "Applying deployment..."
kubectl apply -f deployment.yaml

# Ожидание запуска оператора
echo "Waiting for operator to be ready..."
kubectl wait --for=condition=available deployment/nixos-operator -n nixos-operator-system --timeout=300s

echo "NixOS Infrastructure Operator installed successfully!"
echo ""
echo "To create a machine:"
echo "kubectl apply -f examples/machine-example.yaml"
echo ""
echo "To create a configuration:"
echo "kubectl apply -f examples/nixosconfiguration-example.yaml"
