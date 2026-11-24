"""Fit config/estimates.json v1 from Phase 2 profile.csv (step 2.3-B).

    python tools/fit.py \\
        --profile results/p2/p2-size-retry-samuel-s11/profile.csv \\
        --catalog datasets/datasets.csv \\
        --out config/estimates.json

Reads SUCCEEDED rows with GDAL_PROFILE=defaults. Per (dataset, format), takes the
median of reps, then fits work_s / mem_mb / disk_mb as a + b × input_mib (work_s from
wall_ms/1000 = convert seconds). out_ratio is the median of output_bytes/input_bytes.
fit_r2 is the work_s R². small_threshold_s is the equal-load cutoff over real catalog
datasets × formats at 40/40/20 (Harchol-Balter): jobs below it carry half the total
predicted work, so gen_trace.py can build traces against a stable threshold.
"""
from __future__ import annotations

import argparse
import csv
import json
import math
import re
import statistics
import sys
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path

MIB = 2**20
FORMATS = ("gpkg", "shp", "fgdb")
FORMAT_SHARES = {"gpkg": 0.40, "shp": 0.40, "fgdb": 0.20}
# Same exclusion as tools/gen_trace.py: shapes, poison, GeoJSONSeq, ca-all duplicate.
NOT_IN_MIX = re.compile(r"^[sp]\d|-seq$|-all$")


def fit_line(xs: list[float], ys: list[float]) -> tuple[float, float, float]:
    """Ordinary least squares: y ≈ a + b x. Returns (a, b, r²)."""
    if len(xs) != len(ys) or len(xs) < 2:
        raise ValueError(f"fit_line needs ≥2 points, got {len(xs)}")
    n = len(xs)
    mean_x = sum(xs) / n
    mean_y = sum(ys) / n
    var_x = sum((x - mean_x) ** 2 for x in xs)
    if var_x == 0:
        raise ValueError("fit_line: all x values are identical")
    cov = sum((x - mean_x) * (y - mean_y) for x, y in zip(xs, ys))
    b = cov / var_x
    a = mean_y - b * mean_x
    ss_tot = sum((y - mean_y) ** 2 for y in ys)
    ss_res = sum((y - (a + b * x)) ** 2 for x, y in zip(xs, ys))
    r2 = 1.0 if ss_tot == 0 else 1.0 - ss_res / ss_tot
    return a, b, r2


def _num(row: dict, key: str) -> float:
    v = row.get(key, "")
    if v is None or v == "":
        return float("nan")
    return float(v)


def load_fit_rows(profile: Path) -> list[dict]:
    """SUCCEEDED defaults-profile rows usable for the size fit."""
    with profile.open(newline="") as f:
        rows = list(csv.DictReader(f))
    out = []
    for r in rows:
        if (r.get("error") or "").strip():
            continue
        if r.get("gdal_profile", "defaults") != "defaults":
            continue
        if r.get("output_format") not in FORMATS:
            continue
        if not r.get("input_bytes") or not r.get("wall_ms"):
            continue
        out.append(r)
    return out


def _median(vals: list[float]) -> float:
    return float(statistics.median(vals))


def aggregate_points(rows: list[dict]) -> dict[str, list[dict]]:
    """Per format: one point per dataset_id (median of reps)."""
    groups: dict[tuple[str, str], list[dict]] = defaultdict(list)
    for r in rows:
        groups[(r["output_format"], r["dataset_id"])].append(r)

    by_fmt: dict[str, list[dict]] = {f: [] for f in FORMATS}
    for (fmt, _dataset), reps in sorted(groups.items()):
        input_bytes = _median([_num(r, "input_bytes") for r in reps])
        wall_ms = _median([_num(r, "wall_ms") for r in reps])
        peak_rss = _median([_num(r, "peak_rss_mb") for r in reps])
        peak_disk = _median([_num(r, "peak_disk_mb") for r in reps])
        output_bytes = _median([_num(r, "output_bytes") for r in reps])
        by_fmt[fmt].append({
            "input_mib": input_bytes / MIB,
            "work_s": wall_ms / 1000.0,
            "mem_mb": peak_rss,
            "disk_mb": peak_disk,
            "out_ratio": output_bytes / input_bytes if input_bytes else float("nan"),
        })
    return by_fmt


def _r2(xs: list[float], ys: list[float], a: float, b: float) -> float:
    mean_y = sum(ys) / len(ys)
    ss_tot = sum((y - mean_y) ** 2 for y in ys)
    ss_res = sum((y - (a + b * x)) ** 2 for x, y in zip(xs, ys))
    return 1.0 if ss_tot == 0 else 1.0 - ss_res / ss_tot


def _fit_metric(points: list[dict], y_key: str, *, skip_nonpositive: bool = False) -> tuple[list[float], float]:
    xs, ys = [], []
    for p in points:
        y = p[y_key]
        if math.isnan(y) or math.isnan(p["input_mib"]):
            continue
        if skip_nonpositive and y <= 0:
            continue
        xs.append(p["input_mib"])
        ys.append(y)
    if len(xs) < 2:
        raise ValueError(f"need ≥2 points for {y_key}, got {len(xs)}")
    a, b, r2 = fit_line(xs, ys)
    # Cost models must stay non-negative for tiny inputs; refit through the origin.
    if a < 0:
        b = sum(x * y for x, y in zip(xs, ys)) / sum(x * x for x in xs)
        a = 0.0
        r2 = _r2(xs, ys, a, b)
    return [a, b], r2


