"""Shared plumbing for the samplers (watch_queues.py, watch_ecs.py, resources.py).

A sampler polls on a fixed interval until it is stopped. run.py starts each one as a child
process and terminates it when the run ends; on Windows that kills it without a signal handler,
so every sampler writes each sample to disk as soon as it has it, never only on exit.
"""
from __future__ import annotations

import csv
import os
import re
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Iterable


def iso_ms(dt: datetime) -> str:
    """UTC, millisecond precision: the format of the job record's timestamps."""
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.") + f"{dt.microsecond // 1000:03d}Z"


def now_iso() -> str:
    return iso_ms(datetime.now(timezone.utc))


_FRACTION = re.compile(r"\.(\d+)")


def parse_iso(value: str) -> datetime:
    """Parse an RFC 3339 timestamp, including Docker's nanosecond fractions."""
    value = _FRACTION.sub(lambda m: "." + m.group(1)[:6].ljust(6, "0"), value.replace("Z", "+00:00"), count=1)
    return datetime.fromisoformat(value)


def append_rows(path: Path, columns: list[str], rows: Iterable[dict]) -> None:
    new = not path.exists() or path.stat().st_size == 0
    with path.open("a", newline="") as f:
        w = csv.DictWriter(f, fieldnames=columns, lineterminator="\n")
        if new:
            w.writeheader()
        w.writerows(rows)


def write_rows(path: Path, columns: list[str], rows: Iterable[dict]) -> None:
    """Replace path in one step, so a reader (or a kill mid-write) never sees half a file."""
    tmp = path.with_name(path.name + ".tmp")
    with tmp.open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=columns, lineterminator="\n")
        w.writeheader()
        w.writerows(rows)
    os.replace(tmp, path)


def run_every(interval_s: float, once: bool, tick: Callable[[], None]) -> int:
    """Call tick every interval_s seconds (or once). A failed tick is reported and retried on
    the next interval: one throttled API call must not end the sampling for the whole run."""
    while True:
        started = time.monotonic()
        try:
            tick()
        except Exception as e:  # report and keep sampling; the gap shows in the CSV's timestamps
            print(f"{Path(sys.argv[0]).name}: sample failed: {e}", file=sys.stderr, flush=True)
            if once:
                return 1
        if once:
            return 0
        time.sleep(max(0.0, interval_s - (time.monotonic() - started)))
