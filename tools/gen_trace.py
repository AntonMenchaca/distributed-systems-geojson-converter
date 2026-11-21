"""Generate a seeded workload trace (step H.3).

Trace CSV: t_offset_s,dataset_id,output_format,chaos. The same arguments and seed always give the
same trace, which is what makes before/after runs comparable.

    # the handoff command: mixed sizes at 70% utilisation, 20 minutes
    python tools/gen_trace.py --kind mixed --seed 11 --rho 0.7 --estimates config/estimates.json \
        --duration-s 1200 --out traces/mixed-s11.csv

    # v0 behaviour (uniform datasets and formats), as used by traces/sample.csv
    python tools/gen_trace.py --seed 11 --jobs 30 --rate 0.5 --catalog datasets/local/datasets.csv --out t.csv

Kinds (definitions posted on issue #11):
    uniform   v0: Poisson arrivals, any dataset, any format
    mixed     small/medium/large jobs at 85/12/3%, formats gpkg/shp/fgdb at 40/40/20%
    burst     mixed, but in cycles of 5 min with no arrivals then 3 min at 5x the rate
    overload  mixed at 1.5x the rate (--overload-factor); --oversized-share adds P5 jobs
    fail      mixed plus 2% poison jobs (p1, p2, p4, p6) and 5% chaos jobs ({"crash_at":0.5})

A job's class comes from its predicted work, work_s = a + b * input_mb with (a, b) from
estimates.json for its output format and input_mb = bytes / 2**20: small below
small_threshold_s, large at or above --large-threshold-s, medium between. If the catalog has no
dataset in a class for some format (the local catalog is all small), that class borrows the
nearest non-empty class and a warning is printed.

The rate is either given (--rate, alias --lambda, jobs/s) or derived from utilisation:
lambda = rho * c / E[S], with c = --servers (default 4: the baseline 4 x 1 vCPU workers) and
E[S] the mean predicted work of the job mix this trace draws.
"""
from __future__ import annotations

import argparse
import csv
import json
import random
import re
import sys
from pathlib import Path

FORMATS = ["shp", "gpkg", "fgdb"]
FORMAT_SHARES = {"gpkg": 0.40, "shp": 0.40, "fgdb": 0.20}
CLASSES = ["small", "medium", "large"]
CLASS_SHARES = {"small": 0.85, "medium": 0.12, "large": 0.03}
POISON_SHARE = 0.02
POISON_IDS = ("p1", "p2", "p4", "p6")      # P3 succeeds (renamed fields); P5 is the oversized job
P2_FORMATS = ("shp", "fgdb")               # P2 converts fine to GeoPackage, so it isn't poison there
FAIL_CHAOS = {"crash_at": 0.5}
FAIL_CHAOS_SHARE = 0.05
BURST_IDLE_S, BURST_ON_S, BURST_FACTOR = 300.0, 180.0, 5.0
# Never in the size mix: s1-s6 shapes and p1-p6 poison (drawn separately), GeoJSONSeq copies (an
# input-format variable for Phase 8), and ca-all (the same file as ca, which would count it twice).
NOT_IN_MIX = re.compile(r"^[sp]\d|-seq$|-all$")
MIB = 2 ** 20


def read_catalog(path: Path) -> dict[str, dict]:
    with path.open(newline="") as f:
        return {r["dataset_id"]: r for r in csv.DictReader(f)}


def read_estimates(path: Path) -> dict:
    return json.loads(path.read_text())


def work_s(dataset: dict, fmt: str, est: dict) -> float:
    a, b = est["formats"][fmt]["work_s"]
    return a + b * int(dataset["bytes"]) / MIB


def classify(dataset: dict, fmt: str, est: dict, large_threshold_s: float) -> str:
    w = work_s(dataset, fmt, est)
    if w < est["small_threshold_s"]:
        return "small"
    return "large" if w >= large_threshold_s else "medium"


