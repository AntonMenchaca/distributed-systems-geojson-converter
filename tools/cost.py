"""A run's row in results/costs.csv, and a check for stacks left running.

    python tools/cost.py --run-dir results/p1/p1-base-anton-s11              # append the run's row
    python tools/cost.py --run-dir results/p1/p1-base-anton-s11 --dry-run    # print it only
    python tools/cost.py --open-stacks --prefix gc-anton                     # exit 1 if anything is up

run.py calls both at the end of every AWS run, before you destroy the stack.

What a row covers: from where the previous row for the same account prefix ended (or, for the
first run of a session, when the stack's first task started) to the run's end event. So the first
run of a session also pays for setup and staging, later runs in the same session don't count the
same hours twice. Time between a session's last run and `terraform destroy` is not counted, so
destroy soon after the last run.

    usd = vCPU-hours x FARGATE_VCPU_HR + GB-hours x FARGATE_GB_HR + window hours x ALB_HR

Task hours come from the run's tasks.csv; each task's size comes from its task definition (the
ARNs in config.json), which AWS can still describe after the stack is destroyed. S3, SQS,
DynamoDB, ECR and CloudWatch cost under a cent per run and are left out. Rows added by hand for
work outside the stack (h1-measure-ikwo ran on EC2) are kept as they are.
"""
from __future__ import annotations

import argparse
import csv
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

from run import ALB_HR, FARGATE_GB_HR, FARGATE_VCPU_HR, REPO
from sampling import parse_iso

COSTS = REPO / "results/costs.csv"
COLUMNS = ["run_id", "owner", "account_prefix", "started_at", "ended_at", "task_hours",
           "vcpu_hours", "gb_hours", "usd"]


def usd(vcpu_h: float, gb_h: float, alb_h: float) -> float:
    return vcpu_h * FARGATE_VCPU_HR + gb_h * FARGATE_GB_HR + alb_h * ALB_HR


