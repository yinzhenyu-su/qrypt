#!/usr/bin/env python3
"""Simulate a video player reading a mounted file and report live throughput.

The simulator keeps a playback buffer: it reads ahead through the FUSE mount
until the buffer is full, then sleeps while the virtual playhead advances at
--bitrate. Every --seek-every seconds it jumps to a new position and measures
how long the first block takes to arrive. Underruns (the playhead overtaking
the buffered data) are counted as playback stalls.

A background reporter samples the shared counters once per second, so a read
that blocks for tens of seconds still shows up as a live stall instead of
freezing the output.

Optional --mount queries the qrypt debug socket after the run and summarizes
the window events (time to first byte vs body time, timeouts) so provider
latency and mount overhead stay distinguishable. --cold clears qrypt's read
cache for that mount first, and --cold-every-seek drops the target file's cache
before each seek, so the run measures cache-miss load time.

Examples:
  scripts/dev/playback-sim.py --path ~/Qrypt/quark-encrypt/movie.mp4 --duration 60s
  scripts/dev/playback-sim.py --path ... --bitrate 40m --seek-every 10s --mount quark-encrypt
  scripts/dev/playback-sim.py --path ... --max-speed --cold --mount quark-encrypt
  scripts/dev/playback-sim.py --path ... --cold-every-seek --mount quark-encrypt
  scripts/dev/playback-sim.py --path ... --json /tmp/playback.json
"""

import argparse
import fcntl
import http.client
import json
import os
import random
import re
import socket
import statistics
import sys
import threading
import time
from urllib.parse import quote

F_NOCACHE = 48
DEFAULT_SOCKET = os.path.expanduser("~/.qrypt/run/debug.sock")
MIB = 1 << 20


def parse_size(text):
    match = re.fullmatch(r"\s*([0-9.]+)\s*([kmgt]?)i?b?\s*", text.lower())
    if not match:
        raise argparse.ArgumentTypeError(f"invalid size: {text}")
    value = float(match.group(1))
    factor = {"": 1, "k": 1 << 10, "m": 1 << 20, "g": 1 << 30, "t": 1 << 40}[match.group(2)]
    return int(value * factor)


def parse_duration(text):
    match = re.fullmatch(r"\s*([0-9.]+)\s*([smh]?)\s*", text.lower())
    if not match:
        raise argparse.ArgumentTypeError(f"invalid duration: {text}")
    value = float(match.group(1))
    return value * {"": 1, "s": 1, "m": 60, "h": 3600}[match.group(2)]


def parse_args():
    parser = argparse.ArgumentParser(description="Simulate playback through a qrypt mount.")
    parser.add_argument("--path", required=True, help="mounted file path (supports ~)")
    parser.add_argument("--duration", type=parse_duration, default=60, help="run time, e.g. 60s (default 60s)")
    parser.add_argument("--bitrate", type=parse_size, default=25 << 20, help="playback bitrate, e.g. 25m")
    parser.add_argument("--block", type=parse_size, default=4 << 20, help="read block size (default 4m)")
    parser.add_argument("--start", type=parse_size, default=0, help="initial offset (default 0)")
    parser.add_argument("--buffer", type=parse_duration, default=6, help="read-ahead buffer in seconds (default 6)")
    parser.add_argument("--seek-every", type=parse_duration, default=15, help="seconds between seeks, 0 disables")
    parser.add_argument("--seek-mode", choices=["random", "scrub"], default="random")
    parser.add_argument("--seek-count", type=int, default=0, help="stop seeking after N seeks (default unlimited)")
    parser.add_argument("--seed", type=int, default=0, help="random seed for the seek pattern (0 = random)")
    parser.add_argument("--max-speed", action="store_true",
                        help="read as fast as possible and report peak/steady throughput instead of playback stalls")
    parser.add_argument("--warmup", type=parse_duration, default=2, help="seconds excluded from max-speed steady rate (default 2)")
    parser.add_argument("--warm", action="store_true", help="use the OS page cache instead of F_NOCACHE")
    parser.add_argument("--cold", action="store_true",
                        help="clear qrypt's read cache for --mount before the run")
    parser.add_argument("--cold-every-seek", action="store_true",
                        help="drop qrypt's read cache for --path before every seek")
    parser.add_argument("--remote-path",
                        help="virtual path used by --cold-every-seek (default: derived from --path and --mount)")
    parser.add_argument("--probe-socket", default=None,
                        help="qrypt probe socket used by --cold (default: probe.sock next to --socket)")
    parser.add_argument("--json", metavar="FILE", help="write the per-second series and summary as JSON")
    parser.add_argument("--mount", help="qrypt mount name; also summarize /v1/reads window events")
    parser.add_argument("--socket", default=DEFAULT_SOCKET, help="qrypt debug socket")
    return parser.parse_args()


