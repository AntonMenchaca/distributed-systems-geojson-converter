"""Generate a seeded workload trace (v0).

Trace CSV: t_offset_s,dataset_id,output_format,chaos. Arrivals are Poisson at --rate jobs/s;
each job picks a dataset from the catalog and an output format uniformly at random.

    python tools/gen_trace.py --seed 11 --jobs 30 --rate 0.5 --out traces/sample.csv
"""
from __future__ import annotations

import argparse
import csv
import random
from pathlib import Path

FORMATS = ["shp", "gpkg", "fgdb"]


def generate(seed: int, jobs: int, rate: float, dataset_ids: list[str]) -> list[dict[str, str]]:
    rng = random.Random(seed)
    t = 0.0
    rows = []
    for _ in range(jobs):
        rows.append({
            "t_offset_s": f"{t:.3f}",
            "dataset_id": rng.choice(dataset_ids),
            "output_format": rng.choice(FORMATS),
            "chaos": "",
        })
        t += rng.expovariate(rate)
    return rows


def main(argv: list[str] | None = None) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--seed", type=int, required=True)
    ap.add_argument("--jobs", type=int, required=True)
    ap.add_argument("--rate", type=float, required=True, help="mean arrivals per second")
    ap.add_argument("--catalog", type=Path, default=Path("datasets/local/datasets.csv"))
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args(argv)

    with args.catalog.open(newline="") as f:
        ids = sorted(row["dataset_id"] for row in csv.DictReader(f))
    if not ids:
        raise SystemExit(f"{args.catalog}: no datasets")
    rows = generate(args.seed, args.jobs, args.rate, ids)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["t_offset_s", "dataset_id", "output_format", "chaos"], lineterminator="\n")
        w.writeheader()
        w.writerows(rows)


if __name__ == "__main__":
    main()
