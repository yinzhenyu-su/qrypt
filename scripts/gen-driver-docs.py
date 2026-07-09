#!/usr/bin/env python3
"""Generate docs/for-user/support-drivers.md from qrypt.schema.json."""

import json
import os

SCHEMA_PATH = os.path.join(os.path.dirname(__file__), "..", "qrypt.schema.json")
OUTPUT_PATH = os.path.join(os.path.dirname(__file__), "..", "docs", "for-user", "support-drivers.md")

DRIVER_META = {
    "localfs":       {"name": "本地目录",                "type": "localfs"},
    "aliyundrive":   {"name": "阿里云盘",                "type": "aliyundrive"},
    "baiduNetdisk":  {"name": "百度网盘",                "type": "baidu_netdisk"},
    "quark":         {"name": "夸克网盘",                "type": "quark"},
    "yun139":        {"name": "天翼云盘",                "type": "yun139"},
    "p115":          {"name": "115 云盘",                "type": "115"},
    "p189":          {"name": "天翼云盘 189",            "type": "189"},
    "webdav":        {"name": "WebDAV",                  "type": "webdav"},
    "s3":            {"name": "Amazon S3 / 兼容 S3",     "type": "s3"},
}

DRIVER_ORDER = ["localfs", "aliyundrive", "baiduNetdisk", "quark", "yun139", "p115", "p189", "webdav", "s3"]


def load_schema():
    with open(SCHEMA_PATH) as f:
        return json.load(f)


def fmt_default(v):
    if v is None:
        return ""
    if isinstance(v, bool):
        return str(v).lower()
    return str(v)


def fmt_type(params_def, param_name, prop):
    return prop.get("type", "string")


def build_driver_list_table(defs):
    """Build the driver overview table."""
    rows = []
    rows.append("| 驱动类型 | 名称 | 必填参数 |")
    rows.append("|---|---|---|")
    for key in DRIVER_ORDER:
        meta = DRIVER_META[key]
        def_name = f"{key}Params"
        if def_name not in defs:
            continue
        required = ", ".join(f"`{r}`" for r in defs[def_name].get("required", []))
        rows.append(f"| `{meta['type']}` | {meta['name']} | {required} |")
    return "\n".join(rows)


def toml_value(val, prop_type):
    if prop_type == "boolean":
        return str(val).lower() if isinstance(val, bool) else val
    return f'"{val}"'


def build_driver_section(defs, key):
    meta = DRIVER_META[key]
    def_name = f"{key}Params"
    param_def = defs[def_name]
    props = param_def.get("properties", {})
    required_set = set(param_def.get("required", []))

    example_lines = ["[mounts.params]"]
    for pname, prop in props.items():
        prop_type = prop.get("type", "string")
        if pname in required_set:
            val = prop.get("x-example", prop.get("example", "..."))
            example_lines.append(f'{pname} = {toml_value(val, prop_type)}')
        else:
            val = prop.get("x-example", prop.get("example", prop.get("default", "")))
            if val != "":
                example_lines.append(f'# {pname} = {toml_value(val, prop_type)}')
    example_toml = "\n".join(example_lines)

    table_rows = []
    table_rows.append("| 参数 | 类型 | 必填 | 说明 |")
    table_rows.append("|---|---|---|---|")
    for pname, prop in props.items():
        is_required = "是" if pname in required_set else "否"
        t = fmt_type(param_def, pname, prop)
        type_str = t
        if prop.get("x-secret"):
            type_str += " (secret)"
        desc = prop.get("description", "").rstrip(".")
        default_val = fmt_default(prop.get("default"))
        if default_val:
            desc += f"，默认 `{default_val}`"
        table_rows.append(f"| `{pname}` | {type_str} | {is_required} | {desc} |")
    param_table = "\n".join(table_rows)

    return f"""---

## {meta['type']}

{meta['name']}。

```toml
{example_toml}
```

{param_table}
"""


def generate():
    schema = load_schema()
    defs = schema.get("definitions", {})

    lines = []
    lines.append("# 支持的驱动")
    lines.append("")
    lines.append("以下文档由 `qrypt.schema.json` 自动生成。")
    lines.append("")
    lines.append("qrypt 支持以下云盘后端。每个驱动通过配置文件中的 `[[mounts]]` 条目使用，`type` 字段指定驱动类型，`params` 中填写对应参数。")
    lines.append("")
    lines.append("## 驱动列表")
    lines.append("")
    lines.append(build_driver_list_table(defs))
    lines.append("")

    for key in DRIVER_ORDER:
        lines.append(build_driver_section(defs, key))

    content = "\n".join(lines)
    idx = content.find("\n---\n")
    if idx != -1:
        content = content[:idx] + "\n" + content[idx + 5:]

    with open(OUTPUT_PATH, "w") as f:
        f.write(content)
    print(f"Generated {OUTPUT_PATH}")


if __name__ == "__main__":
    generate()
