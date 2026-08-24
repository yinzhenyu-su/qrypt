#!/usr/bin/env python3
"""Generate docs/for-user/full-config.md from qrypt.schema.json."""

import json
import os

SCHEMA_PATH = os.path.join(os.path.dirname(__file__), "..", "qrypt.schema.json")
OUTPUT_PATH = os.path.join(os.path.dirname(__file__), "..", "docs", "for-user", "full-config.md")

STORAGE_DOC_OVERRIDES = {
    "work_dir": {
        "default": "`~/.qrypt`",
        "description": "所有运行数据的根目录；未单独配置的子目录会从这里派生。",
    },
    "read_cache_dir": {
        "default": "`<work_dir>/cache/read`",
        "description": "读取缓存目录的可选覆盖。",
    },
    "thumbnail_cache_dir": {
        "default": "`<work_dir>/cache/thumbnail`",
        "description": "缩略图缓存目录的可选覆盖。",
    },
    "upload_dir": {
        "default": "`<work_dir>/upload`",
        "description": "上传 staging 和 pending journal 目录的可选覆盖。",
    },
    "state_dir": {
        "default": "`<work_dir>/state`",
        "description": "驱动及运行状态目录的可选覆盖。",
    },
    "log_dir": {
        "default": "`<work_dir>/logs`",
        "description": "运行日志目录的可选覆盖。",
    },
    "tmp_dir": {
        "default": "`<work_dir>/tmp`",
        "description": "临时文件目录的可选覆盖。",
    },
}

LOGGING_DOC_OVERRIDES = {
    "log_file": {
        "description": "主日志路径；未设置时使用 `<storage.log_dir>/qrypt.log`，或 `<storage.work_dir>/logs/qrypt.log`。",
    },
    "error_file": {
        "description": "错误日志路径；未设置时使用 `<storage.log_dir>/qrypt-error.log`，或 `<storage.work_dir>/logs/qrypt-error.log`。",
    },
}

DEBUG_DOC_OVERRIDES = {
    "listen": {
        "description": "HTTP 调试入口监听地址；未设置时使用 `127.0.0.1:19090`。",
    },
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


def build_field_rows(props, skip_encryption=False, skip_cache=False, doc_overrides=None):
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
        override = (doc_overrides or {}).get(name, {})
        default_val = override.get("default", fmt_default(prop.get("default")))
        if not default_val:
            default_val = "-"
        desc = override.get("description", prop.get("description", ""))
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

    mount_props = defs.get("mountOptionsConfig", {}).get("properties", {})
    sections.append("## `[mount]`")
    sections.append("")
    sections.append("挂载相关配置集中在 `[mount]` 下。旧的顶层写法仍兼容，但建议只用这一节。")
    sections.append("")
    sections.append(build_field_rows(mount_props))
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

    storage = defs.get("storageConfig", {})
    storage_props = storage.get("properties", {})
    sections.append("## 存储目录")
    sections.append("")
    sections.append(
        "在 `[storage]` 中设置运行数据根目录。"
        "当移动端或其他宿主传入 runtime layout 时，这些目录会被运行时布局覆盖。"
    )
    sections.append("")
    sections.append(build_field_rows(storage_props, doc_overrides=STORAGE_DOC_OVERRIDES))
    sections.append("")
    sections.append(
        "环境变量 `QRYPT_HOME` 用于便携运行和测试隔离。设置后它优先于\n"
        "`storage.work_dir` 及所有子目录覆盖，所有运行数据（包括 sync 会话）\n"
        "都会写入该目录；未设置时才使用 `storage.work_dir`，最终回退到 `~/.qrypt`。\n"
        "sync 会话固定保存在有效工作目录的 `sync/` 子目录。"
    )
    sections.append("")

    read_cache = defs.get("readCacheConfig", {})
    read_cache_props = read_cache.get("properties", {})
    sections.append("## 读取缓存")
    sections.append("")
    sections.append(
        "在 `[read_cache]` 中设置读取缓存默认值。"
        "每个 mount 可以在 `[mounts.read_cache]` 中单独覆盖。"
    )
    sections.append("")
    sections.append(build_field_rows(read_cache_props))
    sections.append("")

    thumbnail_cache = defs.get("thumbnailCacheConfig", {})
    thumbnail_props = thumbnail_cache.get("properties", {})
    sections.append("## 缩略图缓存")
    sections.append("")
    sections.append(
        "在 `[thumbnail_cache]` 中设置缩略图缓存默认值。"
        "生成的缩略图保存在 `thumbnail_cache_dir`"
        "（默认 `<work_dir>/cache/thumbnail`）。"
    )
    sections.append("")
    sections.append(build_field_rows(thumbnail_props))
    sections.append("")

    upload = defs.get("uploadConfig", {})
    upload_props = upload.get("properties", {})
    sections.append("## 上传")
    sections.append("")
    sections.append(
        "在 `[upload]` 中设置上传和删除调度默认值，以及任务上传的默认目标。"
        "调度参数可以在 `[mounts.upload]` 中单独覆盖；"
        "`default_mount` 和 `default_path` 只支持顶层 `[upload]`。"
    )
    sections.append("")
    sections.append(build_field_rows(upload_props))
    sections.append("")

    log = defs.get("loggingConfig", {})
    log_props = log.get("properties", {})
    sections.append("## 日志")
    sections.append("")
    sections.append("\n".join(build_field_rows(log_props, doc_overrides=LOGGING_DOC_OVERRIDES).split("\n")))
    sections.append("")

    debug = defs.get("debugConfig", {})
    debug_props = debug.get("properties", {})
    sections.append("## 调试服务")
    sections.append("")
    sections.append("在 `[debug]` 中启用运行时 HTTP 调试入口。")
    sections.append("")
    sections.append(build_field_rows(debug_props, doc_overrides=DEBUG_DOC_OVERRIDES))
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
    mount_props = defs.get("mountConfig", {}).get("properties", {})
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
        "和 `[mounts.upload]` 中覆盖全局配置，具体参数见上文对应章节。"
    )
    sections.append("")

    content = "\n".join(sections)

    os.makedirs(os.path.dirname(OUTPUT_PATH), exist_ok=True)
    with open(OUTPUT_PATH, "w") as f:
        f.write(content)
    print(f"Generated {OUTPUT_PATH}")


if __name__ == "__main__":
    generate()
