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

Uploads (step 5.1, for Phase 5a): --upload-mb 1,20,100,500 also runs UploadUser beside the trace.
Each round uploads one file of each size (POST /uploads, then a PUT to its put_url: the API in the
proxy modes, S3 directly in presigned mode), --upload-every-s apart, for --upload-rounds rounds.
Stats names: "POST /uploads" and "PUT upload <n> MB". The file is n MB of zeros: Phase 5a measures
the upload path, not conversion. UploadUser is not a separate Locust user class on purpose: with
-u 1 Locust would pick one class by weight, and could skip the trace.
"""
from __future__ import annotations

import csv
import json
import time

import gevent
from locust import HttpUser, events, task
from locust.stats import CSV_STATS_INTERVAL_SEC

MAX_429_RETRIES = 20


@events.init_command_line_parser.add_listener
def _(parser):
    parser.add_argument("--trace", required=True, help="trace CSV from tools/gen_trace.py")
    parser.add_argument("--run-id", required=True, help="run_id for every job, e.g. local-mixed-1")
    parser.add_argument("--upload-mb", default="", help="upload sizes in MB beside the trace, e.g. 1,20,100,500")
    parser.add_argument("--upload-every-s", type=float, default=30.0, help="seconds between uploads")
    parser.add_argument("--upload-rounds", type=int, default=1, help="how many times to upload each size")


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


class ZeroFile:
    """n bytes of zeros, read in blocks: a large body that is never held in memory. len() and
    tell() let requests send a Content-Length (S3 refuses chunked PUTs to presigned URLs)."""

    def __init__(self, n: int):
        self.n, self.pos = n, 0

    def __len__(self) -> int:
        return self.n - self.pos

    def tell(self) -> int:
        return 0

    def read(self, size: int = -1) -> bytes:
        left = self.n - self.pos
        size = left if size is None or size < 0 else min(size, left)
        self.pos += size
        return bytes(size)


class UploadUser:
    """Uploads files through POST /uploads and its put_url, using the TraceUser's HTTP client."""

    def __init__(self, client, result: dict):
        self.client, self.result = client, result
        result.update({"uploads_ok": 0, "uploads_failed": 0})

    def upload(self, mb: int) -> None:
        size = mb * 1_000_000
        with self.client.post("/uploads", json={"bytes": size}, name="POST /uploads", catch_response=True) as resp:
            if resp.status_code != 201:
                resp.failure(f"HTTP {resp.status_code}")
                self.result["uploads_failed"] += 1
                return
            put_url = resp.json()["put_url"]
        with self.client.put(put_url, data=ZeroFile(size), headers={"Content-Type": "application/octet-stream"},
                             name=f"PUT upload {mb} MB", catch_response=True) as resp:
            if 200 <= resp.status_code < 300:
                self.result["uploads_ok"] += 1
            else:
                resp.failure(f"HTTP {resp.status_code}")
                self.result["uploads_failed"] += 1

    def run(self, sizes: list[int], every_s: float, rounds: int) -> None:
        for _ in range(rounds):
            for mb in sizes:
                self.upload(mb)
                gevent.sleep(every_s)


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
        if opts.upload_mb:
            sizes = [int(x) for x in opts.upload_mb.split(",")]
            uploader = UploadUser(self.client, self.result)
            greenlets.append(gevent.spawn(uploader.run, sizes, opts.upload_every_s, opts.upload_rounds))
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
        # Locust's --csv files are rewritten once per CSV_STATS_INTERVAL_SEC and not again at
        # shutdown, so quitting right away drops the requests answered in the last interval.
        gevent.sleep(2 * CSV_STATS_INTERVAL_SEC)
        self.environment.runner.quit()

    @task
    def idle(self):
        gevent.sleep(1)  # the rows are fired by on_start's greenlets, not by this task
