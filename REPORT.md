# Phase 2 predictions (step 2.2-B)

Fill every **Prediction** before the first `p2-`* run, then leave those paragraphs alone. Fill **Compare** at step 2.4 from `results/p2/<run>/profile.csv`. AWS numbers only (`gc-samuel`). One rep per cell. A full-size conversion over 20 minutes is not repeated.

Columns to compare: `wall_ms` (ogr2ogr only), `peak_rss_mb` (that process), `peak_disk_mb` (input + output, plus the zip for Shapefile and FileGDB), output bytes ÷ input bytes, `error`.

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

**Compare (2.4).** Chart: 

Slope actually seen, per format: 

1 GB actually needed, per format: 

What limited each format: 

Gap from the prediction: 

## p2-shape

About 300 MB each, same three formats. S1 is the control (many small polygons). The others isolate one cost.

**Prediction.** Relative to S1:

- S2 (most features per byte, points, no attributes): Slower, more features per byte.
- S3 (few polygons, ~10,000 vertices each): Slower per feature, not much longer than S1 because the overall size is smaller.
- S4 (fifty numeric fields): Similar to S1 but slower due to extra fields.
- S5 (extra coordinate digits, fewer features): Coordinate digits do not add extra work, and fewer features mean this should be faster than S1. 
- S6 (strings longer than 254 characters): Shapefile will fail here, other formats will be faster because it has fewer features..

**Compare (2.4).** Order actually seen: 

Failure actually seen: 

Gap from the prediction: 

## p2-gdal-gpkg

Same GeoPackage files as the `defaults` column of `p2-size`, with `GDAL_PROFILE=gt1` (one transaction per feature). `tuned` is step 2.3.

**Prediction.** Wall time versus `defaults`: Time will be increased compared to the defaults. There is less ability to batch the work, so more effort and time on behalf of the file system. 

Peak RSS versus `defaults`: I think this will stay relatively the same because its the same amount of data per file. 

**Compare (2.4).** Wall-time ratio (gt1 ÷ defaults) on the files that ran: 

Peak RSS: 

Gap from the prediction: 

## p2-limits

`SHP_LIMIT_MODE=none`. The 2 GB cap is on the Shapefile output, not the GeoJSON. Florida is ~2.0 GiB in. California is ~3.6 GB in.

**Prediction.** Florida Shapefile: Based on v0 estimates, this should be successful.

California Shapefile: This will be close to being over the limit, and may fail. 

GeoPackage and FileGDB on those same files: Both pass.

**Compare (2.4).** Florida: 

California: 

Gap from the prediction: 

## p2-stream

`ca-1m-seq` and `ca-all-seq` against the matching download rungs in `p2-size`. Streaming reads the input from S3 instead of writing `in.geojson` first. `INPUT_MODE=stream` is not implemented yet.

**Prediction.** Peak disk versus the download: Disk would be significantly lower because we no longer need to download an entire fil e.

Wall time versus the download: Probably longer given the need for context switching.

**Compare (2.4).** What ran: 

Peak disk and wall time versus the download: 

Gap from the prediction: 

## Phase summary (2.4)

Methodology: 

Observations: 

Outcome: 

Prediction versus result, one gap per run: 
