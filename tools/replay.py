"""Replay a trace against the API and wait for every job (v0).

    python tools/replay.py --trace traces/sample.csv --run-id local-sample-1 --api http://localhost:8080

Submits each row at its t_offset_s, then polls GET /jobs/{id} until every job is terminal.
Exits 0 only if every job SUCCEEDED. Stdlib only, so it runs from a laptop without the venv.
"""
from __future__ import annotations

import argparse
import csv
import json
import sys
import time
import urllib.request
from collections import Counter
from pathlib import Path
from typing import Callable

TERMINAL = {"SUCCEEDED", "FAILED", "DEAD_LETTERED", "REJECTED"}


def submit(rows: list[dict], run_id: str, post: Callable[[dict], dict],
           now: Callable[[], float], sleep: Callable[[float], None]) -> list[str]:
    start, ids = now(), []
    for row in rows:
        wait_s = start + float(row["t_offset_s"]) - now()
        if wait_s > 0:
            sleep(wait_s)
        body = {"input": {"dataset_id": row["dataset_id"]}, "output_format": row["output_format"], "run_id": run_id}
        if row.get("chaos"):
            body["chaos"] = json.loads(row["chaos"])
        ids.append(post(body)["job_id"])
    return ids


def wait(job_ids: list[str], get_status: Callable[[str], str], now: Callable[[], float],
         sleep: Callable[[float], None], timeout_s: float, poll_s: float = 2.0) -> dict[str, str]:
    deadline, statuses = now() + timeout_s, {}
    pending = list(job_ids)
    while pending and now() < deadline:
        for jid in list(pending):
            statuses[jid] = get_status(jid)
            if statuses[jid] in TERMINAL:
                pending.remove(jid)
        if pending:
            sleep(poll_s)
    return statuses


def http_json(method: str, url: str, body: dict | None = None) -> dict:
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read())


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--trace", type=Path, required=True)
    ap.add_argument("--run-id", required=True)
    ap.add_argument("--api", default="http://localhost:8080")
    ap.add_argument("--timeout-s", type=float, default=900)
    args = ap.parse_args(argv)

    with args.trace.open(newline="") as f:
        rows = list(csv.DictReader(f))
    api = args.api.rstrip("/")
    ids = submit(rows, args.run_id, lambda b: http_json("POST", f"{api}/jobs", b), time.monotonic, time.sleep)
    print(f"submitted {len(ids)} jobs for run_id {args.run_id}", file=sys.stderr)
    statuses = wait(ids, lambda j: http_json("GET", f"{api}/jobs/{j}")["status"], time.monotonic, time.sleep, args.timeout_s)
    counts = Counter(statuses.values())
    print(json.dumps({"run_id": args.run_id, "jobs": len(ids), "status_counts": dict(counts)}))
    return 0 if counts.get("SUCCEEDED", 0) == len(ids) else 1


if __name__ == "__main__":
    sys.exit(main())
