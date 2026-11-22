"""Verify a run's outputs (v0).

For each SUCCEEDED job: the output opens in GDAL and has the input dataset's feature count.
A run also fails when a job's status contradicts its attempts (a success that was not the
final attempt, or a SUCCEEDED job whose last attempt did not succeed) or when a SUCCEEDED
Shapefile's object epoch is present and differs from the attempt recorded as SUCCEEDED.
Both of those still look fine in status_counts. A retry whose object epoch matches, and
missing epoch metadata, are not failures.
Writes verify.json and exits non-zero if any check fails, any job is not yet terminal, or no
output was checked.

    python tools/verify.py --run-id local-x --out results/local-x/verify.json

Reads the job records from DynamoDB (run_id index) and the outputs from S3. Endpoints and
credentials come from the environment (AWS_ENDPOINT_URL locally). Needs ogrinfo on PATH:
run it with `docker compose run --rm tools python tools/verify.py ...`.
"""
from __future__ import annotations

import argparse
import csv
import json
import os
import re
import subprocess
import sys
import tempfile
from collections import Counter
from pathlib import Path
from typing import Callable


TERMINAL = {"SUCCEEDED", "FAILED", "DEAD_LETTERED", "REJECTED"}


class OutputError(Exception):
    """The output could not be fetched or opened."""


def verify(jobs: list[dict], catalog: dict[str, dict], count_features: Callable[[dict], int],
           object_epoch: Callable[[dict], str | None] | None = None) -> dict:
    expected_by_key = {d["s3_key"]: int(d["features"]) for d in catalog.values()}
    failures: list[dict] = []
    checked = 0
    for j in jobs:
        # Read the epoch only for a SUCCEEDED shapefile; that is the stale-upload check.
        epoch = None
        if object_epoch is not None and j.get("status") == "SUCCEEDED" and j.get("output_format") == "shp":
            epoch = object_epoch(j)
        failures.extend(record_failures(j, epoch))
        if j["status"] != "SUCCEEDED":
            continue
        checked += 1
        key = j.get("output_key", "")
        if not key.startswith(f"outputs/{j.get('run_id')}/{j['job_id']}/"):
            failures.append({"job_id": j["job_id"], "check": "output_key", "output_key": key})
            continue
        expected = expected_by_key.get(j["input_key"])
        try:
            actual = count_features(j)
        except OutputError as e:
            failures.append({"job_id": j["job_id"], "check": "opens", "error": str(e)})
            continue
        if expected is None:
            failures.append({"job_id": j["job_id"], "check": "known_input", "input_key": j["input_key"]})
        elif actual != expected:
            failures.append({"job_id": j["job_id"], "check": "feature_count", "expected": expected, "actual": actual})
    # A run passes only if it finished (every job terminal) and at least one output was
    # checked; otherwise an unfinished run would look fine in the summary.
    unfinished = [j["job_id"] for j in jobs if j["status"] not in TERMINAL]
    return {
        "version": "v0",
        "passed": not failures and not unfinished and checked > 0,
        "complete": not unfinished,
        "unfinished": unfinished,
        "jobs": len(jobs),
        "checked": checked,
        "status_counts": dict(Counter(j["status"] for j in jobs)),
        "failures": failures,
    }


def attempts_of(job: dict) -> list:
    raw = job.get("attempts") or []
    return raw if isinstance(raw, list) else []


def status_regressed(job: dict) -> bool:
    """True when a success was not final, or the status and the last attempt disagree.

    A later attempt after SUCCEEDED, a SUCCEEDED job whose last attempt did not succeed,
    and an attempt that succeeded while the job status moved off SUCCEEDED are the same
    failure: the status went somewhere it should not have.
    """
    attempts = attempts_of(job)
    if not attempts:
        return False
    success_at = [i for i, a in enumerate(attempts) if isinstance(a, dict) and a.get("outcome") == "SUCCEEDED"]
    if any(i != len(attempts) - 1 for i in success_at):
        return True
    last = attempts[-1]
    last_outcome = last.get("outcome") if isinstance(last, dict) else None
    if job.get("status") == "SUCCEEDED" and last_outcome != "SUCCEEDED":
        return True
    return bool(success_at) and job.get("status") != "SUCCEEDED"


def mixed_attempt_shapefile(job: dict, object_epoch: str | None) -> bool:
    """True when a SUCCEEDED shapefile's object was uploaded by a different attempt.

    The zip is one object built by one attempt, so a retry that finishes is legitimate,
    including in a later fenced run. object_epoch is the user-metadata epoch on that
    object. Missing epoch metadata is not a failure. A GeoPackage is not this check.
    """
    if job.get("status") != "SUCCEEDED" or job.get("output_format") != "shp":
        return False
    if object_epoch is None or str(object_epoch).strip() == "":
        return False
    succeeded = None
    for a in attempts_of(job):
        if isinstance(a, dict) and a.get("outcome") == "SUCCEEDED":
            succeeded = a.get("epoch")
    if succeeded is None:
        return False
    try:
        return int(object_epoch) != int(succeeded)
    except (TypeError, ValueError):
        return str(object_epoch) != str(succeeded)


