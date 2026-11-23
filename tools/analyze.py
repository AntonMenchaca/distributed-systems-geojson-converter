"""Summarise runs as master-table rows, and compare a before/after pair.

    python tools/analyze.py results/p1/p1-base-anton-s11 results/p1/p1-base-anton-s22
    python tools/analyze.py --compare results/p1/p1-base-anton-s11 results/p9/p9-best-anton-s11

Prints one Markdown row per run and saves a turnaround CDF per run to results/figures/<run>.png
(run folders are never edited after their run, so figures go outside them). --compare also prints
the change, and exits 2 if the two runs come from different accounts or seeds: a before/after
pair is only comparable on the same account and the same trace.

Every number comes from the run folder (jobs.csv, verify.json, config.json) and the run's row in
results/costs.csv; latency comes only from job-record timestamps (turnaround_s in jobs.csv):
    failed               FAILED or DEAD_LETTERED
    duplicates           jobs converted more than once (finished_attempts > 1)
    small/large p95/p99  turnaround of SUCCEEDED jobs in that size class; p99 only with 100+ jobs
    GB/h                 input GB of SUCCEEDED jobs / hours from the first submit to the last commit
    $/GB                 the run's costs.csv usd / that input GB (blank for local runs)
    incorrect outputs    jobs verify.json reports a failure for
    healthy jobs failed  failed jobs whose input is not a poison file (p1, p2, ...)
"""
from __future__ import annotations

import argparse
import csv
import json
import math
import re
import sys
from pathlib import Path

from cost import COSTS, load_costs
from sampling import parse_iso

FIGURES = Path(__file__).resolve().parents[1] / "results/figures"
P99_MIN_JOBS = 100
POISON = re.compile(r"^p\d+$")
FAILED = {"FAILED", "DEAD_LETTERED"}
COLUMNS = ["run", "jobs", "succeeded", "failed", "duplicates", "small p95 (s)", "small p99 (s)",
           "large p95 (s)", "large p99 (s)", "GB/h", "$/GB", "incorrect outputs", "healthy jobs failed"]


def percentile(values: list[float], p: float) -> float | None:
    """Linear interpolation between closest ranks (numpy's default)."""
    if not values:
        return None
    xs = sorted(values)
    k = (len(xs) - 1) * p / 100
    lo, hi = math.floor(k), math.ceil(k)
    return xs[lo] + (xs[hi] - xs[lo]) * (k - lo)


def read_jobs(run_dir: Path) -> list[dict]:
    with (run_dir / "jobs.csv").open(newline="") as f:
        return list(csv.DictReader(f))


def dataset_of(job: dict) -> str:
    return Path(job["input_key"]).name.removesuffix(".geojson")


def turnarounds(jobs: list[dict], size_class: str) -> list[float]:
    return [float(j["turnaround_s"]) for j in jobs
            if j["status"] == "SUCCEEDED" and j["size_class"] == size_class and j["turnaround_s"]]


def summarize(run_dir: Path, costs: dict[str, dict]) -> dict:
    """One master-table row; costs maps run_id to its costs.csv row."""
    config = json.loads((run_dir / "config.json").read_text())
    jobs = read_jobs(run_dir)
    done = [j for j in jobs if j["status"] == "SUCCEEDED"]
    failed = [j for j in jobs if j["status"] in FAILED]
    row = {"run": config["run_id"], "jobs": len(jobs), "succeeded": len(done), "failed": len(failed)}

    if jobs and "finished_attempts" in jobs[0]:
        row["duplicates"] = sum(1 for j in jobs if int(j["finished_attempts"] or 0) > 1)
    else:
        row["duplicates"] = None  # collected before finished_attempts existed

    for size in ("small", "large"):
        t = turnarounds(jobs, size)
        row[f"{size} p95 (s)"] = percentile(t, 95)
        row[f"{size} p99 (s)"] = percentile(t, 99) if len(t) >= P99_MIN_JOBS else None

    gb = sum(int(j["input_bytes"]) for j in done) / 1e9
    if done:
        start = min(parse_iso(j["submitted_at"]) for j in jobs if j["submitted_at"])
        end = max(parse_iso(j["committed_at"]) for j in done if j["committed_at"])
        hours = (end - start).total_seconds() / 3600
        row["GB/h"] = gb / hours if hours > 0 else None
    else:
        row["GB/h"] = None
    usd = costs.get(config["run_id"], {}).get("usd")
    row["$/GB"] = float(usd) / gb if usd and gb else None

    verify = run_dir / "verify.json"
    if verify.exists():
        row["incorrect outputs"] = len({f["job_id"] for f in json.loads(verify.read_text()).get("failures", [])})
    else:
        row["incorrect outputs"] = None
    row["healthy jobs failed"] = sum(1 for j in failed if not POISON.match(dataset_of(j)))
    return row


