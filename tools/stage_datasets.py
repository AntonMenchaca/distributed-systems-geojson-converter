"""Stage the dataset catalog in this account's run bucket (step H.1).

Runs inside AWS, after every `terraform apply`: the stack is stateless and `terraform destroy`
deletes the bucket with everything in it (docs/decisions/004-dataset-catalog.md). Downloads the
8 Microsoft states, cuts the California ladder and GeoJSONSeq copies (tools/cut_ladder.py) and
uploads everything under datasets/.

    # every sitting: stage and check each file's sha256 against the committed catalog
    python tools/stage_datasets.py --bucket "$BUCKET"

    # once, to build the catalog: measure every file and write datasets.csv
    python tools/stage_datasets.py --bucket "$BUCKET" --measure --catalog-out /tmp/stage/datasets.csv

With --measure the new catalog is also uploaded to s3://$BUCKET/datasets/datasets.csv, so it can
be fetched from a Fargate task and committed. --only dc stages one state for a quick check.
Disk: California peaks at about 7 GB (unzipped file plus the largest cut); every file is
deleted once uploaded.
"""
from __future__ import annotations

import argparse
import csv
import shutil
import sys
import time
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

import cut_ladder

URL = "https://minedbuildings.z5.web.core.windows.net/legacy/usbuildings-v2/{name}.geojson.zip"
# (dataset_id, Microsoft file name). Chosen in docs/decisions/004-dataset-catalog.md.
STATES = [
    ("dc", "DistrictofColumbia"),
    ("hi", "Hawaii"),
    ("vt", "Vermont"),
    ("mt", "Montana"),
    ("ms", "Mississippi"),
    ("az", "Arizona"),
    ("fl", "Florida"),
    ("ca", "California"),
]
LADDER_STATE = "ca"
COLUMNS = ["dataset_id", "s3_key", "bytes", "features", "geometry_type", "vertex_count",
           "attribute_count", "sha256", "source"]
RETRY_WAITS = (5, 15, 45)       # seconds before each retry of a failed download
MOVED = {403, 404, 409, 410}    # the file is gone or moved: retrying won't help


class MovedError(RuntimeError):
    """The Microsoft host answered, but the file is not there (it moved or was removed)."""


class ShaMismatch(RuntimeError):
    """A staged file is not byte-identical to the one in the committed catalog."""


def download(url: str, dest: Path, waits=RETRY_WAITS, sleep=time.sleep, opener=urllib.request.urlopen) -> Path:
    """Download url to dest, retrying network and 5xx errors; stop at once if the file moved."""
    dest.parent.mkdir(parents=True, exist_ok=True)
    for attempt in range(len(waits) + 1):
        try:
            with opener(url, timeout=60) as r, dest.open("wb") as f:
                shutil.copyfileobj(r, f, cut_ladder.CHUNK)
            return dest
        except urllib.error.HTTPError as e:
            if e.code in MOVED:
                raise MovedError(f"{url}: HTTP {e.code}. The file moved or was removed; check the "
                                 f"Microsoft repo for its new address (decision 004).") from e
            err: Exception = e
        except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
            err = e
        if attempt == len(waits):
            raise RuntimeError(f"{url}: still failing after {attempt + 1} attempts: {err}") from err
        print(f"download failed ({err}); retrying in {waits[attempt]} s", file=sys.stderr)
        sleep(waits[attempt])
    raise AssertionError("unreachable")


def unzip_one(zip_path: Path, dest: Path) -> Path:
    """Extract the single .geojson member of zip_path to dest, then delete the zip."""
    with zipfile.ZipFile(zip_path) as z:
        members = [m for m in z.namelist() if m.endswith(".geojson")]
        if len(members) != 1:
            raise ValueError(f"{zip_path}: expected one .geojson, found {members}")
        with z.open(members[0]) as src, dest.open("wb") as out:
            shutil.copyfileobj(src, out, cut_ladder.CHUNK)
    zip_path.unlink()
    return dest


def key_for(path: Path) -> str:
    return f"datasets/{path.name}"


def row_for(dataset_id: str, path: Path, source: str) -> dict:
    return {"dataset_id": dataset_id, "s3_key": key_for(path), **cut_ladder.measure(path), "source": source}


