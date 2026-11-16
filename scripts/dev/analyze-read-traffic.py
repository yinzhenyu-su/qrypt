#!/usr/bin/env python3
"""Reconcile a playback-session measurement against executor read traffic.

Inputs are the two artifacts a mounted measurement already produces:

* ``--runner``: the JSON written by scripts/dev/measure-playback-session.py,
  whose ``read`` events are the bytes the mount layer actually delivered to the
  caller.
* ``--trace``: the isolated mount's debug log (the ``op=... phase=... extra=map[...]``
  read events), whose window records carry each executor Range's offset, size,
  body bytes and outcome.

The report answers three separate questions instead of collapsing them into one
ratio:

1. How many executor body bytes were received, including rows that ended
   canceled or superseded.
2. How many of those bytes fall inside the caller's delivered intervals, per
   session and per read pass, and how much was shared with another session
   (reuse, not waste).
3. What cannot be attributed from the trace. A canceled window's unused tail
   is a range fact, not a byte fact: without per-chunk receipt we do not know
   whether those bytes arrived, so they stay in ``unknown_residual_bytes``
   instead of being reported as waste.

Raw traces and reports stay outside Git; only sanitized summaries belong in a
change's validation record. ``--self-check`` builds a small synthetic sample in
a temporary directory (never in the repository) and asserts the reconciliation.
"""

import argparse
import datetime as dt
import json
import re
from pathlib import Path


MIB = 1 << 20

LINE_PATTERN = re.compile(
    r"^\[(?P<timestamp>[^\]]+)\]\s+(?P<level>\w+)\s+\[(?P<scope>[^\]]+)\]\s+(?P<body>.*)$"
)
# The read detail rows are key=value pairs followed by a flat
# ``extra=map[key:value ...]`` projection that itself contains spaces, so the
# extra block is sliced out by its brackets instead of by whitespace.
HEAD_PATTERN = re.compile(
    r"^op=(?P<op>\S+) phase=(?P<phase>\S+) offset=(?P<offset>-?\d+) "
    r"requested=(?P<requested>-?\d+) bytes=(?P<bytes>-?\d+) "
    r"duration_us=(?P<duration_us>-?\d+) state=(?P<state>\S+)"
)
EXTRA_PATTERN = re.compile(r"(\w+):([^\s\]]*)")

# Window-level records: one row per executor Range completion with its body
# bytes. Source-range rows are used only to check start/end pairing.
WINDOW_PHASES = ("prefetch_window", "fetch_window")
SOURCE_BEGIN_PHASE = "source_range_begin"
SOURCE_END_PHASE = "source_range_end"
SOURCE_PHASES = (SOURCE_BEGIN_PHASE, SOURCE_END_PHASE)
READ_PHASE = "read"


def parse_timestamp(value):
    """Return epoch nanoseconds for a log timestamp, or None."""
    text = value.strip()
    # The logger writes local time with millisecond precision and an explicit
    # zone offset, e.g. 2026-11-16 12:58:37.807 +08:00.
    for layout in ("%Y-%m-%d %H:%M:%S.%f %z", "%Y-%m-%d %H:%M:%S %z"):
        try:
            parsed = dt.datetime.strptime(text, layout)
        except ValueError:
            continue
        return int(parsed.timestamp() * 1e9)
    return None


def extra_block(body):
    """Return the ``extra=map[...]`` projection text, or an empty string."""
    marker = "extra=map["
    start = body.find(marker)
    if start < 0:
        return ""
    text = body[start + len(marker):]
    end = text.rfind("]")
    return text[:end] if end >= 0 else text


def parse_extra(text):
    """Parse the flat ``map[key:value ...]`` projection."""
    extra = {}
    for key, value in EXTRA_PATTERN.findall(text):
        if value == "":
            extra[key] = ""
            continue
        if value in ("true", "false"):
            extra[key] = value == "true"
            continue
        try:
            extra[key] = int(value)
        except ValueError:
            extra[key] = value
    return extra


def parse_trace(path):
    """Extract window, delivery and source-range records from a mount debug log.

    Source-range rows are kept per ``range_id`` rather than as a bare count: a
    driver call that never reports its end marks a truncated log, and a call
    that no window row encloses marks a missing window row. Together they prove
    that the window rows are complete, which interval coverage alone cannot.
    """
    windows = []
    deliveries = []
    ranges = {}
    source_ends = 0
    rows = 0
    with open(path, encoding="utf-8", errors="replace") as handle:
        for line in handle:
            match = LINE_PATTERN.match(line.rstrip("\n"))
            if not match:
                continue
            body = match.group("body")
            head = HEAD_PATTERN.match(body)
            if not head:
                continue
            rows += 1
            phase = head.group("phase")
            if phase in SOURCE_PHASES:
                extra = parse_extra(extra_block(body))
                range_id = extra.get("range_id", 0)
                record = ranges.setdefault(
                    range_id,
                    {
                        "range_id": range_id,
                        "offset": int(head.group("offset")),
                        "size": int(head.group("requested")),
                        "begins": 0,
                        "ends": 0,
                    },
                )
                if phase == SOURCE_END_PHASE:
                    record["ends"] += 1
                    source_ends += 1
                else:
                    record["begins"] += 1
                continue
            if phase == READ_PHASE:
                extra = parse_extra(extra_block(body))
                session = extra.get("session_id")
                deliveries.append(
                    {
                        "session_id": session if isinstance(session, int) else 0,
                        "offset": int(head.group("offset")),
                        "size": int(head.group("bytes")),
                    }
                )
                continue
            if phase not in WINDOW_PHASES:
                continue
            extra = parse_extra(extra_block(body))
            offset = int(head.group("offset"))
            requested = int(head.group("requested"))
            body_bytes = extra.get("body_bytes")
            body_known = isinstance(body_bytes, int) and not isinstance(body_bytes, bool)
            if not body_known:
                # The row's byte count is caller delivery, not executor
                # traffic: substituting it would hide retries and unpublished
                # bytes, and a missing count is an unknown, not a zero.
                body_bytes = 0
            session = extra.get("session_id")
            finish_ns = parse_timestamp(match.group("timestamp"))
            duration_us = int(head.group("duration_us"))
            streams = extra.get("read_streams", extra.get("granted_streams", 1))
            in_flight = extra.get("actual_in_flight", 0)
            windows.append(
                {
                    "phase": phase,
                    "at_ns": finish_ns,
                    # A window belongs to the pass that requested it, not to the
                    # pass that happened to be running when its row was logged:
                    # a canceled tail is written after the seek that killed it.
                    "request_ns": finish_ns - duration_us * 1000 if finish_ns is not None else None,
                    "offset": offset,
                    "size": requested,
                    "body_bytes": body_bytes,
                    "body_known": body_known,
                    "state": head.group("state"),
                    "speculative": bool(extra.get("speculative", phase == "prefetch_window")),
                    "session_id": session if isinstance(session, int) else 0,
                    "request_id": extra.get("request_id", 0),
                    "range_id": extra.get("range_id", 0),
                    # progress_end is the furthest received offset; unlike the
                    # cumulative body_bytes it never counts a replay twice, but
                    # it is a contiguous frontier only for a single-stream read.
                    "progress_end": extra.get("progress_end"),
                    "read_streams": streams if isinstance(streams, int) else 1,
                    "actual_in_flight": in_flight if isinstance(in_flight, int) else 0,
                }
            )
    return {
        "windows": windows,
        "deliveries": deliveries,
        "source_ranges": list(ranges.values()),
        "source_range_ends": source_ends,
        "read_row_count": rows,
    }


