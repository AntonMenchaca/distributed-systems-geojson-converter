# Knobs

Every distributed-systems mechanism is a knob. A knob changes **only** through its environment variable or Terraform variable, and every run records all of them in `results/<phase>/<run>/config.json`. A new knob is added here in the same PR that introduces it.

The baseline run (`p1-base`) sets every knob to its **Baseline** value; each later run flips one knob at a time.

## Naming convention

- **Env knobs** are read by the service named in "Read by". Locally they are set in `docker-compose.yml`; on AWS each one is set by the Terraform variable of the same name in lower case (`LEASE_MODE` → `var.lease_mode`), which Terraform passes into the task definition's environment.
- **Terraform-only knobs** change infrastructure (queues, services, task sizes) and have no env var.
- Unknown values are a startup error, never a silent default.

## Knobs table

| Knob | Kind | Terraform variable | Read by | Allowed values | Baseline | Fix |
|------|------|--------------------|---------|----------------|----------|-----|
| `UPLOAD_MODE` | env | `upload_mode` | api | `proxy-buffer`, `proxy-stream`, `presigned` | `proxy-buffer` | `proxy-stream`; `presigned` |
| `QUEUE_LAYOUT` | env | `queue_layout` | api, terraform (creates queues) | `single`, `by-size` | `single` | `by-size` |
| `SCHED_POLICY` | env | `sched_policy` | worker | `fcfs`, `small-first`, `weighted-3:1` | `fcfs` | `small-first`, `weighted-3:1` |
| `POOL_LAYOUT` | terraform | `pool_layout` (pool sizes: `worker_count_small`, `worker_count_large`) | terraform | `shared`, `split` | `shared` | `split` (small and large pools, same total budget) |
| `LEASE_MODE` | env | `lease_mode` | worker | `fixed-30s`, `fixed-12h`, `heartbeat` (60 s lease, renewed every 20 s) | `fixed-30s` | `fixed-12h`; `heartbeat` |
| `COMMIT_MODE` | env | `commit_mode` | worker (via `jobstore`) | `blind`, `fenced` | `blind` | `fenced` |
| `DLQ_MAX_RECEIVES` | terraform | `dlq_max_receives` | terraform | `0` (no DLQ), any integer ≥ 1 | `0` (no DLQ: poison retries forever) | `3`, then `5` once permanent errors are handled |
| `WORKER_CONCURRENCY` | env | `worker_concurrency` | worker | `fixed-<n>` (n ≥ 1), `mem-budget` | `fixed-4` | `mem-budget` |
| `SHUTDOWN_MODE` | env | `shutdown_mode` | worker | `ignore`, `drain` | `ignore` | `drain` (stop pulling, let jobs finish within `STOP_TIMEOUT_S`, release the rest, scale-in protection) |
| `STOP_TIMEOUT_S` | env | `stop_timeout_s` | worker, terraform (ECS `stopTimeout`) | `11`–`120` s (Fargate's maximum is 120) | `120` | — (only matters under `SHUTDOWN_MODE=drain`; the worker stops taking jobs 10 s before the timeout) |
| `GDAL_PROFILE` | env | `gdal_profile` | worker | `defaults`, `gt1`, `tuned` | `defaults` | `tuned` (fitted per format from profiling runs) |
| `SHP_LIMIT_MODE` | env | `shp_limit_mode` | worker, api | `none`, `split-2gb`, `reject` | `none` | `split-2gb` (parts under 2 GB); `reject` (at admission) |
| `ADMISSION` | env | `admission` | api | `off`, `validate`, `backlog` | `off` | `validate` (format limits, max size); `backlog` (`429` when predicted wait passes a target) |
| `SCALER` | env | `scaler` | autoscaler | `fixed`, `cpu`, `memory`, `backlog-msgs`, `backlog-work`, `native` | `fixed` | `backlog-work`; others for comparison (`native` = ECS target tracking) |
| `SCALER_COOLDOWN` | env | `scaler_cooldown` | autoscaler | `none`, `out=<s>,in=<s>,band=<fraction>` | `none` (no cooldown, no hysteresis) | `out=60,in=180,band=<to be measured>` |
| `WORKER_IMAGE` | terraform | `worker_image` | terraform | `gdal-full` (ubuntu-full), `gdal-slim` (alpine-small) | `gdal-full` | `gdal-slim` |
| `TASK_SHAPE` | terraform | `worker_count`, `worker_cpu` (CPU units), `worker_memory` (MiB), `worker_ephemeral_gb` | terraform | n × (vCPU, memory, disk) at a fixed budget, e.g. `16x0.25-1`, `4x1-4`, `1x4-16` | `4x1-4` (4 × 1 vCPU, 4 GB, 20 GB default disk) | — (a variable, not a fix) |
| `INPUT_MODE` | env | `input_mode` | worker | `download`, `stream` (`/vsis3/` or GeoJSONSeq) | `download` | `stream` |
| `SPLIT_K`, `SPECULATE` | env | `split_k`, `speculate` | api, worker | `SPLIT_K`: integer ≥ 1; `SPECULATE`: `off`, `on` | `1`, `off` | k chunks; backup copies of slow chunks |

Queue and policy combinations: `SCHED_POLICY=fcfs` reads one queue, so it pairs with `QUEUE_LAYOUT=single` or with `POOL_LAYOUT=split` (each pool reads its own queue). `small-first` and `weighted-3:1` choose between `jobs-small` and `jobs-large`, so they need `QUEUE_LAYOUT=by-size` with `POOL_LAYOUT=shared`. Any other combination is refused by `terraform plan` and by the worker at startup.

## Not knobs

These are fixed settings, not experiment variables: `AWS_REGION`, `JOBS_TABLE`, `BUCKET`, queue URLs, `ESTIMATES_PATH`, `RUN_ID`, `WORKER_POOL` (set per pool by Terraform from `POOL_LAYOUT`), `CHAOS_ENABLED`. They are wiring, set by Compose or Terraform outputs. `CHAOS_ENABLED` is `0` or `1` and only arms the per-job flags already in the trace (`crash_at`, `pause_s`, `slow`). It is not a knob: a run does not flip it as the one change under test.
