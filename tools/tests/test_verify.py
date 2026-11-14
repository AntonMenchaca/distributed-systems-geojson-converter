import verify

CATALOG = {"dc": {"features": 1000, "s3_key": "datasets/dc.geojson"}}


def job(**kw):
    base = {"job_id": "j1", "status": "SUCCEEDED", "input_key": "datasets/dc.geojson",
            "output_format": "gpkg", "output_key": "outputs/j1/out.gpkg", "feature_count": 1000}
    return {**base, **kw}


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