def parse_runner(path):
    """Extract caller deliveries and pass intervals from the runner JSON."""
    result = json.loads(Path(path).read_text(encoding="utf-8"))
    deliveries = []
    passes = {}
    open_pass = None
    for event in result.get("events", []):
        kind = event.get("kind")
        label = event.get("label")
        if kind == "read":
            if label is None:
                continue
            deliveries.append(
                {
                    "label": label,
                    "offset": int(event["offset"]),
                    "size": int(event["bytes"]),
                }
            )
        elif kind == "pass_begin":
            open_pass = label
            passes.setdefault(label, {})["begin_ns"] = event.get("wall_ns")
        elif kind == "pass_end":
            passes.setdefault(label, {})["end_ns"] = event.get("wall_ns")
            open_pass = None
    if open_pass is not None:
        passes.setdefault(open_pass, {})["end_ns"] = None
    return {
        "deliveries": deliveries,
        "passes": passes,
        "complete": bool(result.get("complete")),
        "file_size": result.get("file_size", 0),
    }


def union_overlap(left, right):
    """Return the length of the union of ``left`` covered by ``right``."""
    total = 0
    for start, end in left:
        total += overlap(right, start, end)
    return total


def merge(intervals):
    """Merge [start, end) intervals into a sorted disjoint list."""
    merged = []
    for start, end in sorted(intervals):
        if end <= start:
            continue
        if merged and start <= merged[-1][1]:
            merged[-1][1] = max(merged[-1][1], end)
        else:
            merged.append([start, end])
    return merged


def overlap(merged, start, end):
    """Return the covered length of [start, end) by a merged interval list."""
    total = 0
    for left, right in merged:
        if right <= start:
            continue
        if left >= end:
            break
        total += min(end, right) - max(start, left)
    return total


def span(entry):
    return (entry["offset"], entry["offset"] + entry["size"])


GLOBAL_DELIVERY = object()


