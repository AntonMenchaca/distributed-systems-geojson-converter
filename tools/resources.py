"""CPU and memory per task over a run, in resources.csv: ts,task_id,service,cpu_pct,mem_mb.

    # AWS: query Container Insights after the run (the cluster has it enabled, "enhanced")
    python tools/resources.py --out results/p1/p1-smoke-anton-s11/resources.csv \
        --cluster gc-anton-cluster --start 2026-11-21T10:00:00Z --end 2026-11-21T10:30:00Z
    # local: sample `docker stats` while the run goes, until stopped
    python tools/resources.py --out results/local-smoke/resources.csv --docker --service api --service worker

cpu_pct is the share of the task's reserved CPU (100 = all of it; locally, of one core).
task_id matches tasks.csv and attempts[].task_id: the task ARN on ECS, the 12-character
container ID locally.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import time
from pathlib import Path

from sampling import append_rows, iso_ms, now_iso, parse_iso, run_every, write_rows

COLUMNS = ["ts", "task_id", "service", "cpu_pct", "mem_mb"]

_UNITS = {"B": 1 / 1e6, "kB": 1e-3, "KB": 1e-3, "MB": 1.0, "GB": 1e3,
          "KiB": 1 / 1024, "MiB": 1.0, "GiB": 1024.0}
_SIZE = re.compile(r"([\d.]+)\s*([A-Za-z]+)")


def to_mb(text: str) -> float:
    m = _SIZE.fullmatch(text.strip())
    if not m or m.group(2) not in _UNITS:
        raise ValueError(f"unknown size {text!r}")
    return float(m.group(1)) * _UNITS[m.group(2)]


def docker_stats_rows(ts: str, lines: list[str], names: dict[str, tuple[str, str]]) -> list[dict]:
    """Rows for the containers in names (short ID -> (task_id, service)); others are ignored."""
    rows = []
    for line in lines:
        s = json.loads(line)
        if s["ID"][:12] not in names:
            continue
        task_id, service = names[s["ID"][:12]]
        rows.append({"ts": ts, "task_id": task_id, "service": service,
                     "cpu_pct": float(s["CPUPerc"].rstrip("%")),
                     "mem_mb": to_mb(s["MemUsage"].split("/")[0])})
    return rows


def docker_sample(services: list[str]) -> list[dict]:
    ids = subprocess.run(["docker", "compose", "ps", "-q", *services],
                         capture_output=True, text=True, check=True).stdout.split()
    if not ids:
        return []
    inspect = json.loads(subprocess.run(["docker", "inspect", *ids], capture_output=True, text=True, check=True).stdout)
    names = {c["Id"][:12]: (c["Id"][:12], c["Config"]["Labels"].get("com.docker.compose.service", "")) for c in inspect}
    stats = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{json .}}", *ids],
                           capture_output=True, text=True, check=True).stdout.splitlines()
    return docker_stats_rows(now_iso(), [l for l in stats if l.strip()], names)


# Container Insights writes one performance record per task per minute to this log group.
INSIGHTS_QUERY = ("fields @timestamp, TaskId, ServiceName, CpuUtilized, CpuReserved, MemoryUtilized"
                  " | filter Type = \"Task\" | sort @timestamp asc | limit 10000")


def insights_rows(results: list[list[dict]], cluster_arn_prefix: str) -> list[dict]:
    rows = []
    for r in results:
        f = {x["field"]: x["value"] for x in r}
        reserved = float(f.get("CpuReserved") or 0)
        rows.append({
            "ts": iso_ms(parse_iso(f["@timestamp"].replace(" ", "T") + "Z")),
            "task_id": cluster_arn_prefix + f["TaskId"],
            "service": f.get("ServiceName", ""),
            "cpu_pct": round(100 * float(f.get("CpuUtilized") or 0) / reserved, 2) if reserved else "",
            "mem_mb": float(f.get("MemoryUtilized") or 0),
        })
    return rows


def insights_query(cluster: str, start: str, end: str, poll_s: float = 2.0) -> list[dict]:
    import boto3

    logs = boto3.client("logs")
    ident = boto3.client("sts").get_caller_identity()
    region = logs.meta.region_name
    q = logs.start_query(logGroupName=f"/aws/ecs/containerinsights/{cluster}/performance",
                         startTime=int(parse_iso(start).timestamp()), endTime=int(parse_iso(end).timestamp()) + 60,
                         queryString=INSIGHTS_QUERY)
    while True:
        res = logs.get_query_results(queryId=q["queryId"])
        if res["status"] in ("Complete", "Failed", "Cancelled", "Timeout"):
            break
        time.sleep(poll_s)
    if res["status"] != "Complete":
        raise RuntimeError(f"Container Insights query {res['status']}")
    if len(res["results"]) >= 10000:
        print("resources: hit the 10,000-row query limit; the end of the run is missing", file=sys.stderr)
    return insights_rows(res["results"], f"arn:aws:ecs:{region}:{ident['Account']}:task/{cluster}/")


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", type=Path, required=True)
    target = ap.add_mutually_exclusive_group(required=True)
    target.add_argument("--cluster", help="ECS cluster name: query Container Insights for --start..--end")
    target.add_argument("--docker", action="store_true", help="sample the local Compose stack until stopped")
    ap.add_argument("--service", action="append", default=[], help="with --docker: Compose services to sample")
    ap.add_argument("--start")
    ap.add_argument("--end")
    ap.add_argument("--interval", type=float, default=10.0)
    ap.add_argument("--once", action="store_true")
    args = ap.parse_args(argv)

    if args.docker:
        return run_every(args.interval, args.once, lambda: append_rows(args.out, COLUMNS, docker_sample(args.service)))
    if not (args.start and args.end):
        ap.error("--cluster needs --start and --end")
    rows = insights_query(args.cluster, args.start, args.end)
    write_rows(args.out, COLUMNS, rows)
    print(f"resources: {len(rows)} task samples -> {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
