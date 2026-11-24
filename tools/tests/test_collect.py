import csv
import io

import pytest

import collect


def ddb_item(**fields):
    """A job record in DynamoDB's wire format, as the run_id index returns it."""
    out = {}
    for k, v in fields.items():
        if isinstance(v, bool):
            out[k] = {"BOOL": v}
        elif isinstance(v, (int, float)):
            out[k] = {"N": str(v)}
        elif isinstance(v, list):
            out[k] = {"L": [{"M": ddb_item(**a)} for a in v]}
        else:
            out[k] = {"S": v}
    return out


class FakeTable:
    """Answers run_id-index queries from a fixed set of items, one item per page so that
    pagination is exercised too."""

    def __init__(self, items):
        self.items = items

    def query(self, **kw):
        assert kw["IndexName"] == "run_id-index"
        run_id = kw["ExpressionAttributeValues"][":r"]["S"]
        matching = [it for it in self.items if it["run_id"]["S"] == run_id]
        start = int(kw.get("ExclusiveStartKey", {}).get("i", 0))
        page = {"Items": matching[start:start + 1]}
        if start + 1 < len(matching):
            page["LastEvaluatedKey"] = {"i": start + 1}
        return page


def succeeded(job_id, run_id="local-a"):
    return ddb_item(
        job_id=job_id, run_id=run_id, output_format="gpkg", size_class="small", queue="jobs",
        input_key="datasets/dc.geojson", input_bytes=1000, status="SUCCEEDED", epoch=1,
        submitted_at="2026-11-21T10:00:00.000Z", enqueued_at="2026-11-21T10:00:00.500Z",
        first_started_at="2026-11-21T10:00:02.500Z", started_at="2026-11-21T10:00:02.500Z",
        committed_at="2026-11-21T10:00:10.500Z", convert_ms=7000, output_key=f"outputs/{run_id}/{job_id}/x.gpkg",
        attempts=[{"epoch": 1, "task_id": "t1", "started_at": "2026-11-21T10:00:02.500Z", "outcome": "SUCCEEDED"}],
    )


def export(items, run_id):
    buf = io.StringIO()
    n = collect.export(FakeTable(items), "jobs", run_id, buf)
    return n, list(csv.DictReader(io.StringIO(buf.getvalue())))


def test_collect_exports_one_run_only():
    items = [succeeded("a1", "local-a"), succeeded("b1", "local-b"), succeeded("a2", "local-a")]
    n, rows = export(items, "local-a")
    assert n == 2
    assert sorted(r["job_id"] for r in rows) == ["a1", "a2"]
    assert {r["run_id"] for r in rows} == {"local-a"}


def test_jobs_csv_has_metric_columns():
    _, rows = export([succeeded("a1")], "local-a")
    row = rows[0]
    for col in ("queue_wait_s", "processing_s", "turnaround_s", "slowdown"):
        assert col in row
    assert float(row["queue_wait_s"]) == pytest.approx(2.0)    # first_started_at - enqueued_at
    assert float(row["processing_s"]) == pytest.approx(8.0)    # committed_at - started_at
    assert float(row["turnaround_s"]) == pytest.approx(10.5)   # committed_at - submitted_at
    assert float(row["slowdown"]) == pytest.approx(10.5 / 8.0)


def test_unfinished_job_has_blank_metrics_but_a_queue_wait():
    item = succeeded("a1")
    item["status"] = {"S": "RUNNING"}
    del item["committed_at"]
    _, rows = export([item], "local-a")
    assert rows[0]["queue_wait_s"] != ""
    assert rows[0]["processing_s"] == rows[0]["turnaround_s"] == rows[0]["slowdown"] == ""


def test_attempts_are_summarised_not_dumped():
    item = succeeded("a1")
    item["attempts"] = {"L": [{"M": ddb_item(epoch=1, task_id="t1", started_at="2026-11-21T10:00:01.000Z", outcome="LOST")}]
                        + item["attempts"]["L"]}
    _, rows = export([item], "local-a")
    assert "attempts" not in rows[0]
    assert rows[0]["attempt_count"] == "2"
    assert rows[0]["last_outcome"] == "SUCCEEDED"


def test_columns_are_stable_whatever_fields_a_record_has():
    # Analysis reads columns by name; a run whose jobs all lack a field still has the column.
    _, rows = export([succeeded("a1")], "local-a")
    assert list(rows[0].keys()) == collect.COLUMNS


