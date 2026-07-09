#!/usr/bin/env python3
"""Generate docs/for-user/full-config.md from qrypt.schema.json."""

import json
import os

SCHEMA_PATH = os.path.join(os.path.dirname(__file__), "..", "qrypt.schema.json")
OUTPUT_PATH = os.path.join(os.path.dirname(__file__), "..", "docs", "for-user", "full-config.md")


def load_schema():
    with open(SCHEMA_PATH) as f:
        return json.load(f)


def fmt_default(v):
    if v is None:
        return ""
    if isinstance(v, bool):
        return str(v).lower()
    return str(v)


def build_field_rows(props, skip_encryption=False, skip_cache=False):
    rows = []
    rows.append("| 参数 | 类型 | 默认值 | 说明 |")
    rows.append("|---|---|---|---|")
    for name, prop in props.items():
        if skip_encryption and name in ("encryption",):
            continue
        if skip_cache and name in ("cache",):
            continue
        if name == "type":
            continue
        t = prop.get("type", "string")
        default_val = fmt_default(prop.get("default"))
        if not default_val:
            default_val = "-"
        desc = prop.get("description", "")
        rows.append(f"| `{name}` | {t} | {default_val} | {desc} |")
    return "\n".join(rows)


def generate():
    schema = load_schema()
    props = schema.get("properties", {})
    defs = schema.get("definitions", {})

    sections = []
    sections.append("# 完整配置参考")
    sections.append("")
    sections.append("本文档为 qrypt 全部配置项的参考说明，由 `qrypt.schema.json` 自动生成。")
    sections.append("")

    global_keys = [
        "mount_point", "cache_dir", "volume_name", "no_apple_double",
        "no_apple_xattr", "read_only", "allow_other", "default_permissions",
    ]
    rows = []
    rows.append("| 参数 | 类型 | 默认值 | 说明 |")
    rows.append("|---|---|---|---|")
    for key in global_keys:
        prop = props.get(key)
        if not prop:
            continue
        t = prop.get("type", "string")
        default_val = fmt_default(prop.get("default"))
        desc = prop.get("description", "")
        rows.append(f"| `{key}` | {t} | {default_val} | {desc} |")
    sections.append("## 全局设置")
    sections.append("")
    sections.append("\n".join(rows))
    sections.append("")

    fuse_keys = [
        "attr_timeout", "entry_timeout", "negative_timeout",
        "total_space", "free_space",
    ]
    rows = []
    rows.append("| 参数 | 类型 | 默认值 | 说明 |")
    rows.append("|---|---|---|---|")
    for key in fuse_keys:
        prop = props.get(key)
        if not prop:
            continue
        t = prop.get("type", "string")
        default_val = fmt_default(prop.get("default"))
        desc = prop.get("description", "")
        rows.append(f"| `{key}` | {t} | {default_val} | {desc} |")
    sections.append("## FUSE 参数")
    sections.append("")
    sections.append("控制 FUSE 内核驱动的缓存行为和文件系统容量显示。")
    sections.append("")
    sections.append("\n".join(rows))
    sections.append("")

    enc = defs.get("encryptionConfig", {})
    enc_props = enc.get("properties", {})
    sections.append("## 加密配置")
    sections.append("")
    sections.append(
        "在顶层 `[encryption]` 中设置，作为所有云盘的加密默认值。"
        "每个 mount 可以在 `[mounts.encryption]` 中单独覆盖。"
    )
    sections.append("")
    sections.append("格式与 rclone 兼容，可直接使用 rclone 的加密配置。")
    sections.append("")
    sections.append(build_field_rows(enc_props))
    sections.append("")

    cache = defs.get("cacheConfig", {})
    cache_props = cache.get("properties", {})
    sections.append("## 缓存配置")
    sections.append("")
    sections.append(
        "在 `[defaults.cache]` 中设置，作为所有云盘的缓存默认值。"
        "每个 mount 可以在 `[mounts.cache]` 中单独覆盖。"
    )
    sections.append("")
    sections.append(build_field_rows(cache_props))
    sections.append("")

    log = defs.get("loggingConfig", {})
    log_props = log.get("properties", {})
    sections.append("## 日志")
    sections.append("")
    sections.append("\n".join(build_field_rows(log_props).split("\n")))
    sections.append("")

    time = defs.get("timeConfig", {})
    time_props = time.get("properties", {})
    sections.append("## 时间同步（NTP）")
    sections.append("")
    sections.append(
        "qrypt 依赖精确的系统时间进行文件操作。"
        "当系统时间可能不准确时（如嵌入式设备或刚开机），建议启用 NTP。"
    )
    sections.append("")
    sections.append("\n".join(build_field_rows(time_props).split("\n")))
    sections.append("")

    bw = defs.get("bandwidthConfig", {})
    bw_props = bw.get("properties", {})
    sections.append("## 带宽控制")
    sections.append("")
    sections.append("限制文件上传和下载的带宽。单位如 `10Mbps`、`5MBps`，留空表示不限制。")
    sections.append("")
    sections.append("\n".join(build_field_rows(bw_props).split("\n")))
    sections.append("")

    sections.append("## 云盘挂载")
    sections.append("")
    sections.append("每个 `[[mounts]]` 条目对应一个云盘服务。驱动类型和参数请参考：")
    sections.append("")
    sections.append("- [支持的驱动](support-drivers.md)")
    sections.append("")
    sections.append(
        "每个 mount 可以在 `[mounts.encryption]` 和 `[mounts.cache]` 中"
        "覆盖全局加密和缓存配置，具体参数见上文对应章节。"
    )
    sections.append("")

    content = "\n".join(sections)

    os.makedirs(os.path.dirname(OUTPUT_PATH), exist_ok=True)
    with open(OUTPUT_PATH, "w") as f:
        f.write(content)
    print(f"Generated {OUTPUT_PATH}")


if __name__ == "__main__":
    generate()