def bucket_report(windows, deliveries, per_session_delivery, bucket_deliveries=GLOBAL_DELIVERY):
    """Reconcile one bucket (all rows, one session, or one pass).

    ``deliveries`` is every runner delivery and stays the reference for which
    window bytes were consumed at all. ``bucket_deliveries`` is the subset this
    bucket is responsible for: a pass must be divided by its own delivery, not
    by the whole session's, or one long pass would flatter every other pass.
    ``GLOBAL_DELIVERY`` keeps the default denominator, while ``None`` means the
    bucket's delivery is unknown, so no ratio is reported instead of a
    misleading one.

    ``per_session_delivery`` is None when the trace did not record every read,
    in which case sessions cannot be separated and all delivered bytes are
    attributed as useful to whichever window covers them.
    """
    all_spans = merge([span(entry) for entry in deliveries])
    merged_by_session = {}
    if per_session_delivery is not None:
        for session, spans in per_session_delivery.items():
            merged_by_session[session] = merge(spans)

    report = {
        "executor_body_bytes": 0,
        "delivered_bytes_total": sum(entry["size"] for entry in deliveries),
        "delivered_bytes_unique": sum(end - start for start, end in all_spans),
        "attributed_useful_bytes": 0,
        "attributed_shared_bytes": 0,
        "unused_tail_bytes_completed": 0,
        "canceled_unused_tail_range_bytes": 0,
        "unknown_residual_bytes": 0,
        "replay_bytes": 0,
        "body_unknown_windows": 0,
        "duplicate_attempt_bytes": 0,
        "undelivered_window_bytes": 0,
        "window_count": len(windows),
        "completed_count": 0,
        "canceled_count": 0,
        "small_window_count": 0,
        "unmatched_pass_windows": 0,
        "cross_pass_tail_bytes": 0,
        "complete": True,
        "completeness_problems": [],
    }
    previous_spans = []
    for window in windows:
        start, end = span(window)
        size = max(0, end - start)
        body = max(0, window["body_bytes"])
        if not window.get("body_known", True):
            report["body_unknown_windows"] += 1
        completed = window["state"] == "completed"
        progress = window.get("progress_end")
        streams = window.get("read_streams", 1)
        in_flight = window.get("actual_in_flight", 0)
        # body_bytes is cumulative traffic: a retried attempt counts its bytes
        # again, so it cannot say where the received prefix ends. progress_end
        # is the furthest received offset and never counts a replay twice, but
        # it is a contiguous frontier only for a single-stream window.
        prefix_trusted = (
            window.get("body_known", True)
            and isinstance(progress, int)
            and not isinstance(progress, bool)
            and streams <= 1
            and in_flight <= 1
        )
        if completed:
            # A completed window delivered its whole request; a short one that
            # still returned without an error keeps the prefix it did return.
            receipt = min(size, body)
            receipt_end = start + receipt
            receipt_known = True
        elif prefix_trusted:
            receipt_end = min(end, max(start, progress))
            receipt = receipt_end - start
            receipt_known = True
        else:
            # Without an evidenced prefix the body is only a byte volume: which
            # offsets came back is unknown, so nothing can be called useful.
            receipt, receipt_end, receipt_known = 0, start, False
        replay = max(0, body - receipt) if receipt_known else 0
        covered_any = overlap(all_spans, start, receipt_end)
        if per_session_delivery is None:
            covered_own = covered_any
        else:
            covered_own = overlap(merged_by_session.get(window["session_id"], []), start, receipt_end)
        if receipt_known:
            useful = min(receipt, covered_own)
            shared = min(receipt - useful, max(0, covered_any - covered_own))
            remaining = receipt - useful - shared
        else:
            useful = 0
            shared = 0
            remaining = 0
        report["executor_body_bytes"] += body
        report["replay_bytes"] += replay
        report["attributed_useful_bytes"] += useful
        report["attributed_shared_bytes"] += shared
        report["completed_count"] += int(completed)
        report["canceled_count"] += int(not completed)
        if completed:
            report["unused_tail_bytes_completed"] += remaining
            if covered_any == 0 and receipt:
                report["undelivered_window_bytes"] += receipt
        elif receipt_known:
            # The received prefix is a byte fact; whether those bytes were the
            # delivered ones is still unknowable, so they stay unknown.
            report["unknown_residual_bytes"] += remaining
            report["canceled_unused_tail_range_bytes"] += max(
                0, size - max(overlap(all_spans, start, end), receipt)
            )
        else:
            report["unknown_residual_bytes"] += body
        if size and size < window.get("chunk_size", 0):
            report["small_window_count"] += 1
        duplicate = 0
        for left, right in previous_spans:
            duplicate += max(0, min(end, right) - max(start, left))
        if duplicate:
            report["duplicate_attempt_bytes"] += min(body, duplicate)
        previous_spans.append((start, end))
        if window.get("cross_pass_tail"):
            report["cross_pass_tail_bytes"] += max(0, body)
        if "pass" in window and window["pass"] is None:
            report["unmatched_pass_windows"] += 1
    attributed = (
        report["attributed_useful_bytes"]
        + report["attributed_shared_bytes"]
        + report["unused_tail_bytes_completed"]
        + report["unknown_residual_bytes"]
        + report["replay_bytes"]
    )
    report["reconciliation_error_bytes"] = report["executor_body_bytes"] - attributed
    if bucket_deliveries is None:
        # A bucket whose own delivery is unknown must not borrow another
        # bucket's: the ratio would be an artifact of the denominator.
        report["delivered_bytes_total"] = None
        report["delivered_bytes_unique"] = None
        report["amplification_over_delivered"] = None
        report["amplification_over_unique"] = None
        report["amplification_known"] = False
        return report
    if bucket_deliveries is not GLOBAL_DELIVERY:
        report["delivered_bytes_total"] = sum(entry["size"] for entry in bucket_deliveries)
        report["delivered_bytes_unique"] = sum(
            end - start for start, end in merge([span(entry) for entry in bucket_deliveries])
        )
    if report["body_unknown_windows"]:
        # Without a traffic count for every row the sum is a lower bound, so it
        # cannot support an acceptance ratio.
        report["complete"] = False
        report["completeness_problems"] = [
            f"{report['body_unknown_windows']} window row(s) have no executor body count; "
            "traffic is unknown"
        ]
        report["amplification_known"] = False
        report["amplification_over_delivered"] = None
        report["amplification_over_unique"] = None
        return report
    known = bool(report["window_count"]) and report["executor_body_bytes"] > 0
    known = known and (report["delivered_bytes_total"] or 0) > 0
    report["amplification_known"] = known
    if known:
        report["amplification_over_delivered"] = (
            report["executor_body_bytes"] / report["delivered_bytes_total"]
        )
        report["amplification_over_unique"] = (
            report["executor_body_bytes"] / report["delivered_bytes_unique"]
        )
    else:
        report["amplification_over_delivered"] = None
        report["amplification_over_unique"] = None
    return report


def assign_passes(windows, passes):
    """Pair each window with the pass whose interval holds the request itself.

    Completion time is the wrong key: a prefetch canceled by a seek is logged
    after the next pass has already begun, and charging it to that pass would
    invent traffic in a pass that never asked for it. Windows whose request
    started before the first pass stay unattributed and are reported as such.
    """
    if not passes:
        return
    spans = [
        (label, info.get("begin_ns"), info.get("end_ns"))
        for label, info in passes.items()
        if info.get("begin_ns") is not None
    ]
    spans.sort(key=lambda item: item[1])
    for window in windows:
        requested_at = window.get("request_ns")
        if requested_at is None:
            requested_at = window.get("at_ns")
        window["pass"] = None
        window["cross_pass_tail"] = False
        if requested_at is None:
            continue
        for label, begin, end in spans:
            if begin is None:
                continue
            if requested_at < begin:
                continue
            if end is None or requested_at <= end:
                window["pass"] = label
                at = window.get("at_ns")
                window["cross_pass_tail"] = end is not None and at is not None and at > end
                break


def delivery_multiset(entries):
    """Count deliveries by (offset, size) so a partial trace is detectable."""
    counts = {}
    for entry in entries:
        key = (entry.get("offset"), entry.get("size"))
        counts[key] = counts.get(key, 0) + 1
    return counts