def iso_s(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def read_csv(path: Path) -> list[dict]:
    if not path.exists():
        return []
    with path.open(newline="") as f:
        return list(csv.DictReader(f))


load_costs = read_csv


def sizes_from(task_definitions: list[dict]) -> dict[str, tuple[float, float]]:
    """family -> (vCPU, GB), from describe_task_definition results."""
    return {td["family"]: (int(td["cpu"]) / 1024, int(td["memory"]) / 1024) for td in task_definitions}


def task_hours(tasks: list[dict], sizes: dict[str, tuple[float, float]],
               start: datetime, end: datetime) -> dict[str, float]:
    """Task-, vCPU- and GB-hours of every task, clipped to [start, end].

    A task still running has no stopped_at and counts to end. The ECS module names each
    service's task definition family "<service>-task", so that is how a task finds its size.
    """
    totals = {"task_h": 0.0, "vcpu_h": 0.0, "gb_h": 0.0}
    for t in tasks:
        began = max(parse_iso(t["started_at"]), start) if t.get("started_at") else start
        stopped = min(parse_iso(t["stopped_at"]), end) if t.get("stopped_at") else end
        hours = max(0.0, (stopped - began).total_seconds() / 3600)
        if hours == 0:
            continue
        vcpu, gb = sizes[t["service"] + "-task"]
        totals["task_h"] += hours
        totals["vcpu_h"] += hours * vcpu
        totals["gb_h"] += hours * gb
    return totals


def window_start(costs: list[dict], prefix: str, tasks: list[dict], run_id: str, end: datetime) -> datetime:
    """The later of the stack's first task and the end of the previous row for prefix.

    Only rows that ended by this run's end count, and never the run's own row, so re-costing a
    run (or one that later runs follow) gives the same window.
    """
    first_task = min(parse_iso(t["started_at"]) for t in tasks if t.get("started_at"))
    ends = [e for r in costs
            if r.get("account_prefix") == prefix and r.get("run_id") != run_id and r.get("ended_at")
            and (e := parse_iso(r["ended_at"])) <= end]
    return max([first_task, *ends])


def run_end(events: Path) -> datetime:
    ends = [parse_iso(r["timestamp"]) for r in read_csv(events) if r["event"] == "end"]
    if not ends:
        raise ValueError(f"{events} has no end event: the run did not finish")
    return max(ends)


def is_local(run_dir: Path) -> bool:
    config = run_dir / "config.json"
    return config.exists() and json.loads(config.read_text()).get("phase") == "local"


def cost_row(run_dir: Path, costs: list[dict], sizes: dict[str, tuple[float, float]]) -> dict:
    config = json.loads((run_dir / "config.json").read_text())
    tasks = read_csv(run_dir / "tasks.csv")
    end = run_end(run_dir / "events.csv")
    start = window_start(costs, config["account_prefix"], tasks, config["run_id"], end)
    h = task_hours(tasks, sizes, start, end)
    window_h = max(0.0, (end - start).total_seconds() / 3600)
    return {
        "run_id": config["run_id"], "owner": config["owner"], "account_prefix": config["account_prefix"],
        "started_at": iso_s(start), "ended_at": iso_s(end),
        "task_hours": f"{h['task_h']:.2f}", "vcpu_hours": f"{h['vcpu_h']:.2f}", "gb_hours": f"{h['gb_h']:.2f}",
        "usd": f"{usd(h['vcpu_h'], h['gb_h'], window_h):.2f}",
    }


def append_row(costs_path: Path, row: dict) -> bool:
    """Append row unless its run is already there; True when it was appended."""
    if any(r["run_id"] == row["run_id"] for r in read_csv(costs_path)):
        return False
    new = not costs_path.exists() or costs_path.stat().st_size == 0
    with costs_path.open("a", newline="") as f:
        w = csv.DictWriter(f, fieldnames=COLUMNS, lineterminator="\n")
        if new:
            w.writeheader()
        w.writerow(row)
    return True


def open_stacks(ecs, elbv2, prefix: str) -> list[str]:
    """ECS services with tasks running or wanted, and load balancers, under prefix."""
    found = []
    for cluster in ecs.list_clusters()["clusterArns"]:
        if not cluster.rsplit("/", 1)[-1].startswith(prefix):
            continue
        arns = ecs.list_services(cluster=cluster)["serviceArns"]
        for i in range(0, len(arns), 10):  # describe_services takes at most 10
            for s in ecs.describe_services(cluster=cluster, services=arns[i:i + 10])["services"]:
                if s["desiredCount"] or s["runningCount"]:
                    found.append(f"ECS service {s['serviceName']} "
                                 f"({s['runningCount']} running, {s['desiredCount']} desired)")
    for lb in elbv2.describe_load_balancers()["LoadBalancers"]:
        if lb["LoadBalancerName"].startswith(prefix):
            found.append(f"load balancer {lb['LoadBalancerName']}")
    return found


def describe_sizes(ecs, task_defs: dict[str, str]) -> dict[str, tuple[float, float]]:
    return sizes_from([ecs.describe_task_definition(taskDefinition=arn)["taskDefinition"]
                       for arn in task_defs.values()])


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--run-dir", type=Path, help="append this run's row to results/costs.csv")
    mode.add_argument("--open-stacks", action="store_true", help="list services and load balancers still up")
    ap.add_argument("--prefix", help="with --open-stacks: gc-anton, gc-ikwo or gc-samuel")
    ap.add_argument("--costs", type=Path, default=COSTS)
    ap.add_argument("--dry-run", action="store_true", help="with --run-dir: print the row, don't append it")
    args = ap.parse_args(argv)

    import boto3

    if args.open_stacks:
        if not args.prefix:
            ap.error("--open-stacks needs --prefix")
        found = open_stacks(boto3.client("ecs"), boto3.client("elbv2"), args.prefix)
        for item in found:
            print(f"cost: still up: {item}")
        if not found:
            print(f"cost: nothing running under {args.prefix}")
        return 1 if found else 0

    if is_local(args.run_dir):
        print("cost: local run, no cost")
        return 0
    config = json.loads((args.run_dir / "config.json").read_text())
    row = cost_row(args.run_dir, load_costs(args.costs), describe_sizes(boto3.client("ecs"), config["task_defs"]))
    print("cost: " + ",".join(row[c] for c in COLUMNS))
    if args.dry_run:
        return 0
    if not append_row(args.costs, row):
        print(f"cost: {row['run_id']} is already in {args.costs}; not added again")
    return 0


if __name__ == "__main__":
    sys.exit(main())
