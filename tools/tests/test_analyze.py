import csv
import json

import pytest

import analyze
import collect


def job(n, size_class="small", status="SUCCEEDED", turnaround=10.0, dataset="dc", finished=1,
        input_bytes=2**30, submitted="2026-11-22T17:00:00.000Z", committed="2026-11-22T18:00:00.000Z"):
    row = {c: "" for c in collect.COLUMNS}
    row.update(job_id=f"j{n}", status=status, size_class=size_class, input_key=f"datasets/{dataset}.geojson",
               input_bytes=str(input_bytes), finished_attempts=str(finished), submitted_at=submitted,
               committed_at=committed if status == "SUCCEEDED" else "",
               turnaround_s=str(turnaround) if status == "SUCCEEDED" else "")
    return row


def run_folder(tmp_path, name, jobs, prefix="gc-anton", seed=11, failures=()):
    d = tmp_path / name
    d.mkdir()
    (d / "config.json").write_text(json.dumps({"run_id": name, "account_prefix": prefix, "seed": seed}))
    with (d / "jobs.csv").open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=collect.COLUMNS)
        w.writeheader()
        w.writerows(jobs)
    (d / "verify.json").write_text(json.dumps({"passed": not failures, "failures": list(failures)}))
    return d


def test_percentiles_from_job_records():
    values = [float(v) for v in range(1, 11)]   # 1..10
    assert analyze.percentile(values, 50) == pytest.approx(5.5)
    assert analyze.percentile(values, 95) == pytest.approx(9.55)
    assert analyze.percentile([], 95) is None


def test_p95_comes_from_turnaround_per_size_class(tmp_path):
    jobs = [job(i, turnaround=float(i)) for i in range(1, 11)] + [job(99, "large", turnaround=500.0)]
    s = analyze.summarize(run_folder(tmp_path, "r", jobs), costs={})
    assert s["small p95 (s)"] == pytest.approx(9.55)
    assert s["large p95 (s)"] == pytest.approx(500.0)


def test_p99_omitted_under_100_jobs(tmp_path):
    few = analyze.summarize(run_folder(tmp_path, "few", [job(i) for i in range(99)]), costs={})
    many = analyze.summarize(run_folder(tmp_path, "many", [job(i) for i in range(100)]), costs={})
    assert few["small p99 (s)"] is None
    assert many["small p99 (s)"] == pytest.approx(10.0)


def test_counts_for_the_master_table(tmp_path):
    jobs = [job(1, finished=2),                                  # a duplicate
            job(2, status="FAILED", dataset="p1"),               # poison: expected to fail
            job(3, status="FAILED", dataset="dc"),               # a healthy job that failed
            job(4, status="DEAD_LETTERED", dataset="ca-1k")]     # also healthy
    s = analyze.summarize(run_folder(tmp_path, "r", jobs, failures=[{"job_id": "j1", "check": "feature_count"}]),
                          costs={})
    assert (s["jobs"], s["succeeded"], s["failed"]) == (4, 1, 3)
    assert s["duplicates"] == 1
    assert s["healthy jobs failed"] == 2
    assert s["incorrect outputs"] == 1


def test_throughput_and_cost_per_gb(tmp_path):
    # Two 1 GiB jobs converted in the hour between first submit and last commit, for $0.50.
    jobs = [job(1), job(2)]
    s = analyze.summarize(run_folder(tmp_path, "r", jobs), costs={"r": {"usd": "0.50"}})
    assert s["GB/h"] == pytest.approx(2 * 2**30 / 1e9)
    assert s["$/GB"] == pytest.approx(0.50 / (2 * 2**30 / 1e9))
    assert analyze.summarize(run_folder(tmp_path, "local", jobs), costs={})["$/GB"] is None


def test_markdown_row_has_one_cell_per_column(tmp_path):
    s = analyze.summarize(run_folder(tmp_path, "r", [job(1)]), costs={})
    header, rule, row = analyze.markdown([s]).splitlines()
    assert header.count("|") == rule.count("|") == row.count("|") == len(analyze.COLUMNS) + 1
    assert "| — |" in row   # p99 under 100 jobs


def test_compare_rejects_different_accounts(tmp_path):
    before = run_folder(tmp_path, "before", [job(1)], prefix="gc-anton")
    after = run_folder(tmp_path, "after", [job(1)], prefix="gc-ikwo")
    assert analyze.compare_problems(before, after)
    same = run_folder(tmp_path, "same", [job(1)], prefix="gc-anton")
    assert analyze.compare_problems(before, same) == []


def test_compare_rejects_different_seeds(tmp_path):
    before = run_folder(tmp_path, "before", [job(1)], seed=11)
    after = run_folder(tmp_path, "after", [job(1)], seed=22)
    assert analyze.compare_problems(before, after)


def test_compare_exits_non_zero_on_a_mismatch(tmp_path):
    before = run_folder(tmp_path, "before", [job(1)], prefix="gc-anton")
    after = run_folder(tmp_path, "after", [job(1)], prefix="gc-ikwo")
    assert analyze.main(["--compare", str(before), str(after), "--no-figures"]) == 2
