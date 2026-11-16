# 001: Local AWS emulator

Date: 2026-11-14 · Decided by: Anton

## Context

Every run type runs once against Docker Compose before AWS. The stack needs S3, SQS and DynamoDB locally. Since March 23, 2026 the LocalStack image needs a free account and auth token; the choice is between tokens for everyone and pinning `localstack/localstack:4.14`, the last image that runs without one.

## Decision

- **S3 and SQS: `localstack/localstack:4.14`**, pinned, no auth token. Verified on 2026-11-14 that it starts and serves S3 and SQS with no token set.
- **DynamoDB: `amazon/dynamodb-local:2.5.2`**, the same image the `jobstore` tests and CI use, so the table the tests exercise is the table Compose runs.
- **Init:** `local/init.sh` runs as a LocalStack ready hook. It creates the bucket, the three queues and the jobs table (same key and `run_id-index` GSI as `jobstore.CreateTableInput`), downloads DC once into a named volume, generates the synthetic files, and refuses to stage any file whose sha256 differs from `datasets/local/datasets.csv`. Compose starts the API and worker only after LocalStack's init-ready healthcheck passes.

## Consequences

- Nobody manages a LocalStack token; a clean clone needs only Docker.
- We are pinned to an image that will not get fixes. If 4.14 breaks a feature we need (for example newer SQS APIs), switch to tokens and update this record.
- Emulator behavior is not AWS behavior (visibility timing, throughput, consistency). Report numbers come only from AWS runs.
