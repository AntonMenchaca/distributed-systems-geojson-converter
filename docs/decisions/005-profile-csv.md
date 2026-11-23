# 005: Profile rows live in S3 until collection

Date: 2026-11-23 · Decided by: Samuel

## Context

`PROFILE=1` measures one conversion at a time (RSS every 250 ms, disk every 1 s, ogrinfo timed apart from the conversion). The Phase 2 table needs those measurements as `profile.csv` in the run's results folder. The worker container has no results directory mounted, and `terraform destroy` deletes the bucket. The other run files that start inside the account (`jobs.csv`, `attempts.csv`) are written later by `tools/collect.py`, which `scripts/run.sh` calls before anyone destroys the stack.

## Decision

- When `PROFILE=1`, the worker uploads one object per job: `profiles/<run_id>/<job_id>.csv`. The object is the Phase 2 header plus that job's row. A later attempt of the same job replaces the object.
- There is no path variable for the file. `PROFILE=1` is the only switch.
- `tools/collect.py` lists that prefix and writes `results/.../profile.csv` with the header once. `scripts/run.sh` passes `--profile-out`. A run with no profile objects does not get the file.
- `peak_rss_mb` in that file is the /proc RSS peak, rounded up to whole MiB. `peak_disk_mb` is the whole job directory, same rounding. `output_features` is the ogrinfo count. `error` is the failure code, empty on success. A measurement whose stage did not run is an empty cell. A failed upload of the profile object is logged and does not change the job's outcome.
- `attempts.csv` `peak_rss_mb` stays the child's rusage figure on the job record. The two columns are different measurements.

## Consequences

- Copy the file out with the rest of the run: `scripts/run.sh` (or `tools/collect.py --profile-out`) before `terraform destroy`. After destroy the objects are gone. `fit.py` reads the results file, not the bucket.
- LocalStack is the same key in bucket `gc-local`. Collection uses `AWS_ENDPOINT_URL`, so one command covers a Compose run and an AWS run.
- One object per job, because S3 replaces a whole object and a Compose run can have more than one worker. The profiling service is still one task, so a phase-2 run has one writer either way.
- A job that fails before it has a `job_id` writes no row. A claimed job that fails writes a row with its code.
