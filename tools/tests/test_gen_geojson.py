import json

import pytest

import gen_geojson


def make(tmp_path, *args, name="out.geojson"):
    out = tmp_path / name
    gen_geojson.main([*args, "--out", str(out)])
    return out


def features(path):
    """Parse a generated file line by line (the Microsoft layout), not as one document."""
    out = []
    for line in path.read_text(encoding="utf-8").splitlines():
        s = line.strip().rstrip(",")
        if s.startswith('{"type":"Feature"'):
            out.append(json.loads(s))
    return out


def test_same_seed_same_bytes(tmp_path):
    a = make(tmp_path, "--shape", "S1", "--seed", "11", "--target-mb", "0.2", name="a.geojson")
    b = make(tmp_path, "--shape", "S1", "--seed", "11", "--target-mb", "0.2", name="b.geojson")
    c = make(tmp_path, "--shape", "S1", "--seed", "12", "--target-mb", "0.2", name="c.geojson")
    assert a.read_bytes() == b.read_bytes()
    assert a.read_bytes() != c.read_bytes()


def test_s6_has_strings_over_254(tmp_path):
    feats = features(make(tmp_path, "--shape", "S6", "--target-mb", "0.2"))
    assert feats and all(len(f["properties"]["note"]) > 254 for f in feats)


def test_p2_mixed_geometry_types(tmp_path):
    feats = features(make(tmp_path, "--poison", "P2"))
    assert {f["geometry"]["type"] for f in feats} == {"Point", "LineString", "Polygon"}


def test_p3_field_names_collide_at_10_chars(tmp_path):
    names = list(features(make(tmp_path, "--poison", "P3"))[0]["properties"])
    assert len(names) == len(set(names)) == 2
    assert names[0] != names[1] and names[0][:10] == names[1][:10]


@pytest.mark.slow
def test_p4_single_feature_over_200mb(tmp_path):
    out = make(tmp_path, "--poison", "P4")
    with out.open("rb") as f:
        lines = f.read().split(b"\n")
    feature_lines = [ln for ln in lines if ln.startswith(b'{"type":"Feature"')]
    assert len(feature_lines) == 1
    assert len(feature_lines[0]) > 200 * 1024 * 1024
    assert feature_lines[0].endswith(b"}") and out.read_bytes().endswith(gen_geojson.FOOTER)


# Beyond the plan's five: the properties the rest of the project relies on.

@pytest.mark.parametrize("shape", ["S1", "S2", "S4", "S5", "S6"])
def test_s_files_hit_the_target_size(tmp_path, shape):
    out = make(tmp_path, "--shape", shape, "--target-mb", "1")
    assert abs(out.stat().st_size - 1024 * 1024) / (1024 * 1024) < 0.05


def test_every_file_is_valid_json_one_feature_per_line_without_id(tmp_path):
    for shape in ["S1", "S2", "S3", "S4", "S5", "S6"]:
        out = make(tmp_path, "--shape", shape, "--target-mb", "0.6", name=f"{shape}.geojson")
        doc = json.loads(out.read_text())
        assert len(doc["features"]) == len(features(out)) > 0
        assert all("id" not in f and "id" not in f["properties"] for f in doc["features"])


def test_s3_polygons_have_10000_vertices(tmp_path):
    ring = features(make(tmp_path, "--shape", "S3", "--target-mb", "0.6"))[0]["geometry"]["coordinates"][0]
    assert len(ring) == 10_001 and ring[0] == ring[-1]


def test_s5_is_s1_geometry_with_15_decimals(tmp_path):
    s1 = features(make(tmp_path, "--shape", "S1", "--target-mb", "0.1", name="s1.geojson"))
    s5_path = make(tmp_path, "--shape", "S5", "--target-mb", "0.1", name="s5.geojson")
    s5 = features(s5_path)
    a, b = s1[0]["geometry"]["coordinates"][0], s5[0]["geometry"]["coordinates"][0]
    assert all(round(x, 7) == p and round(y, 7) == q for (x, y), (p, q) in zip(b, a))
    first_x = s5_path.read_text().split('"coordinates":[[[', 1)[1].split(",", 1)[0]
    assert len(first_x.split(".")[1]) == 15


def test_p1_is_truncated_json(tmp_path):
    out = make(tmp_path, "--poison", "P1")
    with pytest.raises(json.JSONDecodeError):
        json.loads(out.read_text())


def test_p6_has_invalid_utf8(tmp_path):
    out = make(tmp_path, "--poison", "P6")
    with pytest.raises(UnicodeDecodeError):
        out.read_bytes().decode("utf-8")


def test_p5_needs_target_mb(tmp_path):
    with pytest.raises(SystemExit):
        make(tmp_path, "--poison", "P5")
    out = make(tmp_path, "--poison", "P5", "--target-mb", "0.5", name="p5.geojson")
    assert abs(out.stat().st_size - 0.5 * 1024 * 1024) / (0.5 * 1024 * 1024) < 0.05
