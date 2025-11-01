#!/bin/bash

# Скрипт для запуска nixos-operator в kind кластере
set -e

echo "🚀 Настройка nixos-operator в kind кластере..."

# Проверка наличия kind
if ! command -v kind &> /dev/null; then
    echo "❌ kind не установлен. Установите kind: https://kind.sigs.k8s.io/docs/user/quick-start/"
    exit 1
fi

# Проверка наличия kubectl
if ! command -v kubectl &> /dev/null; then
    echo "❌ kubectl не установлен. Установите kubectl: https://kubernetes.io/docs/tasks/tools/"
    exit 1
fi

# Проверка наличия Podman
if ! command -v podman &> /dev/null; then
    echo "❌ Podman не установлен. Установите Podman: https://podman.io/getting-started/installation"
    exit 1
fi

# Создание kind кластера если не существует
CLUSTER_NAME="nixos-operator-cluster"
if ! kind get clusters | grep -q "$CLUSTER_NAME"; then
    echo "📦 Создание kind кластера: $CLUSTER_NAME"
    cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 8080
    protocol: TCP
  - containerPort: 443
    hostPort: 8443
    protocol: TCP
EOF
else
    echo "✅ Kind кластер $CLUSTER_NAME уже существует"
fi

# Установка контекста
kubectl config use-context "kind-$CLUSTER_NAME"

# Создание namespace для оператора
echo "📁 Создание namespace..."
kubectl create namespace nixos-operator-system --dry-run=client -o yaml | kubectl apply -f -

# Применение CRD
echo "📋 Применение Custom Resource Definitions..."
kubectl apply -f crds/

# Сборка образа оператора
echo "🐳 Сборка образа оператора..."
podman build -t nixos-operator:latest .

# Загрузка образа в kind кластер
echo "📤 Загрузка образа в kind кластер..."
podman save nixos-operator:latest | kind load image-archive /dev/stdin --name "$CLUSTER_NAME"

# Применение deployment
echo "🚀 Запуск оператора..."
kubectl apply -f deployment.yaml

# Ожидание готовности оператора
echo "⏳ Ожидание готовности оператора..."
kubectl wait --for=condition=available deployment/nixos-operator -n nixos-operator-system --timeout=300s

# Проверка статуса
echo "🔍 Проверка статуса оператора..."
kubectl get pods -n nixos-operator-system

echo ""
echo "✅ nixos-operator успешно запущен в kind кластере!"
echo ""
echo "📝 Для тестирования можно использовать примеры из examples/ директории:"
echo "   kubectl apply -f examples/machine-example.yaml"
echo "   kubectl apply -f examples/nixosconfiguration-example.yaml"
echo ""
echo "🔍 Для просмотра логов оператора:"
echo "   kubectl logs -f deployment/nixos-operator -n nixos-operator-system"