def session_attribution(trace_deliveries, deliveries):
    """Return per-session delivery spans, or None plus the reason they are unusable.

    Interval coverage is not evidence. Two sessions reading the same range
    produce 100% interval coverage while the trace recorded only one of them,
    and the unrecorded session would then be reported as pure reuse. Each runner
    delivery must appear in the trace with a session identity instead.
    """
    if not trace_deliveries:
        return None, "the trace recorded no read rows, so sessions cannot be separated"
    missing = delivery_multiset(deliveries)
    matched = 0
    for entry in trace_deliveries:
        key = (entry.get("offset"), entry.get("size"))
        if missing.get(key):
            missing[key] -= 1
            matched += 1
    if matched < len(deliveries):
        return None, (
            f"the trace matched {matched} of {len(deliveries)} deliveries; "
            "session identity is incomplete"
        )
    unidentified = sum(1 for entry in trace_deliveries if not entry.get("session_id"))
    if unidentified:
        return None, f"{unidentified} traced deliveries carry no session identity"
    per_session = {}
    for entry in trace_deliveries:
        per_session.setdefault(entry["session_id"], []).append(span(entry))
    return per_session, None


def reconcile(trace, runner):
    windows = trace["windows"]
    for window in windows:
        window["chunk_size"] = 512 * 1024
    assign_passes(windows, runner["passes"])
    # The runner is authoritative for what the mount layer delivered. The trace
    # adds session identity, but only when it recorded every read with a
    # session: the logger suppresses repeated rows, so a partial trace must not
    # split the delivery between sessions.
    deliveries = runner["deliveries"]
    trace_deliveries = trace.get("deliveries", [])
    trace_unique = sum(
        end - start for start, end in merge([span(entry) for entry in trace_deliveries])
    )
    delivered_unique = sum(end - start for start, end in merge([span(entry) for entry in deliveries]))
    coverage = trace_unique / delivered_unique if delivered_unique else 0.0
    per_session_delivery, session_gap = session_attribution(trace_deliveries, deliveries)
    if per_session_delivery is not None and coverage < 0.99:
        per_session_delivery = None
        session_gap = (
            f"trace read rows cover {coverage:.1%} of delivered bytes; "
            "session identity is incomplete"
        )

    sessions = sorted({window["session_id"] for window in windows})
    per_session = {}
    for session in sessions:
        rows = [window for window in windows if window["session_id"] == session]
        if not rows:
            continue
        if per_session_delivery is None:
            own = None
        else:
            own = [
                {"offset": start, "size": end - start}
                for start, end in per_session_delivery.get(session, [])
            ]
        per_session[str(session)] = bucket_report(rows, deliveries, per_session_delivery, own)
    per_pass = {}
    for label in runner["passes"]:
        rows = [window for window in windows if window.get("pass") == label]
        own = [entry for entry in deliveries if entry.get("label") == label]
        report = bucket_report(rows, deliveries, per_session_delivery, own)
        # Tail traffic from a neighbouring pass can land inside this pass's
        # windows; it is reported separately instead of being divided in.
        others = [entry for entry in deliveries if entry.get("label") != label]
        report["cross_pass_delivered_bytes"] = union_overlap(
            merge([span(window) for window in rows]),
            merge([span(entry) for entry in others]),
        )
        per_pass[label] = report

    window_spans = merge([span(window) for window in windows])
    delivery_spans = merge([span(entry) for entry in deliveries])
    covered_delivery = union_overlap(window_spans, delivery_spans)
    window_coverage = covered_delivery / delivered_unique if delivered_unique else 1.0
    # Covered intervals do not prove the rows are complete: a dropped window row
    # is invisible when a neighbouring window still covers the same bytes. The
    # driver-call rows do prove it, because every call opens and closes.
    ranges = trace.get("source_ranges", [])
    unpaired = [
        record for record in ranges if record.get("begins", 0) != record.get("ends", 0)
    ]
    unwindowed = [
        record
        for record in ranges
        if record.get("ends", 0)
        and record.get("size", 0) > 0
        and overlap(window_spans, record["offset"], record["offset"] + record["size"])
        < record["size"]
    ]
    problems = []
    if not runner["complete"]:
        problems.append("runner reported an incomplete pass; the comparison covers a partial run")
    if not windows:
        problems.append("no read window rows in the trace; window attribution is impossible")
    elif window_coverage < 0.99:
        problems.append(
            f"trace windows cover {window_coverage:.1%} of delivered bytes; the log is probably truncated"
        )
    if unpaired:
        problems.append(
            f"{len(unpaired)} driver range(s) have an unmatched begin/end row; the log ends mid-call"
        )
    if unwindowed:
        problems.append(
            f"{len(unwindowed)} driver range(s) sit outside every window row; window rows are missing"
        )

    totals = bucket_report(windows, deliveries, per_session_delivery)
    if totals["body_unknown_windows"]:
        problems.append(
            f"{totals['body_unknown_windows']} window row(s) have no executor body count; "
            "traffic is unknown"
        )
    totals["complete"] = not problems
    totals["completeness_problems"] = problems
    if problems:
        # A partial log or a partial run cannot support an acceptance ratio.
        totals["amplification_known"] = False
        totals["amplification_over_delivered"] = None
        totals["amplification_over_unique"] = None
    report = {
        "schema_version": 1,
        "inputs": {
            "window_count": len(windows),
            "source_range_ends": trace["source_range_ends"],
            "read_rows": trace["read_row_count"],
            "passes": sorted(runner["passes"]),
            "runner_complete": runner["complete"],
        },
        "totals": totals,
        "session_delivery_coverage": round(coverage, 4),
        "window_attribution_coverage": round(window_coverage, 4),
        "per_session_attribution": per_session_delivery is not None,
        "per_session_gap": session_gap,
        "unpaired_source_ranges": len(unpaired),
        "unwindowed_source_ranges": len(unwindowed),
        "sessions": per_session,
        "passes": per_pass,
        "windows": windows,
        "metric_boundaries": [
            "executor body bytes are plaintext bytes received by the read executor, not wire bytes",
            "delivered bytes are the mount layer's caller-facing bytes, not decoded frames or player consumption",
            "unique delivery is the union of delivered spans; repeated delivery inflates delivered_bytes_total only",
            "a canceled window's unused tail is a range claim; its unattributed received bytes stay in unknown_residual_bytes",
            "useful bytes can exceed delivered bytes because a window's body is attributed to the delivered interval it covers even when the same interval was downloaded twice",
            "shared bytes were delivered to another session and are reuse, not waste",
            "per-session attribution requires the trace to record every read with a session identity; a suppressed trace reports global delivery only",
            "a window's received prefix is progress_end for a single-stream window and the request size once it completes; cumulative body_bytes may count a replay twice",
            "a canceled window without an evidenced prefix contributes its whole body to unknown_residual_bytes",
            "a window row without a traffic count is unknown traffic, not delivery traffic: the report cannot support an acceptance ratio",
            "duplicate attempts are later windows overlapping an earlier window range, counted at most once per window",
            "closing the FD may drain canceled tails after the last caller read",
        ],
    }
    return report