def buckets(catalog: dict[str, dict], est: dict, large_threshold_s: float, warn=print) -> dict:
    """(class, format) -> sorted dataset_ids, each empty class borrowing its nearest class."""
    real = {d: r for d, r in catalog.items() if not NOT_IN_MIX.search(d)}
    raw = {(c, f): sorted(d for d, r in real.items() if classify(r, f, est, large_threshold_s) == c)
           for c in CLASSES for f in FORMATS}
    out = {}
    for f in FORMATS:
        for i, c in enumerate(CLASSES):
            if raw[(c, f)]:
                out[(c, f)] = raw[(c, f)]
                continue
            near = sorted((j for j in range(len(CLASSES)) if raw[(CLASSES[j], f)]), key=lambda j: (abs(j - i), j))
            if not near:
                raise SystemExit("catalog has no real datasets to draw from")
            out[(c, f)] = raw[(CLASSES[near[0]], f)]
            warn(f"warning: no {c} datasets for {f}; drawing {CLASSES[near[0]]} ones instead", file=sys.stderr)
    return out


def mean_work(catalog: dict, est: dict, bk: dict) -> float:
    """E[S] of the mixed job mix: sum over format and class of share x mean work in the bucket."""
    total = 0.0
    for f, pf in FORMAT_SHARES.items():
        for c, pc in CLASS_SHARES.items():
            ids = bk[(c, f)]
            total += pf * pc * sum(work_s(catalog[d], f, est) for d in ids) / len(ids)
    return total


def rate_from_rho(rho: float, servers: int, mean_s: float) -> float:
    return rho * servers / mean_s


def pick(rng: random.Random, shares: dict[str, float]) -> str:
    return rng.choices(list(shares), weights=list(shares.values()))[0]


def arrivals(rng: random.Random, rate: float, jobs: int | None, duration_s: float | None,
             kind: str) -> list[float]:
    """Poisson arrival times; burst alternates BURST_IDLE_S of silence with BURST_ON_S at 5x."""
    times, t = [], 0.0
    while True:
        if kind == "burst":
            cycle = BURST_IDLE_S + BURST_ON_S
            phase = t % cycle
            if phase < BURST_IDLE_S:            # skip the silent part of the cycle
                t += BURST_IDLE_S - phase
            t += rng.expovariate(rate * BURST_FACTOR)
            if t % cycle < BURST_IDLE_S:        # the gap crossed into the next silent window
                continue
        else:
            t += rng.expovariate(rate)
        if (duration_s is not None and t >= duration_s) or (jobs is not None and len(times) == jobs):
            return times
        times.append(t)


def generate(kind: str, seed: int, rate: float, catalog: dict[str, dict], est: dict | None = None,
             jobs: int | None = None, duration_s: float | None = None, large_threshold_s: float = 300.0,
             oversized_share: float = 0.0, chaos: dict | None = None, chaos_share: float | None = None,
             warn=print) -> list[dict[str, str]]:
    rng = random.Random(seed)
    if kind == "uniform":  # exactly v0's draws, so traces/sample.csv regenerates byte for byte
        ids, rows, t = sorted(catalog), [], 0.0
        while not ((jobs is not None and len(rows) == jobs) or (duration_s is not None and t >= duration_s)):
            rows.append({"t_offset_s": f"{t:.3f}", "dataset_id": rng.choice(ids),
                         "output_format": rng.choice(FORMATS), "chaos": ""})
            t += rng.expovariate(rate)
        return rows

    bk = buckets(catalog, est, large_threshold_s, warn)
    poison = [d for d in POISON_IDS if d in catalog]
    if kind == "fail" and not poison:
        raise SystemExit("--kind fail needs poison rows (p1, p2, p4, p6) in the catalog")
    if oversized_share and "p5" not in catalog:
        raise SystemExit("--oversized-share needs a p5 row in the catalog (its size comes from Phase 2)")
    if chaos_share is None:
        chaos_share = FAIL_CHAOS_SHARE if kind == "fail" else 0.0
    chaos = chaos or (FAIL_CHAOS if kind == "fail" else None)
    if chaos_share and not chaos:
        raise SystemExit("--chaos-share needs --chaos")

    rows = []
    for t in arrivals(rng, rate, jobs, duration_s, kind):
        u, spec = rng.random(), ""
        if kind == "fail" and u < POISON_SHARE:
            d = rng.choice(poison)
            f = rng.choice(P2_FORMATS) if d == "p2" else pick(rng, FORMAT_SHARES)
        elif oversized_share and u < oversized_share:
            d, f = "p5", pick(rng, FORMAT_SHARES)
        else:
            f = pick(rng, FORMAT_SHARES)
            d = rng.choice(bk[(pick(rng, CLASS_SHARES), f)])
            if chaos_share and rng.random() < chaos_share:
                spec = json.dumps(chaos, separators=(",", ":"))
        rows.append({"t_offset_s": f"{t:.3f}", "dataset_id": d, "output_format": f, "chaos": spec})
    return rows


