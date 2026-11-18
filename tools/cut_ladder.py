"""Measure GeoJSON datasets and cut the California size ladder (stdlib only).

Reads files laid out like the Microsoft US Building Footprints: a FeatureCollection with one
feature per line. That layout lets every operation stream line by line, so a 3.35 GiB file is
never held in memory. A line that is neither wrapper nor a single feature stops the run.

    python tools/cut_ladder.py --src California.geojson --out-dir /tmp/stage

writes ca-1k ... ca-3m (.geojson) and the GeoJSONSeq copies ca-1m-seq and ca-all-seq
(.geojsonl). The full file itself is the "all" rung, so it is not copied.

measure() returns the datasets.csv columns that come from the file: bytes, features,
geometry_type, vertex_count, attribute_count, sha256.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from pathlib import Path
from typing import Iterator

# The wrapper lines of a Microsoft file, stripped. Anything else must be one feature.
WRAPPER = {"{", "}", "[", "]", "],", '"type":"FeatureCollection",', '"features":', '"features":['}

# California ladder rungs (dataset_id suffix, feature count). "all" is the source file itself.
RUNGS = [("1k", 1_000), ("10k", 10_000), ("100k", 100_000), ("1m", 1_000_000), ("3m", 3_000_000)]
SEQ_COPIES = ["1m", "all"]  # GeoJSONSeq copies, per the step plan

HEADER = '{\n"type":"FeatureCollection",\n"features":\n[\n'
FOOTER = "]\n}\n"
CHUNK = 1 << 20


class LayoutError(ValueError):
    """The file is not one feature per line."""


def feature_lines(path: Path) -> Iterator[str]:
    """Yield each feature's JSON text (indentation and trailing comma removed), in file order."""
    with path.open("r", encoding="utf-8") as f:
        for lineno, raw in enumerate(f, 1):
            s = raw.strip()
            if not s or s in WRAPPER:
                continue
            if s.endswith(","):
                s = s[:-1]
            if not s.startswith("{") or s.startswith('{"type":"FeatureCollection"'):
                raise LayoutError(f"{path}:{lineno}: not one feature per line")
            yield s


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(CHUNK), b""):
            h.update(block)
    return h.hexdigest()


def count_positions(coords) -> int:
    """Number of coordinate pairs, counting each ring's closing point (it repeats the first)."""
    if not coords:
        return 0
    if isinstance(coords[0], (int, float)):
        return 1
    return sum(count_positions(c) for c in coords)


def geometry_vertices(geom: dict | None) -> int:
    if not geom:
        return 0
    if geom["type"] == "GeometryCollection":
        return sum(geometry_vertices(g) for g in geom["geometries"])
    return count_positions(geom["coordinates"])


def measure(path: Path) -> dict:
    """One streaming pass for the counts, one for the sha256."""
    features = vertices = 0
    types: set[str] = set()
    keys: set[str] = set()
    for lineno, s in enumerate(feature_lines(path), 1):
        try:
            feat = json.loads(s)
        except json.JSONDecodeError as e:
            raise LayoutError(f"{path}: feature {lineno} is not valid JSON: {e}") from e
        if feat.get("type") != "Feature":
            raise LayoutError(f"{path}: line item {lineno} is a {feat.get('type')!r}, not a Feature")
        features += 1
        geom = feat.get("geometry")
        if geom:
            types.add(geom["type"])
        vertices += geometry_vertices(geom)
        keys.update((feat.get("properties") or {}).keys())
    return {
        "bytes": path.stat().st_size,
        "features": features,
        "geometry_type": "|".join(sorted(types)) or "None",
        "vertex_count": vertices,
        "attribute_count": len(keys),
        "sha256": sha256(path),
    }


def cut(src: Path, out: Path, n: int) -> Path:
    """Write the first n features of src as a FeatureCollection, one feature per line."""
    out.parent.mkdir(parents=True, exist_ok=True)
    written = 0
    with out.open("w", encoding="utf-8", newline="\n") as f:
        f.write(HEADER)
        for s in feature_lines(src):
            if written == n:
                break
            f.write((",\n" if written else "") + s)
            written += 1
        f.write("\n" + FOOTER)
    if written < n:
        out.unlink()
        raise ValueError(f"{src} has {written} features, fewer than {n}")
    return out


def to_seq(src: Path, out: Path, n: int | None = None) -> Path:
    """Write features as GeoJSONSeq: one feature per line, no wrapper (newline-delimited)."""
    out.parent.mkdir(parents=True, exist_ok=True)
    written = 0
    with out.open("w", encoding="utf-8", newline="\n") as f:
        for s in feature_lines(src):
            if n is not None and written == n:
                break
            f.write(s + "\n")
            written += 1
    if n is not None and written < n:
        out.unlink()
        raise ValueError(f"{src} has {written} features, fewer than {n}")
    return out


def ladder(src: Path, out_dir: Path, prefix: str = "ca") -> list[tuple[str, Path]]:
    """Cut every rung and GeoJSONSeq copy. Returns (dataset_id, path) in catalog order."""
    made: list[tuple[str, Path]] = []
    sizes = dict(RUNGS)
    for name, n in RUNGS:
        made.append((f"{prefix}-{name}", cut(src, out_dir / f"{prefix}-{name}.geojson", n)))
    made.append((f"{prefix}-all", src))
    for name in SEQ_COPIES:
        n = None if name == "all" else sizes[name]
        made.append((f"{prefix}-{name}-seq", to_seq(src, out_dir / f"{prefix}-{name}-seq.geojsonl", n)))
    return made


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    p.add_argument("--src", required=True, type=Path, help="full California .geojson")
    p.add_argument("--out-dir", required=True, type=Path)
    p.add_argument("--prefix", default="ca")
    a = p.parse_args(argv)
    for dataset_id, path in ladder(a.src, a.out_dir, a.prefix):
        print(dataset_id, path, os.path.getsize(path))
    return 0


if __name__ == "__main__":
    sys.exit(main())
