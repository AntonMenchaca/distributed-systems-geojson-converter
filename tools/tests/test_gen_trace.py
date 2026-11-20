import csv
import json
from collections import Counter
from pathlib import Path

import pytest

import gen_trace

CATALOG = "dataset_id,s3_key,bytes,features,geometry_type,vertex_count,attribute_count,sha256,source\n" \
          "dc,datasets/dc.geojson,10,1,Polygon,5,2,x,microsoft\n" \
          "pts-1k,datasets/pts-1k.geojson,10,1000,Point,1000,2,y,synthetic\n"


def write_catalog(tmp_path: Path) -> Path:
    p = tmp_path / "datasets.csv"
    p.write_text(CATALOG)
    return p


def test_same_seed_same_trace(tmp_path):
    cat = write_catalog(tmp_path)
    a, b = tmp_path / "a.csv", tmp_path / "b.csv"
    gen_trace.main(["--seed", "11", "--jobs", "30", "--rate", "0.5", "--catalog", str(cat), "--out", str(a)])
    gen_trace.main(["--seed", "11", "--jobs", "30", "--rate", "0.5", "--catalog", str(cat), "--out", str(b)])
    assert a.read_text() == b.read_text()


def test_trace_shape(tmp_path):
    cat = write_catalog(tmp_path)
    out = tmp_path / "t.csv"
    gen_trace.main(["--seed", "22", "--jobs", "30", "--rate", "0.5", "--catalog", str(cat), "--out", str(out)])
    rows = list(csv.DictReader(out.open()))
    assert list(rows[0].keys()) == ["t_offset_s", "dataset_id", "output_format", "chaos"]
    assert len(rows) == 30
    offsets = [float(r["t_offset_s"]) for r in rows]
    assert offsets == sorted(offsets) and offsets[0] >= 0
    assert {r["dataset_id"] for r in rows} <= {"dc", "pts-1k"}
    assert {r["output_format"] for r in rows} <= {"shp", "gpkg", "fgdb"}
    assert all(r["chaos"] == "" for r in rows)


# --- H.3: sized kinds --------------------------------------------------------------------------

EST = {"small_threshold_s": 30, "formats": {
    "gpkg": {"work_s": [1.0, 0.10]}, "shp": {"work_s": [1.0, 0.08]}, "fgdb": {"work_s": [1.0, 0.12]}}}
MIB = 2 ** 20
# With EST and a 300 s large threshold: 10/50 MiB are small, 500/1000 MiB medium, 4000 MiB large,
# whatever the format.
SIZED = {"tiny": 10, "small": 50, "mid": 500, "big": 1000, "huge": 4000}
EXTRA = ["s1", "p1", "p2", "p3", "p4", "p6", "ca-all", "ca-1m-seq"]


def sized_catalog(tmp_path, sizes=SIZED, extra=EXTRA):
    lines = ["dataset_id,s3_key,bytes,features,geometry_type,vertex_count,attribute_count,sha256,source"]
    for d, mib in sizes.items():
        lines.append(f"{d},datasets/{d}.geojson,{mib * MIB},1,Polygon,5,2,x,microsoft")
    for d in extra:
        lines.append(f"{d},datasets/{d}.geojson,1000,1,Polygon,5,2,x,synthetic")
    (tmp_path / "datasets.csv").write_text("\n".join(lines) + "\n")
    (tmp_path / "est.json").write_text(json.dumps(EST))
    return tmp_path / "datasets.csv", tmp_path / "est.json"


def trace(tmp_path, *args, name="t.csv"):
    cat, est = sized_catalog(tmp_path)
    out = tmp_path / name
    gen_trace.main([*args, "--catalog", str(cat), "--estimates", str(est), "--out", str(out)])
    return list(csv.DictReader(out.open()))


def job_class(row, catalog):
    return gen_trace.classify(catalog[row["dataset_id"]], row["output_format"], EST, 300.0)


def test_mixed_class_shares(tmp_path):
    rows = trace(tmp_path, "--kind", "mixed", "--seed", "11", "--jobs", "10000", "--rate", "1")
    catalog = gen_trace.read_catalog(tmp_path / "datasets.csv")
    classes = Counter(job_class(r, catalog) for r in rows)
    formats = Counter(r["output_format"] for r in rows)
    for c, share in {"small": 0.85, "medium": 0.12, "large": 0.03}.items():
        assert abs(classes[c] / 10000 - share) <= 0.02
    for f, share in {"gpkg": 0.40, "shp": 0.40, "fgdb": 0.20}.items():
        assert abs(formats[f] / 10000 - share) <= 0.02
    assert not {r["dataset_id"] for r in rows} & set(EXTRA)  # shapes and poison never in the size mix