def build_self_check_fixture(directory, with_reads=False):
    """Write a non-uniform synthetic trace and runner with duplicate/canceled tail."""
    trace_path = Path(directory) / "read-trace.log"
    runner_path = Path(directory) / "result.json"
    base = 1_700_000_000_000_000_000  # runner wall_ns
    stamp = dt.datetime.fromtimestamp(base / 1e9).astimezone()
    lines = []

    def log_row(at_ms, body):
        moment = stamp + dt.timedelta(milliseconds=at_ms)
        lines.append(f"[{moment:%Y-%m-%d %H:%M:%S.%f %z}] DEBUG [READ] {body}")

    def window(at_ms, phase, offset, size, body, state, session, request, speculative, progress=None):
        progress_field = f" progress_end:{progress}" if progress is not None else ""
        log_row(
            at_ms,
            f"op=read-{session} phase={phase} offset={offset} requested={size} "
            f"bytes={body} duration_us=2000 state={state} "
            f"extra=map[body_bytes:{body} session_id:{session} request_id:{request} "
            f"read_streams:1 actual_in_flight:1{progress_field} "
            f"speculative:{'true' if speculative else 'false'} window_start:{offset // 524288} "
            f"window_end:{(offset + size) // 524288}]",
        )

    def read_row(at_ms, offset, size, session, request):
        log_row(
            at_ms,
            f"op=read-{session} phase=read offset={offset} requested={size} bytes={size} "
            f"duration_us=2000 state=completed "
            f"extra=map[session_id:{session} request_id:{request} source:remote]",
        )

    def source_range(at_ms, phase, offset, size, range_id):
        log_row(
            at_ms,
            f"op=read-1 phase={phase} offset={offset} requested={size} bytes=0 "
            f"duration_us=1 state=completed extra=map[range_id:{range_id} session_id:1 speculative:true]",
        )

    # Delivered passes: 1 MiB continuous, then 256 KiB at 4 MiB, with the
    # second pass overlapping the first window's tail.
    deliveries = [
        {"kind": "pass_begin", "label": "continuous", "offset": 0, "mib": 1, "wall_ns": base},
        {"kind": "read", "label": "continuous", "index": 0, "offset": 0, "bytes": 524288},
        {"kind": "read", "label": "continuous", "index": 1, "offset": 524288, "bytes": 524288},
        {"kind": "pass_end", "label": "continuous", "offset": 0, "bytes": 1 << 20, "wall_ns": base + 200_000_000},
        {"kind": "pass_begin", "label": "seek-01", "offset": 4 << 20, "mib": 0, "wall_ns": base + 300_000_000},
        {"kind": "read", "label": "seek-01", "index": 0, "offset": 4 << 20, "bytes": 262144},
        {"kind": "pass_end", "label": "seek-01", "offset": 4 << 20, "bytes": 262144, "wall_ns": base + 400_000_000},
    ]

    # Window 1 completes 512 KiB, all delivered: useful.
    window(10, "prefetch_window", 0, 524288, 524288, "completed", 1, 1, True)
    # Window 2 completes 1 MiB but only the first 512 KiB is delivered: the
    # tail is a real unused claim.
    window(20, "prefetch_window", 524288, 1 << 20, 1 << 20, "completed", 1, 2, True)
    # Window 3 is a duplicate attempt over window 2's range and is canceled
    # after receiving its head plus part of the unused tail: the delivered
    # prefix is useful, and the received bytes past it are not attributable.
    window(
        30,
        "prefetch_window",
        524288,
        1 << 20,
        768 * 1024,
        "canceled",
        1,
        3,
        True,
        progress=524288 + 768 * 1024,
    )
    # Window 4 is a small metadata-shaped foreground range with no delivery.
    window(310, "fetch_window", 4 << 20, 4096, 4096, "completed", 2, 1, False)
    # Window 5 covers the seek delivery and completes.
    window(320, "fetch_window", 4 << 20, 524288, 524288, "completed", 2, 1, False)
    # Window 6 is session 1's speculative window at the seek position: session 2
    # delivered half of it (reuse), the other half was never consumed.
    window(330, "prefetch_window", 4 << 20, 524288, 524288, "completed", 1, 4, True)
    # Every admitted driver call opens and closes, and each one is enclosed by
    # the window row that requested it: that is what proves the window rows are
    # complete, which covered intervals alone cannot.
    for row, (offset, size) in enumerate(
        [
            (0, 524288),
            (524288, 1 << 20),
            (524288, 1 << 20),
            (4 << 20, 4096),
            (4 << 20, 524288),
            (4 << 20, 524288),
        ],
        start=1,
    ):
        source_range(row * 10, "source_range_begin", offset, size, row)
        source_range(row * 10 + 5, "source_range_end", offset, size, row)
    if with_reads:
        # A complete read trace gives every delivery a session identity; the
        # suppressed case (default) must fall back to global attribution.
        read_row(11, 0, 524288, 1, 1)
        read_row(21, 524288, 524288, 1, 2)
        read_row(321, 4 << 20, 262144, 2, 1)

    trace_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    runner_path.write_text(
        json.dumps({"complete": True, "file_size": 64 << 20, "events": deliveries}, indent=2),
        encoding="utf-8",
    )
    return trace_path, runner_path


