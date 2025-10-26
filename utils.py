#!/usr/bin/env python3

import git
import tempfile
import shutil
import urllib.parse
import os
import re
import hashlib
from typing import Dict, Optional, Tuple, List
from datetime import datetime

from clients import get_secret_data


def get_workdir_path(
    namespace: str, name: str, repo_name: str, commit_hash: str
) -> str:
    """Получить предсказуемый путь рабочей директории"""
    base_path = "/tmp/nixos-config"
    workdir = f"{base_path}/{namespace}/{name}/{repo_name}@{commit_hash}"
    os.makedirs(workdir, exist_ok=True)
    return workdir


def parse_flake_reference(flake_ref: str) -> Tuple[str, str, str]:
    """
    Парсит flake-ссылку и возвращает (repo_name, repo_url, commit_hash)

    Поддерживает форматы:
    - github:owner/repo#host
    - github:owner/repo/v1.0#host
    - github:owner/repo/abcdef123456#host
    - .#host (локальный)
    """
    if flake_ref.startswith("."):
        # Локальный flake
        return "local", ".", "local"

    # Извлекаем часть до #
    flake_parts = flake_ref.split("#", 1)
    flake_source = flake_parts[0]

    # Парсим источник
    if flake_source.startswith("github:"):
        # github:owner/repo или github:owner/repo/ref
        parts = flake_source[7:].split("/")
        owner = parts[0]
        repo = parts[1]
        repo_name = f"{owner}/{repo}"

        # Определяем ref (ветка/тег/коммит)
        if len(parts) > 2:
            ref = parts[2]
            # Проверяем, является ли ref коммитом (хеш из 40 символов)
            if re.match(r"^[a-f0-9]{40}$", ref):
                commit_hash = ref
            else:
                # Это ветка или тег - коммит будет определен позже
                commit_hash = "floating"
        else:
            # По умолчанию main ветка
            commit_hash = "floating"

        repo_url = f"https://github.com/{owner}/{repo}.git"
        return repo_name, repo_url, commit_hash

    # Для других источников можно добавить парсинг
    return "unknown", flake_source, "unknown"


def extract_repo_name_from_url(git_url: str) -> str:
    """Извлекает имя репозитория из Git URL"""
    # Убираем протокол и .git
    clean_url = re.sub(r"^https?://", "", git_url)
    clean_url = re.sub(r"\.git$", "", clean_url)

    # Извлекаем owner/repo
    parts = clean_url.split("/")
    if len(parts) >= 2:
        return f"{parts[-2]}/{parts[-1]}"

    return clean_url


def calculate_directory_hash(directory_path: str) -> str:
    """Вычисляет SHA256 хеш содержимого директории"""
    if not os.path.exists(directory_path):
        return ""

    hash_obj = hashlib.sha256()

    for root, dirs, files in os.walk(directory_path):
        # Сортируем для детерминированности
        dirs.sort()
        files.sort()

        for file in files:
            file_path = os.path.join(root, file)
            # Добавляем относительный путь к хешу
            rel_path = os.path.relpath(file_path, directory_path)
            hash_obj.update(rel_path.encode("utf-8"))

            # Добавляем содержимое файла
            try:
                with open(file_path, "rb") as f:
                    while chunk := f.read(8192):
                        hash_obj.update(chunk)
            except Exception:
                # Если файл нельзя прочитать, пропускаем
                pass

    return hash_obj.hexdigest()


async def clone_git_repo(
    git_url: str,
    credentials_ref: Optional[Dict] = None,
    namespace: str = "default",
    target_path: Optional[str] = None,
) -> Tuple[str, str]:
    """Клонировать Git репозиторий и вернуть путь и хеш коммита"""
    if target_path:
        work_dir = target_path
        # Если директория уже существует, используем её
        if os.path.exists(work_dir):
            try:
                repo = git.Repo(work_dir)
                commit_hash = repo.head.commit.hexsha
                return work_dir, commit_hash
            except Exception:
                # Если репозиторий поврежден, удаляем и клонируем заново
                shutil.rmtree(work_dir, ignore_errors=True)
    else:
        # Старый режим с временными директориями (для обратной совместимости)
        work_dir = tempfile.mkdtemp(prefix="nixos-operator-")

    try:
        # Подготовка credentials если есть
        git_kwargs = {}
        if credentials_ref:
            secret_data = await get_secret_data(credentials_ref["name"], namespace)
            # Предполагаем, что в secret есть ssh-privatekey или token
            if "ssh-privatekey" in secret_data:
                ssh_key = secret_data["ssh-privatekey"]
                git_kwargs["env"] = {"GIT_SSH_COMMAND": f"ssh -i {ssh_key}"}
            elif "token" in secret_data:
                # Для HTTPS с токеном
                parsed_url = urllib.parse.urlparse(git_url)
                auth_url = f"{parsed_url.scheme}://token:{secret_data['token']}@{parsed_url.netloc}{parsed_url.path}"
                git_url = auth_url

        # Клонирование репозитория
        repo = git.Repo.clone_from(git_url, work_dir, **git_kwargs)
        commit_hash = repo.head.commit.hexsha

        return work_dir, commit_hash

    except Exception as e:
        if not target_path:  # Удаляем только временные директории
            shutil.rmtree(work_dir, ignore_errors=True)
        raise


async def get_remote_commit_hash(
    git_url: str,
    ref: str,
    credentials_ref: Optional[Dict] = None,
    namespace: str = "default",
) -> str:
    """Получить хеш коммита для указанной ветки/тега из удаленного репозитория"""
    try:
        # Создаем временную директорию для ls-remote
        temp_dir = tempfile.mkdtemp(prefix="nixos-operator-lsremote-")

        try:
            # Подготовка credentials если есть
            git_kwargs = {}
            if credentials_ref:
                secret_data = await get_secret_data(credentials_ref["name"], namespace)
                if "ssh-privatekey" in secret_data:
                    ssh_key = secret_data["ssh-privatekey"]
                    git_kwargs["env"] = {"GIT_SSH_COMMAND": f"ssh -i {ssh_key}"}
                elif "token" in secret_data:
                    parsed_url = urllib.parse.urlparse(git_url)
                    auth_url = f"{parsed_url.scheme}://token:{secret_data['token']}@{parsed_url.netloc}{parsed_url.path}"
                    git_url = auth_url

            # Выполняем git ls-remote
            repo = git.Repo.init(temp_dir)
            origin = repo.create_remote("origin", git_url)
            origin.fetch(**git_kwargs)

            # Ищем ref
            for ref_info in repo.git.ls_remote(git_url, ref).split("\n"):
                if ref_info:
                    parts = ref_info.split()
                    if len(parts) >= 2:
                        return parts[0]  # Хеш коммита

            raise Exception(f"Ref '{ref}' not found in repository")

        finally:
            shutil.rmtree(temp_dir, ignore_errors=True)

    except Exception as e:
        raise Exception(f"Failed to get remote commit hash for {ref}: {e}")
