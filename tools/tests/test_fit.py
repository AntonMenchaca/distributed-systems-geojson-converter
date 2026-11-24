"""Tests for tools/fit.py (step 2.3-B: estimates.json v1)."""
from __future__ import annotations

import csv
import json
from pathlib import Path

import pytest

import fit

ROOT = Path(__file__).resolve().parents[2]
V0_KEYS = {
    "version",
    "fitted_at",
    "small_threshold_s",
    "formats",
}
V0_FORMAT_KEYS = {"work_s", "mem_mb", "disk_mb", "out_ratio", "fit_r2"}


def test_fit_recovers_known_line():
    """Synthetic points on y = 2 + 3x give a ≈ 2, b ≈ 3."""
    xs = [1.0, 2.0, 3.0, 4.0, 5.0]
    ys = [2.0 + 3.0 * x for x in xs]
    a, b, r2 = fit.fit_line(xs, ys)
    assert a == pytest.approx(2.0, abs=1e-9)
    assert b == pytest.approx(3.0, abs=1e-9)
    assert r2 == pytest.approx(1.0, abs=1e-9)


def test_estimates_schema_unchanged():
    """v1 keeps the same top-level and per-format keys as the v0 contract."""
    est = json.loads((ROOT / "config" / "estimates.json").read_text())
    assert set(est) == V0_KEYS
    assert set(est["formats"]) == {"gpkg", "shp", "fgdb"}
    for fmt in est["formats"].values():
        assert set(fmt) == V0_FORMAT_KEYS
        for key in ("work_s", "mem_mb", "disk_mb"):
            assert len(fmt[key]) == 2
            assert all(isinstance(x, (int, float)) for x in fmt[key])
        assert isinstance(fmt["out_ratio"], (int, float))
        assert fmt["out_ratio"] > 0


def test_equal_load_threshold_splits_half_the_work():
    """Threshold is where weighted work below it is about half the catalog load."""
    # Three sizes × one format weight 1: works 10, 20, 30 → total 60; half = 30.
    # Jobs with work < 30 carry 10+20 = 30 → threshold 30.
    catalog = {
        "a": {"bytes": str(10 * fit.MIB)},
        "b": {"bytes": str(20 * fit.MIB)},
        "c": {"bytes": str(30 * fit.MIB)},
    }
    estimates = {
        "formats": {
            "gpkg": {"work_s": [0.0, 1.0]},  # work_s = input_mib
            "shp": {"work_s": [0.0, 1.0]},
            "fgdb": {"work_s": [0.0, 1.0]},
        }
    }
    # Use equal format shares so each dataset's three formats don't change the ratio.
    t = fit.equal_load_threshold(catalog, estimates, format_shares={"gpkg": 1.0})
    assert t == pytest.approx(30.0)


def test_fit_estimates_from_profile_csv(tmp_path):
    """One format, two rungs: recovers work_s = 1 + 2×mib from wall_ms."""
    path = tmp_path / "profile.csv"
    with path.open("w", newline="") as f:
        w = csv.DictWriter(
            f,
            fieldnames=[
                "run_id", "job_id", "dataset_id", "output_format", "gdal_profile",
                "input_bytes", "output_bytes", "output_features", "wall_ms", "package_ms",
                "cpu_s", "peak_rss_mb", "peak_disk_mb", "ogrinfo_ms", "error",
            ],
        )
        w.writeheader()
        # 1 MiB → work 3 s; 2 MiB → work 5 s  ⇒  a=1, b=2. mem/disk similarly.
        for mib, wall_s, rss, disk, out_ratio in [
            (1, 3.0, 100, 10, 0.5),
            (2, 5.0, 200, 20, 0.5),
        ]:
            w.writerow({
                "run_id": "t", "job_id": f"j{mib}", "dataset_id": f"d{mib}",
                "output_format": "gpkg", "gdal_profile": "defaults",
                "input_bytes": mib * fit.MIB, "output_bytes": int(out_ratio * mib * fit.MIB),
                "output_features": 1, "wall_ms": wall_s * 1000, "package_ms": 0,
                "cpu_s": wall_s, "peak_rss_mb": rss, "peak_disk_mb": disk,
                "ogrinfo_ms": 1, "error": "",
            })
    models = fit.fit_formats(path)
    g = models["gpkg"]
    assert g["work_s"][0] == pytest.approx(1.0, abs=1e-6)
    assert g["work_s"][1] == pytest.approx(2.0, abs=1e-6)
    assert g["mem_mb"][0] == pytest.approx(0.0, abs=1e-6)
    assert g["mem_mb"][1] == pytest.approx(100.0, abs=1e-6)
    assert g["out_ratio"] == pytest.approx(0.5, abs=1e-9)
    assert g["fit_r2"] == pytest.approx(1.0, abs=1e-9)
