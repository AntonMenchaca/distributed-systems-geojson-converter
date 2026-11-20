import hashlib
import json

import pytest

import cut_ladder


def write_footprints(path, n):
    """A file in the Microsoft layout: wrapper lines, then one 5-vertex polygon per line."""
    lines = ["{", '  "type":"FeatureCollection",', '  "features":', "  ["]
    for i in range(n):
        x, y = round(-77 + i * 1e-4, 7), 38.9
        ring = [[x, y], [x + 1e-5, y], [x + 1e-5, y + 1e-5], [x, y + 1e-5], [x, y]]
        feat = {"type": "Feature", "geometry": {"type": "Polygon", "coordinates": [ring]},
                "properties": {"release": 2, "capture_dates_range": f"d{i}"}}
        lines.append("    " + json.dumps(feat, separators=(",", ":")) + ("," if i < n - 1 else ""))
    lines += ["  ]", "}"]
    path.write_text("\n".join(lines) + "\n")
    return path


def test_cut_ladder_first_n_features(tmp_path):
    src = write_footprints(tmp_path / "ca.geojson", 1500)
    out = cut_ladder.cut(src, tmp_path / "ca-1k.geojson", 1000)
    doc = json.loads(out.read_text())  # valid JSON as a whole
    assert doc["type"] == "FeatureCollection"
    assert len(doc["features"]) == 1000
    first = [json.loads(s) for s in list(cut_ladder.feature_lines(src))[:1000]]
    assert doc["features"] == first  # the first 1000, in order


def test_sha256_matches_file(tmp_path):
    src = write_footprints(tmp_path / "dc.geojson", 50)
    assert cut_ladder.measure(src)["sha256"] == hashlib.sha256(src.read_bytes()).hexdigest()


def test_measure_counts_every_column(tmp_path):
    src = write_footprints(tmp_path / "dc.geojson", 3)
    m = cut_ladder.measure(src)
    assert m["bytes"] == src.stat().st_size
    assert m["features"] == 3
    assert m["geometry_type"] == "Polygon"
    assert m["vertex_count"] == 15  # 5 per square, closing point included (as in datasets/local)
    assert m["attribute_count"] == 2


def test_cut_of_a_cut_is_byte_identical(tmp_path):
    # Re-staging checks sha256s, so a cut must be deterministic.
    src = write_footprints(tmp_path / "ca.geojson", 30)
    a = cut_ladder.cut(src, tmp_path / "a.geojson", 10)
    b = cut_ladder.cut(src, tmp_path / "b.geojson", 10)
    assert a.read_bytes() == b.read_bytes()
    assert cut_ladder.measure(a)["features"] == 10


def test_cut_with_too_few_features_fails(tmp_path):
    src = write_footprints(tmp_path / "ca.geojson", 5)
    with pytest.raises(ValueError):
        cut_ladder.cut(src, tmp_path / "out.geojson", 10)
    assert not (tmp_path / "out.geojson").exists()


def test_seq_copy_is_one_feature_per_line(tmp_path):
    src = write_footprints(tmp_path / "ca.geojson", 20)
    out = cut_ladder.to_seq(src, tmp_path / "ca-seq.geojsonl", 7)
    lines = out.read_text().splitlines()
    assert len(lines) == 7 and all(json.loads(s)["type"] == "Feature" for s in lines)
    assert cut_ladder.measure(out)["features"] == 7


def test_single_line_feature_collection_is_rejected(tmp_path):
    # gen_synthetic.py's compact files are one line; measuring one must fail, not count 1 feature.
    src = tmp_path / "compact.geojson"
    src.write_text(json.dumps({"type": "FeatureCollection", "features": [
        {"type": "Feature", "geometry": None, "properties": {}}]}, separators=(",", ":")))
    with pytest.raises(cut_ladder.LayoutError):
        cut_ladder.measure(src)


def test_ladder_ids_and_order(tmp_path, monkeypatch):
    monkeypatch.setattr(cut_ladder, "RUNGS", [("1k", 2), ("10k", 3), ("100k", 4), ("1m", 5), ("3m", 6)])
    src = write_footprints(tmp_path / "ca.geojson", 8)
    made = cut_ladder.ladder(src, tmp_path / "out")
    assert [d for d, _ in made] == ["ca-1k", "ca-10k", "ca-100k", "ca-1m", "ca-3m", "ca-all",
                                    "ca-1m-seq", "ca-all-seq"]
    counts = {d: cut_ladder.measure(p)["features"] for d, p in made}
    assert counts == {"ca-1k": 2, "ca-10k": 3, "ca-100k": 4, "ca-1m": 5, "ca-3m": 6, "ca-all": 8,
                      "ca-1m-seq": 5, "ca-all-seq": 8}
