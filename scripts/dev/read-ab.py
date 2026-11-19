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
import statistics
import subprocess
import sys
import tempfile
import time

from pathlib import Path

import sys

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
from qrypt_ab import (  # noqa: E402 - the module path is set up first
    F_NOCACHE,
    PREAD_BLOCK,
    Side,
    format_size,
    load_config,
    parse_size,
)

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
