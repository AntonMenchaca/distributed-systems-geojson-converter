"""Open-loop replay of a trace CSV with Locust (step H.3).

    locust -f locust/locustfile.py --headless -u 1 -r 1 --host http://localhost:8080 \
        --trace traces/mixed-s11.csv --run-id local-mixed-1

Run it with exactly one user (-u 1): TraceUser fires every row of the trace itself. Each row is
sent at its t_offset_s in its own greenlet, so a slow or stuck response never delays the rows after
it (open loop: arrivals don't slow down when the system does). A 429 is retried after its
Retry-After seconds (Phase 5's backlog admission), still without holding up other rows. When every
row has been answered, Locust stops and prints a summary line.

Request names in Locust's stats: "POST /jobs" (accepted), "POST /jobs 429" (asked to retry),
"POST /jobs error" (anything else, including 4xx rejections).
"""
from __future__ import annotations

import csv
import json
import time

import gevent
from locust import HttpUser, events, task

MAX_429_RETRIES = 20


@events.init_command_line_parser.add_listener
def _(parser):
    parser.add_argument("--trace", required=True, help="trace CSV from tools/gen_trace.py")
    parser.add_argument("--run-id", required=True, help="run_id for every job, e.g. local-mixed-1")


def load_rows(path: str) -> list[dict]:
    with open(path, newline="") as f:
        return list(csv.DictReader(f))


def job_body(row: dict, run_id: str) -> dict:
    """The same body tools/replay.py sends."""
    body = {"input": {"dataset_id": row["dataset_id"]}, "output_format": row["output_format"], "run_id": run_id}
    if row.get("chaos"):
        body["chaos"] = json.loads(row["chaos"])
    return body


def retry_after_s(headers, default: float = 1.0) -> float:
    try:
        return max(0.0, float(headers.get("Retry-After", default)))
    except (TypeError, ValueError):
        return default


class TraceUser(HttpUser):
    started = False  # one replay per Locust process, however many users are spawned

    def on_start(self):
        opts = self.environment.parsed_options
        if TraceUser.started:
            return
        TraceUser.started = True
        rows = load_rows(opts.trace)
        self.result = {"rows": len(rows), "accepted": 0, "rejected": 0, "retries_429": 0}
        t0 = time.monotonic()
        greenlets = [gevent.spawn_later(max(0.0, float(r["t_offset_s"]) - (time.monotonic() - t0)),
                                        self.fire, r, opts.run_id) for r in rows]
        gevent.spawn(self.finish, greenlets)

    def fire(self, row: dict, run_id: str) -> None:
        body = job_body(row, run_id)
        for _ in range(MAX_429_RETRIES + 1):
            with self.client.post("/jobs", json=body, name="POST /jobs", catch_response=True) as resp:
                if resp.status_code == 429:
                    resp.request_meta["name"] = "POST /jobs 429"
                    resp.success()  # an expected answer under backlog admission, not an error
                    self.result["retries_429"] += 1
                    wait_s = retry_after_s(resp.headers)
                elif 200 <= resp.status_code < 300:
                    self.result["accepted"] += 1
                    return
                else:
                    resp.request_meta["name"] = "POST /jobs error"
                    resp.failure(f"HTTP {resp.status_code}")
                    self.result["rejected"] += 1
                    return
            gevent.sleep(wait_s)
        self.result["rejected"] += 1

    def finish(self, greenlets) -> None:
        gevent.joinall(greenlets)
        print("trace replay:", json.dumps(self.result), flush=True)
        self.environment.runner.quit()

    @task
    def idle(self):
        gevent.sleep(1)  # the rows are fired by on_start's greenlets, not by this task
