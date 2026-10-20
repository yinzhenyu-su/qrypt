#!/usr/bin/env python3
"""Regression check: schema-only i18n changes reach both generated docs."""

import importlib.util
import json
import os
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent.parent


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def main():
    driver_docs = load_module("gen_driver_docs", ROOT / "scripts" / "schema" / "gen-driver-docs.py")
    config_docs = load_module("gen_config_docs", ROOT / "scripts" / "schema" / "gen-config-docs.py")

    schema = json.loads((ROOT / "schema" / "qrypt.schema.json").read_text(encoding="utf-8"))
    schema["definitions"]["localfsParams"]["properties"]["root_path"]["x-i18n"]["zh-CN"][
        "description"
    ] = "TEST_DRIVER_I18N"
    schema["definitions"]["mountOptionsConfig"]["properties"]["mount_point"]["x-i18n"][
        "zh-CN"
    ]["description"] = "TEST_CONFIG_I18N"

    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        schema_path = tmp / "qrypt.schema.json"
        schema_path.write_text(
            json.dumps(schema, ensure_ascii=False, indent=2), encoding="utf-8", newline="\n"
        )

        driver_output = tmp / "support-drivers.md"
        driver_docs.SCHEMA_PATH = str(schema_path)
        driver_docs.OUTPUT_PATH = str(driver_output)
        driver_docs.generate()
        if "TEST_DRIVER_I18N" not in driver_output.read_text(encoding="utf-8"):
            raise SystemExit("driver docs did not use the updated schema translation")

        config_output = tmp / "full-config.md"
        config_docs.SCHEMA_PATH = str(schema_path)
        config_docs.OUTPUT_PATH = str(config_output)
        config_docs.generate()
        if "TEST_CONFIG_I18N" not in config_output.read_text(encoding="utf-8"):
            raise SystemExit("config docs did not use the updated schema translation")

    print("generator i18n regression ok")


if __name__ == "__main__":
    main()
