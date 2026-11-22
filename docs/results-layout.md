# Results layout

Every run writes one folder, `results/<phase>/<run>/`. A results folder is **never edited after its run**; a rerun gets a new run name.

- `<phase>`: `local`, `p1`, `p2`, ... `p9` (local Compose runs go under `results/local-*`).
- `<run>`: the run name. It is the `run_id` on every job record and part of every output's S3 key and file name, so it uses only lowercase letters, digits and dashes, at most 63 characters (the API rejects anything else). Format: `<phase>-<change>-<owner>-s<seed>`:
  - `<phase>`: the folder above (`p1` … `p9`).
  - `<change>`: `base` when every knob is at its **Baseline** in [`knobs.md`](../knobs.md); otherwise the one knob changed and its value, shortened, e.g. `lease-fixed12h`, `commit-fenced`, `concurrency-mem-budget`.
  - `<owner>`: whose account ran it: `anton`, `ikwo` or `samuel` (the Terraform `prefix` without `gc-`).
  - `<seed>`: the trace seed.

  Examples: `p1-base-anton-s11`, `p3-lease-fixed12h-ikwo-s22`. Local Compose runs only need the character rule, e.g. `local-sample-1`.

## Outputs in S3

Every SUCCEEDED job has exactly one output object, named so the file alone says which run, dataset and job produced it:

```
outputs/<run>/<job_id>/<run>_<dataset>_<first 8 characters of job_id>.gpkg | .shp.zip | .gdb.zip
```

- `<dataset>` is the input file's name (`datasets/dc.geojson` → `dc`). The layer inside the file is the dataset too, with any character FileGDB does not allow replaced by `_` (`polys-10k` → `polys_10k`).
- Shapefile and FileGDB outputs are zipped; GDAL opens `.shp.zip` and `.gdb.zip` without unzipping. Zipping is timed as `package_ms`, apart from `convert_ms`.
- Each object's S3 user metadata repeats `run-id`, `job-id`, `dataset-id` and `output-format`.
- Outputs live only in the account's bucket and are deleted by `terraform destroy`; a run's lasting record is its results folder (`verify.json` checks every output).

## Files in every AWS run folder

| File | Written by | Contents |
|------|------------|----------|
| `config.json` | `scripts/run.sh` | `run_id, phase, owner, account_prefix, git_sha, gdal_version, trace_file, seed, knobs{...}, task_defs{...}, started_at`, plus the `estimates.json` `version` |
| `jobs.csv` | `tools/collect.py` | one row per job record (DynamoDB export by the `run_id` index), plus `attempt_count`, `last_outcome` and the latency metrics `queue_wait_s`, `processing_s`, `turnaround_s`, `slowdown` (definitions in `collect.py`) |
| `attempts.csv` | `tools/collect.py` | one row per attempt of each job: `run_id, job_id, epoch, task_id, started_at, ended_at, outcome, error_code, peak_rss_mb, cpu_s, peak_disk_mb`. The job record's attempts list, kept because the table is destroyed with the stack |
| `queues.csv` | sampler | SQS visible / in-flight per queue over time |
| `tasks.csv` | sampler | ECS task start and stop times per service |
| `events.csv` | `run.sh`, chaos tools | timestamped run events (start, kill, drain, end) |
| `resources.csv` | sampler | Container Insights CPU and memory per task |
| `locust_*.csv` | Locust | submit-side stats only; latency never comes from here |
| `verify.json` | `tools/verify.py` | per-job output checks; the run fails on any failure |

A run that could not finish has an `INCOMPLETE` file in its folder explaining why.

## Shared files

- `results/costs.csv`: `run_id,owner,account_prefix,started_at,ended_at,task_hours,vcpu_hours,gb_hours,usd`, one row per run.

## Statistics

- Headline comparisons: seeds `11`, `22`, `33`; report the median with min–max.
- Exploratory runs: seed `11` only.
- p99 only for classes with 100 or more jobs.
- Latency always comes from job-record timestamps.
