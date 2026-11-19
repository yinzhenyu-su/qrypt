#!/usr/bin/env python3
"""Measure read shapes for one file with `probe tune read` on an isolated mount.

A tune run needs a mounted file, a test-enabled mount and a throwaway
configuration that does not disturb the user's real mount. This script reuses
the same isolated-mount plumbing as `read-ab.py`: it rewrites a copy of the
client config into a temporary home, starts a read-only mount, runs
`probe tune read` against the given virtual path and prints the recommended
policy block. Everything under the temporary root (including the rewritten
config) is removed on exit, so no credential-bearing file is left behind.

Requires Python 3.11+ (tomllib). Builds `./cmd/qrypt` from the working tree
unless --binary points at an existing binary.

Examples:
  scripts/dev/tune.py --mount koofr --path "/My videos/sample.mp4"
  scripts/dev/tune.py --mount quark-encrypt --path "/videos/sample.mp4" \
      --size 64m --rounds 3 --json
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
import tempfile
import time

from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from qrypt_ab import Side, format_size, load_config, parse_size  # noqa: E402


def cli_size(n: int) -> str:
    """Render a byte count in the compact form probe flags accept (8m, 64k)."""
    for unit, shift in (("g", 30), ("m", 20), ("k", 10)):
        if n % (1 << shift) == 0 and n // (1 << shift) > 0:
            return f"{n >> shift}{unit}"
    return str(n)


def main() -> int:
    ap = argparse.ArgumentParser(
        description="Run `probe tune read` on an isolated mount of one file.",
        epilog="The temporary mount, home and rewritten config are removed on exit.",
    )
    ap.add_argument("--config", default=str(REPO / "qrypt.toml"),
                    help="client config to copy for the isolated mount (default: repo qrypt.toml)")
    ap.add_argument("--mount", required=True, help="[[mounts]] name to test")
    ap.add_argument("--path", required=True,
                    help="absolute virtual path of an existing file, e.g. /My videos/sample.mp4")
    ap.add_argument("--size", type=parse_size, default=32 << 20,
                    help="bytes per tune sample (default 32MiB)")
    ap.add_argument("--rounds", type=int, default=3,
                    help="rounds per shape; probe caps this at 5 (default 3)")
    ap.add_argument("--block-size", default="16k",
                    help="mounted read block size (default 16k, playback-shaped)")
    ap.add_argument("--log-level", default="info",
                    help="mount log level; debug halves throughput (default info)")
    ap.add_argument("--binary", help="skip `go build` and use this qrypt binary")
    ap.add_argument("--json", action="store_true", help="print the raw probe JSON result")
    ap.add_argument("--keep", action="store_true", help="keep the temporary workdir for inspection")
    args = ap.parse_args()

    if not args.path.startswith("/"):
        raise SystemExit("--path must be an absolute virtual path")
    cfg_path = Path(args.config).expanduser().resolve()
    if not cfg_path.is_file():
        raise SystemExit(f"config not found: {cfg_path}")
    base_cfg = load_config(cfg_path)

    tmp = Path(tempfile.mkdtemp(prefix="qrypt-tune-"))
    side = None
    started = time.time()
    try:
        if args.binary:
            binary = Path(args.binary).expanduser().resolve()
            if not binary.is_file():
                raise SystemExit(f"binary not found: {binary}")
        else:
            binary = tmp / "qrypt"
            print(f"building qrypt -> {binary}")
            build = subprocess.run(["go", "build", "-o", str(binary), "./cmd/qrypt"],
                                   cwd=REPO, capture_output=True, text=True)
            if build.returncode != 0:
                raise SystemExit(f"go build failed:\n{build.stderr.strip()}")

        side = Side("tune", binary, tmp / "side", args.mount, args.path, base_cfg, args.log_level)
        side.start()
        print(f"isolated mount: {side.mnt}  (QRYPT_HOME {side.home}, sample size {format_size(args.size)})")
        size = cli_size(args.size)
        argv = [
            str(binary), "probe", "tune", "read",
            "-c", str(side.cfg_path),
            "--mount", args.mount,
            "--mount-point", str(side.mnt),
            "--path", args.path,
            "--size", size,
            "--rounds", str(max(1, args.rounds)),
            "--block-size", args.block_size,
        ]
        if args.json:
            argv.append("--json")
        print(f"running: {' '.join(argv)}")
        print()
        sys.stdout.flush()
        env = dict(os.environ)
        env["QRYPT_HOME"] = str(side.home)
        proc = subprocess.run(argv, env=env, capture_output=True, text=True)
        sys.stdout.write(proc.stdout)
        if proc.stderr.strip():
            sys.stderr.write(proc.stderr)
        if proc.returncode != 0:
            raise SystemExit(f"probe tune read failed with exit code {proc.returncode}")
        return 0
    finally:
        if side is not None:
            side.stop()
        if not args.keep:
            shutil.rmtree(tmp, ignore_errors=True)
            print(f"\nworked for {time.time() - started:.0f}s; temporary mount and config removed")
        else:
            print(f"\nkept {tmp} (contains the rewritten config; delete it when done)")


if __name__ == "__main__":
    sys.exit(main())
