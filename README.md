# distributed-systems-geojson-converter

A distributed service that converts GeoJSON to Shapefile, GeoPackage and FileGDB. A Go API records jobs in DynamoDB and enqueues them on SQS; Go workers run `ogr2ogr` (GDAL 3.13) per job and upload the output to S3. Every distributed-systems mechanism is a knob (see [`knobs.md`](knobs.md)).

## Status

Early development: contracts, service skeletons and stubs.

## Contracts

Build against these files. Changing one needs a PR that updates the contract and its consumers together.

- [`api.yaml`](api.yaml): the HTTP API
- [`schemas/queue-message.json`](schemas/queue-message.json), [`schemas/job-record.json`](schemas/job-record.json)
- [`knobs.md`](knobs.md): every knob, its env or Terraform name, allowed values and baseline
- [`config/estimates.json`](config/estimates.json): per-format work, memory and disk models (v0 placeholder values, not yet fitted from measurements)

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

## Deploying to AWS (your own Learner Lab account)

Each of us deploys the full stack in our own AWS Academy Learner Lab account and tears it down in the same sitting.

1. Start the lab and copy its credentials into an AWS CLI profile.
2. `export AWS_PROFILE=<your Learner Lab profile>`, then `aws sts get-caller-identity`. The ARN must contain `assumed-role/voclabs/`. If it doesn't, stop: you're about to deploy into another account. Terraform checks this too, and `plan` fails for any other caller.
3. `cd terraform && terraform init && terraform apply -var prefix=gc-<a|b|c>`. This builds both images for linux/amd64 and pushes them to ECR. Every task uses the lab's existing `LabRole`; the stack creates no IAM resources, because Learner Lab doesn't allow it.
4. For a smoke test before the full dataset catalog is staged in S3, copy DC to `s3://$(terraform output -raw bucket)/datasets/dc.geojson`, then `curl -XPOST $(terraform output -raw alb_url)/jobs ...` as in Getting started.
5. `terraform destroy -var prefix=gc-<a|b|c>`. Then check that nothing is left: `aws resourcegroupstaggingapi get-resources --tag-filters Key=Prefix,Values=gc-<a|b|c>`.

Knobs change only through Terraform variables, e.g. `-var lease_mode=fixed-12h`. A value whose code hasn't been built yet fails at plan, naming the step that builds it.

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
