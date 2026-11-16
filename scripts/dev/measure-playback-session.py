#!/usr/bin/env python3
"""Measure one read-only mounted FD through continuous reads, seeks and pauses.

Start an isolated mount and clear its VFS cache before invoking this runner.
The runner owns only its FD, never a mount, config, cache or remote fixture.
Keep output JSON outside Git. A read completion is not a decoded video frame.
"""

import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import signal
import sys
import time


MIB = 1 << 20


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--path", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--sequential-mib", type=int, default=256)
    parser.add_argument("--seek-count", type=int, default=20)
    parser.add_argument("--seek-mib", type=int, default=8)
    parser.add_argument("--seek-offset-bias", type=int, default=0,
                        help="add a fixed byte offset to seek points (e.g. 137 for unaligned reads)")
    parser.add_argument("--resume-mib", type=int, default=16)
    parser.add_argument("--pause-seconds", type=float, default=3)
    parser.add_argument("--idle-seconds", type=float, default=35)
    parser.add_argument("--timeout-seconds", type=int, default=600)
    parser.add_argument("--allow-os-cache", action="store_true",
                        help="for local runner checks; does not establish OS-cold reads")
    args = parser.parse_args()
    for field in ("sequential_mib", "seek_count", "seek_mib", "resume_mib", "timeout_seconds"):
        if getattr(args, field) <= 0:
            parser.error(f"--{field.replace('_', '-')} must be positive")
    if args.pause_seconds < 0 or args.idle_seconds < 0:
        parser.error("pause durations must be nonnegative")
    if args.seek_offset_bias < 0:
        parser.error("--seek-offset-bias must be nonnegative")
    if not args.allow_os_cache and sys.platform != "darwin":
        parser.error("OS-cold mode currently requires Darwin F_NOCACHE")
    if args.output.exists():
        parser.error("output already exists; preserve previous samples")
    if not args.path.is_file():
        parser.error("path must be an existing regular file")
    size = args.path.stat().st_size
    if size < max(args.sequential_mib, args.seek_mib, args.resume_mib) * MIB:
        parser.error("file is too small for the requested passes")
    offsets = []
    for index in range(1, args.seek_count + 1):
        point = (index + 1) // 2 if index % 2 else args.seek_count + 1 - index // 2
        offset = point * (size - args.seek_mib * MIB) // (args.seek_count + 1)
        offset += args.seek_offset_bias
        if offset + args.seek_mib * MIB > size:
            parser.error("seek offset bias extends a pass beyond EOF")
        offsets.append(offset)

    os.umask(0o077)
    args.output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    result = {
        "schema_version": 1, "file_size": size, "block_bytes": MIB,
        "os_cache_control": "allowed" if args.allow_os_cache else "F_NOCACHE",
        "cache_semantics": "same FD; no VFS clear between reads; hot/in-flight reuse remains",
        "vfs_traversal": "verify against the isolated mount's READ logs",
        "options": {k: str(v) if isinstance(v, Path) else v for k, v in vars(args).items()},
        "events": [], "complete": False,
    }

    def save():
        args.output.write_text(json.dumps(result, indent=2) + "\n")

    def event(kind, **values):
        row = {"kind": kind, "wall_ns": time.time_ns(),
               "monotonic_ns": time.monotonic_ns(), **values}
        result["events"].append(row)
        save()
        return row

    def expire(_signum, _frame):
        raise TimeoutError("playback session runner exceeded its time budget")

    signal.signal(signal.SIGALRM, expire)
    signal.alarm(args.timeout_seconds)
    fd = None
    try:
        fd = os.open(args.path, os.O_RDONLY)
        if not args.allow_os_cache:
            # Darwin sys/fcntl.h defines F_NOCACHE as 48. Do not use on Linux.
            fcntl.fcntl(fd, 48, 1)
        event("open")

        def read_pass(label, offset, length_mib):
            event("pass_begin", label=label, offset=offset, mib=length_mib)
            started = previous = time.monotonic_ns()
            read_ns = 0
            digest = hashlib.sha256()
            for index in range(length_mib):
                read_started = time.monotonic_ns()
                block = os.pread(fd, MIB, offset + index * MIB)
                finished = time.monotonic_ns()
                read_ns += finished - read_started
                event("read", label=label, index=index, offset=offset + index * MIB,
                      bytes=len(block), elapsed_ms=(finished - read_started) / 1e6,
                      sha256=hashlib.sha256(block).hexdigest())
                if len(block) != MIB:
                    raise OSError("short mounted read")
                digest.update(block)
                if (index + 1) % 8 == 0:
                    event("span", label=label, end_mib=index + 1,
                          mib_per_sec=8e9 / (finished - previous))
                    previous = finished
            elapsed = time.monotonic_ns() - started
            row = event("pass_end", label=label, offset=offset, bytes=length_mib * MIB,
                        elapsed_ms=elapsed / 1e6, read_elapsed_ms=read_ns / 1e6,
                        mib_per_sec=length_mib * 1e9 / elapsed, sha256=digest.hexdigest())
            print(f"{label}: {row['mib_per_sec']:.3f} MiB/s", flush=True)

        def pause(seconds):
            event("pause_begin", seconds=seconds)
            time.sleep(seconds)
            event("pause_end")

        read_pass("continuous", 0, args.sequential_mib)
        offset = 0
        for index, offset in enumerate(offsets, 1):
            read_pass(f"seek-{index:02d}", offset, args.seek_mib)
            if index == args.seek_count // 2:
                pause(args.pause_seconds)
        pause(args.idle_seconds)
        offset += args.seek_mib * MIB
        if offset + args.resume_mib * MIB > size:
            offset = 0
        read_pass("resume", offset, args.resume_mib)
        event("close_begin")
        os.close(fd)
        fd = None
        event("close_end")
        result["complete"] = True
        save()
    except BaseException as error:
        result["error"] = f"{type(error).__name__}: {error}"
        save()
        raise
    finally:
        if fd is not None:
            os.close(fd)
        signal.alarm(0)


if __name__ == "__main__":
    main()
