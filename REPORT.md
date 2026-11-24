# Phase 2 predictions (step 2.2-B)

Locked at: 11/23/26 11:46am · git SHA: f602c4c

## p2-size

Real building footprints, `GDAL_PROFILE=defaults`, downloaded in full. Size varies (California ladder, 1k through all features). This is the log-log chart: wall time and peak RSS against features, one line per format.

**Prediction:**

For about 1 GB of input, using the preexisting estimates.json:

| Format | Wall time (s) | Peak RSS (MiB) | Peak disk (MiB) | Output ÷ input | What runs out first |
| ------ | ------------- | -------------- | --------------- | -------------- | ------------------- |
| shp    | 81            | 4000           | 2400            | 0.5            | NA                  |
| gpkg   | 101           | 4000           | 2400            | 0.6            | NA                  |
| fgdb   | 121           | 4000           | 2400            | 0.4            | NA                  |

**Compare (2.4).** Chart: [`results/figures/p2-size-wall-rss.png`](results/figures/p2-size-wall-rss.png) (from `p2-size-retry-samuel-s11`).

Slope actually seen, per format: On log–log wall time vs features (rungs ≥ 100k), slope ≈ 1.00 for all three (shp 0.998, gpkg 1.000, fgdb 0.994). Peak RSS vs features slopes ≈ 0.52–0.58 (sub-linear). Below ~100k features, wall time is near a second and RSS often rounds to 0 MiB, so those points sit flat relative to the large-file line.

1 GB actually needed, per format: Closest rung is `ca-3m` (~920 MB in). Measured:

| Format | Wall (s) | Peak RSS (MiB) | Peak disk (MiB) | Output ÷ input |
| ------ | -------- | -------------- | --------------- | -------------- |
| shp    | 53.1     | 264            | 1898            | 0.289          |
| gpkg   | 49.2     | 323            | 1627            | 0.854          |
| fgdb   | 43.1     | 309            | 1360            | 0.185          |

v1 `estimates.json` at exactly 1000 MiB input: shp 59 s / 270 MiB / 2148 MiB disk / 0.287; gpkg 57 s / 355 / 1847 / 0.866; fgdb 49 s / 339 / 1544 / 0.185.

What limited each format: Nothing ran out on the ladder. Disk was the largest consumer (input + output; Shapefile also zips). Full California peaked at 7.4 GB disk (shp), 6.3 GB (gpkg), 5.3 GB (fgdb), with peak RSS about 0.8–1.1 GB. Shapefile’s 2 GB `.shp`/`.dbf` cap did not bind: zip outputs stayed ~0.29× input.

Gap from the prediction: Wall times were roughly half the v0 guess, and peak RSS was ~10× lower (~300 MiB near 1 GB in, not 4 GB). Output ratios were lower for shp/fgdb (0.29 / 0.19 vs 0.5 / 0.4) and higher for gpkg (0.85 vs 0.6). Disk was in the right ballpark.

## p2-shape

About 300 MB each, same three formats. S1 is the control (many small polygons). The others isolate one cost.

**Prediction.** Relative to S1:

- S2 (most features per byte, points, no attributes): Slower, more features per byte.
- S3 (few polygons, ~10,000 vertices each): Slower per feature, not much longer than S1 because the overall size is smaller.
- S4 (fifty numeric fields): Similar to S1 but slower due to extra fields.
- S5 (extra coordinate digits, fewer features): Coordinate digits do not add extra work, and fewer features mean this should be faster than S1. 
- S6 (strings longer than 254 characters): Shapefile will fail here, other formats will be faster because it has fewer features..

**Compare (2.4).** Order actually seen: Wall time relative to S1 (all SUCCEEDED). Fastest → slowest:

- shp: S6 (0.59×) < S3 (0.63×) < S5 (0.77×) < S1 < S4 (1.34×) < S2 (1.58×)
- gpkg: S6 (0.54×) < S3 (0.58×) < S5 (0.76×) < S1 < S4 (1.19×) < S2 (1.84×)
- fgdb: S6 (0.62×) < S5 (0.78×) < S3 (0.82×) < S1 < S2 (1.16×) < S4 (1.60×)

Feature count dominated (S2 slowest on shp/gpkg). Wide attributes (S4) hurt FileGDB most. Dense vertices (S3) finished faster than S1 overall because it has only 1,269 features.

Failure actually seen: None. S6 Shapefile SUCCEEDED (`verify.json` passed on feature counts).

Gap from the prediction: S2/S4/S5 matched directionally. S3 was faster than S1, not “not much longer.” S6 Shapefile did not fail; GDAL wrote the layer (strings are likely truncated to the DBF 254-char limit; verify does not check attribute values).

## p2-gdal-gpkg

Same GeoPackage files as the `defaults` column of `p2-size`, with `GDAL_PROFILE=gt1` (one transaction per feature). `tuned` is step 2.3.

**Prediction.** Wall time versus `defaults`: Time will be increased compared to the defaults. There is less ability to batch the work, so more effort and time on behalf of the file system. 

Peak RSS versus `defaults`: I think this will stay relatively the same because its the same amount of data per file. 

**Compare (2.4).** Wall-time ratio (gt1 ÷ defaults) on the files that ran: ca-1k 2.5×; ca-10k 6.1×; ca-100k 7.7×; ca-1m 8.0×; ca-3m 8.0×; ca 7.7×. Above ~100k features, gt1 is about 8× slower.

