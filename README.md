# distributed-systems-geojson-converter

A distributed service that converts GeoJSON to Shapefile, GeoPackage and FileGDB. A Go API records jobs in DynamoDB and enqueues them on SQS; Go workers run `ogr2ogr` (GDAL 3.13) per job and upload the output to S3. Every distributed-systems mechanism is a knob (see [`knobs.md`](knobs.md)).

## Status

Early development: contracts, service skeletons and stubs.

## Contracts

Build against these files. Changing one needs a PR that updates the contract and its consumers together.

- [`api.yaml`](api.yaml): the HTTP API
- [`schemas/queue-message.json`](schemas/queue-message.json), [`schemas/job-record.json`](schemas/job-record.json)
- [`knobs.md`](knobs.md): every knob, its env or Terraform name, allowed values and baseline
- [`config/estimates.json`](config/estimates.json): per-format work, memory and disk models (v1 fitted from Phase 2 `profile.csv`)
- [`docs/results-layout.md`](docs/results-layout.md): where every run's results go, and what each run folder holds

## Getting started

Needs Docker only. On a clean clone:

```sh
docker compose up --build        # API :8080, Prometheus :9090, Grafana :3000 (no login)
```

The first start downloads the DC footprints (3 MB zip) into a Docker volume and stages the local catalog (`datasets/local/datasets.csv`) in the emulated S3. LocalStack (S3 and SQS) and DynamoDB Local stand in for AWS.

Submit one job, replay the sample trace, and verify the outputs:

```sh
curl -XPOST localhost:8080/jobs -H 'Content-Type: application/json' \
  -d '{"input":{"dataset_id":"dc"},"output_format":"gpkg","run_id":"local-x"}'
python3 tools/replay.py --trace traces/sample.csv --run-id local-sample-1
docker compose run --rm tools python tools/verify.py --run-id local-sample-1 --out results/local-sample-1/verify.json
```

Change a knob through its environment variable, e.g. `LEASE_MODE=fixed-12h docker compose up --build` (see [`knobs.md`](knobs.md)).

## Runs and outputs

Every job belongs to a run, its `run_id`. The run name is part of every output's S3 key and file name, so the API accepts only lowercase letters, digits and dashes, at most 63 characters. Name runs `<phase>-<change>-<owner>-s<seed>`:

- `<phase>`: the experiment group, `p1` to `p9`.
- `<change>`: `base` when every knob is at its baseline in [`knobs.md`](knobs.md); otherwise the one knob changed and its value, shortened, e.g. `lease-fixed12h`.
- `<owner>`: whose account ran it: `anton`, `ikwo` or `samuel` (the Terraform `prefix` without `gc-`).
- `<seed>`: the trace seed.

For example `p1-base-anton-s11` or `p3-lease-fixed12h-ikwo-s22`. Local Compose runs only need the character rule, e.g. `local-sample-1`.

Each successful job writes exactly one output object, named so the file alone says which run, dataset and job produced it:

```
outputs/<run>/<job_id>/<run>_<dataset>_<first 8 characters of job_id>.gpkg | .shp.zip | .gdb.zip
```

- The layer inside is the dataset name (`dc`), with characters FileGDB does not allow replaced by `_`.
- GDAL opens `.shp.zip` and `.gdb.zip` without unzipping. Zipping is timed as `package_ms`, apart from `convert_ms`.
- The object's S3 metadata repeats `run-id`, `job-id`, `dataset-id` and `output-format`.
- Outputs are deleted with the stack. A run's lasting record is `results/<phase>/<run>/`, e.g. its `verify.json`.

## Deploying to AWS (your own Learner Lab account)

Each of us deploys the full stack in our own AWS Academy Learner Lab account and tears it down in the same sitting.

1. Start the lab and copy its credentials into an AWS CLI profile.
2. `export AWS_PROFILE=<your Learner Lab profile>`, then `aws sts get-caller-identity`. The ARN must contain `assumed-role/voclabs/`. If it doesn't, stop: you're about to deploy into another account. Terraform checks this too, and `plan` fails for any other caller.
3. `cd terraform && terraform init && terraform apply -var prefix=gc-<anton|ikwo|samuel>`. This builds both images for linux/amd64 and pushes them to ECR. Every task uses the lab's existing `LabRole`; the stack creates no IAM resources, because Learner Lab doesn't allow it.
4. For a smoke test before the full dataset catalog is staged in S3, copy DC to `s3://$(terraform output -raw bucket)/datasets/dc.geojson`, then `curl -XPOST $(terraform output -raw alb_url)/jobs ...` as in Getting started.
5. `terraform destroy -var prefix=gc-<anton|ikwo|samuel>`. Then check that nothing is left: `aws resourcegroupstaggingapi get-resources --tag-filters Key=Prefix,Values=gc-<anton|ikwo|samuel>`.

