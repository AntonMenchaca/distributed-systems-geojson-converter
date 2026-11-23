import csv
from datetime import datetime, timezone

import pytest

import resources
import sampling
import watch_ecs
import watch_queues

ARN = "arn:aws:ecs:us-east-1:123456789012:task/gc-anton-cluster/14e24591df164827aa4277dfa60b3fff"


# --- watch_queues -------------------------------------------------------------------------

class FakeSQS:
    def __init__(self, attrs):
        self.attrs = attrs

    def get_queue_attributes(self, QueueUrl, AttributeNames):
        assert set(AttributeNames) == {"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible"}
        return {"Attributes": self.attrs[QueueUrl]}


def test_queue_sample_has_one_row_per_queue_with_visible_and_in_flight():
    sqs = FakeSQS({"u1": {"ApproximateNumberOfMessages": "3", "ApproximateNumberOfMessagesNotVisible": "2"},
                   "u2": {"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "0"}})
    rows = watch_queues.sample(sqs, {"jobs": "u1", "jobs-dlq": "u2"}, ts="T")
    assert rows == [{"ts": "T", "queue": "jobs", "visible": 3, "in_flight": 2},
                    {"ts": "T", "queue": "jobs-dlq", "visible": 0, "in_flight": 0}]


def test_queue_args_parse_name_equals_url():
    assert watch_queues.parse_queues(["jobs=http://x/1", "jobs-dlq=http://x/2"]) == {
        "jobs": "http://x/1", "jobs-dlq": "http://x/2"}
    with pytest.raises(ValueError):
        watch_queues.parse_queues(["no-equals-sign"])


# --- watch_ecs ----------------------------------------------------------------------------

def test_ecs_task_row_uses_the_arn_workers_record_as_task_id():
    task = {"taskArn": ARN, "group": "service:gc-anton-worker", "lastStatus": "STOPPED",
            "startedAt": datetime(2026, 11, 21, 10, 0, 0, tzinfo=timezone.utc),
            "stoppedAt": datetime(2026, 11, 21, 10, 5, 0, 250000, tzinfo=timezone.utc),
            "stopCode": "UserInitiated", "stoppedReason": "chaos-kill"}
    assert watch_ecs.ecs_task_row(task) == {
        "task_id": ARN, "service": "gc-anton-worker", "last_status": "STOPPED",
        "started_at": "2026-11-21T10:00:00.000Z", "stopped_at": "2026-11-21T10:05:00.250Z",
        "stop_code": "UserInitiated", "stopped_reason": "chaos-kill"}


def test_running_ecs_task_has_blank_stop_columns():
    task = {"taskArn": ARN, "group": "service:gc-anton-api", "lastStatus": "PROVISIONING"}
    row = watch_ecs.ecs_task_row(task)
    assert row["started_at"] == row["stopped_at"] == row["stop_code"] == row["stopped_reason"] == ""
    assert row["service"] == "gc-anton-api"


def test_docker_task_row_uses_the_hostname_workers_record_as_task_id():
    # Locally a worker's task_id is its hostname: the first 12 characters of the container ID.
    inspect = {"Id": "0123456789abcdef" * 4, "Config": {"Labels": {"com.docker.compose.service": "worker"}},
               "State": {"Status": "exited", "StartedAt": "2026-11-21T10:00:00.123456789Z",
                         "FinishedAt": "2026-11-21T10:05:00.5Z", "ExitCode": 137}}
    assert watch_ecs.docker_task_row(inspect) == {
        "task_id": "0123456789ab", "service": "worker", "last_status": "exited",
        "started_at": "2026-11-21T10:00:00.123Z", "stopped_at": "2026-11-21T10:05:00.500Z",
        "stop_code": "exit 137", "stopped_reason": ""}


def test_running_docker_task_has_blank_stop_columns():
    inspect = {"Id": "f" * 64, "Config": {"Labels": {"com.docker.compose.service": "api"}},
               "State": {"Status": "running", "StartedAt": "2026-11-21T10:00:00Z",
                         "FinishedAt": "0001-01-01T00:00:00Z", "ExitCode": 0}}
    row = watch_ecs.docker_task_row(inspect)
    assert row["stopped_at"] == row["stop_code"] == ""


def test_tasks_file_keeps_every_task_seen_and_the_latest_state(tmp_path):
    out = tmp_path / "tasks.csv"
    seen = {}
    first = {"task_id": "a", "service": "worker", "last_status": "running", "started_at": "s",
             "stopped_at": "", "stop_code": "", "stopped_reason": ""}
    watch_ecs.record(seen, [first], out)
    # Task a has gone (a stopped task can drop out of ECS's listing) and b replaced it.
    watch_ecs.record(seen, [{**first, "task_id": "b"}], out)
    watch_ecs.record(seen, [{**first, "task_id": "b", "last_status": "exited", "stop_code": "exit 137"}], out)
    rows = list(csv.DictReader(out.open()))
    assert [r["task_id"] for r in rows] == ["a", "b"]
    assert rows[1]["stop_code"] == "exit 137"


# --- resources ----------------------------------------------------------------------------

def test_docker_stats_rows_convert_cpu_percent_and_memory_to_mb():
    names = {"0123456789ab": ("0123456789ab", "worker")}
    lines = ['{"ID":"0123456789ab","CPUPerc":"87.50%","MemUsage":"512MiB / 7.66GiB"}',
             '{"ID":"ffffffffffff","CPUPerc":"1.00%","MemUsage":"10MiB / 7.66GiB"}']
    assert resources.docker_stats_rows("T", lines, names) == [
        {"ts": "T", "task_id": "0123456789ab", "service": "worker", "cpu_pct": 87.5, "mem_mb": 512.0}]


@pytest.mark.parametrize("text,mb", [("1.5GiB", 1536.0), ("512MiB", 512.0), ("2048KiB", 2.0), ("1kB", 0.001), ("3MB", 3.0)])
def test_memory_units(text, mb):
    assert resources.to_mb(text) == pytest.approx(mb)


def test_insights_rows_turn_task_cpu_units_into_percent_of_reserved():
    # Container Insights task records report CpuUtilized and CpuReserved in CPU units.
    results = [[{"field": "@timestamp", "value": "2026-11-21 10:00:00.000"},
                {"field": "TaskId", "value": "14e24591df164827aa4277dfa60b3fff"},
                {"field": "ServiceName", "value": "gc-anton-worker"},
                {"field": "CpuUtilized", "value": "512"}, {"field": "CpuReserved", "value": "1024"},
                {"field": "MemoryUtilized", "value": "900"}]]
    rows = resources.insights_rows(results, cluster_arn_prefix="arn:aws:ecs:us-east-1:123456789012:task/gc-anton-cluster/")
    assert rows == [{"ts": "2026-11-21T10:00:00.000Z", "task_id": ARN, "service": "gc-anton-worker",
                     "cpu_pct": 50.0, "mem_mb": 900.0}]


# --- sampling -----------------------------------------------------------------------------

def test_append_rows_writes_the_header_once(tmp_path):
    out = tmp_path / "q.csv"
    sampling.append_rows(out, ["a", "b"], [{"a": 1, "b": 2}])
    sampling.append_rows(out, ["a", "b"], [{"a": 3, "b": 4}])
    assert out.read_text() == "a,b\n1,2\n3,4\n"


def test_iso_ms_is_utc_with_milliseconds():
    assert sampling.iso_ms(datetime(2026, 11, 21, 5, 0, 0, 123456, tzinfo=timezone.utc)) == "2026-11-21T05:00:00.123Z"
