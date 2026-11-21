# Build from the repo root: docker build -f worker/Dockerfile .
# Tests that need GDAL:       docker build -f worker/Dockerfile --target test .
#
# GDAL_IMAGE is the WORKER_IMAGE knob's base (knobs.md). Default: GDAL 3.13.3 alpine-small
# (gdal-slim), pinned by its multi-arch index digest (linux/amd64 for Fargate, linux/arm64 for laptops).
# gdal-full: --build-arg GDAL_IMAGE=ghcr.io/osgeo/gdal:ubuntu-full-3.13.3@sha256:2dd0f81ef927ff4c3d4dbe4f73c029dc86d4073974c0564f8e196f6e1412e2e0
ARG GDAL_IMAGE=ghcr.io/osgeo/gdal:alpine-small-3.13.3@sha256:58e21a794e3e49c2882a078cf39625aa8d1f86188e0273f74156e9f3f3cd0253

FROM golang:1.27.1 AS build
WORKDIR /src
COPY jobstore/ ./jobstore/
COPY worker/go.mod worker/go.sum ./worker/
RUN cd worker && go mod download
COPY worker/cmd ./worker/cmd
COPY worker/internal ./worker/internal
# Static binaries run in the GDAL image, which has no Go toolchain.
RUN cd worker && CGO_ENABLED=0 go build -o /out/worker ./cmd/worker \
 && CGO_ENABLED=0 go test -c -o /out/gdal.test ./internal/gdal

FROM ${GDAL_IMAGE} AS test
COPY --from=build /out/gdal.test /w/internal/gdal/gdal.test
COPY worker/testdata /w/testdata
WORKDIR /w/internal/gdal
RUN ./gdal.test -test.v -test.count=1

FROM ${GDAL_IMAGE}
COPY --from=build /out/worker /usr/local/bin/worker
CMD ["worker"]
