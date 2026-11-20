# 004: Dataset catalog

Date: 2026-11-18 · Owner: Ikwo · Reviewer: Anton · Status: **accepted**

Step H.1 stages the real datasets every phase runs against. The step plan names DC and California but not the other six states, and assumed the catalog would be staged once and copied to the other accounts. This record covers both choices.

## 1. Which 8 states

**Decision:** DC, Hawaii, Vermont, Montana, Mississippi, Arizona, Florida, California.

**Why:** Phase 2 fits cost against input size on log-log axes, so the most useful catalog has real files evenly spaced on a log scale. From DC (22.5 MB) to California (about 3.35 GiB), eight files means each is about 2× the one before. The state nearest each target was picked.

| State | Zipped | Est. unzipped | Target |
|---|---|---|---|
| DC | 2.9 MB | 22.5 MB (exact) | 22.5 MB |
| Hawaii | 8.7 MB | ~68 MB | ~46 MB |
| Vermont | 12.1 MB | ~94 MB | ~95 MB |
| Montana | 25.3 MB | ~196 MB | ~195 MB |
| Mississippi | 51.7 MB | ~401 MB | ~400 MB |
| Arizona | 104.7 MB | ~812 MB | ~820 MB |
| Florida | 270.1 MB | ~2.0 GiB | ~1.7 GB |
| California | 444.8 MB | ~3.35 GiB | ~3.4 GB |

- **Zipped sizes** come from `curl -sI` (`Content-Length`) on every state's v2 URL, `https://minedbuildings.z5.web.core.windows.net/legacy/usbuildings-v2/<State>.geojson.zip`.
- **Unzipped sizes** are estimates using DC's ratio (23,611,576 bytes unzipped / 2.9 MB zipped ≈ 7.8×). The ratio predicts California at 3.37 GiB against the plan's 3.35 GiB. The staged sizes in `datasets.csv` are measured, not estimated.
- **Florida over Ohio (~1.4 GiB):** Ohio fits the spacing better, but Florida gives the Shapefile 2 GB limit tests (`SHP_LIMIT_MODE`, step 5.2) a real file near the boundary. GeoJSON size is not Shapefile size, so whether Florida's output crosses 2 GB is measured in Phase 2. Agreed with Anton on the H.1 issue.
- The small, medium and large classes come from Phase 2's estimates, so this spread does not guarantee the 85/12/3 trace mix. It guarantees real files across the whole range.

## 2. Staging every sitting (no long-lived bucket)

**Finding:** `terraform destroy` deletes the run bucket with `aws s3 rb --force` (`terraform/modules/s3/main.tf`), so `datasets/` is deleted along with `outputs/`. We destroy every sitting, so a catalog staged once would not survive. The bucket also blocks all public access, so the plan's public copy step could not work on it.

**Options considered:**

- A. A long-lived catalog bucket per account, outside the stack, copied into the run bucket each sitting. About 15 GB, roughly $0.35 a month per account.
- B. Re-stage after every `terraform apply`.

**Decision (Anton):** B. The stack stays stateless: every sitting starts from an empty account, nothing persists between sittings, and nothing is billed between them.

**Consequences:**

- `stage_datasets.py` runs after every `apply`, in each person's own account.
- `datasets.csv` is committed with each file's SHA-256 and counts, measured once. Later staging runs check each file's SHA-256 against it instead of recounting 11.5M features, so every account proves it staged identical bytes.
- The `--copy-to` step is dropped. Acceptance criterion 2 becomes: Samuel and Anton have each staged the catalog in their own account, and their SHA-256s match `datasets.csv`.

## Known limits

Every sitting depends on Microsoft's download host. A SHA-256 mismatch catches changed files but not an outage, and the host has moved once already (the old `usbuildingdata.blob.core.windows.net` returns 409). Mitigations:

1. Before `terraform apply`, check the host from a laptop: `curl -sI <DC URL> | head -1` should print `200`.
2. `stage_datasets.py` retries a failed download with growing pauses.
3. A 404 or 409 stops the script with a clear message, since it means the files moved, not a short outage.
