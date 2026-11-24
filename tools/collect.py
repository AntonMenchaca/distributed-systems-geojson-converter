"""Export one run's job records to jobs.csv, with the latency metrics every analysis reads.

    python tools/collect.py --run-id local-smoke --out results/local-smoke/jobs.csv \
        --attempts-out results/local-smoke/attempts.csv \
        --profile-out results/local-smoke/profile.csv

Reads the jobs table's run_id index, so only that run's jobs are exported. Endpoints and
credentials come from the environment (AWS_ENDPOINT_URL_DYNAMODB locally, AWS_ENDPOINT_URL
for the profile objects in S3).

profile.csv is written only when the worker uploaded rows for this run (PROFILE=1). Each
object is profiles/<run_id>/<job_id>.csv, one header and one row. This joins them into one
file with a single header. A run with no such objects leaves profile.csv unwritten.

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
import contextlib
import csv
import io
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
# One attempts.csv row per entry in a job's attempts list (schemas/job-record.json).
ATTEMPT_FIELDS = ["epoch", "task_id", "started_at", "ended_at", "outcome", "error_code",
                  "peak_rss_mb", "cpu_s", "peak_disk_mb"]
ATTEMPT_COLUMNS = ["run_id", "job_id"] + ATTEMPT_FIELDS
# Same order as worker/internal/measure.ProfileHeader. One S3 object is one row;
# the joined file has this header once.
PROFILE_COLUMNS = [
    "run_id", "job_id", "dataset_id", "output_format", "gdal_profile",
    "input_bytes", "output_bytes", "max_shp_component_bytes", "output_features",
    "wall_ms", "package_ms", "cpu_s", "peak_rss_mb", "peak_disk_mb",
    "ogrinfo_ms", "error",
]


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


def attempt_rows(job: dict) -> list[dict]:
    attempts = [a for a in (job.get("attempts") or []) if isinstance(a, dict)]
    return [{"run_id": cell(job.get("run_id")), "job_id": cell(job["job_id"]),
             **{f: cell(a.get(f)) for f in ATTEMPT_FIELDS}} for a in attempts]


def export(dynamodb, table: str, run_id: str, out: TextIO, attempts_out: TextIO | None = None) -> int:
    """Write the run's jobs as CSV to out, ordered by submission, and, when attempts_out is
    given, every attempt of those jobs to it; returns how many jobs."""
    jobs = sorted(load_jobs(dynamodb, table, run_id), key=lambda j: (str(j.get("submitted_at", "")), j["job_id"]))
    w = csv.DictWriter(out, fieldnames=COLUMNS, lineterminator="\n")
    w.writeheader()
    for j in jobs:
        w.writerow(row(j))
    if attempts_out is not None:
        aw = csv.DictWriter(attempts_out, fieldnames=ATTEMPT_COLUMNS, lineterminator="\n")
        aw.writeheader()
        for j in jobs:
            aw.writerows(attempt_rows(j))
    return len(jobs)


def profile_object_keys(s3, bucket: str, run_id: str) -> list[str]:
    """Keys of this run's per-job profile rows, profiles/<run_id>/<job_id>.csv."""
    prefix = f"profiles/{run_id}/"
    keys = []
    for page in s3.get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix=prefix):
        for obj in page.get("Contents", []):
            key = obj["Key"]
            if key.endswith(".csv"):
                keys.append(key)
    return keys


def profile_rows_from_bodies(bodies: list[str]) -> list[dict]:
    """One row per object body. Each body is a header plus one row, in PROFILE_COLUMNS order.
    Rows are ordered by job_id. A body with any other header is an error."""
    rows = []
    for body in bodies:
        reader = csv.DictReader(io.StringIO(body))
        if list(reader.fieldnames or []) != PROFILE_COLUMNS:
            raise ValueError(f"profile object header {reader.fieldnames!r} does not match {PROFILE_COLUMNS}")
        for raw in reader:
            rows.append({c: raw.get(c) or "" for c in PROFILE_COLUMNS})
    rows.sort(key=lambda r: r["job_id"])
    return rows


def write_profiles(s3, bucket: str, run_id: str, dest: Path) -> int:
    """Join the run's profile objects into dest. Returns how many rows were written.
    No objects leaves dest uncreated."""
    keys = profile_object_keys(s3, bucket, run_id)
    if not keys:
        return 0
    bodies = []
    for key in sorted(keys):
        obj = s3.get_object(Bucket=bucket, Key=key)
        bodies.append(obj["Body"].read().decode())
    rows = profile_rows_from_bodies(bodies)
    if not rows:
        return 0
    dest.parent.mkdir(parents=True, exist_ok=True)
    with dest.open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=PROFILE_COLUMNS, lineterminator="\n")
        w.writeheader()
        w.writerows(rows)
    return len(rows)


def main(argv: list[str] | None = None) -> int:
    import boto3

    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--attempts-out", type=Path, help="also write one row per attempt here (attempts.csv)")
    ap.add_argument("--profile-out", type=Path, help="join profiles/<run_id>/<job_id>.csv into this file when any exist")
    ap.add_argument("--bucket", default=os.environ.get("BUCKET", "gc-local"))
    ap.add_argument("--table", default=os.environ.get("JOBS_TABLE", "jobs"))
    args = ap.parse_args(argv)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    with contextlib.ExitStack() as stack:
        f = stack.enter_context(args.out.open("w", newline=""))
        attempts = stack.enter_context(args.attempts_out.open("w", newline="")) if args.attempts_out else None
        n = export(boto3.client("dynamodb"), args.table, args.run_id, f, attempts)
    if args.profile_out is not None:
        s3 = boto3.client("s3", config=boto3.session.Config(s3={"addressing_style": "path"}))
        try:
            nprof = write_profiles(s3, args.bucket, args.run_id, args.profile_out)
        except ValueError as e:
            print(f"collect: {e}", file=sys.stderr)
            return 1
        if nprof:
            print(f"collect: {nprof} profile rows -> {args.profile_out}")
    if n == 0:
        print(f"no jobs for run_id {args.run_id}", file=sys.stderr)
        return 2
    print(f"collect: {n} jobs -> {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
