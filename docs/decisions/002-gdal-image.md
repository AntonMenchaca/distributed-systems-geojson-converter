# 002: GDAL image

Date: 2026-11-20 · Decided by: Samuel · Reviewer: Ikwo

## Context

The worker image is built on an OSGeo GDAL 3.13 image. `WORKER_IMAGE` selects that base. Every new Fargate task downloads the image before it can take a job, so the compressed size shows up in Phase 7 scale-out time. The candidates are `ubuntu-full`, `ubuntu-small`, and `alpine-small`. Each must provide GeoJSON, GeoJSONSeq, ESRI Shapefile, GPKG, and OpenFileGDB.

## Measurements

Cold start is a Fargate cold start in `gc-samuel`. "Drivers present" means `ogrinfo --formats` lists all five drivers above.

| Image | Drivers present | arm64 | Cold start (s) | Compressed size (MiB) |
| --- | --- | --- | --- | --- |
| ubuntu-full | yes | yes | 48 | 1201 |
| ubuntu-small | yes | yes | 28 | 157.5 |
| alpine-small | yes | yes | 14 | 46.36 |

## Decision

**Use `alpine-small` for the scale-out spike.** All three images have the drivers and run on arm64. `alpine-small` starts in 14 seconds and is 46.36 MiB compressed, against 28 seconds and 157.5 MiB for `ubuntu-small`, and 48 seconds and 1201 MiB for `ubuntu-full`.

## Consequences

- Handoff to Anton: chosen image `alpine-small`, arm64 yes.
- `gdal-full` (`ubuntu-full` 3.13.3) stays the baseline. `gdal-slim` is `alpine-small` 3.13.3.
