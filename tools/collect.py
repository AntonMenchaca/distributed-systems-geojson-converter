"""Export one run's job records to jobs.csv, with the latency metrics every analysis reads.

    python tools/collect.py --run-id local-smoke --out results/local-smoke/jobs.csv

Reads the jobs table's run_id index, so only that run's jobs are exported. Endpoints and
credentials come from the environment (AWS_ENDPOINT_URL_DYNAMODB locally).

Latency comes only from the job record's server-side timestamps, never from Locust:
    queue_wait_s  first_started_at - enqueued_at   (time before any worker picked the job up)
    processing_s  committed_at - started_at        (the final attempt, from claim to commit)
    turnaround_s  committed_at - submitted_at      (what the client waited end to end)
    slowdown      turnaround_s / processing_s
A metric whose timestamps are missing (the job never started or never committed) is blank.

finished_attempts counts attempts that converted to the end: SUCCEEDED, or STALE when a fenced
commit refused them. More than one means the job was converted twice (a duplicate).
"""
from __future__ import annotations

import argparse
import csv
import os
import sys
from datetime import datetime
from decimal import Decimal
from pathlib import Path
from typing import TextIO

from verify import load_jobs

# Every scalar field of schemas/job-record.json, in schema order; attempts are summarised.
RECORD_FIELDS = [
    "job_id", "run_id", "output_format", "size_class", "queue", "input_key", "input_bytes", "status",
    "epoch", "submitted_at", "enqueued_at", "first_started_at", "started_at", "heartbeat_at",
    "committed_at", "download_ms", "convert_ms", "package_ms", "upload_ms", "output_key",
    "output_bytes", "output_sha256", "feature_count", "error_code",
]
METRICS = ["queue_wait_s", "processing_s", "turnaround_s", "slowdown"]
FINISHED = {"SUCCEEDED", "STALE"}
COLUMNS = RECORD_FIELDS + ["attempt_count", "finished_attempts", "last_outcome"] + METRICS


def parse_ts(value) -> datetime | None:
    if not value:
        return None
    return datetime.fromisoformat(str(value).replace("Z", "+00:00"))


def seconds_between(start, end) -> float | None:
    a, b = parse_ts(start), parse_ts(end)
    if a is None or b is None:
        return None
    return (b - a).total_seconds()


def metrics(job: dict) -> dict:
    processing = seconds_between(job.get("started_at"), job.get("committed_at"))
    turnaround = seconds_between(job.get("submitted_at"), job.get("committed_at"))
    return {
        "queue_wait_s": seconds_between(job.get("enqueued_at"), job.get("first_started_at")),
        "processing_s": processing,
        "turnaround_s": turnaround,
        "slowdown": turnaround / processing if processing and turnaround is not None else None,
    }


def cell(value) -> str:
    if value is None:
        return ""
    if isinstance(value, Decimal):
        return str(int(value)) if value == value.to_integral_value() else str(value)
    if isinstance(value, float):
        return str(round(value, 4))
    return str(value)


def row(job: dict) -> dict:
    attempts = job.get("attempts") or []
    last = attempts[-1] if attempts else {}
    out = {f: job.get(f) for f in RECORD_FIELDS}
    out["attempt_count"] = len(attempts)
    out["finished_attempts"] = sum(1 for a in attempts if isinstance(a, dict) and a.get("outcome") in FINISHED)
    out["last_outcome"] = last.get("outcome") if isinstance(last, dict) else None
    out.update(metrics(job))
    return {k: cell(out[k]) for k in COLUMNS}


def export(dynamodb, table: str, run_id: str, out: TextIO) -> int:
    """Write the run's jobs as CSV to out, ordered by submission; returns how many."""
    jobs = sorted(load_jobs(dynamodb, table, run_id), key=lambda j: (str(j.get("submitted_at", "")), j["job_id"]))
    w = csv.DictWriter(out, fieldnames=COLUMNS, lineterminator="\n")
    w.writeheader()
    for j in jobs:
        w.writerow(row(j))
    return len(jobs)


def main(argv: list[str] | None = None) -> int:
    import boto3

    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--table", default=os.environ.get("JOBS_TABLE", "jobs"))
    args = ap.parse_args(argv)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w", newline="") as f:
        n = export(boto3.client("dynamodb"), args.table, args.run_id, f)
    if n == 0:
        print(f"no jobs for run_id {args.run_id}", file=sys.stderr)
        return 2
    print(f"collect: {n} jobs -> {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
