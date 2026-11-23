"""Sample every queue's depth into queues.csv: ts,queue,visible,in_flight.

    python tools/watch_queues.py --out results/local-smoke/queues.csv \
        --queue jobs=http://localhost:4566/000000000000/jobs --queue jobs-small=... [--interval 5]

visible is ApproximateNumberOfMessages (waiting), in_flight is
ApproximateNumberOfMessagesNotVisible (received and not yet deleted or expired). Pass every
queue and DLQ. Runs until stopped; --once takes a single sample.
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from sampling import append_rows, now_iso, run_every

COLUMNS = ["ts", "queue", "visible", "in_flight"]


def parse_queues(specs: list[str]) -> dict[str, str]:
    out = {}
    for spec in specs:
        name, sep, url = spec.partition("=")
        if not sep or not name or not url:
            raise ValueError(f"--queue {spec!r}: want name=url")
        out[name] = url
    return out


def sample(sqs, queues: dict[str, str], ts: str) -> list[dict]:
    rows = []
    for name, url in queues.items():
        a = sqs.get_queue_attributes(
            QueueUrl=url, AttributeNames=["ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible"],
        )["Attributes"]
        rows.append({"ts": ts, "queue": name, "visible": int(a["ApproximateNumberOfMessages"]),
                     "in_flight": int(a["ApproximateNumberOfMessagesNotVisible"])})
    return rows


def main(argv: list[str] | None = None) -> int:
    import boto3

    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--queue", action="append", required=True, help="name=url; repeat for every queue and DLQ")
    ap.add_argument("--interval", type=float, default=5.0)
    ap.add_argument("--once", action="store_true")
    args = ap.parse_args(argv)
    queues = parse_queues(args.queue)
    sqs = boto3.client("sqs")
    return run_every(args.interval, args.once, lambda: append_rows(args.out, COLUMNS, sample(sqs, queues, now_iso())))


if __name__ == "__main__":
    sys.exit(main())
