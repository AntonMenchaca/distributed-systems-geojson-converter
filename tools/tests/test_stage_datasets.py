import io
import urllib.error
import zipfile

import pytest

import cut_ladder
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
                         "--catalog-out", str(out), "--only", "dc"], upload=up)
    monkeypatch.setattr(stage_datasets, "download", fake_download(tmp_path, 5))  # Microsoft changed it
    with pytest.raises(stage_datasets.ShaMismatch):
        stage_datasets.main(["--bucket", "b", "--workdir", str(tmp_path / "w2"), "--catalog", str(out),
                             "--only", "dc"], upload=up)
