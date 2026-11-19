#!/usr/bin/env python3
"""Shared qrypt measurement plumbing for the dev scripts.

`read-ab.py` and `tune.py` both need the same isolated-mount lifecycle: rewrite
a copy of the user's client config to point at a temporary mount point, give the
child its own QRYPT_HOME and read cache, start it, and tear everything down
afterwards. Keeping that in one module means a tune run and an A/B run agree on
mount options, cache size and credential handling, and neither leaves a
credential-bearing config in /tmp.

Requires Python 3.11+ (tomllib).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import signal
import subprocess
import time

try:
    import tomllib
except ModuleNotFoundError:  # pragma: no cover - Python < 3.11
    raise SystemExit("qrypt_ab.py needs Python 3.11+ (tomllib)") from None

from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
F_NOCACHE = 48  # macOS fcntl flag: bypass the unified buffer cache
PREAD_BLOCK = 16 << 10  # playback-shaped read granularity


def parse_size(raw: str) -> int:
    raw = raw.strip().lower()
    m = re.fullmatch(r"(\d+)\s*([kmg]i?b|[kmg]|b)?", raw)
    if not m:
        raise argparse.ArgumentTypeError(f"invalid size {raw!r}")
    value = int(m.group(1))
    unit = m.group(2) or "b"
    scale = {
        "b": 1,
        "k": 1 << 10,
        "kb": 1 << 10,
        "kib": 1 << 10,
        "m": 1 << 20,
        "mb": 1 << 20,
        "mib": 1 << 20,
        "g": 1 << 30,
        "gb": 1 << 30,
        "gib": 1 << 30,
    }[unit]
    return value * scale


def format_size(n: int) -> str:
    for unit, scale in (("GiB", 1 << 30), ("MiB", 1 << 20), ("KiB", 1 << 10)):
        if n >= scale and n % scale == 0:
            return f"{n // scale}{unit}"
    return f"{n}B"


# ---------------------------------------------------------------------------
# TOML writing (no stdlib writer, and we need to override parsed tables)
# ---------------------------------------------------------------------------

def toml_scalar(value) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        return repr(value)
    if isinstance(value, list):
        return "[" + ", ".join(toml_scalar(item) for item in value) + "]"
    text = str(value)
    if "\n" in text:
        escaped = text.replace("\\", "\\\\").replace('"', '\\"')
        return '"""' + escaped + '"""'
    return json.dumps(text)


def toml_dump_document(data: dict) -> str:
    out: list[str] = []

    def is_table(value) -> bool:
        return isinstance(value, dict)

    def is_table_array(value) -> bool:
        return (
            isinstance(value, list)
            and value
            and all(isinstance(item, dict) for item in value)
        )

    def emit_table(table: dict, header: tuple[str, ...], array: bool) -> None:
        if header:
            if out:
                out.append("")
            if array:
                out.append("[[" + ".".join(header) + "]]")
            else:
                out.append("[" + ".".join(header) + "]")
        for key, value in table.items():
            if is_table(value) or is_table_array(value):
                continue
            out.append(f"{key} = {toml_scalar(value)}")
        for key, value in table.items():
            if is_table(value):
                emit_table(value, header + (key,), False)
            elif is_table_array(value):
                for item in value:
                    emit_table(item, header + (key,), True)

    emit_table(data, (), False)
    return "\n".join(out) + "\n"


# ---------------------------------------------------------------------------
# Config shaping: keep the user's credentials and mount definitions, replace
# everything that makes a mount usable for measurement.
# ---------------------------------------------------------------------------

def load_config(path: Path) -> dict:
    with path.open("rb") as fh:
        return tomllib.load(fh)


def make_measurement_config(base: dict, mount: str, home: Path, mnt: Path, log_level: str) -> dict:
    cfg = json.loads(json.dumps(base))  # deep copy through JSON
    cfg["version"] = cfg.get("version", "1")
    cfg["mount_point"] = str(mnt)
    cfg["volume_name"] = "QryptReadAB"
    cfg["read_only"] = True
    mount_table = dict(cfg.get("mount") or {})
    mount_table.update({
        "mount_point": str(mnt),
        "volume_name": "QryptReadAB",
        "attr_timeout": "",
        "entry_timeout": "",
        "negative_timeout": "",
    })
    options = dict(mount_table.get("options") or {})
    darwin = list(options.get("darwin") or [])
    for opt in ("defer_permissions", "auto_xattr", "iosize=3145728"):
        if opt not in darwin:
            darwin.append(opt)
    options["darwin"] = darwin
    mount_table["options"] = options
    cfg["mount"] = mount_table
    cfg["logging"] = {
        "log_level": log_level,
        "log_file": str(home / "logs" / "qrypt.log"),
        "error_file": str(home / "logs" / "qrypt-error.log"),
    }
    cfg["debug"] = {"enabled": True}
    cfg["bandwidth"] = {"download": "", "upload": ""}
    cfg["storage"] = {"work_dir": str(home)}
    cfg["read_cache"] = {"max_size": "2G"}
    cfg["upload"] = {
        "upload_delay": "10s",
        "upload_workers": 2,
        "delete_delay": "2s",
        "default_mount": mount,
        "default_path": "/",
    }
    mounts = cfg.get("mounts")
    if not isinstance(mounts, list) or not mounts:
        raise SystemExit("config has no [[mounts]]; pass a client config with at least one mount")
    keep = [item for item in mounts if item.get("name") == mount]
    if not keep:
        raise SystemExit(f"mount {mount!r} not found in config")
    # Keep every mount so the config still validates, but only the chosen one
    # is ever mounted by mount-ab.
    return cfg


# ---------------------------------------------------------------------------
# Mount lifecycle
# ---------------------------------------------------------------------------

class Side:
    def __init__(self, role: str, binary: Path, root: Path, mount: str, path: str, base_cfg: dict, log_level: str):
        self.role = role
        self.binary = binary
        self.root = root
        self.mount = mount
        self.path = path
        self.base_cfg = base_cfg
        self.log_level = log_level
        self.home = root / "home"
        self.mnt = root / "mnt"
        self.cfg_path = root / "qrypt.toml"
        self.proc: subprocess.Popen | None = None
        self.home.mkdir(parents=True, exist_ok=True)
        self.mnt.mkdir(parents=True, exist_ok=True)
        (self.home / "logs").mkdir(parents=True, exist_ok=True)

    @property
    def local_path(self) -> Path:
        return self.mnt / self.path.lstrip("/")

    @property
    def log_path(self) -> Path:
        return self.home / "logs" / "qrypt.log"

    def start(self, timeout: float = 40.0) -> None:
        cfg = make_measurement_config(self.base_cfg, self.mount, self.home, self.mnt, self.log_level)
        self.cfg_path.write_text(toml_dump_document(cfg))
        env = dict(os.environ)
        env["QRYPT_HOME"] = str(self.home)
        log = open(self.root / "mount.log", "w", encoding="utf-8")
        self.proc = subprocess.Popen(
            [str(self.binary), "mount", self.mount, "-c", str(self.cfg_path), "--mount-point", str(self.mnt)],
            env=env,
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        deadline = time.time() + timeout
        sock = self.home / "run" / "debug.sock"
        while time.time() < deadline:
            if self.proc.poll() is not None:
                tail = (self.root / "mount.log").read_text(errors="replace")[-2000:]
                raise SystemExit(f"{self.role}: mount exited early:\n{tail}")
            if sock.exists() and self.local_path.exists():
                return
            time.sleep(0.25)
        tail = (self.root / "mount.log").read_text(errors="replace")[-2000:]
        raise SystemExit(f"{self.role}: mount not ready within {timeout:.0f}s:\n{tail}")

    def read_sample(self, offset: int, length: int) -> dict:
        fd = os.open(self.local_path, os.O_RDONLY)
        try:
            try:
                import fcntl

                fcntl.fcntl(fd, F_NOCACHE, 1)
            except Exception:  # pragma: no cover - non-macOS development hosts
                pass
            started = time.time()
            ttfb = None
            got = 0
            while got < length:
                block = os.pread(fd, min(PREAD_BLOCK, length - got), offset + got)
                if not block:
                    break
                if ttfb is None:
                    ttfb = time.time() - started
                got += len(block)
            elapsed = time.time() - started
        finally:
            os.close(fd)
        return {
            "bytes": got,
            "total_s": elapsed,
            "ttfb_s": ttfb,
            "rate_kb_s": (got / elapsed / 1024.0) if elapsed > 0 else 0.0,
        }

    def stop(self) -> None:
        if self.proc is not None and self.proc.poll() is None:
            subprocess.run(["umount", "-f", str(self.mnt)], capture_output=True, check=False)
            try:
                os.killpg(self.proc.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(self.proc.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                self.proc.wait(timeout=5)
        self.proc = None
