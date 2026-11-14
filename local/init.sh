#!/bin/bash
# LocalStack ready hook (mounted into /etc/localstack/init/ready.d/): creates the bucket,
# queues and jobs table, then stages the local catalog in S3. Runs on every LocalStack start,
# because the emulators keep state in memory. Compose waits for it via LocalStack's
# /_localstack/init/ready healthcheck, so `docker compose up --build` needs no manual steps.
set -euo pipefail

BUCKET=${BUCKET:-gc-local}
TABLE=${JOBS_TABLE:-jobs}
DDB=${DYNAMODB_ENDPOINT:-http://dynamodb:8000}
CATALOG=/opt/gc/datasets/datasets.csv
CACHE=/var/lib/gc-data          # named volume: DC is downloaded once per machine
DC_URL=https://minedbuildings.z5.web.core.windows.net/legacy/usbuildings-v2/DistrictofColumbia.geojson.zip

ddb() { AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local AWS_DEFAULT_REGION=us-east-1 aws --endpoint-url "$DDB" dynamodb "$@"; }

awslocal s3 mb "s3://$BUCKET" >/dev/null
for q in jobs jobs-small jobs-large; do
  awslocal sqs create-queue --queue-name "$q" >/dev/null   # 30 s default visibility: LEASE_MODE=fixed-30s
done

# Same key, index and projection as jobstore.CreateTableInput and terraform/modules/dynamodb.
for i in $(seq 1 30); do ddb list-tables >/dev/null 2>&1 && break; sleep 1; done
if ! ddb describe-table --table-name "$TABLE" >/dev/null 2>&1; then
  ddb create-table --table-name "$TABLE" --billing-mode PAY_PER_REQUEST \
    --attribute-definitions AttributeName=job_id,AttributeType=S AttributeName=run_id,AttributeType=S \
    --key-schema AttributeName=job_id,KeyType=HASH \
    --global-secondary-indexes 'IndexName=run_id-index,KeySchema=[{AttributeName=run_id,KeyType=HASH}],Projection={ProjectionType=ALL}' \
    >/dev/null
fi

mkdir -p "$CACHE"
if [ ! -f "$CACHE/dc.geojson" ]; then
  curl -sSfL -o "$CACHE/dc.zip" "$DC_URL"
  unzip -o -q "$CACHE/dc.zip" -d "$CACHE"
  mv "$CACHE/DistrictofColumbia.geojson" "$CACHE/dc.geojson"
  rm -f "$CACHE/dc.zip"
fi
python3 /opt/gc/local/gen_synthetic.py "$CACHE"

# Stage every catalog entry, refusing any file whose sha256 differs from datasets.csv.
tail -n +2 "$CATALOG" | while IFS=, read -r id key _bytes _f _g _v _a sha _src; do
  file="$CACHE/$(basename "$key")"
  got=$(sha256sum "$file" | cut -d' ' -f1)
  if [ "$got" != "$sha" ]; then
    echo "init: $id: sha256 $got does not match datasets.csv ($sha)" >&2
    exit 1
  fi
  awslocal s3 cp --only-show-errors "$file" "s3://$BUCKET/$key"
done
echo "init: bucket $BUCKET, queues, table $TABLE and $(($(wc -l < "$CATALOG") - 1)) datasets ready"
