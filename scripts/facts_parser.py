#!/usr/bin/env python3
import sys
import re
import json
from collections import defaultdict

# Список ключей, значения которых — CSV и должны стать списками
ARRAY_KEYS = {
    "storage.filesystems",
    "network.dns_servers",
    # Можно добавить: "user.groups", "network.routes" и т.д.
}


def should_be_array(full_key: str, value: str) -> bool:
    """Определяет, нужно ли превратить значение в список."""
    if full_key in ARRAY_KEYS:
        return True
    # Эвристика: если значение содержит запятую и не выглядит как IP@iface
    if "," in value and not re.search(r"\d+\.\d+\.\d+\.\d+@", value):
        # Но не для всего: например, os.name может содержать запятую (редко)
        # Поэтому ограничим эвристику только "безопасными" префиксами
        if full_key.startswith(("storage.", "network.", "user.", "system.")):
            return True
    return False


def parse_value(full_key: str, value: str):
    """Преобразует строку в str, list или оставляет как есть."""
    if should_be_array(full_key, value):
        # Разделяем по запятой и убираем пробелы
        parts = [part.strip() for part in value.split(",") if part.strip()]
        return parts if parts else value  # если пусто — оставляем строку
    return value


def parse_facts(lines):
    result = {}
    groups = defaultdict(dict)

    for line in lines:
        line = line.strip()
        if not line or "=" not in line:
            continue
        key, raw_value = line.split("=", 1)
        value = parse_value(key, raw_value)

        if "." in key:
            prefix, subkey = key.split(".", 1)
            groups[prefix][subkey] = value
        else:
            result[key] = value

    # Слияние групп
    for prefix, subdict in groups.items():
        if prefix in result and isinstance(result[prefix], dict):
            result[prefix].update(subdict)
        else:
            result[prefix] = subdict

    return result


def main():
    if len(sys.argv) > 1:
        with open(sys.argv[1], "r") as f:
            lines = f.readlines()
    else:
        lines = sys.stdin.readlines()

    facts = parse_facts(lines)

    # Вывод в JSON (поддерживает списки)
    print(json.dumps(facts, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
