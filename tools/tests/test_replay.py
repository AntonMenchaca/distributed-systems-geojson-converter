import replay

ROWS = [
    {"t_offset_s": "0.000", "dataset_id": "dc", "output_format": "gpkg", "chaos": ""},
    {"t_offset_s": "1.500", "dataset_id": "pts-1k", "output_format": "shp", "chaos": '{"crash_at":0.5}'},
]


class FakeClock:
    def __init__(self):
        self.t = 0.0

    def now(self):
        return self.t

    def sleep(self, s):
        self.t += s


def test_submit_posts_each_row_at_its_offset_with_run_id():
    clock, sent = FakeClock(), []

    def post(body):
        sent.append((clock.now(), body))
        return {"job_id": f"j{len(sent)}"}

    ids = replay.submit(ROWS, "local-x", post, clock.now, clock.sleep)
    assert ids == ["j1", "j2"]
    assert [round(t, 3) for t, _ in sent] == [0.0, 1.5]
    assert sent[0][1] == {"input": {"dataset_id": "dc"}, "output_format": "gpkg", "run_id": "local-x"}
    assert sent[1][1]["chaos"] == {"crash_at": 0.5}


def test_wait_returns_when_every_job_is_terminal():
    clock = FakeClock()
    polls = {"j1": iter(["QUEUED", "RUNNING", "SUCCEEDED"]), "j2": iter(["FAILED"])}
    statuses = replay.wait(["j1", "j2"], lambda jid: next(polls[jid]), clock.now, clock.sleep, timeout_s=60)
    assert statuses == {"j1": "SUCCEEDED", "j2": "FAILED"}
