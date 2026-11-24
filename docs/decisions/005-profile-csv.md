got # 005: Profile rows live in S3 until collection

Date: 2026-11-23 · Decided by: Samuel

## Context

`PROFILE=1` measures one conversion at a time (RSS every 250 ms, disk sampled for the job directory, ogrinfo timed apart from the conversion). The Phase 2 table needs those measurements as `profile.csv` in the run's results folder. The worker container has no results directory mounted, and `terraform destroy` deletes the bucket. The other run files that start inside the account (`jobs.csv`, `attempts.csv`) are written later by `tools/collect.py`, which `scripts/run.sh` calls before anyone destroys the stack.

## Decision

- When `PROFILE=1`, the worker uploads one object per job: `profiles/<run_id>/<job_id>.csv`. The object is the Phase 2 header plus that job's row. A later attempt of the same job replaces the object.
- There is no path variable for the file. `PROFILE=1` is the only switch.
- `tools/collect.py` lists that prefix and writes `results/.../profile.csv` with the header once. `scripts/run.sh` passes `--profile-out`. A run with no profile objects does not get the file.
- `peak_rss_mb` in that file is the larger of the /proc RSS sample peak and the child's rusage peak, rounded up to whole MiB. Jobs under the 250 ms sample interval often never land a /proc reading; rusage still has a peak so `fit.py` does not fit zeros.
- `peak_disk_mb` is the whole job directory, same rounding. Disk uses a 1 s ticker plus a final sample when packaging ends (or on earlier failure). Short jobs usually only hit that final sample; it is close to the true peak for conversion then zip, and that is what we record.
- `output_bytes` is the uploaded file (the zip for shp/fgdb). `max_shp_component_bytes` is the larger of the `.shp` and `.dbf` sizes before zipping, empty for other formats: the Shapefile 2 GB limit (`SHP_LIMIT_MODE=split-2gb`) applies to those unzipped files.
- `output_features` is the ogrinfo count. `error` is the failure code, empty on success. A measurement whose stage did not run is an empty cell. A failed upload of the profile object is logged and does not change the job's outcome.
- `attempts.csv` `peak_rss_mb` stays the child's rusage figure on the job record. That column is still rusage alone; profile.csv's `peak_rss_mb` is the max described above.

## Consequences

- Copy the file out with the rest of the run: `scripts/run.sh` (or `tools/collect.py --profile-out`) before `terraform destroy`. After destroy the objects are gone. `fit.py` reads the results file, not the bucket.
- LocalStack is the same key in bucket `gc-local`. Collection uses `AWS_ENDPOINT_URL`, so one command covers a Compose run and an AWS run.
- One object per job, because S3 replaces a whole object and a Compose run can have more than one worker. The profiling service is still one task, so a phase-2 run has one writer either way.
- A job that fails before it has a `job_id` writes no row. A claimed job that fails writes a row with its code.