def record_failures(job: dict, object_epoch: str | None = None) -> list[dict]:
    out = []
    if status_regressed(job):
        out.append({"job_id": job["job_id"], "check": "status_regression", "status": job.get("status")})
    if mixed_attempt_shapefile(job, object_epoch):
        out.append({"job_id": job["job_id"], "check": "mixed_attempt", "output_format": "shp"})
    return out


def load_catalog(path: Path) -> dict[str, dict]:
    with path.open(newline="") as f:
        return {row["dataset_id"]: row for row in csv.DictReader(f)}


_COUNT = re.compile(r"Feature Count: (\d+)")


def ogr_feature_count(path: str) -> int:
    proc = subprocess.run(["ogrinfo", "-ro", "-so", "-al", path], capture_output=True, text=True)
    if proc.returncode != 0:
        raise OutputError(proc.stderr.strip()[-500:] or f"ogrinfo exited {proc.returncode}")
    counts = _COUNT.findall(proc.stdout)
    if not counts:
        raise OutputError(f"ogrinfo found no layers in {path}")
    return sum(int(c) for c in counts)


def s3_counter(s3, bucket: str) -> Callable[[dict], int]:
    """Download a job's single output object and count it. The file keeps its name, because GDAL
    picks the driver from the extension (.gpkg, .shp.zip, .gdb.zip)."""

    def count(job: dict) -> int:
        key = job.get("output_key", "")
        with tempfile.TemporaryDirectory() as tmp:
            local = str(Path(tmp, Path(key).name))
            try:
                s3.download_file(bucket, key, local)
            except Exception as e:  # botocore errors: report as an unopenable output, keep checking others
                raise OutputError(f"fetch s3://{bucket}/{key}: {e}") from e
            return ogr_feature_count(local)

    return count


def s3_object_epoch(s3, bucket: str) -> Callable[[dict], str | None]:
    """The attempt epoch in the output object's user metadata, or None when it is absent.

    A failed HEAD is also None. Missing epoch metadata is not a mixed-attempt failure;
    an object that cannot be fetched still fails the feature-count check.
    """

    def epoch_of(job: dict) -> str | None:
        key = job.get("output_key") or ""
        if not key:
            return None
        try:
            head = s3.head_object(Bucket=bucket, Key=key)
        except Exception:
            return None
        meta = head.get("Metadata") or {}
        value = next((v for k, v in meta.items() if str(k).lower() == "epoch"), None)
        if value is None or str(value).strip() == "":
            return None
        return str(value)

    return epoch_of


def load_jobs(dynamodb, table: str, run_id: str) -> list[dict]:
    from boto3.dynamodb.types import TypeDeserializer

    des = TypeDeserializer()
    items, kwargs = [], {"TableName": table, "IndexName": "run_id-index",
                         "KeyConditionExpression": "run_id = :r",
                         "ExpressionAttributeValues": {":r": {"S": run_id}}}
    while True:
        page = dynamodb.query(**kwargs)
        items += [{k: des.deserialize(v) for k, v in it.items()} for it in page["Items"]]
        if "LastEvaluatedKey" not in page:
            return items
        kwargs["ExclusiveStartKey"] = page["LastEvaluatedKey"]


def main(argv: list[str] | None = None) -> int:
    import boto3

    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--table", default=os.environ.get("JOBS_TABLE", "jobs"))
    ap.add_argument("--bucket", default=os.environ.get("BUCKET", "gc-local"))
    ap.add_argument("--catalog", type=Path, default=Path("datasets/local/datasets.csv"))
    args = ap.parse_args(argv)

    s3 = boto3.client("s3", config=boto3.session.Config(s3={"addressing_style": "path"}))
    jobs = load_jobs(boto3.client("dynamodb"), args.table, args.run_id)
    if not jobs:
        print(f"no jobs for run_id {args.run_id}", file=sys.stderr)
        return 2
    report = verify(jobs, load_catalog(args.catalog), s3_counter(s3, args.bucket),
                    s3_object_epoch(s3, args.bucket))
    return finish(args.out, args.run_id, report)


def finish(out: Path, run_id: str, report: dict) -> int:
    """Write verify.json and return the process status: 0 when the run passed, 1 when it did not."""
    report["run_id"] = run_id
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2, default=str) + "\n")
    print(json.dumps({k: report[k] for k in ("passed", "jobs", "checked", "status_counts")}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
