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

_Not set up yet: the local Docker Compose stack goes here._

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
