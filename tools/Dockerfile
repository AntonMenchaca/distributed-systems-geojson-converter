# Tools image: GDAL CLI (ogrinfo for verify.py) plus the tools' Python dependencies.
# Build from the repo root: docker compose build tools
FROM ghcr.io/osgeo/gdal:ubuntu-small-3.13.3@sha256:64250faf833c06d4b21afce4c27190039ba7ab58d70f0eebc87cf77d929c0b40
COPY tools/requirements.txt /tmp/requirements.txt
# The GDAL image ships python3 without ensurepip, so venv needs the distro's python3-venv.
RUN apt-get update && apt-get install -y --no-install-recommends python3-venv && rm -rf /var/lib/apt/lists/*
RUN python3 -m venv --system-site-packages /venv && /venv/bin/pip install --no-cache-dir -q -r /tmp/requirements.txt
ENV PATH=/venv/bin:$PATH
WORKDIR /repo
# Bake the tools and the committed catalog in, so the image also runs as a one-off ECS task
# (tools/stage_datasets.py). Compose mounts the repo over /repo, so local runs still see live files.
COPY tools/ /repo/tools/
COPY datasets/ /repo/datasets/