def check_sha(dataset_id: str, path: Path, catalog: dict[str, dict]) -> None:
    if dataset_id not in catalog:
        raise ShaMismatch(f"{dataset_id}: not in the committed catalog; run with --measure first")
    want, got = catalog[dataset_id]["sha256"], cut_ladder.sha256(path)
    if got != want:
        raise ShaMismatch(f"{dataset_id}: sha256 {got} does not match datasets.csv ({want}). "
                          "The source file changed; do not run experiments on it.")


def read_catalog(path: Path) -> dict[str, dict]:
    with path.open(newline="") as f:
        return {r["dataset_id"]: r for r in csv.DictReader(f)}


def write_catalog(rows: list[dict], path: Path) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=COLUMNS, lineterminator="\n")
        w.writeheader()
        w.writerows(rows)
    return path


def ladder_source(dataset_id: str, url: str) -> str:
    """How a derived file was made, kept free of commas for the CSV."""
    rung = dataset_id.split("-", 1)[1]
    return f"tools/cut_ladder.py {rung} of {url}"


def stage(bucket: str, workdir: Path, catalog: dict[str, dict] | None, only: set[str] | None,
          upload) -> list[dict]:
    """Stage every state (and the ladder). With catalog=None, measure; else check sha256s."""
    rows: list[dict] = []
    workdir.mkdir(parents=True, exist_ok=True)

    def handle(dataset_id: str, path: Path, source: str, keep: bool = False) -> None:
        if catalog is None:
            rows.append(row_for(dataset_id, path, source))
        else:
            check_sha(dataset_id, path, catalog)
        upload(path, bucket, key_for(path))
        if not keep:
            path.unlink()

    for dataset_id, name in STATES:
        if only and dataset_id not in only:
            continue
        url = URL.format(name=name)
        print(f"{dataset_id}: downloading {url}", flush=True)
        src = unzip_one(download(url, workdir / f"{dataset_id}.zip"), workdir / f"{dataset_id}.geojson")
        if dataset_id != LADDER_STATE:
            handle(dataset_id, src, url)
            continue
        # The full file is both the state row and the ladder's "all" rung; it is uploaded once.
        handle(dataset_id, src, url, keep=True)
        state_row = rows[-1] if catalog is None else None
        for rung_id, path in cut_ladder.ladder(src, workdir, prefix=dataset_id):
            if path == src:  # the "all" rung: same file and key as the state row
                if state_row is not None:
                    rows.append({**state_row, "dataset_id": rung_id})
                else:
                    check_sha(rung_id, src, catalog)
                continue
            handle(rung_id, path, ladder_source(rung_id, url))
        src.unlink()
    return rows


def s3_uploader():
    import boto3  # only needed for real runs; tests pass their own uploader

    s3 = boto3.client("s3")

    def upload(path: Path, bucket: str, key: str) -> None:
        print(f"  upload s3://{bucket}/{key} ({path.stat().st_size:,} bytes)", flush=True)
        s3.upload_file(str(path), bucket, key)

    return upload


def main(argv: list[str] | None = None, upload=None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    p.add_argument("--bucket", required=True)
    p.add_argument("--workdir", type=Path, default=Path("/tmp/stage"))
    p.add_argument("--catalog", type=Path, default=Path("datasets/datasets.csv"),
                   help="committed catalog to check sha256s against")
    p.add_argument("--measure", action="store_true", help="build the catalog instead of checking it")
    p.add_argument("--catalog-out", type=Path, help="where --measure writes datasets.csv")
    p.add_argument("--only", help="comma-separated dataset_ids, e.g. dc")
    a = p.parse_args(argv)
    if a.measure and not a.catalog_out:
        p.error("--measure needs --catalog-out")
    upload = upload or s3_uploader()
    only = set(a.only.split(",")) if a.only else None
    catalog = None if a.measure else read_catalog(a.catalog)
    rows = stage(a.bucket, a.workdir, catalog, only, upload)
    if a.measure:
        out = write_catalog(rows, a.catalog_out)
        upload(out, a.bucket, "datasets/datasets.csv")
        print(f"wrote {out} ({len(rows)} rows)")
    else:
        print("staged; every sha256 matches datasets.csv")
    return 0


if __name__ == "__main__":
    sys.exit(main())
