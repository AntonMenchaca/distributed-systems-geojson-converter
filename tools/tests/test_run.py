import csv
from datetime import datetime, timedelta
from pathlib import Path

import pytest

import run

NOW = datetime(2026, 11, 21, 12, 0, 0)


# --- run names and folders ----------------------------------------------------------------

def test_aws_run_name_must_follow_the_convention_and_name_the_owner():
    assert run.name_problems("p1", "p1-base-anton-s11", owner="anton") == []
    assert run.name_problems("p1", "p1-base-ikwo-s11", owner="anton")       # someone else's name
    assert run.name_problems("p1", "p2-base-anton-s11", owner="anton")      # wrong phase
    assert run.name_problems("p1", "p1-base-anton", owner="anton")          # no seed
    assert run.name_problems("p1", "P1-Base-anton-s11", owner="anton")      # rejected by the API
    assert run.name_problems("local", "p1-base-anton-s11", owner="anton")   # local is not an AWS phase


def test_local_run_name_only_needs_the_local_prefix():
    assert run.name_problems("local", "local-smoke", owner=None) == []
    assert run.name_problems("local", "smoke", owner=None)


def test_results_folder_layout():
    assert run.results_dir(Path("results"), "local", "local-smoke") == Path("results/local-smoke")
    assert run.results_dir(Path("results"), "p1", "p1-base-anton-s11") == Path("results/p1/p1-base-anton-s11")


def test_seed_comes_from_the_run_name():
    assert run.seed_of("p1-base-anton-s22") == 22
    assert run.seed_of("local-smoke") is None


# --- session ------------------------------------------------------------------------------

def test_session_end_is_four_hours_after_the_credentials_were_written():
    assert run.session_end(credentials_mtime=NOW - timedelta(hours=3), override=None, now=NOW) == NOW + timedelta(hours=1)


def test_session_end_override_is_a_local_clock_time_today():
    assert run.session_end(credentials_mtime=None, override="13:30", now=NOW) == NOW.replace(hour=13, minute=30)


def test_less_than_an_hour_left_refuses():
    assert run.session_problems(NOW + timedelta(minutes=59), now=NOW)
    assert run.session_problems(NOW + timedelta(minutes=61), now=NOW) == []
    assert run.session_problems(None, now=NOW)  # no credentials file and no --session-ends


# --- knobs --------------------------------------------------------------------------------

def test_tfvars_parse_strings_numbers_and_comments(tmp_path):
    f = tmp_path / "p1.tfvars"
    f.write_text('# baseline\nprefix = "gc-anton"\nlease_mode="fixed-30s"  # inline\nworker_count = 4\n\n')
    assert run.parse_tfvars(f) == {"prefix": "gc-anton", "lease_mode": "fixed-30s", "worker_count": "4"}


KNOBS = {"LEASE_MODE": "fixed-30s", "DLQ_MAX_RECEIVES": "0", "TASK_SHAPE": "4x1-4-20gb"}


def test_tfvars_matching_the_deployed_knobs_pass():
    tfvars = {"prefix": "gc-anton", "lease_mode": "fixed-30s", "dlq_max_receives": "0",
              "worker_count": "4", "worker_cpu": "1024", "worker_memory": "4096", "worker_ephemeral_gb": "20"}
    assert run.knob_problems(tfvars, KNOBS, deployed_prefix="gc-anton") == []


def test_a_knob_that_differs_from_the_deployment_refuses():
    problems = run.knob_problems({"lease_mode": "heartbeat"}, KNOBS, deployed_prefix="gc-anton")
    assert len(problems) == 1 and "LEASE_MODE" in problems[0]


def test_task_shape_variables_are_checked_against_the_deployed_shape():
    assert run.knob_problems({"worker_cpu": "512"}, KNOBS, deployed_prefix="gc-anton")
    assert run.knob_problems({"worker_count": "8"}, KNOBS, deployed_prefix="gc-anton")
    assert run.knob_problems({"worker_memory": "4096"}, KNOBS, deployed_prefix="gc-anton") == []


def test_a_tfvars_prefix_for_another_stack_refuses():
    assert run.knob_problems({"prefix": "gc-ikwo"}, KNOBS, deployed_prefix="gc-anton")


# --- stack state --------------------------------------------------------------------------

def test_queues_must_be_empty():
    rows = [{"queue": "jobs", "visible": 0, "in_flight": 0}, {"queue": "jobs-dlq", "visible": 2, "in_flight": 0}]
    problems = run.queue_problems(rows)
    assert len(problems) == 1 and "jobs-dlq" in problems[0]


def test_services_must_be_steady_and_autoscalers_at_their_minimum():
    services = [{"serviceName": "w", "runningCount": 3, "desiredCount": 4},
                {"serviceName": "a", "runningCount": 1, "desiredCount": 1}]
    assert len(run.service_problems(services, scalable_targets=[])) == 1
    steady = [{"serviceName": "w", "runningCount": 4, "desiredCount": 4}]
    target = [{"ResourceId": "service/c/w", "MinCapacity": 2}]
    assert run.service_problems(steady, scalable_targets=target)            # 4 running, minimum 2
    assert run.service_problems(steady, [{"ResourceId": "service/c/w", "MinCapacity": 4}]) == []


# --- cost ---------------------------------------------------------------------------------

def test_estimate_prices_vcpu_and_memory_hours_plus_the_load_balancer():
    usd = run.estimate_usd(vcpu=4.5, gb=17, hours=0.5)
    assert usd == pytest.approx(0.5 * (4.5 * run.FARGATE_VCPU_HR + 17 * run.FARGATE_GB_HR + run.ALB_HR))


def test_budget_counts_every_earlier_run(tmp_path):
    costs = tmp_path / "costs.csv"
    with costs.open("w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["run_id", "owner", "account_prefix", "started_at", "ended_at", "task_hours", "vcpu_hours", "gb_hours", "usd"])
        w.writerow(["a", "anton", "gc-anton", "", "", "", "", "", "16.50"])
    assert run.spent_usd(costs) == pytest.approx(16.5)
    assert run.budget_problems(spent=16.5, estimate=0.4) == []
    assert run.budget_problems(spent=16.5, estimate=0.6)
    assert run.spent_usd(tmp_path / "missing.csv") == 0.0


# --- trace --------------------------------------------------------------------------------

def test_trace_datasets_must_be_in_the_catalog(tmp_path):
    trace = tmp_path / "t.csv"
    trace.write_text("t_offset_s,dataset_id,output_format,chaos\n0.0,dc,gpkg,\n12.5,ca-1,shp,\n")
    rows = run.load_trace(trace)
    assert run.trace_duration_s(rows) == 12.5
    problems = run.trace_problems(rows, catalog={"dc": {}})
    assert len(problems) == 1 and "ca-1" in problems[0]


# --- events -------------------------------------------------------------------------------

def test_events_file_uses_the_chaos_killer_columns(tmp_path):
    # The task killer appends kill rows to the same file: timestamp,event,task_id.
    events = tmp_path / "events.csv"
    run.event(events, "start")
    run.event(events, "end")
    rows = list(csv.DictReader(events.open()))
    assert [r["event"] for r in rows] == ["start", "end"]
    assert set(rows[0]) == {"timestamp", "event", "task_id"}


def test_gdal_version_is_the_number_only():
    assert run.gdal_version_of('GDAL 3.13.3 "Iowa City", released 2026/09/01') == "3.13.3"
    assert run.gdal_version_of("") == "unknown"