def max_speed_summary(completions, wall, warmup):
    samples = sorted((ts, n) for ts, n, _, kind in completions if kind in ("read", "seek") and n > 0)
    if not samples:
        return {}
    start = samples[0][0]
    windows = []
    for ts, _ in samples:
        total = sum(n for other, n in samples if ts <= other < ts + 1.0)
        windows.append(total / MIB)
    steady = [n for ts, n in samples if ts - start >= warmup]
    steady_seconds = max(wall - warmup, 0.001)
    return {
        "avg_mib_s": round(sum(n for _, n in samples) / MIB / wall, 2),
        "steady_mib_s": round(sum(steady) / MIB / steady_seconds, 2) if steady else 0,
        "peak_1s_mib_s": round(max(windows), 2),
        "window_1s_p50": round(statistics.median(windows), 2),
        "window_1s_p95": round(sorted(windows)[int(round(0.95 * (len(windows) - 1)))], 2),
        "samples": len(samples),
    }


def percentiles(values, points=(50, 95, 99)):
    if not values:
        return {}
    ordered = sorted(values)
    out = {}
    for point in points:
        index = min(len(ordered) - 1, int(round(point / 100.0 * (len(ordered) - 1))))
        out[f"p{point}"] = round(ordered[index], 3)
    out["max"] = round(ordered[-1], 3)
    out["min"] = round(ordered[0], 3)
    return out


def debug_get(socket_path, endpoint):
    conn = http.client.HTTPConnection("localhost", timeout=10)
    conn.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    conn.sock.connect(socket_path)
    conn.request("GET", endpoint)
    body = conn.getresponse().read()
    conn.close()
    return json.loads(body)


def debug_post(socket_path, endpoint, payload):
    conn = http.client.HTTPConnection("localhost", timeout=30)
    conn.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    conn.sock.connect(socket_path)
    body = json.dumps(payload).encode()
    conn.request("POST", endpoint, body=body, headers={"Content-Type": "application/json"})
    response = conn.getresponse()
    data = response.read()
    status = response.status
    conn.close()
    if status >= 300:
        detail = data.decode(errors="replace").strip()
        raise RuntimeError(f"{endpoint} returned {status}: {detail}")
    return json.loads(data) if data else {}


def probe_socket_path(args):
    if args.probe_socket:
        return os.path.expanduser(args.probe_socket)
    return os.path.join(os.path.dirname(os.path.expanduser(args.socket)), "probe.sock")


def derive_remote_path(path, mount):
    if not mount:
        return None
    parts = os.path.expanduser(path).split(os.sep)
    for index in range(len(parts) - 1, -1, -1):
        if parts[index] == mount:
            return "/" + "/".join(parts[index:])
    return None


def summarize_windows(socket_path, mount, since):
    payload = debug_get(socket_path, f"/v1/reads?mount={mount}&since={int(since)}s&limit=400")
    events = payload.get("reads") or []
    windows = [e for e in events if e.get("phase") in ("fetch_window", "prefetch_window")]
    rows = []
    for event in windows:
        extra = event.get("extra") or {}
        rows.append({
            "phase": event.get("phase"),
            "state": event.get("state"),
            "bytes": event.get("requested_bytes") or 0,
            "ms": event.get("duration_ms") or 0,
            "open_ms": extra.get("driver_open_ms") or 0,
            "body_ms": extra.get("driver_body_ms") or 0,
            "timeouts": extra.get("fetch_timeouts") or 0,
            "error": event.get("error") or "",
        })
    completed = [r for r in rows if r["state"] == "completed" and r["ms"] > 0]
    throughputs = [r["bytes"] / MIB / (r["ms"] / 1000.0) for r in completed if r["bytes"] > 0]
    return {
        "events": len(rows),
        "completed": len(completed),
        "failed": len([r for r in rows if r["state"] == "failed"]),
        "canceled": len([r for r in rows if r["state"] == "canceled"]),
        "timeouts": sum(r["timeouts"] for r in rows),
        "latency_ms": percentiles([r["ms"] for r in completed]),
        "open_ms": percentiles([r["open_ms"] for r in completed]),
        "body_ms": percentiles([r["body_ms"] for r in completed if r["body_ms"]]),
        "mib_s": percentiles(throughputs),
        "phases": {p: len([r for r in rows if r["phase"] == p]) for p in ("fetch_window", "prefetch_window")},
    }


