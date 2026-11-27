# 006: Permanent errors, the FAILED claim fence and the local P1 check

Date: 2026-11-27 · Owner: Samuel · Reviewer: Ikwo · Status: **accepted**

Step 3.3-B ends jobs that can never succeed instead of retrying them. Before it, every convert failure left the job and its message in place for redelivery, so a bad file came back after every visibility timeout. This record covers which GDAL failures count as permanent, how the rule was chosen, and the local run that checked it end to end.

## 1. Which failures are permanent

**Decision:** a failed `ogr2ogr` is permanent only when its stderr holds one of these messages. Everything else is transient: the job keeps its status and the message returns to the queue for another attempt.

| Message | Output formats | Poison file |
|---|---|---|
| `Failed to read GeoJSON data` | all | P1 (truncated JSON) |
| `GeoJSON object too complex/large` | all | P4 (one feature over 200 MB) |
| `Attempt to write non-<type> (<TYPE>) geometry to <type> shapefile` | `shp` | P2 (mixed geometry) |
| `Unsupported geometry type` | `fgdb` | P2 (mixed geometry) |

**Why:** each message is deterministic. The same file and format fail the same way on every attempt, so a retry cannot succeed. The messages come from running the worker's own `ogr2ogr` command on the poison files (section 3), not from guesses.

- **P2 depends on the format.** GeoPackage accepts mixed geometry and converts all 1,000 features, so P2 is permanent only for `shp` and `fgdb`. The step plan says "P2 is permanent"; this narrows it.
- **P4 keeps GDAL's default limit.** With `OGR_GEOJSON_MAX_OBJ_SIZE=0`, GDAL converts P4 to a 168 MB GeoPackage with one feature in about two minutes. So "permanent" is a choice to keep the 200 MB default. Lifting it needs a memory measurement and a new knob. Decided: keep the default.
- **No message means transient.** An `ogr2ogr` killed for memory ends with a signal and no GDAL text, so an OOM retries. So does any error that is not an `ogr2ogr` failure (download, upload, `ogrinfo`).
- **Warnings never classify.** The control shapefile prints `Warning 6: Normalized/laundered field name` and exits 0. Only a failed run is matched.
- **The whole stderr is matched, not the 2 KB tail.** The key line sits above a driver list of about 1 KB, so a long warning block could push it out of the tail that the log shows. `gdal.ConvertError` carries the full stderr.

## 2. How a permanent failure ends the job

**Decision:**

- The worker calls `Fail(permanent=true)`, which sets `FAILED`, and then deletes the message. It deletes only if the failure was recorded. A stale epoch or a store error keeps the message, because the job is not `FAILED`.
- `Claim` refuses `FAILED` as well as `SUCCEEDED`, in both commit modes, and returns `ErrAlreadyFailed`. A redelivered message for a `FAILED` job is deleted without converting. A transient `Fail` leaves the status alone, so its redelivery can still claim.
- `CHAOS_INVALID` was already marked permanent. It now deletes its message too.

**Why:** without the claim fence, a duplicate or late delivery of a `FAILED` job would claim it again, convert it again and fail again. Deleting the message only after `FAILED` is recorded means a lost write cannot drop a job that is still live.

## 3. The GDAL runs

Date: 2026-11-27. Image: the worker image built from `worker/Dockerfile` (GDAL 3.13.3, `alpine-small`).

```sh
python tools/gen_geojson.py --poison P1 --seed 11 --out p1.geojson   # also P2, P4 (default 220 MiB target), and P5 --target-mb 5 as the control
docker run --rm -v <dir>:/data --entrypoint sh <worker image> -c \
  "ogr2ogr -f '<driver>' /data/out/<name> /data/<file>.geojson -nln <layer> 2>/data/<name>.err; echo $? > /data/<name>.exit"
```

The command is the one in `worker/internal/gdal/runner.go` under the default profile. Drivers: `GPKG`, `ESRI Shapefile`, `OpenFileGDB`. Stderr was captured inside the container so the byte counts are exact.

| Input | Size (bytes) | GeoPackage | Shapefile | FileGDB | Stderr bytes |
|---|---|---|---|---|---|
| control (valid) | 5,243,057 | exit 0 | exit 0, `Warning 6` only | exit 0 | 0 / 82 / 0 |
| P1 | 256,972 | exit 1 | exit 1 | exit 1 | 1,117 each |
| P2 | 197,291 | exit 0, 1,000 features, mixed types kept | exit 1 | exit 1 | 0 / 327 / 152 |
| P4 | 259,815,570 | exit 1 | exit 1 | exit 1 | 1,283 each |

Messages:

- **P1:** `ERROR 1: At line 1005, character 30: Unterminated object`, then `ERROR 4: Failed to read GeoJSON data`, then `Unable to open datasource` followed by the list of drivers it tried.
- **P2, shapefile:** `ERROR 1: Attempt to write non-point (LINESTRING) geometry to point shapefile.`
- **P2, FileGDB:** `ERROR 6: Unsupported geometry type`
- **P4:** `ERROR 1: At line 5, character 4772603: GeoJSON object too complex/large. You may define the OGR_GEOJSON_MAX_OBJ_SIZE configuration option ...`

**P4 with the limit lifted.** Run once for GeoPackage with `-e OGR_GEOJSON_MAX_OBJ_SIZE=0`. It produced a 168,042,496-byte file with one feature and empty stderr. The exit code was not captured cleanly (the shell expanded `$?`), so the evidence is the complete output and the empty stderr. Peak memory was not measured.

## 4. The local end-to-end P1 run

Run id `local-p1-check`, job `11eb0cfb-b7e7-4ffb-81e9-4c37b71bba2b`, 2026-11-27.