def fit_formats(profile: Path, formats: tuple[str, ...] | None = None) -> dict[str, dict]:
    """Fit FormatEstimate dicts from a profile.csv. Defaults to every format present."""
    points = aggregate_points(load_fit_rows(profile))
    want = formats if formats is not None else tuple(f for f in FORMATS if points[f])
    models = {}
    for fmt in want:
        pts = points[fmt]
        if len(pts) < 2:
            raise ValueError(f"{fmt}: need ≥2 SUCCEEDED defaults rows, got {len(pts)}")
        work, work_r2 = _fit_metric(pts, "work_s")
        # Zero RSS is a measurement floor on tiny/fast jobs; do not pull the mem line.
        mem, _ = _fit_metric(pts, "mem_mb", skip_nonpositive=True)
        disk, _ = _fit_metric(pts, "disk_mb")
        ratios = [p["out_ratio"] for p in pts if not math.isnan(p["out_ratio"]) and p["out_ratio"] > 0]
        if not ratios:
            raise ValueError(f"{fmt}: no positive out_ratio samples")
        models[fmt] = {
            "work_s": work,
            "mem_mb": mem,
            "disk_mb": disk,
            "out_ratio": _median(ratios),
            "fit_r2": work_r2,
        }
    return models


def work_s(dataset: dict, fmt: str, estimates: dict) -> float:
    a, b = estimates["formats"][fmt]["work_s"]
    return a + b * int(dataset["bytes"]) / MIB


def equal_load_threshold(
    catalog: dict[str, dict],
    estimates: dict,
    format_shares: dict[str, float] | None = None,
) -> float:
    """Size cutoff where weighted work below it is half the catalog's predicted work.

    Each real dataset × format is a job type weighted by format_shares (default 40/40/20).
    Sort by predicted work; walk ascending until cumulative load reaches half of total.
    The threshold is that job's work (router: small when work < threshold).
    """
    shares = format_shares or FORMAT_SHARES
    real = {d: r for d, r in catalog.items() if not NOT_IN_MIX.search(d)}
    if not real:
        raise ValueError("catalog has no real datasets for the equal-load threshold")
    items: list[tuple[float, float]] = []
    for row in real.values():
        for fmt, share in shares.items():
            items.append((work_s(row, fmt, estimates), share))
    total = sum(w * s for w, s in items)
    if total <= 0:
        raise ValueError("total predicted work is not positive")
    items.sort(key=lambda t: t[0])
    target = total / 2.0
    cum = 0.0
    for work, share in items:
        # Include this job in the small class while the load stays ≤ half.
        # Router: small when work < threshold, so the first job that would
        # push past half becomes the cutoff (and is itself large).
        if cum + work * share > target:
            return work
        cum += work * share
    return items[-1][0]


def read_catalog(path: Path) -> dict[str, dict]:
    with path.open(newline="") as f:
        return {r["dataset_id"]: r for r in csv.DictReader(f)}


def build_estimates(profile: Path, catalog: Path, fitted_at: str | None = None) -> dict:
    formats = fit_formats(profile, formats=FORMATS)
    missing = [f for f in FORMATS if f not in formats]
    if missing:
        raise ValueError(f"profile is missing formats: {missing}")
    est = {
        "version": "v1",
        "fitted_at": fitted_at or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "small_threshold_s": 30.0,  # placeholder until formats are attached
        "formats": formats,
    }
    est["small_threshold_s"] = equal_load_threshold(read_catalog(catalog), est)
    return est


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    p.add_argument("--profile", type=Path, required=True, help="results/.../profile.csv")
    p.add_argument("--catalog", type=Path, default=Path("datasets/datasets.csv"))
    p.add_argument("--out", type=Path, default=Path("config/estimates.json"))
    args = p.parse_args(argv)

    est = build_estimates(args.profile, args.catalog)
    # Round for a readable committed file; keep enough digits for routing.
    # Recompute the threshold after rounding so it matches work_s from the file.
    for fmt in est["formats"].values():
        for key in ("work_s", "mem_mb", "disk_mb"):
            fmt[key] = [round(x, 6) for x in fmt[key]]
        fmt["out_ratio"] = round(fmt["out_ratio"], 6)
        fmt["fit_r2"] = round(fmt["fit_r2"], 6)
    # Floor to 6 decimals so the first large job (work == threshold before
    # rounding) stays large under the router's work < threshold rule.
    t = equal_load_threshold(read_catalog(args.catalog), est)
    est["small_threshold_s"] = math.floor(t * 1e6) / 1e6

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(est, indent=2) + "\n")
    print(
        f"wrote {args.out} version={est['version']} "
        f"small_threshold_s={est['small_threshold_s']} fitted_at={est['fitted_at']}",
        file=sys.stderr,
    )
    for fmt, m in est["formats"].items():
        print(
            f"  {fmt}: work_s={m['work_s']} mem_mb={m['mem_mb']} disk_mb={m['disk_mb']} "
            f"out_ratio={m['out_ratio']} fit_r2={m['fit_r2']}",
            file=sys.stderr,
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
