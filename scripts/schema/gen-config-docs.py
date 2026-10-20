#!/usr/bin/env python3
"""Generate docs/for-user/full-config.md from schema/qrypt.schema.json."""

import json
import os

SCHEMA_PATH = os.path.join(os.path.dirname(__file__), "..", "..", "schema", "qrypt.schema.json")
OUTPUT_PATH = os.path.join(os.path.dirname(__file__), "..", "..", "docs", "for-user", "full-config.md")


def load_schema():
    with open(SCHEMA_PATH, encoding="utf-8") as f:
        return json.load(f)


def localized(definition, key="description"):
    return (
        definition.get("x-i18n", {}).get("zh-CN", {}).get(key)
        or definition.get(key, "")
    )


def fmt_default(v):
    if v is None:
        return ""
    if isinstance(v, bool):
        return str(v).lower()
    return str(v)


def display_default(prop):
    return prop.get("x-doc-default", fmt_default(prop.get("default")))


def build_field_rows(props, skip_encryption=False, skip_cache=False):
    rows = []
    rows.append("| 参数 | 类型 | 默认值 | 说明 |")
    rows.append("|---|---|---|---|")
    for name, prop in props.items():
        if skip_encryption and name in ("encryption",):
            continue
        if skip_cache and name in ("read_cache", "upload"):
            continue
        if name == "type":
            continue
        t = prop.get("type", "string")
        default_val = display_default(prop)
        if not default_val:
            default_val = "-"
        desc = localized(prop)
        rows.append(f"| `{name}` | {t} | {default_val} | {desc} |")
    return "\n".join(rows)


def build_platform_options_section(prop):
    examples = prop.get("examples", [])
    if not examples:
        return ""
    platform_options = examples[0]
    rows = [
        "### `mount.options`",
        "",
        localized(prop),
        "",
        "| 平台 | 示例值 |",
        "|---|---|",
    ]
    for platform, values in platform_options.items():
        rendered = json.dumps(values, ensure_ascii=False)
        rows.append(f"| `{platform}` | `{rendered}` |")
    return "\n".join(rows)


def section(defs, name):
    definition = defs[name]
    return definition.get("properties", {}), localized(definition)


def generate():
    schema = load_schema()
    defs = schema.get("definitions", {})

    sections = []
    sections.append("# 完整配置参考")
    sections.append("")
    sections.append(
        "本文档列出 qrypt 的全部配置项。大多数用户只需要 `[mount]`、"
        "`[[mounts]]` 和 `[mounts.encryption]`；其余章节按需查阅。"
    )
    sections.append("")

    mount_props, mount_desc = section(defs, "mountOptionsConfig")
    sections.append("## `[mount]`")
    sections.append("")
    sections.append(mount_desc)
    sections.append("")
    sections.append(build_field_rows(mount_props))
    options_prop = mount_props.get("options")
    if options_prop:
        sections.append("")
        sections.append(build_platform_options_section(options_prop))
    sections.append("")

    mount_props, mount_desc = section(defs, "mountConfig")
    sections.append("## 云盘挂载")
    sections.append("")
    sections.append(mount_desc)
    sections.append("")
    sections.append("- [支持的驱动](support-drivers.md)")
    sections.append("")
    common_mount_props = {
        name: mount_props[name]
        for name in ("name", "type", "test_enabled")
        if name in mount_props
    }
    sections.append("通用参数：")
    sections.append("")
    sections.append(build_field_rows(common_mount_props))
    sections.append("")
    sections.append(
        "每个 mount 可以在 `[mounts.encryption]`、`[mounts.read_cache]` "
        "和 `[mounts.upload]` 中覆盖全局配置，具体参数见对应章节。"
    )
    sections.append("")

    enc_props, enc_desc = section(defs, "encryptionConfig")
    sections.append("## 加密配置")
    sections.append("")
    sections.append(enc_desc)
    sections.append("")
    sections.append(build_field_rows(enc_props))
    sections.append("")

    storage_props, storage_desc = section(defs, "storageConfig")
    sections.append("## 存储目录")
    sections.append("")
    sections.append(storage_desc)
    sections.append("")
    sections.append(build_field_rows(storage_props))
    sections.append("")
    sections.append(
        "环境变量 `QRYPT_HOME` 用于便携运行和测试隔离。设置后它优先于\n"
        "`storage.work_dir` 及所有子目录覆盖，所有运行数据（包括 sync 会话）\n"
        "都会写入该目录；未设置时才使用 `storage.work_dir`，最终回退到 `~/.qrypt`。\n"
        "sync 会话固定保存在有效工作目录的 `sync/` 子目录。"
    )
    sections.append("")

    read_cache_props, read_cache_desc = section(defs, "readCacheConfig")
    sections.append("## 读取缓存")
    sections.append("")
    sections.append(read_cache_desc)
    sections.append("")
    sections.append(build_field_rows(read_cache_props))
    sections.append("")

    thumbnail_props, thumbnail_desc = section(defs, "thumbnailCacheConfig")
    sections.append("## 缩略图缓存")
    sections.append("")
    sections.append(thumbnail_desc)
    sections.append("")
    sections.append(build_field_rows(thumbnail_props))
    sections.append("")

    upload_props, upload_desc = section(defs, "uploadConfig")
    sections.append("## 上传")
    sections.append("")
    sections.append(upload_desc)
    sections.append("")
    sections.append(build_field_rows(upload_props))
    sections.append("")

    log_props, _ = section(defs, "loggingConfig")
    sections.append("## 日志")
    sections.append("")
    sections.append(build_field_rows(log_props))
    sections.append("")

    debug_props, debug_desc = section(defs, "debugConfig")
    sections.append("## 调试服务")
    sections.append("")
    sections.append(debug_desc)
    sections.append("")
    sections.append(build_field_rows(debug_props))
    sections.append("")

    time_props, time_desc = section(defs, "timeConfig")
    sections.append("## 时间同步（NTP）")
    sections.append("")
    sections.append(time_desc)
    sections.append("")
    sections.append(build_field_rows(time_props))
    sections.append("")

    bw_props, bw_desc = section(defs, "bandwidthConfig")
    sections.append("## 带宽控制")
    sections.append("")
    sections.append(bw_desc)
    sections.append("")
    sections.append(build_field_rows(bw_props))
    sections.append("")

    content = "\n".join(sections)
    os.makedirs(os.path.dirname(OUTPUT_PATH), exist_ok=True)
    with open(OUTPUT_PATH, "w", encoding="utf-8", newline="\n") as f:
        f.write(content)
    print(f"Generated {OUTPUT_PATH}")


if __name__ == "__main__":
    generate()
