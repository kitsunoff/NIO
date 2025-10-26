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
    && rm -rf /var/lib/apt/lists/*

# Установка kind для управления кластером
RUN curl -Lo /usr/local/bin/kind https://kind.sigs.k8s.io/dl/v0.20.0/kind-linux-amd64   \
    && chmod +x /usr/local/bin/kind

# Установка debugpy для отладки Python

# Создание пользователя и группы для безопасности (с UID/GID 1000)
# Сначала попробуем создать группу с GID 1000, если она не существует
# Если groupadd не сработает, значит GID 1000 уже занят другой группой
RUN set -ex; \
    if ! getent group 1000 > /dev/null 2>&1; then \
        groupadd -g 1000 operator_group; \
        USER_GID=1000; \
    else \
        # Если GID 1000 уже занят, используем другую группу для пользователя
        # или просто используем группу, которая уже есть (например, staff в python:slim)
        # Найдём имя группы с GID 1000
        EXISTING_GROUP_NAME=$(getent group 1000 | cut -d: -f1); \
        echo "Группа с GID 1000 уже существует: $EXISTING_GROUP_NAME"; \
        USER_GID=$(getent group $EXISTING_GROUP_NAME | cut -d: -f3); \
    fi; \
    # Теперь создаём пользователя с UID 1000 и используем найденную/созданную группу
    # Если мы создали operator_group, USER_GID будет 1000
    # Если группа уже существовала, USER_GID будет 1000
    USER_UID=1000; \
    # Используем имя группы, которое мы нашли или создали
    EXISTING_GROUP_NAME=${EXISTING_GROUP_NAME:-operator_group}; \
    useradd -u $USER_UID -g $EXISTING_GROUP_NAME -m -s /bin/bash operator_user; \
    echo "Создан пользователь operator_user с UID $USER_UID и GID $USER_GID в группе $EXISTING_GROUP_NAME"

# Создание рабочей директории
WORKDIR /app

# Копирование зависимостей
COPY requirements.txt .

# Установка Python зависимостей
RUN pip install --no-cache-dir -r requirements.txt

# Копирование исходного кода оператора
COPY . .
COPY crds/ ./crds/

# Меняем владельца рабочей директории на нового пользователя
RUN chown -R operator_user:$EXISTING_GROUP_NAME /app

# Переход к пользователю operator_user
USER operator_user

# Команда по умолчанию (будет переопределена в docker-compose)
CMD ["python", "main.py"]