class Playback:
    def __init__(self, args):
        self.args = args
        self.path = os.path.expanduser(args.path)
        self.size = os.stat(self.path).st_size
        self.lock = threading.Lock()
        self.bytes_read = 0
        self.pos = 0
        self.current_start = None
        self.completions = []  # (finished_at, bytes, latency_s, kind)
        self.seek_pending = None  # (requested_at, offset)
        self.seeks = []
        self.stalls = 0
        self.in_stall = False
        self.stall_seconds = 0.0
        self.stop = False

    def read_loop(self, fd):
        args = self.args
        while not self.stop:
            with self.lock:
                pos = self.pos
                pending = self.seek_pending
                if pending is not None:
                    self.seek_pending = None
                    pos = pending[1]
                    self.pos = pos
            seek_picked = time.time() if pending is not None else None
            if pos + args.block > self.size:
                pos = 0
                with self.lock:
                    self.pos = 0
            started = time.time()
            with self.lock:
                self.current_start = started
            try:
                data = os.pread(fd, min(args.block, self.size - pos), pos)
                error = None
            except OSError as exc:
                data, error = b"", exc
            finished = time.time()
            with self.lock:
                self.current_start = None
                if error is not None:
                    self.completions.append((finished, 0, finished - started, "error"))
                    self.pos = 0
                    continue
                if not data:
                    self.pos = 0
                    continue
                self.bytes_read += len(data)
                self.pos += len(data)
                kind = "seek" if pending is not None else "read"
                self.completions.append((finished, len(data), finished - started, kind))
                if kind == "seek":
                    self.seeks.append({
                        "offset": pos,
                        "queued_s": seek_picked - pending[0],
                        "first_byte_s": finished - seek_picked,
                        "total_s": finished - started,
                        "bytes": len(data),
                    })
                if len(self.completions) > 5000:
                    del self.completions[:2000]

