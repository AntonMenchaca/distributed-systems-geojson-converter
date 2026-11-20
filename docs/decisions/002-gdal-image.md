# 002: GDAL image

Date: 2026-11-20 · Decided by: Samuel · Reviewer: Ikwo

## Context

The worker image is built on an OSGeo GDAL 3.13 image. `WORKER_IMAGE` selects that base. Every new Fargate task downloads the image before it can take a job, so the compressed size shows up in Phase 7 scale-out time. The candidates are `ubuntu-full`, `ubuntu-small`, and `alpine-small`. Each must provide GeoJSON, GeoJSONSeq, ESRI Shapefile, GPKG, and OpenFileGDB.

## Measurements

| Image | Drivers present | arm64 | Cold start (s) | Compressed size (MiB) |
| --- | --- | --- | --- | --- |
| ubuntu-full | yes | yes | 48 | 1201 |
| ubuntu-small | yes | yes | 28 | 157.5 |
| alpine-small | yes | yes | 14 | 46.36 |

## Methods

**Drivers present.** `docker run --rm <image> ogrinfo --formats` for `ghcr.io/osgeo/gdal:ubuntu-full-3.13.3`, `ubuntu-small-3.13.3`, and `alpine-small-3.13.3`. Yes means the output lists GeoJSON, GeoJSONSeq, ESRI Shapefile, GPKG, and OpenFileGDB.

**arm64.** The same command with `--platform linux/arm64`. Yes means that container started and printed the formats.

**Compressed size.** After each image was pushed to `gc-samuel-worker`:

```sh
aws ecr describe-images --repository-name gc-samuel-worker \
  --query 'sort_by(imageDetails,& imagePushedAt)[-1].imageSizeInBytes'
```

The byte count is converted to MiB. The applies were `terraform apply -var prefix=gc-samuel -var worker_count=1` (ubuntu-full), then `-var worker_image=gdal-slim` (ubuntu-small), then `-var worker_image=gdal-small` (alpine-small).

**Cold start.** In the AWS console, stopped a running worker task and timed how long the replacement took to start. No CLI command.

## Decision

**Use `alpine-small` for the scale-out spike.** All three images have the drivers and run on arm64. `alpine-small` starts in 14 seconds and is 46.36 MiB compressed, against 28 seconds and 157.5 MiB for `ubuntu-small`, and 48 seconds and 1201 MiB for `ubuntu-full`.

## Consequences

- Handoff to Anton: chosen image `alpine-small`, arm64 yes.
- `gdal-full` (`ubuntu-full` 3.13.3) stays the baseline. `gdal-slim` is `alpine-small` 3.13.3.