**Settings.** From the worker's startup log: concurrency `fixed-4`, `LEASE_MODE=heartbeat`, `COMMIT_MODE=fenced`, `CHAOS_ENABLED=1`, GDAL profile `defaults`, `SHUTDOWN_MODE=ignore`, queue layout `single`, `SCHED_POLICY=fcfs`, pool `shared`, task id `28c8459cf38d`. Compose's defaults are `fixed-30s`, `blind` and chaos off, so the three non-default values were set in the shell. The compose defaults were not run. P1 fails before commit, so the lease and commit modes should not change the outcome, but that is reasoning, not a result.

**Catalog.** The API reads `datasets/local/datasets.csv` by default, which has no `p1` row. The full catalog, `datasets/datasets.csv`, has `p1,datasets/p1.geojson,256972,...`. A compose override file outside the repo pointed the API at the full catalog.

**Commands** (PowerShell, from the repo root; on macOS use `python3`, `/tmp`, `curl`, and `:` instead of `;` in `COMPOSE_FILE`):

```powershell
python tools\gen_geojson.py --poison P1 --seed 11 --out "$env:TEMP\p1-check.geojson"   # 256972 bytes, sha256 dfe257e681349afdedbdf43d973b7471f4f1446cad8105ce0951956716e9ddb4

# C:\Users\<you>\p1-override.yml, outside the repo:
#   services: { api: { environment: { DATASETS_CSV: datasets/datasets.csv } } }
$env:COMPOSE_FILE = "docker-compose.yml;C:\Users\<you>\p1-override.yml"
$env:LEASE_MODE = "heartbeat"; $env:COMMIT_MODE = "fenced"; $env:CHAOS_ENABLED = "1"
docker compose up -d --build

# the local bucket is in memory: repeat this after every LocalStack start
docker compose cp "$env:TEMP\p1-check.geojson" localstack:/tmp/p1.geojson
docker compose exec localstack awslocal s3 cp /tmp/p1.geojson s3://gc-local/datasets/p1.geojson

Invoke-RestMethod -Method Post -Uri http://localhost:8080/jobs -ContentType 'application/json' `
  -Body '{"input":{"dataset_id":"p1"},"output_format":"gpkg","run_id":"local-p1-check"}'
Invoke-RestMethod http://localhost:8080/jobs/<job_id>
docker compose logs worker
docker compose exec localstack awslocal sqs get-queue-attributes --queue-url http://localhost:4566/000000000000/jobs --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible
docker compose run --rm tools python tools/verify.py --run-id local-p1-check --out results/local-p1-check/verify.json
```

**Result.**

| Check | Observed |
|---|---|
| Job record | `FAILED`, `error_code` `CONVERT_FAILED`, one attempt (epoch 1, task `28c8459cf38d`) |
| Timing | submitted 19:41:25.694Z, started 19:41:25.841Z, attempt ended 19:41:26.165Z (0.47 s after submission) |
| Worker log | `ERROR "job attempt failed"`, `"permanent":true`, `Unterminated object` at line 1005, character 30 |
| Queue | 0 visible, 0 in flight |
| `verify.json` | `passed: false`, `checked: 0`, `jobs: 1`, `FAILED: 1`, `failures: []`, `complete: true` |

`results/local-p1-check/` holds only `verify.json`, because the steps were run by hand, not through `scripts/run.sh`. `passed: false` is expected: `verify.py` has no `SUCCEEDED` job to check.

"Exactly one receive" is inferred, not measured. There was one attempt, one failure log line and an empty queue. A second receive would have hit the claim fence and been deleted without a log line, so the logs cannot rule it out.

## 5. Tests

- **Worker** (`worker/internal/gdal`, `worker/internal/loop`): the module does not build on Windows (`measure` uses `syscall.Rusage`), so run `go vet ./... && go test ./...` on Linux or in a Go container. The real-GDAL tests run with `docker build -f worker/Dockerfile --target test .`.
  - Classifier: `TestTruncatedJSONIsPermanent`, `TestTruncatedJSONIsPermanentWhenBuriedInWarnings`, `TestOOMIsNotPermanent`, `TestWarningsAloneAreNotPermanent`, `TestP4IsPermanentInEveryFormat`, `TestMixedGeometryIsPermanentOnlyForItsFormat`, `TestNonConvertErrorsAreTransient`, `TestConvertErrorMessageKeepsTheLogTail`.
  - Real GDAL: `TestRealGDALFailuresClassify` (five cases), `TestRealGDALMixedGeometryConvertsToGeoPackage`.
  - Worker behavior: `TestPermanentConvertFailureFailsJobAndDeletes`, `TestPermanentFailureStaleEpochKeepsMessage`, `TestPermanentFailureStoreErrorKeepsMessage`, `TestAlreadyFailedDeletesWithoutConverting`, `TestOOMConvertFailureIsTransient`.
- **Jobstore:** `DYNAMODB_ENDPOINT=<DynamoDB Local> go test ./...` in `jobstore/`. Without the variable the tests skip. New: `TestClaimAfterFailedReturnsErrAlreadyFailed`, `TestTransientFailLeavesJobClaimable`. All 11 passed against DynamoDB Local.

## 6. Consequences and open items

- **`verify.py` reports `passed: false` for a run with no `SUCCEEDED` job.** Whether a `FAILED`-only run should pass is open.
- **One receive is not observable.** Logging the claim-fence path (`ErrAlreadyFailed`, `ErrAlreadyDone`) would make a duplicate receive visible.
- **The local run needs an override file** because `datasets/local/datasets.csv` has no poison files. Adding them to the local catalog would remove it.
- **The compose defaults were not run** for P1 (section 4).
