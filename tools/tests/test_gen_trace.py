import csv
from pathlib import Path

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