Knobs change only through Terraform variables, e.g. `-var lease_mode=fixed-12h`. A value whose code hasn't been built yet fails at plan, naming the step that builds it.

## Running an experiment

Every result comes from `scripts/run.sh`, which writes one results folder per run. **Never hand-edit a results folder**; a rerun gets a new run name.

```sh
.venv/bin/pip install -r tools/requirements.txt -r locust/requirements.txt   # once

# local, against `docker compose up --build`: writes results/<run>/
scripts/run.sh local local-smoke traces/sample.csv local

# AWS, against the stack you deployed with that tfvars file: writes results/<phase>/<run>/
terraform -chdir=terraform apply -var-file=p1-base.tfvars
scripts/run.sh p1 p1-base-anton-s11 traces/fail-s11.csv terraform/p1-base.tfvars
```

On Windows without WSL, run the same arguments with `.venv\Scripts\python tools\run.py ...`.

Tfvars files stay on your machine (git ignores `*.tfvars`). To repeat a run, write one from the `knobs` in its `config.json`.

The script never deploys or destroys anything. Before starting it refuses (exit 2) when:

- the run name is reused or doesn't follow "Runs and outputs";
- a trace dataset isn't in the catalog, the API isn't healthy, or a queue or DLQ isn't empty;
- on AWS: the caller isn't a Learner Lab session; the session won't outlast the run's time limit (trace plus `--drain-s`, at least 60 minutes) plus 10 minutes to collect and destroy (it ends 4 hours after your credentials file was written; `--session-ends HH:MM` overrides); the tfvars sets a variable `terraform/` doesn't declare, or a knob that differs from what is deployed; a service isn't steady; or your account's rows in `results/costs.csv` include one without a price, or plus this run's estimate would pass $17.

Pull `main` before an AWS run, so the budget check sees your latest `costs.csv` rows.

It then writes `config.json`, samples queues, tasks and resources, replays the trace with Locust, waits until every job is terminal (or the trace length plus `--drain-s`, default 30 min), and writes `jobs.csv` and `verify.json`. The files are listed in [`docs/results-layout.md`](docs/results-layout.md).

Exit status: 0 verified, 1 verify failed, 2 refused, 3 incomplete (the folder has an `INCOMPLETE` file saying why). On AWS, post the start and the teardown in `#runs`, and `terraform destroy` in the same sitting.

On AWS the script ends with `tools/cost.py`: it appends the run's row to `results/costs.csv` and lists anything still running under your prefix. A row runs from the end of your previous row (or your stack's first task) to the run's end, so time after your last run isn't counted: destroy soon after it. After destroying, `python tools/cost.py --open-stacks --prefix gc-<you>` should print `nothing running`.

## Analyzing runs

```sh
.venv/bin/python tools/analyze.py results/p1/p1-base-anton-s11 results/p1/p1-base-anton-s22
.venv/bin/python tools/analyze.py --compare results/p1/p1-base-anton-s11 results/p9/p9-best-anton-s11
```

Each run becomes one Markdown row of the master table (jobs, failures, duplicates, small and large p95/p99 turnaround, GB/h, $/GB, incorrect outputs, healthy jobs failed), and its turnaround CDF is saved to `results/figures/<run>.png`. `--compare` adds the change, and refuses (exit 2) when the two runs come from different accounts or seeds. The column definitions are in `tools/analyze.py`.

## Project structure

| Path | What | Owner |
|------|------|-------|
| `api/` | Go API (Gin) | Ikwo |
| `worker/` | Go worker running `ogr2ogr` | Samuel |
| `jobstore/` | Go package: every job-record write | Samuel |
| `autoscaler/` | Go autoscaler | Anton |
| `tools/`, `locust/` | Python: traces, datasets, collection, verification, analysis; load generation | all |
| `terraform/` | AWS stack | Anton |
| `results/` | one folder per run | all |

## Development checks

```sh
python3.11 -m venv .venv && .venv/bin/pip install -r tools/requirements-dev.txt
.venv/bin/python -m pytest tools/tests
npx --yes @redocly/cli@1.34.3 lint api.yaml
```

## Configuration

Environment variables go in a local `.env` file. Git ignores it, so never commit it.

## Data

Building footprints from [Microsoft US Building Footprints](https://github.com/microsoft/USBuildingFootprints), licensed under the [Open Database License (ODbL)](https://opendatacommons.org/licenses/odbl/). © Microsoft.
