#!/usr/bin/env python3
"""Interleaved A/B read measurement on isolated qrypt mounts.

Builds a control binary from a git ref and a candidate binary from the working
tree, mounts each side on its own temporary mount point with its own
QRYPT_HOME and read cache (quiet logging, no bandwidth limit), then reads the
same cloud file through both mounts in interleaved order. The table reports
throughput, time to first byte, and the candidate/control ratio; --amplify
adds a debug-log pass that reports provider wire bytes per delivered byte.

The user's real mount is never touched: every side gets a fresh mount point,
home directory, and caches under one temporary root that is removed on exit.

Requires Python 3.11+ (tomllib).

Example:
  scripts/dev/read-ab.py --path "/videos/sample.mp4" \
      --offsets 200,450,650 --length 2MiB --rounds 2

Example with amplification:
  scripts/dev/read-ab.py --path "/videos/sample.mp4" \
      --offsets 1000,1200 --amplify --amplify-bytes 16MiB
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import signal
import statistics
import subprocess
import sys
import tempfile
import time

try:
    import tomllib
except ModuleNotFoundError:  # pragma: no cover - Python < 3.11
    raise SystemExit("read-ab.py needs Python 3.11+ (tomllib)") from None

from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
F_NOCACHE = 48  # macOS fcntl flag: bypass the unified buffer cache
PREAD_BLOCK = 16 << 10  # playback-shaped read granularity


def parse_size(raw: str) -> int:
    raw = raw.strip().lower()
    m = re.fullmatch(r"(\d+)\s*([kmg]i?b|b)?", raw)
    if not m:
        raise argparse.ArgumentTypeError(f"invalid size {raw!r}")
    value = int(m.group(1))
    unit = m.group(2) or "b"
    scale = {
        "b": 1,
        "kb": 1 << 10,
        "kib": 1 << 10,
        "mb": 1 << 20,
        "mib": 1 << 20,
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


# ---------------------------------------------------------------------------
# Measurement
# ---------------------------------------------------------------------------

def read_offsets(raw: str) -> list[int]:
    out = []
    for item in raw.split(","):
        item = item.strip()
        if not item:
            continue
        out.append(int(float(item)))
    if not out:
        raise SystemExit("--offsets is empty")
    return out


def report_rows(rows: list[dict]) -> None:
    print()
    print(f"{'offset':>10} {'ctl MB/s':>9} {'cand MB/s':>10} {'ratio':>7} "
          f"{'ctl ttfb':>9} {'cand ttfb':>10} {'note':>6}")
    for row in rows:
        note = "" if not row.get("note") else row["note"]
        ratio = row["ratio"]
        print(
            f"{row['offset_mib']:>10} {row['ctl_rate'] / 1024:>9.2f} {row['cand_rate'] / 1024:>10.2f} "
            f"{ratio:>7.2f} {row['ctl_ttfb']:>9.3f} {row['cand_ttfb']:>10.3f} {note:>6}"
        )


def summarize(rows: list[dict]) -> float | None:
    clean = [row for row in rows if row["ratio"] > 0 and not row.get("note")]
    skipped = [row for row in rows if row.get("note")]
    if not clean:
        return None
    ratios = [row["ratio"] for row in clean]
    print()
    if skipped:
        counts = {}
        for row in skipped:
            counts[row["note"]] = counts.get(row["note"], 0) + 1
        detail = ", ".join(f"{k} {v}" for k, v in sorted(counts.items()))
        print(f"excluded pairs: {detail}")
    median = statistics.median(ratios)
    print(
        f"clean pairs: {len(clean)}/{len(rows)}  "
        f"median cand/ctl: {median:.2f}x  "
        f"min {min(ratios):.2f}x  max {max(ratios):.2f}x"
    )
    return median


BODY_BYTES = re.compile(r"body_bytes:(\d+)")


def wire_bytes(log_path: Path) -> int:
    total = 0
    for line in log_path.read_text(errors="replace").splitlines():
        if "phase=fetch_window" not in line and "phase=prefetch_window" not in line:
            continue
        for match in BODY_BYTES.finditer(line):
            total += int(match.group(1))
    return total


def amplification_pass(args, base_cfg: dict, tmp: Path, binaries: dict[str, Path]) -> dict[str, float]:
    print()
    print("amplification pass (debug logging, providers wire bytes per delivered byte)")
    sides = {
        role: Side(role, binaries[role], tmp / f"amp-{role}", args.mount, args.path, base_cfg, "debug")
        for role in ("ctl", "cand")
    }
    try:
        for side in sides.values():
            side.start()
        rows = []
        for role, side in sides.items():
            before = wire_bytes(side.log_path)
            sample = side.read_sample(args.amplify_offset << 20, args.amplify_bytes)
            time.sleep(0.5)  # let the last window event reach the log
            wire = wire_bytes(side.log_path) - before
            delivered = sample["bytes"] or 1
            rows.append((role, wire, delivered, wire / delivered, sample))
        print(f"{'side':>6} {'wire':>10} {'delivered':>10} {'amp':>6} {'rate MB/s':>10} {'ttfb':>7}")
        for role, wire, delivered, amp, sample in rows:
            print(
                f"{role:>6} {format_size(wire):>10} {format_size(delivered):>10} {amp:>6.2f} "
                f"{sample['rate_kb_s'] / 1024:>10.2f} {sample['ttfb_s']:>7.3f}"
            )
        return {role: amp for role, _wire, _delivered, amp, _sample in rows}
    finally:
        for side in sides.values():
            side.stop()


def main() -> int:
    ap = argparse.ArgumentParser(formatter_class=argparse.RawDescriptionHelpFormatter, description=__doc__)
    ap.add_argument("--config", default=str(REPO / "qrypt.toml"), help="client config (default: repo qrypt.toml)")
    ap.add_argument("--mount", default="", help="mount name to use (default: first [[mounts]] entry)")
    ap.add_argument("--path", required=True, help="virtual path of the file to read, e.g. /videos/x.mp4")
    ap.add_argument("--control", default="HEAD", help="git ref for the control build (default: HEAD)")
    ap.add_argument("--offsets", default="200,450,650,850,1000,1200", help="comma separated MiB offsets")
    ap.add_argument("--length", type=parse_size, default=2 << 20, help="bytes per sample (default 2MiB)")
    ap.add_argument("--rounds", type=int, default=2, help="interleaved rounds (default 2)")
    ap.add_argument("--max-retries", type=int, default=2,
                    help="retry a pair at a shifted offset when provider TTFB stalls (default 2)")
    ap.add_argument("--retry-shift", type=int, default=200,
                    help="MiB shift per stall retry (default 200)")
    ap.add_argument("--stall-ttfb", type=float, default=4.0,
                    help="TTFB seconds that mark a sample as a provider stall (default 4.0)")
    ap.add_argument("--round-shift", type=int, default=128,
                    help="MiB shift per round so every round reads a fresh region (default 128)")
    ap.add_argument("--warm-rate-mbps", type=float, default=30.0,
                    help="MB/s above which a sample is treated as a read-cache hit (default 30)")
    ap.add_argument("--amplify", action="store_true", help="add a debug-log amplification pass")
    ap.add_argument("--min-ratio", type=float, default=0.0,
                    help="fail when the clean median cand/ctl ratio is below this (0 = no gate)")
    ap.add_argument("--max-amplification", type=float, default=0.0,
                    help="fail when the candidate amplification pass exceeds this (0 = no gate)")
    ap.add_argument("--amplify-offset", type=int, default=1600, help="MiB offset for the amplification read")
    ap.add_argument("--amplify-bytes", type=parse_size, default=16 << 20, help="bytes per amplification read")
    ap.add_argument("--workdir", default="", help="temporary root (default: mktemp -d)")
    ap.add_argument("--keep", action="store_true", help="keep temporary root and logs for inspection")
    ap.add_argument("--skip-build", action="store_true", help="reuse binaries from --workdir (advanced)")
    args = ap.parse_args()

    if not args.path.startswith("/"):
        raise SystemExit("--path must be an absolute virtual path inside the mount")

    config_path = Path(args.config).expanduser().resolve()
    if not config_path.is_file():
        raise SystemExit(f"config not found: {config_path}")
    base_cfg = load_config(config_path)
    mounts = base_cfg.get("mounts") or []
    if not mounts:
        raise SystemExit("config has no [[mounts]]")
    mount = args.mount or mounts[0].get("name", "")
    if mount not in [item.get("name") for item in mounts]:
        names = ", ".join(str(item.get("name")) for item in mounts)
        raise SystemExit(f"mount {mount!r} not in config; available: {names}")

    tmp = Path(args.workdir).expanduser().resolve() if args.workdir else Path(tempfile.mkdtemp(prefix="qrypt-read-ab-"))
    tmp.mkdir(parents=True, exist_ok=True)
    os.chmod(tmp, 0o700)
    if args.keep or args.workdir:
        print("warning: kept workdir contains generated configs with cloud credentials; delete it when done")
    print(f"workdir: {tmp}")

    binaries = {"ctl": tmp / "bin" / "qrypt-ctl", "cand": tmp / "bin" / "qrypt-cand"}
    mount_dirs: dict[str, Path] = {}
    worktree = tmp / "control-tree"
    sides: dict[str, Side] = {}
    try:
        if not args.skip_build:
            binaries["ctl"].parent.mkdir(parents=True, exist_ok=True)
            print(f"building control from {args.control} ...")
            subprocess.run(
                ["git", "worktree", "add", "--detach", str(worktree), args.control],
                cwd=REPO, check=True, capture_output=True,
            )
            subprocess.run(["go", "build", "-o", str(binaries["ctl"]), "./cmd/qrypt"], cwd=worktree, check=True)
            print("building candidate from working tree ...")
            subprocess.run(["go", "build", "-o", str(binaries["cand"]), "./cmd/qrypt"], cwd=REPO, check=True)

        for role in ("ctl", "cand"):
            sides[role] = Side(role, binaries[role], tmp / role, mount, args.path, base_cfg, "info")
            sides[role].start()
        for side in sides.values():
            size = side.local_path.stat().st_size
            print(f"{side.role}: mounted, file size {format_size(size)}")

        offsets = read_offsets(args.offsets)
        rows = []
        stalls = {"ctl": 0, "cand": 0}
        for round_idx in range(args.rounds):
            for offset_idx, offset_mib in enumerate(offsets):
                note = ""
                attempt = 0
                base_offset = offset_mib + round_idx * args.round_shift
                while True:
                    probe = base_offset + attempt * args.retry_shift
                    order = ("ctl", "cand") if (round_idx + offset_idx + attempt) % 2 == 0 else ("cand", "ctl")
                    samples = {}
                    for role in order:
                        samples[role] = sides[role].read_sample(probe << 20, args.length)
                    ctl, cand = samples["ctl"], samples["cand"]
                    stalled_ctl = (ctl["ttfb_s"] or 0.0) > args.stall_ttfb
                    stalled_cand = (cand["ttfb_s"] or 0.0) > args.stall_ttfb
                    if stalled_ctl:
                        stalls["ctl"] += 1
                    if stalled_cand:
                        stalls["cand"] += 1
                    if (stalled_ctl or stalled_cand) and attempt < args.max_retries:
                        print(
                            f"r{round_idx} {offset_mib:>6} MiB  provider stall "
                            f"(ctl ttfb {ctl['ttfb_s'] or 0:.2f}s, cand ttfb {cand['ttfb_s'] or 0:.2f}s) "
                            f"-> retry at {probe + args.retry_shift} MiB"
                        )
                        attempt += 1
                        continue
                    if stalled_ctl or stalled_cand:
                        note = "stall"
                    if (
                        ctl["rate_kb_s"] > args.warm_rate_mbps * 1024
                        or cand["rate_kb_s"] > args.warm_rate_mbps * 1024
                    ):
                        note = "cached"
                    offset_mib = probe
                    break
                ratio = (cand["rate_kb_s"] / ctl["rate_kb_s"]) if ctl["rate_kb_s"] else 0.0
                row = {
                    "round": round_idx,
                    "offset_mib": offset_mib,
                    "ctl_rate": ctl["rate_kb_s"],
                    "cand_rate": cand["rate_kb_s"],
                    "ratio": ratio,
                    "ctl_ttfb": ctl["ttfb_s"] or 0.0,
                    "cand_ttfb": cand["ttfb_s"] or 0.0,
                    "note": note,
                }
                rows.append(row)
                print(
                    f"r{round_idx} {offset_mib:>6} MiB  ctl {ctl['rate_kb_s']/1024:>7.2f} MB/s "
                    f"(ttfb {ctl['ttfb_s'] or 0:.3f}s)  cand {cand['rate_kb_s']/1024:>7.2f} MB/s "
                    f"(ttfb {cand['ttfb_s'] or 0:.3f}s)  ratio {ratio:.2f}"
                )

        report_rows(rows)
        median = summarize(rows)
        gates: list[str] = []
        if args.min_ratio > 0:
            if median is None:
                gates.append(f"min-ratio {args.min_ratio:.2f}x: no clean pairs")
            elif median < args.min_ratio:
                gates.append(f"min-ratio {args.min_ratio:.2f}x: median {median:.2f}x")
        if stalls["ctl"] or stalls["cand"]:
            print(
                f"provider stalls observed: ctl {stalls['ctl']}, cand {stalls['cand']} "
                f"(retried up to {args.max_retries}x at shifted offsets; 'stall' rows had no clean retry)"
            )
        out = tmp / "results.json"
        out.write_text(json.dumps({"path": args.path, "mount": mount, "length": args.length,
                                   "rounds": args.rounds, "rows": rows}, indent=2))
        print(f"results: {out}")

        ctl_mount_log = sides["ctl"].log_path
        cand_mount_log = sides["cand"].log_path
        print(f"logs: {ctl_mount_log} {cand_mount_log}")

        if args.amplify:
            for side in sides.values():
                side.stop()
            amps = amplification_pass(args, base_cfg, tmp, binaries)
            if args.max_amplification > 0:
                cand_amp = amps.get("cand", 0.0)
                if cand_amp > args.max_amplification:
                    gates.append(f"max-amplification {args.max_amplification:.2f}: candidate {cand_amp:.2f}")
        if gates:
            print()
            for gate in gates:
                print(f"gate failed: {gate}")
            return 1
        if args.min_ratio > 0 or args.max_amplification > 0:
            print("gate passed")
    finally:
        for side in sides.values():
            try:
                side.stop()
            except Exception as exc:  # pragma: no cover - best effort cleanup
                print(f"cleanup: {side.role}: {exc}", file=sys.stderr)
        if not args.skip_build:
            subprocess.run(["git", "worktree", "remove", "--force", str(worktree)],
                           cwd=REPO, capture_output=True, check=False)
        if args.keep or args.workdir:
            print(f"kept: {tmp}")
        else:
            shutil.rmtree(tmp, ignore_errors=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
