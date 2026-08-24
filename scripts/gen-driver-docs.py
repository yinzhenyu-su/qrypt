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
    "onedrive":      {"name": "OneDrive",                "type": "onedrive"},
    "onedriveApp":   {"name": "OneDrive 应用权限",      "type": "onedrive_app"},
    "quark":         {"name": "夸克网盘",                "type": "quark"},
    "yun139":        {"name": "天翼云盘",                "type": "yun139"},
    "p115":          {"name": "115 云盘",                "type": "115"},
    "p115open":      {"name": "115 云盘（开放平台）",   "type": "115_open"},
    "p189":          {"name": "天翼云盘 189",            "type": "189"},
    "webdav":        {"name": "WebDAV",                  "type": "webdav"},
    "s3":            {"name": "Amazon S3 / 兼容 S3",     "type": "s3"},
}

DRIVER_ORDER = ["localfs", "aliyundrive", "baiduNetdisk", "onedrive", "onedriveApp", "quark", "yun139", "p115", "p115open", "p189", "webdav", "s3"]

# Optional per-driver notes rendered right after the driver heading.
DRIVER_INTRO = {
    "p115open": (
        "115 云盘（开放平台 API，`refresh_token` 认证）。使用官方 "
        "[115 开放平台](https://open.115.com) 的 Bearer token 认证，token 过期自动刷新并持久化，无需手动更新 cookie。\n\n"
        "**获取 refresh_token**:在 [open.115.com](https://open.115.com) 注册应用，或使用公共应用 ID，通过 PKCE 设备码扫码"
        "（115 App 扫码授权）获得 `access_token` 与 `refresh_token`。注意：同一应用同一账号最多持有 2 个 refresh_token，"
        "第 3 次获取会使第 1 个失效；每次刷新都会轮换（旧 refresh_token 立即作废）。"
    ),
}


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


def required_alternatives(param_def):
    alternatives = param_def.get("anyOf")
    if alternatives:
        return [option.get("required", []) for option in alternatives]
    return [param_def.get("required", [])]


def format_required(alternatives):
    parts = [
        " + ".join(f"`{name}`" for name in alternative)
        for alternative in alternatives
        if alternative
    ]
    return " 或 ".join(parts)


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
        required = format_required(required_alternatives(defs[def_name]))
        rows.append(f"| `{meta['type']}` | {meta['name']} | {required} |")
    return "\n".join(rows)


def toml_value(val, prop_type):
    if prop_type == "boolean":
        return str(val).lower() if isinstance(val, bool) else val
    if prop_type in ("integer", "number"):
        return val
    return f'"{val}"'


def build_driver_section(defs, key):
    meta = DRIVER_META[key]
    def_name = f"{key}Params"
    param_def = defs[def_name]
    props = param_def.get("properties", {})
    alternatives = required_alternatives(param_def)
    required_set = set(alternatives[0]) if len(alternatives) == 1 else set()
    conditional_set = set().union(*(set(alternative) for alternative in alternatives))

    intro = DRIVER_INTRO.get(key, "")
    if intro:
        intro = intro + "\n"


    example_lines = ["[mounts.params]"]
    if len(alternatives) > 1:
        example_lines.append(f"# 认证方式任选其一：{format_required(alternatives)}")
    for pname, prop in props.items():
        prop_type = prop.get("type", "string")
        if pname in required_set or (len(alternatives) > 1 and pname in alternatives[0]):
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
        if len(alternatives) > 1:
            is_required = "条件" if pname in conditional_set else "否"
        else:
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
    if len(alternatives) > 1:
        param_table = f"必填条件：{format_required(alternatives)}。\n\n{param_table}"

    return f"""---

## {meta['type']}

{meta['name']}。

{intro}```toml
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
