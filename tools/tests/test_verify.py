import verify

CATALOG = {"dc": {"features": 1000, "s3_key": "datasets/dc.geojson"}}


def job(**kw):
    base = {"job_id": "j1", "run_id": "local-x", "status": "SUCCEEDED", "input_key": "datasets/dc.geojson",
            "output_format": "gpkg", "output_key": "outputs/local-x/j1/local-x_dc_j1.gpkg", "feature_count": 1000}
    return {**base, **kw}


def test_output_not_under_its_run_and_job_fails():
    # outputs/{run_id}/{job_id}/ is how a file in S3 is traced back to its run and job.
    report = verify.verify([job(output_key="outputs/j1/out.gpkg")], CATALOG, count_features=lambda j: 1000)
    assert report["passed"] is False
    assert report["failures"] == [{"job_id": "j1", "check": "output_key", "output_key": "outputs/j1/out.gpkg"}]


def test_feature_count_mismatch_fails():
    report = verify.verify([job()], CATALOG, count_features=lambda j: 999)
    assert report["passed"] is False
    assert report["failures"] == [{"job_id": "j1", "check": "feature_count", "expected": 1000, "actual": 999}]


def test_matching_counts_pass():
    report = verify.verify([job()], CATALOG, count_features=lambda j: 1000)
    assert report["passed"] is True
    assert report["checked"] == 1 and report["failures"] == []


def test_unopenable_output_fails():
    def boom(j):
        raise verify.OutputError("ogrinfo: unable to open")
    report = verify.verify([job()], CATALOG, count_features=boom)
    assert report["passed"] is False
    assert report["failures"][0]["check"] == "opens"


def test_only_succeeded_jobs_are_opened_and_statuses_are_counted():
    opened = []
    report = verify.verify([job(), job(job_id="j2", status="FAILED")], CATALOG,
                           count_features=lambda j: opened.append(j["job_id"]) or 1000)
    assert opened == ["j1"]
    assert report["status_counts"] == {"SUCCEEDED": 1, "FAILED": 1}


def test_unfinished_run_does_not_pass():
    # A run with jobs still QUEUED/RUNNING must not look fine in the summary.
    report = verify.verify([job(), job(job_id="j2", status="RUNNING")], CATALOG, count_features=lambda j: 1000)
    assert report["passed"] is False
    assert report["complete"] is False
    assert report["unfinished"] == ["j2"]


def test_nothing_checked_does_not_pass():
    report = verify.verify([job(status="FAILED")], CATALOG, count_features=lambda j: 1000)
    assert report["checked"] == 0
    assert report["passed"] is False


def test_verify_flags_status_regression():
    # Succeeded, then another attempt. status_counts still say SUCCEEDED and the feature
    # count matches, which is the summary that would otherwise look fine.
    later = job(attempts=[
        {"epoch": 1, "task_id": "task-a", "outcome": "SUCCEEDED"},
        {"epoch": 2, "task_id": "task-b", "outcome": "FAILED"},
    ])
    report = verify.verify([later], CATALOG, count_features=lambda j: 1000)
    assert report["passed"] is False
    assert report["checked"] == 1
    assert report["failures"] == [{"job_id": "j1", "check": "status_regression", "status": "SUCCEEDED"}]

    # The status moved off a success (QUEUED after an attempt already succeeded).
    backwards = job(job_id="j2", status="QUEUED",
                    attempts=[{"epoch": 1, "task_id": "task-a", "outcome": "SUCCEEDED"}])
    report = verify.verify([backwards], CATALOG, count_features=lambda j: 1000)
    assert report["checked"] == 0
    assert report["failures"] == [{"job_id": "j2", "check": "status_regression", "status": "QUEUED"}]


def test_verify_flags_mixed_attempt_shapefile():
    # A shapefile zip is one object from one attempt. A retry that finishes is legitimate.
    # The failure is a stale upload: the epoch on the object is not the SUCCEEDED attempt.
    # Tests pass the object epoch in. The verify command reads it from the object's user metadata.
    attempts = [
        {"epoch": 1, "task_id": "task-a", "outcome": "LOST"},
        {"epoch": 2, "task_id": "task-b", "outcome": "SUCCEEDED"},
    ]
    shp = job(output_format="shp", output_key="outputs/local-x/j1/local-x_dc_j1.shp.zip", attempts=attempts)
    report = verify.verify([shp], CATALOG, count_features=lambda j: 1000, object_epoch=lambda j: "1")
    assert report["passed"] is False
    assert report["failures"] == [{"job_id": "j1", "check": "mixed_attempt", "output_format": "shp"}]

    # The same job passes when the object epoch is the attempt that succeeded, even though
    # an earlier attempt exists. Missing epoch metadata is not a failure.
    assert verify.verify([shp], CATALOG, count_features=lambda j: 1000, object_epoch=lambda j: "2")["passed"] is True
    assert verify.verify([shp], CATALOG, count_features=lambda j: 1000, object_epoch=lambda j: None)["passed"] is True

    # A GeoPackage with two attempts is not this check, and a shapefile from one attempt is clean.
    gpkg = job(attempts=attempts)
    one = job(job_id="j2", output_format="shp", output_key="outputs/local-x/j2/local-x_dc_j2.shp.zip",
              attempts=[{"epoch": 1, "task_id": "task-a", "outcome": "SUCCEEDED"}])
    assert verify.verify([gpkg], CATALOG, count_features=lambda j: 1000, object_epoch=lambda j: "1")["passed"] is True
    assert verify.verify([one], CATALOG, count_features=lambda j: 1000, object_epoch=lambda j: "1")["passed"] is True


def test_verify_nonzero_exit_on_failure(tmp_path):
    # A failed check has to fail the process, not only the JSON a summary might skip.
    bad = tmp_path / "verify.json"
    report = verify.verify([job()], CATALOG, count_features=lambda j: 999)
    assert verify.finish(bad, "local-x", report) == 1
    assert '"passed": false' in bad.read_text()
    ok = verify.verify([job()], CATALOG, count_features=lambda j: 1000)
    assert verify.finish(tmp_path / "ok.json", "local-x", ok) == 0
