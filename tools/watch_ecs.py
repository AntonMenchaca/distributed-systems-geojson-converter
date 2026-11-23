"""Record every task of the run's services, with start and stop times, in tasks.csv.

    # AWS: ECS tasks of the named services
    python tools/watch_ecs.py --out results/p1/p1-smoke-anton-s11/tasks.csv \
        --cluster gc-anton-cluster --service gc-anton-api --service gc-anton-worker [--interval 10]
    # local: the Compose containers of the named services
    python tools/watch_ecs.py --out results/local-smoke/tasks.csv --docker --service api --service worker

tasks.csv has one row per task ever seen: task_id,service,last_status,started_at,stopped_at,
stop_code,stopped_reason. task_id is what workers write in attempts[].task_id (the task ARN on
ECS, the 12-character container ID locally), so attempts join to the task that ran them. The
file is rewritten after every sample, keeping tasks that have since dropped out of the listing.
Runs until stopped; --once takes a single sample.
"""
from __future__ import annotations

import argparse
import csv
import json
import subprocess
import sys
from pathlib import Path

from sampling import iso_ms, parse_iso, run_every, write_rows

COLUMNS = ["task_id", "service", "last_status", "started_at", "stopped_at", "stop_code", "stopped_reason"]


def ecs_task_row(task: dict) -> dict:
    stamp = lambda k: iso_ms(task[k]) if task.get(k) else ""  # noqa: E731
    return {
        "task_id": task["taskArn"],
        "service": task.get("group", "").removeprefix("service:"),
        "last_status": task.get("lastStatus", ""),
        "started_at": stamp("startedAt"),
        "stopped_at": stamp("stoppedAt"),
        "stop_code": task.get("stopCode", ""),
        "stopped_reason": task.get("stoppedReason", ""),
    }


def docker_task_row(inspect: dict) -> dict:
    state = inspect["State"]
    # Docker reports a container that has not stopped with the zero time.
    stopped = state.get("FinishedAt", "") if not state.get("FinishedAt", "").startswith("0001-") else ""
    started = state.get("StartedAt", "") if not state.get("StartedAt", "").startswith("0001-") else ""
    return {
        "task_id": inspect["Id"][:12],
        "service": inspect["Config"]["Labels"].get("com.docker.compose.service", ""),
        "last_status": state.get("Status", ""),
        "started_at": iso_ms(parse_iso(started)) if started else "",
        "stopped_at": iso_ms(parse_iso(stopped)) if stopped else "",
        "stop_code": f"exit {state.get('ExitCode')}" if stopped else "",
        "stopped_reason": "",
    }


def record(seen: dict[str, dict], rows: list[dict], out: Path) -> None:
    for r in rows:
        seen[r["task_id"]] = r
    write_rows(out, COLUMNS, seen.values())


def ecs_rows(ecs, cluster: str, services: list[str]) -> list[dict]:
    arns: list[str] = []
    for service in services:
        for status in ("RUNNING", "STOPPED"):
            for page in ecs.get_paginator("list_tasks").paginate(cluster=cluster, serviceName=service,
                                                                 desiredStatus=status):
                arns += page["taskArns"]
    rows = []
    for i in range(0, len(arns), 100):  # describe_tasks takes at most 100 tasks
        rows += [ecs_task_row(t) for t in ecs.describe_tasks(cluster=cluster, tasks=arns[i:i + 100])["tasks"]]
    return rows


def compose_containers(services: list[str]) -> list[dict]:
    """docker inspect output for every container (running or stopped) of the Compose services."""
    ids = subprocess.run(["docker", "compose", "ps", "-a", "-q", *services],
                         capture_output=True, text=True, check=True).stdout.split()
    if not ids:
        return []
    return json.loads(subprocess.run(["docker", "inspect", *ids], capture_output=True, text=True, check=True).stdout)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", type=Path, required=True)
    ap.add_argument("--service", action="append", required=True)
    target = ap.add_mutually_exclusive_group(required=True)
    target.add_argument("--cluster", help="ECS cluster name")
    target.add_argument("--docker", action="store_true", help="the local Compose stack")
    ap.add_argument("--interval", type=float, default=10.0)
    ap.add_argument("--once", action="store_true")
    args = ap.parse_args(argv)

    # Start from the file so far: the final --once pass after a run must not drop tasks the
    # long-running sampler saw that have since left the listing.
    seen: dict[str, dict] = {}
    if args.out.exists():
        with args.out.open(newline="") as f:
            seen = {r["task_id"]: r for r in csv.DictReader(f)}
    if args.docker:
        sample = lambda: [docker_task_row(c) for c in compose_containers(args.service)]  # noqa: E731
    else:
        import boto3
        ecs = boto3.client("ecs")
        sample = lambda: ecs_rows(ecs, args.cluster, args.service)  # noqa: E731
    return run_every(args.interval, args.once, lambda: record(seen, sample(), args.out))


if __name__ == "__main__":
    sys.exit(main())