BODY_BYTES = 524288 + (1 << 20) + 768 * 1024 + 4096 + 524288 + 524288
DELIVERED_UNIQUE = (1 << 20) + 262144


def self_check():
    """Validate both attribution modes on a non-uniform synthetic sample."""
    import tempfile

    with tempfile.TemporaryDirectory() as directory:
        # A suppressed trace: delivery stays global, so cross-session reuse
        # cannot be separated and must not be claimed.
        trace_path, runner_path = build_self_check_fixture(directory)
        report = reconcile(parse_trace(trace_path), parse_runner(runner_path))
        totals = report["totals"]
        assert report["per_session_attribution"] is False, report["session_delivery_coverage"]
        assert totals["executor_body_bytes"] == BODY_BYTES, totals
        assert totals["delivered_bytes_unique"] == DELIVERED_UNIQUE, totals
        assert totals["delivered_bytes_total"] == DELIVERED_UNIQUE, totals
        assert totals["attributed_useful_bytes"] == 2101248, totals
        assert totals["attributed_shared_bytes"] == 0, totals
        assert totals["unused_tail_bytes_completed"] == 1 << 20, totals
        assert totals["unknown_residual_bytes"] == 256 * 1024, totals
        assert totals["canceled_unused_tail_range_bytes"] == 262144, totals
        assert totals["duplicate_attempt_bytes"] == 768 * 1024 + 4096 + 524288, totals
        assert totals["small_window_count"] == 1, totals
        assert totals["reconciliation_error_bytes"] == 0, totals
        assert totals["canceled_count"] == 1 and totals["completed_count"] == 5, totals
        assert abs(totals["amplification_over_unique"] - BODY_BYTES / DELIVERED_UNIQUE) < 1e-9

    with tempfile.TemporaryDirectory() as directory:
        # A complete trace: per-session attribution separates reuse from waste.
        trace_path, runner_path = build_self_check_fixture(directory, with_reads=True)
        report = reconcile(parse_trace(trace_path), parse_runner(runner_path))
        totals = report["totals"]
        assert report["per_session_attribution"] is True, report["session_delivery_coverage"]
        assert totals["attributed_useful_bytes"] == 1839104, totals
        assert totals["attributed_shared_bytes"] == 262144, totals
        assert totals["unused_tail_bytes_completed"] == 1 << 20, totals
        assert totals["unknown_residual_bytes"] == 256 * 1024, totals
        assert totals["reconciliation_error_bytes"] == 0, totals
        assert report["sessions"]["1"]["attributed_useful_bytes"] == 1572864, report["sessions"]
        assert report["sessions"]["2"]["attributed_useful_bytes"] == 266240, report["sessions"]
        assert report["sessions"]["1"]["attributed_shared_bytes"] == 262144, report["sessions"]
        assert report["passes"]["continuous"]["attributed_useful_bytes"] == 1572864, report["passes"]
        assert report["passes"]["seek-01"]["attributed_useful_bytes"] == 266240, report["passes"]
        assert report["passes"]["continuous"]["window_count"] == 3, report["passes"]
        # Each pass is divided by its own delivery: the continuous pass
        # downloaded 1 MiB, so its windows' 2.25 MiB is a 2.25x amplification.
        assert report["passes"]["continuous"]["delivered_bytes_total"] == 1 << 20, report["passes"]
        assert abs(report["passes"]["continuous"]["amplification_over_delivered"] - 2.25) < 1e-9
        assert report["passes"]["seek-01"]["delivered_bytes_total"] == 262144, report["passes"]
        assert report["passes"]["seek-01"]["cross_pass_delivered_bytes"] == 0, report["passes"]
        assert totals["complete"] is True and totals["completeness_problems"] == [], totals
    self_check_review()
    print("read-traffic self-check passed")