def test_attempts_that_converted_to_the_end_are_counted():
    # A duplicate is a job converted more than once: under blind commits both attempts end
    # SUCCEEDED; under fenced commits the loser ends STALE. A lost or failed attempt is not one.
    item = succeeded("a1")
    extra = [ddb_item(epoch=2, task_id="t2", started_at="2026-11-21T10:00:05.000Z", outcome=o)
             for o in ("STALE", "SUCCEEDED", "LOST", "FAILED")]
    item["attempts"] = {"L": item["attempts"]["L"] + [{"M": a} for a in extra]}
    _, rows = export([item], "local-a")
    assert rows[0]["finished_attempts"] == "3"


def test_each_attempt_is_a_row():
    # The table is destroyed with the stack; lease and crash results need every attempt kept.
    job = succeeded("a1")
    job["attempts"] = {"L": [
        {"M": ddb_item(epoch=1, task_id="t1", started_at="2026-11-21T10:00:01.000Z",
                       ended_at="2026-11-21T10:00:31.000Z", outcome="LOST")},
        {"M": ddb_item(epoch=2, task_id="t2", started_at="2026-11-21T10:00:32.000Z",
                       ended_at="2026-11-21T10:00:40.000Z", outcome="SUCCEEDED", peak_rss_mb=180.5)},
    ]}
    jobs_buf, attempts_buf = io.StringIO(), io.StringIO()
    assert collect.export(FakeTable([job]), "jobs", "local-a", jobs_buf, attempts_out=attempts_buf) == 1
    assert len(list(csv.DictReader(io.StringIO(jobs_buf.getvalue())))) == 1
    attempts = list(csv.DictReader(io.StringIO(attempts_buf.getvalue())))
    assert [(a["run_id"], a["job_id"], a["epoch"], a["task_id"], a["outcome"]) for a in attempts] == [
        ("local-a", "a1", "1", "t1", "LOST"), ("local-a", "a1", "2", "t2", "SUCCEEDED")]
    assert attempts[0]["ended_at"] == "2026-11-21T10:00:31.000Z" and attempts[0]["peak_rss_mb"] == ""
    assert attempts[1]["peak_rss_mb"] == "180.5"
    assert list(attempts[0]) == collect.ATTEMPT_COLUMNS


def _profile_body(job_id, run_id="local-10k"):
    row = {c: "" for c in collect.PROFILE_COLUMNS}
    row["run_id"] = run_id
    row["job_id"] = job_id
    buf = io.StringIO()
    w = csv.DictWriter(buf, fieldnames=collect.PROFILE_COLUMNS, lineterminator="\n")
    w.writeheader()
    w.writerow(row)
    return buf.getvalue().encode()


class FakeS3:
    def __init__(self, objects):
        self.objects = objects

    def get_paginator(self, name):
        assert name == "list_objects_v2"
        return self

    def paginate(self, **kw):
        prefix = kw["Prefix"]
        contents = [{"Key": k} for k in sorted(self.objects) if k.startswith(prefix)]
        yield {"Contents": contents} if contents else {}

    def get_object(self, **kw):
        return {"Body": io.BytesIO(self.objects[kw["Key"]])}


def test_profiles_join_to_one_header_sorted_by_job(tmp_path):
    s3 = FakeS3({
        "profiles/local-10k/b.csv": _profile_body("b"),
        "profiles/local-10k/a.csv": _profile_body("a"),
        "profiles/other/a.csv": _profile_body("a", run_id="other"),
    })
    dest = tmp_path / "profile.csv"
    assert collect.write_profiles(s3, "gc-local", "local-10k", dest) == 2
    text = dest.read_text()
    assert text.splitlines()[0] == ",".join(collect.PROFILE_COLUMNS)
    rows = list(csv.DictReader(io.StringIO(text)))
    assert [r["job_id"] for r in rows] == ["a", "b"]
    assert all(r["run_id"] == "local-10k" for r in rows)


def test_no_profile_objects_writes_no_file(tmp_path):
    dest = tmp_path / "profile.csv"
    assert collect.write_profiles(FakeS3({}), "gc-local", "local-10k", dest) == 0
    assert not dest.exists()


def test_bad_profile_header_is_refused(tmp_path):
    s3 = FakeS3({"profiles/local-10k/a.csv": b"nope,columns\n1,2\n"})
    with pytest.raises(ValueError):
        collect.write_profiles(s3, "gc-local", "local-10k", tmp_path / "profile.csv")
