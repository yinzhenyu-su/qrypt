#!/usr/bin/env python3
"""Validate qrypt's documentation schema metadata."""

import argparse
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent
DEFAULT_SCHEMA = ROOT / "schema" / "qrypt.schema.json"

CONFIG_DEFINITIONS = [
    "mountOptionsConfig",
    "mountConfig",
    "credentialConfig",
    "encryptionConfig",
    "storageConfig",
    "readCacheConfig",
    "thumbnailCacheConfig",
    "uploadConfig",
    "loggingConfig",
    "debugConfig",
    "timeConfig",
    "bandwidthConfig",
]


def zh_description(prop):
    return prop.get("x-i18n", {}).get("zh-CN", {}).get("description")


def check_schema(schema):
    errors = []
    definitions = schema.get("definitions", {})

    driver_defs = {
        name: definition
        for name, definition in definitions.items()
        if isinstance(definition, dict) and "x-driver" in definition
    }
    if not driver_defs:
        errors.append("no driver definitions with x-driver")
    for name, definition in definitions.items():
        if name.endswith("Params") and isinstance(definition, dict) and "x-driver" not in definition:
            errors.append(f"{name}: missing x-driver metadata")

    orders = {}
    for name, definition in driver_defs.items():
        order = definition.get("x-driver", {}).get("order")
        if order in orders:
            errors.append(f"{name}: duplicate x-driver.order {order} (also {orders[order]})")
        elif isinstance(order, int):
            orders[order] = name

    mount_config = definitions.get("mountConfig", {})
    mount_props = mount_config.get("properties", {})
    type_enum = set(
        mount_props.get("type", {}).get("enum", [])
    )
    all_of = mount_config.get("allOf", [])
    all_of_types = {
        entry.get("if", {}).get("properties", {}).get("type", {}).get("const")
        for entry in all_of
    }
    all_of_refs = {
        entry.get("then", {}).get("properties", {}).get("params", {}).get("$ref")
        for entry in all_of
    }

    credential_config = definitions.get("credentialConfig", {})
    credential_all_of = credential_config.get("allOf", [])
    credential_all_of_types = {
        entry.get("if", {}).get("properties", {}).get("type", {}).get("const")
        for entry in credential_all_of
    }
    credential_all_of_refs = {
        entry.get("then", {}).get("properties", {}).get("params", {}).get("$ref")
        for entry in credential_all_of
    }

    for name, definition in driver_defs.items():
        driver = definition.get("x-driver", {})
        driver_type = driver.get("type")
        if not driver_type:
            errors.append(f"{name}: missing x-driver.type")
            continue
        if not isinstance(driver.get("order"), int):
            errors.append(f"{name}: missing integer x-driver.order")
        if not definition.get("title"):
            errors.append(f"{name}: missing English title")
        if not definition.get("x-i18n", {}).get("zh-CN", {}).get("title"):
            errors.append(f"{name}: missing x-i18n.zh-CN.title")
        if driver_type not in type_enum:
            errors.append(f"{name}: x-driver.type {driver_type!r} missing from mountConfig.type.enum")
        if driver_type not in all_of_types:
            errors.append(f"{name}: missing allOf if/then block for {driver_type!r}")
        if f"#/definitions/{name}" not in all_of_refs:
            errors.append(f"{name}: missing allOf $ref to #/definitions/{name}")
        if driver_type not in credential_all_of_types:
            errors.append(f"{name}: missing credentialConfig allOf block for {driver_type!r}")
        if f"#/definitions/{name}" not in credential_all_of_refs:
            errors.append(f"{name}: missing credentialConfig allOf $ref to #/definitions/{name}")

        props = definition.get("properties", {})
        for pname, prop in props.items():
            if not prop.get("description"):
                errors.append(f"{name}.{pname}: missing English description")
            if not zh_description(prop):
                errors.append(f"{name}.{pname}: missing x-i18n.zh-CN.description")
            runtime_type = prop.get("x-param-type")
            if runtime_type is not None and runtime_type not in {
                "string", "int", "bool", "duration",
            }:
                errors.append(f"{name}.{pname}: unsupported x-param-type {runtime_type!r}")
            scope = prop.get("x-param-scope")
            if scope is None:
                errors.append(f"{name}.{pname}: missing x-param-scope")
            elif scope not in {"credential", "mount", "both"}:
                errors.append(f"{name}.{pname}: unsupported x-param-scope {scope!r}")

        for group_name in ("required", "anyOf"):
            groups = definition.get(group_name)
            if group_name == "required" and groups:
                groups = [groups]
            for group in groups or []:
                names = group.get("required", []) if isinstance(group, dict) else group
                for pname in names:
                    if pname not in props:
                        errors.append(f"{name}: {group_name} references unknown parameter {pname!r}")

    for def_name in CONFIG_DEFINITIONS:
        definition = definitions.get(def_name)
        if not definition:
            errors.append(f"missing definition {def_name}")
            continue
        if not definition.get("title"):
            errors.append(f"{def_name}: missing English title")
        if not definition.get("x-i18n", {}).get("zh-CN", {}).get("title"):
            errors.append(f"{def_name}: missing x-i18n.zh-CN.title")
        if not definition.get("x-i18n", {}).get("zh-CN", {}).get("description"):
            errors.append(f"{def_name}: missing x-i18n.zh-CN.description")
        for pname, prop in definition.get("properties", {}).items():
            if not prop.get("description"):
                errors.append(f"{def_name}.{pname}: missing English description")
            if not zh_description(prop):
                errors.append(f"{def_name}.{pname}: missing x-i18n.zh-CN.description")

    return errors


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--schema", type=Path, default=DEFAULT_SCHEMA)
    args = parser.parse_args()

    schema = json.loads(args.schema.read_text(encoding="utf-8"))
    errors = check_schema(schema)
    if errors:
        for error in errors:
            print(f"schema: {error}", file=sys.stderr)
        return 1
    print(f"schema ok: {args.schema}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