def self_check_review():
    """Pin the review's counter-examples: receipts, passes, sessions, completeness."""
    import tempfile

    base = 1_700_000_000_000_000_000
    stamp = dt.datetime.fromtimestamp(base / 1e9).astimezone()

    def write(directory, windows, events, complete=True, ranges=()):
        lines = []
        for window in windows:
            at_ms = window["at_ms"]
            moment = stamp + dt.timedelta(milliseconds=at_ms)
            fields = []
            if not window.get("omit_body"):
                fields.append(f"body_bytes:{window['body']}")
            fields.extend(
                [
                    f"session_id:{window.get('session', 1)}",
                    f"request_id:{window.get('request', 1)}",
                    f"read_streams:{window.get('streams', 1)}",
                    f"actual_in_flight:{window.get('in_flight', 1)}",
                ]
            )
            extra = " ".join(fields)
            if window.get("progress") is not None:
                extra += f" progress_end:{window['progress']}"
            extra += f" speculative:{'true' if window.get('speculative', True) else 'false'}"
            lines.append(
                f"[{moment:%Y-%m-%d %H:%M:%S.%f %z}] DEBUG [READ] op=read-1 "
                f"phase={window.get('phase', 'prefetch_window')} offset={window['offset']} "
                f"requested={window['size']} bytes={window['body']} duration_us=2000 "
                f"state={window['state']} extra=map[{extra}]"
            )
        for index, (offset, size, paired) in enumerate(ranges, start=1):
            for phase, at_ms in (
                ("source_range_begin", 5 + index),
                ("source_range_end", 6 + index),
            ):
                if phase == "source_range_end" and not paired:
                    continue
                moment = stamp + dt.timedelta(milliseconds=at_ms)
                lines.append(
                    f"[{moment:%Y-%m-%d %H:%M:%S.%f %z}] DEBUG [READ] op=read-1 phase={phase} "
                    f"offset={offset} requested={size} bytes=0 duration_us=1 state=completed "
                    f"extra=map[range_id:{index} session_id:1 speculative:true]"
                )
        trace_path = Path(directory) / "trace.log"
        trace_path.write_text("\n".join(lines) + "\n" if lines else "", encoding="utf-8")
        runner_path = Path(directory) / "result.json"
        runner_path.write_text(
            json.dumps({"complete": complete, "file_size": 64 << 20, "events": events}),
            encoding="utf-8",
        )
        return trace_path, runner_path

    def report_for(directory, windows, events, complete=True, ranges=()):
        trace_path, runner_path = write(directory, windows, events, complete, ranges)
        return reconcile(parse_trace(trace_path), parse_runner(runner_path))

    def pass_events(label, offset, size, begin_ms, end_ms):
        return [
            {"kind": "pass_begin", "label": label, "offset": offset, "mib": 0, "wall_ns": base + begin_ms * 1_000_000},
            {"kind": "read", "label": label, "index": 0, "offset": offset, "bytes": size},
            {"kind": "pass_end", "label": label, "offset": offset, "bytes": size, "wall_ns": base + end_ms * 1_000_000},
        ]

    # (5) A canceled 8 MiB window received its first 1 MiB while a separate read
    # delivered [7 MiB, 8 MiB): the request overlaps that delivery, the received
    # prefix does not, so the prefix earns no useful bytes.
    with tempfile.TemporaryDirectory() as directory:
        report = report_for(
            directory,
            [
                {"at_ms": 10, "offset": 0, "size": 8 << 20, "body": 1 << 20, "state": "canceled", "progress": 1 << 20},
                {"at_ms": 20, "offset": 7 << 20, "size": 1 << 20, "body": 1 << 20, "state": "completed", "progress": 8 << 20},
            ],
            pass_events("read", 7 << 20, 1 << 20, 0, 100),
            ranges=[(0, 8 << 20, True), (7 << 20, 1 << 20, True)],
        )
        totals = report["totals"]
        assert totals["attributed_useful_bytes"] == 1 << 20, totals
        assert totals["unknown_residual_bytes"] == 1 << 20, totals
        assert totals["canceled_unused_tail_range_bytes"] == 7 << 20, totals
        assert totals["unused_tail_bytes_completed"] == 0, totals
        assert totals["reconciliation_error_bytes"] == 0, totals
        assert abs(totals["amplification_over_delivered"] - 2.0) < 1e-9, totals
        assert totals["complete"] is True, totals

    # (5b) The same window without an evidenced prefix has no attributable
    # receipt at all: its whole body is unknown and no tail is claimed.
    with tempfile.TemporaryDirectory() as directory:
        totals = report_for(
            directory,
            [
                {"at_ms": 10, "offset": 0, "size": 8 << 20, "body": 1 << 20, "state": "canceled"},
                {"at_ms": 20, "offset": 7 << 20, "size": 1 << 20, "body": 1 << 20, "state": "completed", "progress": 8 << 20},
            ],
            pass_events("read", 7 << 20, 1 << 20, 0, 100),
        )["totals"]
        assert totals["attributed_useful_bytes"] == 1 << 20, totals
        assert totals["unknown_residual_bytes"] == 1 << 20, totals
        assert totals["canceled_unused_tail_range_bytes"] == 0, totals
        assert totals["reconciliation_error_bytes"] == 0, totals

    # (5c) A delivery inside the received prefix is useful: the prefix, not the
    # request, decides.
    with tempfile.TemporaryDirectory() as directory:
        totals = report_for(
            directory,
            [
                {"at_ms": 10, "offset": 0, "size": 8 << 20, "body": 1 << 20, "state": "canceled", "progress": 1 << 20},
            ],
            pass_events("read", 512 << 10, 512 << 10, 0, 100),
        )["totals"]
        assert totals["attributed_useful_bytes"] == 512 << 10, totals
        assert totals["unknown_residual_bytes"] == 512 << 10, totals
        assert totals["canceled_unused_tail_range_bytes"] == 7 << 20, totals

    # (5d) A replayed body inflates body_bytes but not the prefix: the replay is
    # traffic, never unused window bytes.
    with tempfile.TemporaryDirectory() as directory:
        totals = report_for(
            directory,
            [
                {"at_ms": 10, "offset": 0, "size": 4 << 20, "body": 2 << 20, "state": "canceled", "progress": 1 << 20},
            ],
            pass_events("read", 0, 1 << 20, 0, 100),
        )["totals"]
        assert totals["executor_body_bytes"] == 2 << 20, totals
        assert totals["replay_bytes"] == 1 << 20, totals
        assert totals["attributed_useful_bytes"] == 1 << 20, totals
        assert totals["unused_tail_bytes_completed"] == 0, totals
        assert totals["unknown_residual_bytes"] == 0, totals
        assert totals["reconciliation_error_bytes"] == 0, totals

    # (6) Two passes of 1 MiB and 2 MiB, each with a matching window: both are
    # exactly 1x. Dividing by the session total would print 1/3 and 2/3.
    with tempfile.TemporaryDirectory() as directory:
        report = report_for(
            directory,
            [
                {"at_ms": 10, "offset": 0, "size": 1 << 20, "body": 1 << 20, "state": "completed", "progress": 1 << 20},
                {"at_ms": 310, "offset": 8 << 20, "size": 2 << 20, "body": 2 << 20, "state": "completed", "progress": 10 << 20},
            ],
            pass_events("one", 0, 1 << 20, 0, 100) + pass_events("two", 8 << 20, 2 << 20, 300, 400),
        )
        passes = report["passes"]
        assert passes["one"]["delivered_bytes_total"] == 1 << 20, passes
        assert passes["two"]["delivered_bytes_total"] == 2 << 20, passes
        assert abs(passes["one"]["amplification_over_delivered"] - 1.0) < 1e-9, passes
        assert abs(passes["two"]["amplification_over_delivered"] - 1.0) < 1e-9, passes
        assert passes["one"]["cross_pass_delivered_bytes"] == 0, passes
        assert abs(report["totals"]["amplification_over_delivered"] - 1.0) < 1e-9, report["totals"]

    # (7) A runner that delivered 1 MiB while the trace logged no window row at
    # all cannot support a ratio: the report must say so instead of printing 0x.
    with tempfile.TemporaryDirectory() as directory:
        report = report_for(directory, [], pass_events("read", 0, 1 << 20, 0, 100))
        totals = report["totals"]
        assert totals["complete"] is False, totals
        assert totals["completeness_problems"], totals
        assert totals["amplification_known"] is False, totals
        assert totals["amplification_over_delivered"] is None, totals
        assert totals["amplification_over_unique"] is None, totals
        assert totals["reconciliation_error_bytes"] == 0, totals

    # (8) Two sessions read the same 1 MiB and the trace recorded only one of
    # them: interval coverage is still 100%, but session identity is not, so
    # per-session attribution must switch off instead of reporting reuse.
    with tempfile.TemporaryDirectory() as directory:
        report = report_for(
            directory,
            [
                {"at_ms": 10, "offset": 0, "size": 1 << 20, "body": 1 << 20, "state": "completed", "session": 1, "progress": 1 << 20},
                {"at_ms": 20, "offset": 0, "size": 1 << 20, "body": 1 << 20, "state": "completed", "session": 2, "progress": 1 << 20},
            ],
            [
                {"kind": "pass_begin", "label": "read", "offset": 0, "mib": 1, "wall_ns": base},
                {"kind": "read", "label": "read", "index": 0, "offset": 0, "bytes": 1 << 20},
                {"kind": "read", "label": "read", "index": 1, "offset": 0, "bytes": 1 << 20},
                {"kind": "pass_end", "label": "read", "offset": 0, "bytes": 2 << 20, "wall_ns": base + 100_000_000},
            ],
            ranges=[(0, 1 << 20, True)],
        )
        assert report["per_session_attribution"] is False, report["session_delivery_coverage"]
        assert report["per_session_gap"], report
        totals = report["totals"]
        assert totals["attributed_useful_bytes"] == 2 << 20, totals
        assert totals["attributed_shared_bytes"] == 0, totals

    # (9) Covered intervals are not completeness: a driver call whose window row
    # never appeared is invisible to coverage when another window covers the
    # delivered bytes, but the unenclosed call still proves the gap.
    with tempfile.TemporaryDirectory() as directory:
        report = report_for(
            directory,
            [{"at_ms": 10, "offset": 0, "size": 1 << 20, "body": 1 << 20, "state": "completed", "progress": 1 << 20}],
            pass_events("read", 0, 1 << 20, 0, 100),
            ranges=[(0, 1 << 20, True), (2 << 20, 1 << 20, True)],
        )
        totals = report["totals"]
        assert report["window_attribution_coverage"] == 1.0, report["window_attribution_coverage"]
        assert report["unwindowed_source_ranges"] == 1, report
        assert totals["complete"] is False, totals
        assert any("window row" in problem for problem in totals["completeness_problems"]), totals

    # (10) A driver call that never closed means the log ends mid-flight.
    with tempfile.TemporaryDirectory() as directory:
        report = report_for(
            directory,
            [{"at_ms": 10, "offset": 0, "size": 1 << 20, "body": 1 << 20, "state": "completed", "progress": 1 << 20}],
            pass_events("read", 0, 1 << 20, 0, 100),
            ranges=[(0, 1 << 20, False)],
        )
        totals = report["totals"]
        assert report["unpaired_source_ranges"] == 1, report
        assert totals["complete"] is False, totals
        assert any("begin/end" in problem for problem in totals["completeness_problems"]), totals

    # (11) A window row without a traffic count is unknown traffic, not delivery
    # traffic: the row's own byte field would report a comfortable 1.0x while
    # retries and unpublished bytes are invisible.
    with tempfile.TemporaryDirectory() as directory:
        report = report_for(
            directory,
            [
                {
                    "at_ms": 10,
                    "offset": 0,
                    "size": 1 << 20,
                    "body": 1 << 20,
                    "omit_body": True,
                    "state": "completed",
                    "progress": 1 << 20,
                }
            ],
            pass_events("read", 0, 1 << 20, 0, 100),
        )
        totals = report["totals"]
        assert totals["body_unknown_windows"] == 1, totals
        assert totals["complete"] is False, totals
        assert totals["amplification_known"] is False, totals
        assert totals["amplification_over_unique"] is None, totals
        assert any("no executor body count" in problem for problem in totals["completeness_problems"]), totals

    # The command line must render that report instead of raising on a missing
    # ratio.
    assert ratio(None) == "unknown"
    assert count(None) == "unknown"
    assert ratio(2.0) == "2.000x" and count(7) == "7"


