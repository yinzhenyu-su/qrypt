#!/usr/bin/env python3
"""Shared qrypt measurement plumbing for the dev scripts.

`read-ab.py` and `tune.py` both need the same disposable mount: a read-only
mount of one configured drive with its own home, mount point and read cache, and
no copy of the user's credential-bearing config. The mount command grows that
shape itself (`qrypt mount --isolated`), so this module only starts it, parses
the JSON it prints once ready, and tears it down.

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


def load_config(path: Path) -> dict:
    with path.open("rb") as fh:
        return tomllib.load(fh)


# ---------------------------------------------------------------------------
# Mount lifecycle
# ---------------------------------------------------------------------------

class Side:
    """One disposable read-only mount of a configured drive."""

    def __init__(self, role: str, binary: Path, root: Path, mount: str, path: str, config_path: Path, cache: str = "2G"):
        self.role = role
        self.binary = binary
        self.root = root
        self.mount = mount
        self.path = path
        self.config_path = config_path
        self.cache = cache
        self.log_level = "info"
        self.home: Path | None = None
        self.mnt: Path | None = None
        self._runtime_log: Path | None = None
        self.proc: subprocess.Popen | None = None
        self._info_path = root / "mount.json"
        self._log_path = root / "mount.log"
        self._runtime_log = root / "runtime.log"
        self._stdout = None
        root.mkdir(parents=True, exist_ok=True)

    @property
    def local_path(self) -> Path:
        if self.mnt is None:
            raise SystemExit(f"{self.role}: mount point unknown before start")
        return self.mnt / self.path.lstrip("/")

    @property
    def log_path(self) -> Path:
        return self._log_path

    @property
    def runtime_log_path(self) -> Path:
        """Where the isolated mount writes its structured debug log."""
        if self._runtime_log is not None and self._runtime_log.exists():
            return self._runtime_log
        return self._log_path

    def start(self, log_level: str = "info", timeout: float = 40.0) -> None:
        self.log_level = log_level
        self._info_path.unlink(missing_ok=True)
        env = dict(os.environ)
        env.pop("QRYPT_HOME", None)
        self._stdout = self._info_path.open("w", encoding="utf-8")
        log = self._log_path.open("w", encoding="utf-8")
        self.proc = subprocess.Popen(
            [
                str(self.binary), "mount", self.mount,
                "-c", str(self.config_path),
                "--isolated",
                "--isolated-cache", self.cache,
                "--isolated-log-level", log_level,
                "--isolated-log-file", str(self._runtime_log),
            ],
            env=env,
            stdout=self._stdout,
            stderr=log,
            start_new_session=True,
        )
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.proc.poll() is not None:
                tail = self._log_path.read_text(errors="replace")[-2000:]
                raise SystemExit(f"{self.role}: mount exited early:\n{tail}")
            try:
                info = json.loads(self._info_path.read_text())
            except (OSError, ValueError):
                time.sleep(0.25)
                continue
            self.home = Path(info["qrypt_home"])
            self.mnt = Path(info["mount_point"])

            if self.local_path.exists():
                return
            time.sleep(0.25)
        tail = self._log_path.read_text(errors="replace")[-2000:]
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
            if self.mnt is not None:
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
        if self._stdout is not None:
            self._stdout.close()
            self._stdout = None
        self.proc = None
