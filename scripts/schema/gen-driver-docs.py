#!/usr/bin/env python3
"""Generate docs/for-user/support-drivers.md from schema/qrypt.schema.json.

Driver capabilities come from the Go implementation through
scripts/schema/gen-driver-capabilities.
"""

import json
import os
import subprocess
import sys

ROOT = os.path.join(os.path.dirname(__file__), "..", "..")
SCHEMA_PATH = os.path.join(ROOT, "schema", "qrypt.schema.json")
OUTPUT_PATH = os.path.join(ROOT, "docs", "for-user", "support-drivers.md")
CAPABILITIES_CMD = ["go", "run", "./scripts/schema/gen-driver-capabilities"]

# User-facing capability matrix. Keys are drive.Capability values, except
# "remote_hash", which comes from the drive.RemoteHasher interface.
CAPABILITY_COLUMNS = [
    ("writer", "写入"),
    ("resumable_uploader", "断点续传"),
    ("server_side_copy", "服务端复制"),
    ("remote_hash", "服务端哈希"),
    ("mtime", "保留修改时间"),
    ("space", "空间查询"),
    ("offline_download", "离线下载"),
]


def load_schema():
    with open(SCHEMA_PATH, encoding="utf-8") as f:
        return json.load(f)


def load_capabilities():
    """Return {driver type: {"capabilities": [...], "remote_hash": bool}}."""
    result = subprocess.run(
        CAPABILITIES_CMD,
        cwd=ROOT,
        capture_output=True,
        text=True,
        encoding="utf-8",
    )
    if result.returncode != 0:
        sys.stderr.write(result.stderr)
        raise SystemExit(f"failed to generate driver capabilities: {result.returncode}")
    return json.loads(result.stdout)


def driver_definitions(defs):
    drivers = [
        (name, definition)
        for name, definition in defs.items()
        if isinstance(definition, dict) and "x-driver" in definition
    ]
    drivers.sort(key=lambda item: item[1]["x-driver"].get("order", 0))
    return drivers


def driver_type(definition):
    return definition["x-driver"]["type"]


def driver_title(definition):
    return (
        definition.get("x-i18n", {}).get("zh-CN", {}).get("title")
        or definition.get("title")
        or driver_type(definition)
    )


def prop_description(prop):
    return (
        prop.get("x-i18n", {}).get("zh-CN", {}).get("description")
        or prop.get("description", "").rstrip(".")
    )


def has_capability(info, key):
    if key == "remote_hash":
        return info["remote_hash"]
    return key in info["capabilities"]


def build_capability_table(capabilities, drivers):
    """Build the user-facing driver capability matrix."""
    headers = " | ".join(label for _, label in CAPABILITY_COLUMNS)
    rows = [f"| 驱动 | {headers} |", "|---|" + "---|" * len(CAPABILITY_COLUMNS)]
    for _, definition in drivers:
        driver = driver_type(definition)
        info = capabilities.get(driver)
        if info is None:
            continue
        cells = [
            "✅" if has_capability(info, capability) else "—"
            for capability, _ in CAPABILITY_COLUMNS
        ]
        rows.append(f"| `{driver}` | " + " | ".join(cells) + " |")
    return "\n".join(rows)


def fmt_default(v):
    if v is None:
        return ""
    if isinstance(v, bool):
        return str(v).lower()
    return str(v)


def fmt_type(prop):
    return prop.get("type", "string")


def merge_required(*groups):
    merged = []
    for group in groups:
        for name in group:
            if name not in merged:
                merged.append(name)
    return merged


def required_alternatives(param_def):
    required = param_def.get("required", [])
    alternatives = param_def.get("anyOf")
    if alternatives:
        return [merge_required(required, option.get("required", [])) for option in alternatives]
    return [required]


def format_required(alternatives):
    parts = [
        " + ".join(f"`{name}`" for name in alternative)
        for alternative in alternatives
        if alternative
    ]
    return " 或 ".join(parts)


def format_choice(param_def):
    """Format only the alternative-specific parameters, without the common ones."""
    common = set(param_def.get("required", []))
    parts = []
    for option in param_def.get("anyOf", []):
        names = [name for name in option.get("required", []) if name not in common]
        if names:
            parts.append(" + ".join(f"`{name}`" for name in names))
    return " 或 ".join(parts)


def format_required_condition(param_def):
    if len(required_alternatives(param_def)) == 1:
        return format_required(required_alternatives(param_def))
    common = " + ".join(f"`{name}`" for name in param_def.get("required", []))
    choice = format_choice(param_def)
    return f"{common} + ({choice})" if common else choice


def build_driver_list_table(drivers):
    """Build the driver overview table."""
    rows = []
    rows.append("| 驱动类型 | 名称 | 必填参数 |")
    rows.append("|---|---|---|")
    for _, definition in drivers:
        rows.append(
            f"| `{driver_type(definition)}` | {driver_title(definition)} | "
            f"{format_required_condition(definition)} |"
        )
    return "\n".join(rows)