def main(argv: list[str] | None = None) -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--kind", choices=["uniform", "mixed", "burst", "overload", "fail"], default="uniform")
    ap.add_argument("--seed", type=int, required=True)
    size = ap.add_mutually_exclusive_group(required=True)
    size.add_argument("--jobs", type=int)
    size.add_argument("--duration-s", type=float)
    speed = ap.add_mutually_exclusive_group(required=True)
    speed.add_argument("--rate", "--lambda", dest="rate", type=float, help="mean arrivals per second")
    speed.add_argument("--rho", type=float, help="target utilisation; needs --estimates")
    ap.add_argument("--estimates", type=Path, default=Path("config/estimates.json"))
    ap.add_argument("--servers", type=int, default=4, help="c in lambda = rho*c/E[S] (default 4 workers)")
    ap.add_argument("--large-threshold-s", type=float, default=300.0)
    ap.add_argument("--overload-factor", type=float, default=1.5)
    ap.add_argument("--oversized-share", type=float, default=0.0, help="share of p5 jobs (Phase 5b: 0.02)")
    ap.add_argument("--chaos", type=json.loads, help='chaos JSON for chaos jobs, e.g. {"pause_s":90}')
    ap.add_argument("--chaos-share", type=float)
    ap.add_argument("--catalog", type=Path)
    ap.add_argument("--out", type=Path, required=True)
    a = ap.parse_args(argv)

    default_catalog = "datasets/local/datasets.csv" if a.kind == "uniform" else "datasets/datasets.csv"
    catalog = read_catalog(a.catalog or Path(default_catalog))
    if not catalog:
        raise SystemExit(f"{a.catalog}: no datasets")
    est = read_estimates(a.estimates) if (a.kind != "uniform" or a.rho is not None) else None
    rate = a.rate
    if a.rho is not None:
        if a.kind == "uniform":
            ap.error("--rho needs a sized kind (mixed, burst, overload, fail)")
        bk = buckets(catalog, est, a.large_threshold_s, warn=lambda *x, **k: None)
        rate = rate_from_rho(a.rho, a.servers, mean_work(catalog, est, bk))
    if a.kind == "overload":
        rate *= a.overload_factor
    rows = generate(a.kind, a.seed, rate, catalog, est, a.jobs, a.duration_s, a.large_threshold_s,
                    a.oversized_share, a.chaos, a.chaos_share)
    a.out.parent.mkdir(parents=True, exist_ok=True)
    with a.out.open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["t_offset_s", "dataset_id", "output_format", "chaos"], lineterminator="\n")
        w.writeheader()
        w.writerows(rows)
    print(f"{a.out}: {len(rows)} jobs, rate {rate:.4f}/s", file=sys.stderr)


if __name__ == "__main__":
    main()