def test_burst_shape(tmp_path):
    rows = trace(tmp_path, "--kind", "burst", "--seed", "11", "--duration-s", "960", "--rate", "1")
    t = [float(r["t_offset_s"]) for r in rows]
    assert not [x for x in t if x < 300 or 480 <= x < 780]  # 5 min idle, twice
    for start in (300, 780):                                  # 3 min at 5x the 1 job/s rate
        n = sum(start <= x < start + 180 for x in t)
        assert abs(n / 180 - 5.0) <= 0.5


def test_fail_has_2pct_poison(tmp_path):
    rows = trace(tmp_path, "--kind", "fail", "--seed", "11", "--jobs", "20000", "--rate", "1")
    poison = [r for r in rows if r["dataset_id"] in {"p1", "p2", "p3", "p4", "p5", "p6"}]
    assert abs(len(poison) / len(rows) - 0.02) <= 0.004
    assert {r["dataset_id"] for r in poison} == {"p1", "p2", "p4", "p6"}   # never P3 or P5
    assert {r["output_format"] for r in poison if r["dataset_id"] == "p2"} <= {"shp", "fgdb"}
    chaos = [r for r in rows if r["chaos"]]
    assert all(json.loads(r["chaos"]) == {"crash_at": 0.5} for r in chaos)
    assert abs(len(chaos) / (len(rows) - len(poison)) - 0.05) <= 0.01
    assert not any(r["chaos"] for r in poison)


def test_rate_from_rho(tmp_path):
    sizes = {"tiny": 10, "mid": 500, "huge": 4000}  # one dataset per class
    cat, est = sized_catalog(tmp_path, sizes=sizes, extra=[])
    # E[S] by hand: format share x class share x (a + b * MiB)
    fmt = {"gpkg": (0.40, 0.10), "shp": (0.40, 0.08), "fgdb": (0.20, 0.12)}
    cls = {10: 0.85, 500: 0.12, 4000: 0.03}
    mean_s = sum(pf * pc * (1.0 + b * mib) for pf, b in fmt.values() for mib, pc in cls.items())
    want = 0.7 * 4 / mean_s                                   # lambda = rho * c / E[S]
    catalog = gen_trace.read_catalog(cat)
    bk = gen_trace.buckets(catalog, EST, 300.0)
    assert gen_trace.rate_from_rho(0.7, 4, gen_trace.mean_work(catalog, EST, bk)) == pytest.approx(want)
    out = tmp_path / "rho.csv"
    gen_trace.main(["--kind", "mixed", "--seed", "11", "--jobs", "20000", "--rho", "0.7",
                    "--catalog", str(cat), "--estimates", str(est), "--out", str(out)])
    last = float(list(csv.DictReader(out.open()))[-1]["t_offset_s"])
    assert 20000 / last == pytest.approx(want, rel=0.03)      # the trace really arrives at lambda


def test_overload_is_1_5x_the_rate(tmp_path):
    base = trace(tmp_path, "--kind", "mixed", "--seed", "11", "--jobs", "20000", "--rate", "1", name="m.csv")
    over = trace(tmp_path, "--kind", "overload", "--seed", "11", "--jobs", "20000", "--rate", "1", name="o.csv")
    assert float(base[-1]["t_offset_s"]) / float(over[-1]["t_offset_s"]) == pytest.approx(1.5, rel=0.01)


def test_sized_kinds_same_seed_same_trace(tmp_path):
    for kind in ["mixed", "burst", "overload", "fail"]:
        a = trace(tmp_path, "--kind", kind, "--seed", "22", "--duration-s", "900", "--rate", "0.5", name="a.csv")
        b = trace(tmp_path, "--kind", kind, "--seed", "22", "--duration-s", "900", "--rate", "0.5", name="b.csv")
        assert a == b and a


def test_empty_class_borrows_the_nearest_one(tmp_path):
    # The local catalog is all small; a mixed trace still works, drawing small jobs only.
    cat, est = sized_catalog(tmp_path, sizes={"dc": 22, "pts": 1}, extra=[])
    out = tmp_path / "t.csv"
    gen_trace.main(["--kind", "mixed", "--seed", "11", "--jobs", "200", "--rate", "1",
                    "--catalog", str(cat), "--estimates", str(est), "--out", str(out)])
    assert {r["dataset_id"] for r in csv.DictReader(out.open())} <= {"dc", "pts"}


def test_fail_without_poison_rows_stops(tmp_path):
    cat, est = sized_catalog(tmp_path, extra=[])
    with pytest.raises(SystemExit):
        gen_trace.main(["--kind", "fail", "--seed", "11", "--jobs", "10", "--rate", "1",
                        "--catalog", str(cat), "--estimates", str(est), "--out", str(tmp_path / "t.csv")])
