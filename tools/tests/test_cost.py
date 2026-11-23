import csv
import json
from datetime import datetime, timezone

import pytest

import cost

UTC = timezone.utc


def ts(hh, mm, ss=0):
    return datetime(2026, 11, 22, hh, mm, ss, tzinfo=UTC)


# --- price ----------------------------------------------------------------------------------

def test_cost_formula():
    # 1 task x 1 vCPU x 4 GB x 1 h, without the load balancer.
    assert cost.usd(vcpu_h=1, gb_h=4, alb_h=0) == pytest.approx(0.05826)


def test_load_balancer_hours_are_priced_too():
    assert cost.usd(vcpu_h=0, gb_h=0, alb_h=2) == pytest.approx(0.045)


# --- task hours -----------------------------------------------------------------------------

SIZES = {"gc-anton-api-task": (0.5, 1.0), "gc-anton-worker-task": (1.0, 4.0)}


def task(service, started, stopped=""):
    return {"task_id": f"arn:{service}:{started}", "service": service,
            "started_at": started, "stopped_at": stopped}


def test_task_intervals_are_clipped_to_the_window():
    tasks = [task("gc-anton-worker", "2026-11-22T16:00:00Z"),                          # before the window
             task("gc-anton-api", "2026-11-22T17:30:00Z", "2026-11-22T18:30:00Z")]    # ends after it
    hours = cost.task_hours(tasks, SIZES, start=ts(17, 0), end=ts(18, 0))
    assert hours["task_h"] == pytest.approx(1.5)
    assert hours["vcpu_h"] == pytest.approx(1.0 + 0.25)
    assert hours["gb_h"] == pytest.approx(4.0 + 0.5)


def test_a_task_outside_the_window_costs_nothing():
    tasks = [task("gc-anton-worker", "2026-11-22T15:00:00Z", "2026-11-22T16:00:00Z")]
    assert cost.task_hours(tasks, SIZES, start=ts(17, 0), end=ts(18, 0))["task_h"] == 0


def test_a_service_without_a_known_size_is_an_error():
    with pytest.raises(KeyError):
        cost.task_hours([task("gc-anton-worker-large", "2026-11-22T17:00:00Z")], SIZES, ts(17, 0), ts(18, 0))


def test_sizes_come_from_task_definition_families():
    described = [{"family": "gc-anton-worker-task", "cpu": "1024", "memory": "4096"},
                 {"family": "gc-anton-api-task", "cpu": "512", "memory": "1024"}]
    assert cost.sizes_from(described) == SIZES


# --- the cost window ------------------------------------------------------------------------

COSTS_HEADER = ["run_id", "owner", "account_prefix", "started_at", "ended_at", "task_hours",
                "vcpu_hours", "gb_hours", "usd"]


def costs_file(tmp_path, rows):
    path = tmp_path / "costs.csv"
    with path.open("w", newline="") as f:
        w = csv.writer(f)
        w.writerow(COSTS_HEADER)
        w.writerows(rows)
    return path


def test_first_run_of_a_session_starts_at_the_stacks_first_task(tmp_path):
    costs = costs_file(tmp_path, [["old", "anton", "gc-anton", "", "2026-11-20T10:00:00Z", "", "", "", "0.10"]])
    tasks = [task("gc-anton-worker", "2026-11-22T16:40:00Z"), task("gc-anton-api", "2026-11-22T16:48:00Z")]
    assert cost.window_start(cost.load_costs(costs), "gc-anton", tasks, "new", end=ts(18, 0)) == ts(16, 40)


def test_a_later_run_starts_where_the_previous_row_for_the_prefix_ended(tmp_path):
    costs = costs_file(tmp_path, [["a", "anton", "gc-anton", "", "2026-11-22T17:10:00Z", "", "", "", "0.05"],
                                  ["b", "ikwo", "gc-ikwo", "", "2026-11-22T17:50:00Z", "", "", "", "0.05"]])
    tasks = [task("gc-anton-worker", "2026-11-22T16:40:00Z")]
    assert cost.window_start(cost.load_costs(costs), "gc-anton", tasks, "new", end=ts(18, 0)) == ts(17, 10)


def test_run_end_is_the_last_end_event(tmp_path):
    events = tmp_path / "events.csv"
    events.write_text("timestamp,event,task_id\n2026-11-22T17:00:00.000Z,start,\n"
                      "2026-11-22T17:20:00.000Z,end,\n")
    assert cost.run_end(events) == ts(17, 20)


# --- rows -----------------------------------------------------------------------------------

