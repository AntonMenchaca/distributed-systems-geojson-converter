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
