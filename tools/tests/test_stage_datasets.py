import io
import urllib.error
import zipfile

import pytest

import cut_ladder
import gen_geojson
import stage_datasets
from test_cut_ladder import write_footprints


def http_error(code):
    return urllib.error.HTTPError("https://x", code, "err", {}, io.BytesIO())


def test_moved_file_stops_without_retrying(tmp_path):
    slept = []

    def opener(url, timeout):
        raise http_error(409)

    with pytest.raises(stage_datasets.MovedError):
        stage_datasets.download("https://x", tmp_path / "f.zip", sleep=slept.append, opener=opener)
    assert slept == []


def test_transient_errors_retry_then_succeed(tmp_path):
    slept, calls = [], []

    def opener(url, timeout):
        calls.append(url)
        if len(calls) < 3:
            raise http_error(503)
        return io.BytesIO(b"zipbytes")

    out = stage_datasets.download("https://x", tmp_path / "f.zip", sleep=slept.append, opener=opener)
    assert out.read_bytes() == b"zipbytes"
    assert slept == [5, 15]


def test_gives_up_after_the_last_retry(tmp_path):
    def opener(url, timeout):
        raise urllib.error.URLError("no route")

    with pytest.raises(RuntimeError):
        stage_datasets.download("https://x", tmp_path / "f.zip", sleep=lambda s: None, opener=opener)


def fake_download(tmp_path, n_features):
    """Replace the Microsoft download with a zip holding a footprint file of n features."""
    def download(url, dest, **kw):
        name = url.rsplit("/", 1)[1].replace(".zip", "")
        inner = write_footprints(tmp_path / f"src-{name}", n_features)
        with zipfile.ZipFile(dest, "w") as z:
            z.write(inner, name)
        return dest
    return download


def test_measure_then_restage_checks_every_sha(tmp_path, monkeypatch):
    monkeypatch.setattr(cut_ladder, "RUNGS", [("1k", 2), ("10k", 3), ("100k", 4), ("1m", 5), ("3m", 6)])
    monkeypatch.setattr(stage_datasets, "download", fake_download(tmp_path, 8))
    uploads = []
    upload = lambda path, bucket, key: uploads.append((bucket, key))
    out = tmp_path / "datasets.csv"

    stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w"), "--measure",
                         "--catalog", str(tmp_path / "none.csv"),
                         "--catalog-out", str(out), "--only", "dc,ca"], upload=upload)
    rows = stage_datasets.read_catalog(out)
    assert list(rows) == ["dc", "ca", "ca-1k", "ca-10k", "ca-100k", "ca-1m", "ca-3m", "ca-all",
                          "ca-1m-seq", "ca-all-seq"]
    assert rows["ca-all"]["s3_key"] == rows["ca"]["s3_key"] == "datasets/ca.geojson"
    assert rows["ca-all"]["sha256"] == rows["ca"]["sha256"]
    assert rows["dc"]["source"].endswith("/DistrictofColumbia.geojson.zip")
    assert ("b", "datasets/datasets.csv") in uploads
    assert len({k for _, k in uploads}) == len(uploads)  # nothing uploaded twice

    uploads.clear()
    stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w2"), "--catalog", str(out),
                         "--only", "dc,ca"], upload=upload)
    assert len(uploads) == 9  # 2 states + 5 cuts + 2 seq copies; ca-all reuses ca's key
    assert not list((tmp_path / "w2").iterdir())  # every file deleted after upload


def test_changed_source_stops_restaging(tmp_path, monkeypatch):
    monkeypatch.setattr(stage_datasets, "download", fake_download(tmp_path, 4))
    out = tmp_path / "datasets.csv"
    up = lambda *a: None
    stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w"), "--measure",
                         "--catalog", str(tmp_path / "none.csv"),
                         "--catalog-out", str(out), "--only", "dc"], upload=up)
    monkeypatch.setattr(stage_datasets, "download", fake_download(tmp_path, 5))  # Microsoft changed it
    with pytest.raises(stage_datasets.ShaMismatch):
        stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w2"), "--catalog", str(out),
                             "--only", "dc"], upload=up)


# --- H.2 files -------------------------------------------------------------------------------

H2_IDS = "s1,s2,s3,s4,s5,s6,p1,p2,p3,p4,p6"


def small_h2_files(monkeypatch):
    monkeypatch.setattr(gen_geojson, "DEFAULT_TARGET_MB", {"S": 0.05, "P4": 0.05})


def test_h2_files_are_measured_then_restaged(tmp_path, monkeypatch):
    small_h2_files(monkeypatch)
    out, uploads = tmp_path / "datasets.csv", []
    upload = lambda path, bucket, key: uploads.append(key)
    stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w"), "--measure",
                         "--catalog", str(tmp_path / "none.csv"), "--catalog-out", str(out),
                         "--only", H2_IDS], upload=upload)
    rows = stage_datasets.read_catalog(out)
    assert list(rows) == H2_IDS.split(",")                   # p5 waits for Phase 2
    assert rows["s1"]["source"] == "synthetic:tools/gen_geojson.py --shape S1 --seed 11"
    assert rows["p4"]["source"] == "synthetic:tools/gen_geojson.py --poison P4 --seed 11"
    for p in ("p1", "p6"):                                  # can't be parsed, by design
        assert rows[p]["features"] == "" and int(rows[p]["bytes"]) > 0 and len(rows[p]["sha256"]) == 64
    assert rows["p2"]["geometry_type"] == "LineString|Point|Polygon"
    assert rows["p3"]["features"] == str(gen_geojson.SMALL_POISON_FEATURES)
    assert rows["p4"]["features"] == "1"

    uploads.clear()                                         # next sitting: regenerate and check
    stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w2"), "--catalog", str(out),
                         "--only", H2_IDS], upload=upload)
    assert len(uploads) == 11
    assert not list((tmp_path / "w2").iterdir())


def test_measure_merges_into_the_committed_catalog(tmp_path, monkeypatch):
    small_h2_files(monkeypatch)
    base = tmp_path / "datasets.csv"
    stage_datasets.write_catalog([
        {"dataset_id": "dc", "s3_key": "datasets/dc.geojson", "bytes": 1, "features": 1, "geometry_type": "Polygon",
         "vertex_count": 5, "attribute_count": 2, "sha256": "x", "source": "microsoft"},
        {"dataset_id": "s1", "s3_key": "datasets/s1.geojson", "bytes": 1, "features": 1, "geometry_type": "Polygon",
         "vertex_count": 5, "attribute_count": 2, "sha256": "stale", "source": "old"}], base)
    out = tmp_path / "new.csv"
    stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w"), "--measure", "--catalog", str(base),
                         "--catalog-out", str(out), "--only", "s1,s2"], upload=lambda *a: None)
    rows = stage_datasets.read_catalog(out)
    assert list(rows) == ["dc", "s1", "s2"]                  # dc kept, s1 replaced in place, s2 appended
    assert rows["dc"]["sha256"] == "x" and rows["s1"]["sha256"] != "stale"