def count(value):
    """Render a byte count, or ``unknown`` when the evidence is missing."""
    return "unknown" if value is None else str(value)


def ratio(value):
    """Render an amplification ratio, or ``unknown`` instead of raising."""
    return "unknown" if value is None else f"{value:.3f}x"


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--trace", type=Path, help="isolated mount debug log")
    parser.add_argument("--runner", type=Path, help="measure-playback-session.py JSON output")
    parser.add_argument("--output", type=Path, help="report path; keep it outside Git")
    parser.add_argument("--self-check", action="store_true", help="validate on a synthetic sample")
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return
    if not args.trace or not args.runner:
        parser.error("--trace and --runner are required unless --self-check is used")
    if not args.output:
        parser.error("--output is required")
    if args.output.exists():
        parser.error("output already exists; preserve previous reports")
    report = reconcile(parse_trace(args.trace), parse_runner(args.runner))
    args.output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    totals = report["totals"]
    print(
        f"body={totals['executor_body_bytes']} delivered={count(totals['delivered_bytes_total'])} "
        f"unique={count(totals['delivered_bytes_unique'])} "
        f"useful={totals['attributed_useful_bytes']} "
        f"unused={totals['unused_tail_bytes_completed']} "
        f"unknown={totals['unknown_residual_bytes']} "
        f"amplification={ratio(totals.get('amplification_over_unique'))}"
    )
    if not totals["complete"]:
        # An incomplete report is not a passing measurement; say why instead of
        # printing a ratio that the report deliberately withheld.
        for problem in totals["completeness_problems"]:
            print(f"incomplete: {problem}")


if __name__ == "__main__":
    main()