def main():
    args = parse_args()
    if args.cold and not args.mount:
        raise SystemExit("--cold requires --mount NAME so the read cache can be cleared")
    if args.cold_every_seek and not args.mount:
        raise SystemExit("--cold-every-seek requires --mount NAME")
    if args.seed:
        random.seed(args.seed)
    probe_socket = probe_socket_path(args)
    if args.cold:
        result = debug_post(
            probe_socket,
            f"/v1/debug/cache/clear?mount={quote(args.mount)}",
            {},
        )
        print(f"cleared read cache mount={result.get('mount', args.mount)} "
              f"cleared={result.get('cleared', False)}", flush=True)
    remote_path = args.remote_path
    if args.cold_every_seek and not remote_path:
        remote_path = derive_remote_path(args.path, args.mount)
        if not remote_path:
            raise SystemExit("--cold-every-seek cannot derive a virtual path; pass --remote-path")
    play = Playback(args)
    if 0 <= args.start < play.size:
        play.pos = args.start
    fd = os.open(play.path, os.O_RDONLY)
    if not args.warm:
        try:
            fcntl.fcntl(fd, F_NOCACHE, 1)
        except OSError:
            pass

    started = time.time()
    series = []
    reader = threading.Thread(target=play.read_loop, args=(fd,), daemon=True)
    reader.start()
    next_seek = started + args.seek_every if args.seek_every > 0 else float("inf")
    last_report = started
    last_bytes = 0
    seeks = 0

    try:
        while not play.stop:
            time.sleep(0.2)
            now = time.time()
            if next_seek != float("inf") and now >= next_seek and (args.seek_count == 0 or seeks < args.seek_count):
                if args.seek_mode == "scrub":
                    span = max(args.block, play.size // 10)
                    offset = max(0, min(play.size - args.block, play.pos + random.randint(-span, span)))
                else:
                    offset = random.randint(0, max(0, play.size - args.block))
                if args.cold_every_seek:
                    try:
                        debug_post(
                            probe_socket,
                            f"/v1/debug/cache/invalidate?path={quote(remote_path)}",
                            {},
                        )
                    except Exception as exc:  # noqa: BLE001
                        print(f"[seek #{seeks + 1}] cache invalidate failed: {exc}", flush=True)
                with play.lock:
                    play.seek_pending = (now, offset)
                seeks += 1
                next_seek = now + args.seek_every
                play_seek_pos = offset
                print(f"\n[seek #{seeks}] offset={offset / (1 << 30):.2f}GiB requested", flush=True)
            if now - last_report >= 1.0:
                with play.lock:
                    total = play.bytes_read
                    current = play.current_start
                    completions = list(play.completions)
                    seek_count = len(play.seeks)
                elapsed = now - started
                playhead = elapsed * args.bitrate
                buffered = total - playhead
                if not args.max_speed:
                    if buffered < 0:
                        if not play.in_stall:
                            play.stalls += 1
                            play.in_stall = True
                        play.stall_seconds += now - last_report
                    else:
                        play.in_stall = False
                instant = (total - last_bytes) / MIB / (now - last_report)
                avg = total / MIB / elapsed
                recent = [lat for (ts, _, lat, _) in completions if now - ts <= 5][-50:]
                inflight = (now - current) if current else 0
                print(
                    f"t={elapsed:5.1f}s now={instant:6.2f}MiB/s avg={avg:6.2f}MiB/s "
                    f"read_med={(statistics.median(recent) * 1000 if recent else 0):6.0f}ms "
                    f"inflight={inflight * 1000:5.0f}ms buffer={max(0.0, buffered / args.bitrate):5.1f}s "
                    f"stalls={play.stalls} seeks={seek_count}",
                    flush=True,
                )
                series.append({
                    "t": round(elapsed, 2),
                    "mib_s": round(instant, 2),
                    "avg_mib_s": round(avg, 2),
                    "read_latency_ms": round(statistics.median(recent) * 1000, 1) if recent else 0,
                    "inflight_ms": round(inflight * 1000),
                    "buffer_s": round(max(0.0, buffered / args.bitrate), 2),
                    "stalls": play.stalls,
                    "seeks": seek_count,
                })
                last_report, last_bytes = now, total
            if now - started >= args.duration:
                play.stop = True
    finally:
        play.stop = True
        reader.join(timeout=1.0)
        os.close(fd)

    wall = time.time() - started
    with play.lock:
        completions = list(play.completions)
        seeks_list = list(play.seeks)
        total = play.bytes_read
    read_latencies = [lat for (_, n, lat, kind) in completions if kind == "read"]
    seek_latencies = [s["first_byte_s"] for s in seeks_list]
    summary = {
        "mode": "max-speed" if args.max_speed else "playback",
        "path": play.path,
        "cold_cache": args.cold,
        "cold_every_seek": args.cold_every_seek,
        "bytes": total,
        "wall_s": round(wall, 2),
        "mib_s": round(total / MIB / wall, 2) if wall else 0,
        "stalls": play.stalls,
        "stall_seconds": round(play.stall_seconds, 2),
        "seeks": len(seeks_list),
        "bitrate_mib_s": round(args.bitrate / MIB, 2),
        "read_latency_ms": percentiles([v * 1000 for v in read_latencies]),
        "seek_latency_ms": percentiles([v * 1000 for v in seek_latencies]),
        "seek_queue_ms": percentiles([s["queued_s"] * 1000 for s in seeks_list]),
        "series": series,
    }
    if args.max_speed:
        summary["max_speed"] = max_speed_summary(completions, wall, args.warmup)
    if args.mount:
        try:
            summary["qrypt_windows"] = summarize_windows(args.socket, args.mount, wall + 5)
        except Exception as exc:  # noqa: BLE001
            summary["qrypt_windows_error"] = str(exc)
    print("\n== summary ==")
    print(json.dumps({k: v for k, v in summary.items() if k != "series"}, ensure_ascii=False, indent=1))
    if args.json:
        with open(os.path.expanduser(args.json), "w") as handle:
            json.dump(summary, handle, ensure_ascii=False, indent=1)
        print(f"wrote {args.json}")


if __name__ == "__main__":
    main()