Peak RSS: Same as defaults wherever both are non-zero (e.g. ca-1m 152 vs 153; ca-3m 323 vs 323; ca 1053 vs 1053).

Gap from the prediction: Direction correct; the slowdown is large and stable (~8×), not a mild increase. RSS prediction matched.

## p2-limits

`SHP_LIMIT_MODE=none`. The 2 GB cap is on the Shapefile output, not the GeoJSON. Florida is ~2.0 GiB in. California is ~3.6 GB in.

**Prediction.** Florida Shapefile: Based on v0 estimates, this should be successful.

California Shapefile: This will be close to being over the limit, and may fail. 

GeoPackage and FileGDB on those same files: Both pass.

**Compare (2.4).** Florida: All three SUCCEEDED. shp 127 s / 549 MiB RSS / 4492 MiB disk / out÷in 0.289; gpkg 120 s / 687 / 3844 / 0.863; fgdb 104 s / 638 / 3196 / 0.187.

California: All three SUCCEEDED (including Shapefile). shp 204 s / 767 / 7359 / 0.285; gpkg 194 s / 1053 / 6332 / 0.845; fgdb 168 s / 1028 / 5291 / 0.183. Same ballpark as the `p2-size-retry` full-CA rows.

Gap from the prediction: Florida matched. California Shapefile did not fail: with out÷in ≈ 0.29 the zipped output is ~1.0 GB, so the 2 GB `.shp`/`.dbf` components stay under the limit. GeoPackage and FileGDB matched.

## p2-stream

`ca-1m-seq` and `ca-all-seq` against the matching download rungs in `p2-size`. Streaming reads the input from S3 instead of writing `in.geojson` first. `INPUT_MODE=stream` is not implemented yet.

**Prediction.** Peak disk versus the download: Disk would be significantly lower because we no longer need to download an entire fil e. Because INPUT_MODE=stream does not exist as a valid option, these runs will fail. 

Wall time versus the download: Probably longer given the need for context switching, if implemented.

**Compare (2.4).** What ran: Nothing converted. Preflight refused (`INCOMPLETE`): `INPUT_MODE=stream` not implemented (tfvars stream, deployed download). Both cells recorded that error in `profile.csv`.

Peak disk and wall time versus the download: No measurements.

Gap from the prediction: Matched — the run failed before any job because streaming is not implemented. Disk/wall comparison deferred until `INPUT_MODE=stream` exists.

## Phase summary (2.4)

Methodology: Profiling ran as `TASK_SHAPE=1x2-8-20gb` (1 × 2 vCPU, 8 GB, 20 GB), not the plan's 1 × 4 vCPU, 16 GB, 100 GB — AWS Learner Lab credentials blocked the larger shape. That was enough: across the successful cells, `cpu_s` tracked wall time (ogr2ogr stays near single-threaded), peak RSS stayed under 1.1 GB, and peak disk stayed under 7.4 GB, so the smaller shape did not bind. `p2-size-samuel-s11` failed at collection (`INCOMPLETE`); use `p2-size-retry-samuel-s11` for the size ladder. `p2-stream` refused at preflight (`INPUT_MODE=stream` not implemented). One rep per cell (`seed=11`). Numbers from `results/p2/*/profile.csv` in `gc-samuel` only. Chart: [`results/figures/p2-size-wall-rss.png`](results/figures/p2-size-wall-rss.png).

Observations: Wall time scales linearly with features (log–log slope ≈ 1) once past ~100k features; the three formats stay close, with FileGDB usually fastest and Shapefile usually slowest on real footprints. Peak RSS grows slower than features (~0.55 slope) and stays near 1 GB even on full California. Disk is input+output (plus zip for shp/fgdb) and is the resource that grows toward the task’s ephemeral disk. Feature count and wide attributes dominate shape cost more than vertex density. `GDAL_PROFILE=gt1` multiplies GeoPackage wall time by ~8× with no RSS change. The Shapefile 2 GB component limit and the S6 DBF string limit did not produce failures on these files (S6 shp “succeeded” under count-only verify).

Outcome: Phase 2 produced a usable cost model: `config/estimates.json` is v1, fitted from `p2-size-retry` (`fit_r2` ≈ 1.0 on `work_s`). Near 1 GB of input, expect roughly 45–60 s of convert time, ~270–350 MiB RSS, ~1.4–2.1 GB peak disk, and out÷in ≈ 0.29 (shp) / 0.85 (gpkg) / 0.19 (fgdb). What limits each format on this workload: shp is bounded by the 2 GB `.shp`/`.dbf` rule and DBF field width (neither hit here because outputs are ~0.29× input and verify ignores truncation); gpkg and fgdb show no hard format failure on CA/FL — they are limited by disk and wall time as input grows. Streaming remains unimplemented, so peak-disk savings from `INPUT_MODE=stream` are unmeasured.

Prediction versus result, one gap per run:

- **p2-size:** v0 overstated wall (~2×) and especially RSS (~10×); measured out ratios differ (shp/fgdb lower, gpkg higher).
- **p2-shape:** S6 Shapefile did not fail; S3 was faster than S1, not merely “not much longer.”
- **p2-gdal-gpkg:** Slowdown is ~8×, larger than a mild increase; RSS unchanged as predicted.
- **p2-limits:** California Shapefile succeeded; the 2 GB cap did not bind at out÷in ≈ 0.29.
- **p2-stream:** Matched — refused at preflight; no disk/wall comparison possible.