def run_dir(tmp_path, phase="p1"):
    d = tmp_path / "p1-base-anton-s11"
    d.mkdir()
    (d / "config.json").write_text(json.dumps({"run_id": d.name, "phase": phase, "owner": "anton",
                                               "account_prefix": "gc-anton", "task_defs": {}}))
    (d / "events.csv").write_text("timestamp,event,task_id\n2026-11-22T18:00:00.000Z,end,\n")
    with (d / "tasks.csv").open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["task_id", "service", "last_status", "started_at", "stopped_at",
                                          "stop_code", "stopped_reason"])
        w.writeheader()
        w.writerow({"task_id": "w1", "service": "gc-anton-worker", "started_at": "2026-11-22T17:00:00Z"})
    return d


def test_row_for_a_run(tmp_path):
    costs = costs_file(tmp_path, [])
    row = cost.cost_row(run_dir(tmp_path), cost.load_costs(costs), SIZES)
    assert row == {"run_id": "p1-base-anton-s11", "owner": "anton", "account_prefix": "gc-anton",
                   "started_at": "2026-11-22T17:00:00Z", "ended_at": "2026-11-22T18:00:00Z",
                   "task_hours": "1.00", "vcpu_hours": "1.00", "gb_hours": "4.00",
                   "usd": f"{0.05826 + 0.0225:.2f}"}


def test_rows_ending_after_the_run_dont_move_its_start(tmp_path):
    # Re-costing a run already in costs.csv (or one a later run follows) must not start its
    # window at its own end, or at a later run's.
    costs = costs_file(tmp_path, [["p1-base-anton-s11", "anton", "gc-anton", "", "2026-11-22T18:00:00Z", "", "", "", "0.08"],
                                  ["later", "anton", "gc-anton", "", "2026-11-22T19:00:00Z", "", "", "", "0.05"]])
    row = cost.cost_row(run_dir(tmp_path), cost.load_costs(costs), SIZES)
    assert row["started_at"] == "2026-11-22T17:00:00Z"
    assert row["task_hours"] == "1.00"


def test_a_run_already_in_costs_csv_is_not_added_twice(tmp_path):
    costs = costs_file(tmp_path, [])
    row = cost.cost_row(run_dir(tmp_path), cost.load_costs(costs), SIZES)
    assert cost.append_row(costs, row) is True
    assert cost.append_row(costs, row) is False
    assert len(cost.load_costs(costs)) == 1


def test_local_runs_have_no_cost(tmp_path):
    (tmp_path / "local").mkdir()
    (tmp_path / "aws").mkdir()
    assert cost.is_local(run_dir(tmp_path / "local", phase="local"))
    assert not cost.is_local(run_dir(tmp_path / "aws", phase="p1"))


# --- open stacks ----------------------------------------------------------------------------

class FakeECS:
    def __init__(self, clusters, services):
        self.clusters, self.services = clusters, services

    def list_clusters(self):
        return {"clusterArns": [f"arn:aws:ecs:us-east-1:1:cluster/{c}" for c in self.clusters]}

    def list_services(self, cluster):
        name = cluster.rsplit("/", 1)[-1]
        return {"serviceArns": [f"arn:svc/{s['serviceName']}" for s in self.services.get(name, [])]}

    def describe_services(self, cluster, services):
        name = cluster.rsplit("/", 1)[-1]
        return {"services": self.services.get(name, [])}


class FakeELB:
    def __init__(self, names):
        self.names = names

    def describe_load_balancers(self):
        return {"LoadBalancers": [{"LoadBalancerName": n} for n in self.names]}


def test_open_stacks_lists_running_services():
    ecs = FakeECS(["gc-anton-cluster", "gc-ikwo-cluster", "default"], {
        "gc-anton-cluster": [{"serviceName": "gc-anton-worker", "desiredCount": 4, "runningCount": 4},
                             {"serviceName": "gc-anton-api", "desiredCount": 0, "runningCount": 0}],
        "gc-ikwo-cluster": [{"serviceName": "gc-ikwo-worker", "desiredCount": 1, "runningCount": 1}],
    })
    elb = FakeELB(["gc-anton-alb", "other-alb"])
    assert cost.open_stacks(ecs, elb, "gc-anton") == ["ECS service gc-anton-worker (4 running, 4 desired)",
                                                      "load balancer gc-anton-alb"]


def test_nothing_open_after_destroy():
    assert cost.open_stacks(FakeECS([], {}), FakeELB([]), "gc-anton") == []
