"""Contract tests: the shared files every service builds against."""
import json
import re
from pathlib import Path

import jsonschema
import pytest

ROOT = Path(__file__).resolve().parents[2]


def load_schema(name: str) -> dict:
    schema = json.loads((ROOT / "schemas" / name).read_text())
    jsonschema.Draft202012Validator.check_schema(schema)
    return schema


QUEUE_MESSAGE_EXAMPLE = {
    "job_id": "9b2f6c1e-4a7d-4f0e-8c3b-2d1e5f6a7b8c",
    "run_id": "local-x",
    "input_key": "datasets/dc.geojson",
    "input_bytes": 23611576,
    "output_format": "gpkg",
    "size_class": "small",
    "est_work_s": 4.2,
    "est_mem_mb": 180.0,
    "submitted_at": "2026-11-14T18:30:00.000Z",
    "chaos": {"crash_at": 0.5},
}

JOB_RECORD_EXAMPLE = {
    "job_id": "9b2f6c1e-4a7d-4f0e-8c3b-2d1e5f6a7b8c",
    "run_id": "local-x",
    "output_format": "gpkg",
    "size_class": "small",
    "queue": "jobs",
    "input_key": "datasets/dc.geojson",
    "input_bytes": 23611576,
    "status": "SUCCEEDED",
    "epoch": 1,
    "submitted_at": "2026-11-14T18:30:00.000Z",
    "enqueued_at": "2026-11-14T18:30:00.050Z",
    "first_started_at": "2026-11-14T18:30:01.000Z",
    "started_at": "2026-11-14T18:30:01.000Z",
    "committed_at": "2026-11-14T18:30:06.000Z",
    "download_ms": 400,
    "convert_ms": 4100,
    "package_ms": 0,
    "upload_ms": 300,
    "output_key": "outputs/9b2f6c1e-4a7d-4f0e-8c3b-2d1e5f6a7b8c/out.gpkg",
    "output_bytes": 31000000,
    "output_sha256": "0" * 64,
    "feature_count": 58329,
    "attempts": [
        {
            "epoch": 1,
            "task_id": "local-worker-1",
            "started_at": "2026-11-14T18:30:01.000Z",
            "ended_at": "2026-11-14T18:30:06.000Z",
            "outcome": "SUCCEEDED",
            "peak_rss_mb": 180,
            "cpu_s": 3.9,
            "peak_disk_mb": 60,
        }
    ],
}


@pytest.mark.parametrize(
    "schema_file, example",
    [("queue-message.json", QUEUE_MESSAGE_EXAMPLE), ("job-record.json", JOB_RECORD_EXAMPLE)],
)
def test_example_validates(schema_file, example):
    jsonschema.validate(example, load_schema(schema_file))


@pytest.mark.parametrize(
    "schema_file, example, bad",
    [
        ("queue-message.json", QUEUE_MESSAGE_EXAMPLE, {"output_format": "kml"}),
        ("job-record.json", JOB_RECORD_EXAMPLE, {"status": "DONE"}),
    ],
)
def test_schema_rejects_bad_enum(schema_file, example, bad):
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate({**example, **bad}, load_schema(schema_file))


@pytest.mark.parametrize(
    "schema_file, example, field",
    [("queue-message.json", QUEUE_MESSAGE_EXAMPLE, "job_id"), ("job-record.json", JOB_RECORD_EXAMPLE, "status")],
)
def test_schema_requires_field(schema_file, example, field):
    doc = {k: v for k, v in example.items() if k != field}
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(doc, load_schema(schema_file))


def knobs_table() -> tuple[list[str], list[list[str]]]:
    """Return (header, rows) of the knobs table: rows are those whose first cell is a `code` name."""
    header, rows = [], []
    for line in (ROOT / "knobs.md").read_text().splitlines():
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if cells[0] == "Knob":
            header = cells
        elif cells[0].startswith("`"):
            rows.append(cells)
    return header, rows


def test_knobs_md_has_18_rows_with_baselines():
    header, rows = knobs_table()
    assert len(rows) == 18
    baseline_col = header.index("Baseline")
    for row in rows:
        assert row[baseline_col] not in ("", "—", "-"), row[0]


def test_estimates_v0_shape():
    est = json.loads((ROOT / "config" / "estimates.json").read_text())
    assert est["version"] == "v0"
    assert est["small_threshold_s"] == 30
    assert set(est["formats"]) == {"gpkg", "shp", "fgdb"}
    for fmt in est["formats"].values():
        for key in ("work_s", "mem_mb", "disk_mb"):
            assert len(fmt[key]) == 2 and all(isinstance(x, (int, float)) for x in fmt[key])
        assert fmt["out_ratio"] > 0
