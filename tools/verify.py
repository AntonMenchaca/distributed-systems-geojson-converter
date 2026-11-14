"""Verify a run's outputs (v0).

For each SUCCEEDED job: the output opens in GDAL and has the input dataset's feature count.
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


def verify(jobs: list[dict], catalog: dict[str, dict], count_features: Callable[[dict], int]) -> dict:
    expected_by_key = {d["s3_key"]: int(d["features"]) for d in catalog.values()}
    failures: list[dict] = []
    checked = 0
    for j in jobs:
        if j["status"] != "SUCCEEDED":
            continue
        checked += 1
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
    """Download a job's output (one object, or every object under a prefix ending in /) and count it."""

    def count(job: dict) -> int:
        key = job.get("output_key", "")
        with tempfile.TemporaryDirectory() as tmp:
            try:
                if key.endswith("/"):
                    pages = s3.get_paginator("list_objects_v2").paginate(Bucket=bucket, Prefix=key)
                    objs = [o["Key"] for p in pages for o in p.get("Contents", [])]
                    if not objs:
                        raise OutputError(f"no objects under s3://{bucket}/{key}")
                    for k in objs:
                        dest = Path(tmp, "out", k[len(key):])
                        dest.parent.mkdir(parents=True, exist_ok=True)
                        s3.download_file(bucket, k, str(dest))
                    local = str(Path(tmp, "out"))
                    if job["output_format"] == "fgdb":
                        # OpenFileGDB opens the .gdb directory by name.
                        os.rename(local, local + ".gdb")
                        local += ".gdb"
                else:
                    local = str(Path(tmp, Path(key).name))
                    s3.download_file(bucket, key, local)
            except OutputError:
                raise
            except Exception as e:  # botocore errors: report as an unopenable output, keep checking others
                raise OutputError(f"fetch s3://{bucket}/{key}: {e}") from e
            return ogr_feature_count(local)

    return count


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
    report = verify(jobs, load_catalog(args.catalog), s3_counter(s3, args.bucket))
    report["run_id"] = args.run_id
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(report, indent=2, default=str) + "\n")
    print(json.dumps({k: report[k] for k in ("passed", "jobs", "checked", "status_counts")}))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
