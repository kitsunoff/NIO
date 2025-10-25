FROM python:3.11-slim

# Установка системных зависимостей для разработки
RUN apt-get update && apt-get install -y \
    git \
    xz-utils \
    openssh-client \
    curl \
    wget \
    vim \
    kubectl \
    dnsmasq \
    iproute2 \
    && rm -rf /var/lib/apt/lists/*

# Установка kind для управления кластером
RUN curl -Lo /usr/local/bin/kind https://kind.sigs.k8s.io/dl/v0.20.0/kind-linux-amd64 \
    && chmod +x /usr/local/bin/kind

# Установка debugpy для отладки Python
RUN pip install debugpy

# Создание рабочей директории
WORKDIR /app

# Копирование зависимостей
COPY requirements.txt .

# Установка Python зависимостей
RUN pip install --no-cache-dir -r requirements.txt

# Копирование исходного кода оператора
COPY . .
COPY crds/ ./crds/

# Создание пользователя для безопасности (но с правами для разработки)
# RUN groupadd -r operator && useradd -r -g operator operator
# USER operator

# Команда по умолчанию (будет переопределена в docker-compose)
CMD ["python", "main.py"]