def toml_value(val, prop_type):
    if prop_type == "boolean":
        return str(val).lower() if isinstance(val, bool) else val
    if prop_type in ("integer", "number"):
        return val
    return f'"{val}"'


def build_driver_section(definition):
    props = definition.get("properties", {})
    alternatives = required_alternatives(definition)
    always_required = set(definition.get("required", []))
    any_of = definition.get("anyOf", [])
    conditional_set = set()
    if any_of:
        conditional_set = (
            set().union(*(set(option.get("required", [])) for option in any_of))
            - always_required
        )
    first_choice = set(any_of[0].get("required", [])) if any_of else set()

    intro = definition.get("x-i18n", {}).get("zh-CN", {}).get("intro", "")
    if intro:
        intro = intro + "\n"

    example_lines = ["[mounts.params]"]
    if len(alternatives) > 1:
        example_lines.append(f"# 认证方式任选其一：{format_choice(definition)}")
    for pname, prop in props.items():
        prop_type = fmt_type(prop)
        if pname in always_required or pname in first_choice:
            val = prop.get("x-example", prop.get("example", "..."))
            example_lines.append(f'{pname} = {toml_value(val, prop_type)}')
        elif pname in conditional_set or pname == "root_path":
            val = prop.get("x-example", prop.get("example", prop.get("default", "")))
            if val != "":
                example_lines.append(f'# {pname} = {toml_value(val, prop_type)}')
    example_toml = "\n".join(example_lines)

    table_rows = []
    table_rows.append("| 参数 | 类型 | 必填 | 说明 |")
    table_rows.append("|---|---|---|---|")
    for pname, prop in props.items():
        if pname in always_required:
            is_required = "是"
        elif pname in conditional_set:
            is_required = "条件"
        else:
            is_required = "否"
        type_str = fmt_type(prop)
        if prop.get("x-secret"):
            type_str += " (secret)"
        desc = prop_description(prop)
        default_val = fmt_default(prop.get("default"))
        if default_val:
            desc += f"，默认 `{default_val}`"
        table_rows.append(f"| `{pname}` | {type_str} | {is_required} | {desc} |")
    param_table = "\n".join(table_rows)
    if len(alternatives) > 1:
        param_table = f"必填条件：{format_required_condition(definition)}。\n\n{param_table}"

    return f"""---

## {driver_type(definition)}

{driver_title(definition)}。

{intro}```toml
{example_toml}
```

{param_table}
"""


def generate():
    schema = load_schema()
    defs = schema.get("definitions", {})
    drivers = driver_definitions(defs)
    capabilities = load_capabilities()

    lines = []
    lines.append("# 支持的驱动")
    lines.append("")
    lines.append("qrypt 支持以下云盘后端。每个驱动通过配置文件中的 `[[mounts]]` 条目使用，`type` 字段指定驱动类型，`params` 中填写对应参数。")
    lines.append("")
    lines.append("## 如何选择驱动")
    lines.append("")
    lines.append("- **先体验 qrypt**：使用 `localfs`，不需要云盘账号，配置示例见[快速上手](quickstart.md)。")
    lines.append("- **使用网盘**：从下方列表中选择对应驱动。多数网盘需要从网页端登录后获取 cookie 或 token，具体字段见各驱动小节。")
    lines.append("- **使用标准协议或自建服务**：选择 `webdav`、`sftp` 或 `s3`。")
    lines.append("- **按能力选驱动**：见[能力对照](#能力对照)。")
    lines.append("")
    lines.append("网盘凭证通常有时效，过期后需要重新获取。请将凭证保存在本机配置文件中，不要提交到版本库。")
    lines.append("")
    lines.append("## 驱动列表")
    lines.append("")
    lines.append(build_driver_list_table(drivers))
    lines.append("")

    lines.append("## 能力对照")
    lines.append("")
    lines.append("以下矩阵用于选型参考，不影响基本使用。")
    lines.append("")
    lines.append(build_capability_table(capabilities, drivers))
    lines.append("")
    lines.append("✅ 支持，— 不支持；不支持的能力会由上层降级或返回 unsupported。")
    lines.append("")
    lines.append("**写入**：创建、删除、移动、重命名；**服务端复制**：由存储端直接复制，不经过本地下载再上传；**服务端哈希**：可直接读取存储端内容哈希用于校验；**保留修改时间**：上传或复制后保留源文件 mtime。")
    lines.append("")
    lines.append("`quark` 的离线下载同时支持解析、选择性转存和加速取回。")
    lines.append("")

    for _, definition in drivers:
        lines.append(build_driver_section(definition))

    content = "\n".join(lines)
    idx = content.find("\n---\n")
    if idx != -1:
        content = content[:idx] + content[idx + 5:]

    with open(OUTPUT_PATH, "w", encoding="utf-8", newline="\n") as f:
        f.write(content)
    print(f"Generated {OUTPUT_PATH}")


if __name__ == "__main__":
    generate()