def fmt(value) -> str:
    if value is None:
        return "—"
    if isinstance(value, float):
        return f"{value:.3f}" if abs(value) < 10 else f"{value:.1f}"
    return str(value)


def markdown(rows: list[dict]) -> str:
    lines = ["| " + " | ".join(COLUMNS) + " |", "|" + "---|" * len(COLUMNS)]
    lines += ["| " + " | ".join(fmt(r[c]) for c in COLUMNS) + " |" for r in rows]
    return "\n".join(lines)


def change(before: dict, after: dict) -> dict:
    out = {"run": "change"}
    for c in COLUMNS[1:]:
        a, b = before[c], after[c]
        out[c] = b - a if a is not None and b is not None else None
    return out


def compare_problems(before: Path, after: Path) -> list[str]:
    a = json.loads((before / "config.json").read_text())
    b = json.loads((after / "config.json").read_text())
    problems = []
    if a.get("account_prefix") != b.get("account_prefix"):
        problems.append(f"different accounts: {a.get('account_prefix')} vs {b.get('account_prefix')}")
    if a.get("seed") != b.get("seed"):
        problems.append(f"different seeds: {a.get('seed')} vs {b.get('seed')}")
    return problems


def cdf_figure(run_dir: Path, out: Path) -> None:
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt

    jobs = read_jobs(run_dir)
    fig, ax = plt.subplots(figsize=(6, 4))
    for size in ("small", "medium", "large"):
        t = sorted(turnarounds(jobs, size))
        if t:
            ax.step(t, [(i + 1) / len(t) for i in range(len(t))], where="post", label=f"{size} ({len(t)})")
    ax.set(xlabel="turnaround (s)", ylabel="fraction of jobs", title=run_dir.name)
    ax.grid(alpha=0.3)
    ax.legend()
    out.parent.mkdir(parents=True, exist_ok=True)
    fig.savefig(out, dpi=120, bbox_inches="tight")
    plt.close(fig)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("runs", nargs="*", type=Path, help="run folders")
    ap.add_argument("--compare", nargs=2, type=Path, metavar=("BEFORE", "AFTER"))
    ap.add_argument("--costs", type=Path, default=COSTS)
    ap.add_argument("--figures", type=Path, default=FIGURES, help="where the CDF PNGs go")
    ap.add_argument("--no-figures", action="store_true")
    args = ap.parse_args(argv)
    runs = list(args.compare) if args.compare else args.runs
    if not runs:
        ap.error("give run folders, or --compare BEFORE AFTER")

    if args.compare:
        problems = compare_problems(*args.compare)
        if problems:
            for p in problems:
                print(f"analyze: not comparable: {p}", file=sys.stderr)
            return 2

    costs = {r["run_id"]: r for r in load_costs(args.costs)}
    rows = [summarize(d, costs) for d in runs]
    if args.compare:
        rows.append(change(*rows))
    print(markdown(rows))
    if not args.no_figures:
        for d in runs:
            out = args.figures / f"{d.name}.png"
            cdf_figure(d, out)
            print(f"analyze: {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
