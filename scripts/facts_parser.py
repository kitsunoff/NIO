#!/usr/bin/env python3
import sys
import re
import json
from collections import defaultdict

# List of keys whose values are CSV and should become arrays
ARRAY_KEYS = {
    "storage.filesystems",
    "network.dns_servers",
    # Can add: "user.groups", "network.routes", etc.
}


def should_be_array(full_key: str, value: str) -> bool:
    """Determines whether the value should be converted to a list."""
    if full_key in ARRAY_KEYS:
        return True
    # Heuristic: if value contains comma and doesn't look like IP@iface
    if "," in value and not re.search(r"\d+\.\d+\.\d+\.\d+@", value):
        # But not for everything: e.g., os.name might contain comma (rarely)
        # So limit heuristic to "safe" prefixes only
        if full_key.startswith(("storage.", "network.", "user.", "system.")):
            return True
    return False


def parse_value(full_key: str, value: str):
    """Converts string to str, list, or leaves as is."""
    if should_be_array(full_key, value):
        # Split by comma and remove spaces
        parts = [part.strip() for part in value.split(",") if part.strip()]
        return parts if parts else value  # if empty - leave as string
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

    # Merge groups
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

    # Output to JSON (supports lists)
    print(json.dumps(facts, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
