"""Generate seeded synthetic shapes (S1-S6) and broken files (P1-P6) for step H.2 (stdlib only).

    python tools/gen_geojson.py --shape S1 --seed 11 --out s1.geojson          # ~300 MB
    python tools/gen_geojson.py --poison P5 --target-mb 2500 --out p5.geojson   # size from Phase 2

Real footprints all have the same shape, so these files isolate one cost each at a fixed size:

    S1  control: footprint-like 5-vertex polygons, the Microsoft attributes
    S2  per-feature cost: points with no attributes
    S3  per-vertex cost: few polygons of ~10,000 vertices
    S4  attribute width (field count): S1 polygons plus 50 numeric fields
    S5  bytes without work: S1's geometry with 15-decimal coordinates
    S6  attribute width (value length): S1 polygons plus strings over 254 characters

    P1  truncated JSON (cut mid-feature)            P4  one feature over 200 MB
    P2  points, lines and polygons in one file      P5  valid footprints, size set by --target-mb
    P3  field names that collide at 10 characters   P6  invalid UTF-8 inside attribute strings

Definitions agreed on issue #10. Every file uses the Microsoft layout (a FeatureCollection with
one feature per line, so cut_ladder.measure() reads it), has no "id" property (FileGDB rejects
FID 0), and lies around Washington, DC (EPSG:4326). The same arguments and seed always give the
same bytes. Files are written feature by feature, never held in memory.
"""
from __future__ import annotations

import argparse
import json
import math
import random
import sys
from pathlib import Path
from typing import BinaryIO, Iterator

HEADER = b'{\n"type":"FeatureCollection",\n"features":\n[\n'
FOOTER = b"\n]\n}\n"
MB = 1024 * 1024
DEFAULT_TARGET_MB = {"S": 300.0, "P4": 220.0}  # P4 must pass GDAL's 200 MB GeoJSON object limit
SMALL_POISON_FEATURES = 1000                    # P1, P2, P3 and P6 only need to exist
X0, Y0, SPAN = -77.12, 38.80, 0.2               # Washington, DC, as in local/gen_synthetic.py
P3_FIELDS = ("building_height_m", "building_height_ft")  # both start "building_h"


def dumps(obj) -> str:
    return json.dumps(obj, separators=(",", ":"), ensure_ascii=False)


def footprint_ring(rng: random.Random, digits: int | None = 7) -> list[list[float]]:
    """A small closed square (5 positions), like the local catalog's polys-10k."""
    x, y, d = X0 + rng.random() * SPAN, Y0 + rng.random() * SPAN, 0.0001 + rng.random() * 0.0002
    ring = [[x, y], [x + d, y], [x + d, y + d], [x, y + d], [x, y]]
    if digits is None:
        return ring
    return [[round(a, digits), round(b, digits)] for a, b in ring]


def ms_properties(i: int) -> dict:
    return {"release": 2, "capture_dates_range": f"synthetic-{i}"}


def feature(geometry: dict, properties: dict) -> str:
    # Key order matches the Microsoft files: type, geometry, properties.
    return dumps({"type": "Feature", "geometry": geometry, "properties": properties})


def polygon(ring) -> dict:
    return {"type": "Polygon", "coordinates": [ring]}


# --- S1-S6 and P5: one feature per call -------------------------------------------------------

def s1(rng: random.Random, i: int) -> str:
    return feature(polygon(footprint_ring(rng)), ms_properties(i))


def s2(rng: random.Random, i: int) -> str:
    pt = [round(X0 + rng.random() * SPAN, 7), round(Y0 + rng.random() * SPAN, 7)]
    return feature({"type": "Point", "coordinates": pt}, {})


def star_ring(rng: random.Random, n: int, radius: float) -> list[list[float]]:
    """A closed ring of n distinct vertices at increasing angles, so it never self-intersects."""
    cx, cy = X0 + rng.random() * SPAN, Y0 + rng.random() * SPAN
    ring = []
    for k in range(n):
        a, r = 2 * math.pi * k / n, radius * (0.9 + 0.2 * rng.random())
        ring.append([round(cx + r * math.cos(a), 7), round(cy + r * math.sin(a), 7)])
    return ring + [ring[0]]


def s3(rng: random.Random, i: int) -> str:
    return feature(polygon(star_ring(rng, 10_000, 0.01)), ms_properties(i))


def s4(rng: random.Random, i: int) -> str:
    props = {**ms_properties(i), **{f"f{k:02d}": rng.randrange(1_000_000) for k in range(50)}}
    return feature(polygon(footprint_ring(rng)), props)


def s5(rng: random.Random, i: int) -> str:
    # S1's geometry (same random draws), but every coordinate written with 15 decimals.
    ring = footprint_ring(rng, digits=None)
    coords = ",".join(f"[{x:.15f},{y:.15f}]" for x, y in ring)
    geom = '{"type":"Polygon","coordinates":[[' + coords + "]]}"
    return '{"type":"Feature","geometry":' + geom + ',"properties":' + dumps(ms_properties(i)) + "}"


def s6(rng: random.Random, i: int) -> str:
    note = "".join(rng.choice("abcdefghijklmnopqrstuvwxyz ") for _ in range(rng.randint(255, 400)))
    return feature(polygon(footprint_ring(rng)), {**ms_properties(i), "note": note})


SHAPES = {"S1": s1, "S2": s2, "S3": s3, "S4": s4, "S5": s5, "S6": s6}


# --- writing ----------------------------------------------------------------------------------

def write_features(out: BinaryIO, lines: Iterator[bytes], target_bytes: int | None, count: int | None) -> int:
    """Write features one per line until target_bytes is reached or count features are written."""
    out.write(HEADER)
    written, n = len(HEADER), 0
    for line in lines:
        if (target_bytes is not None and written + len(FOOTER) >= target_bytes) or (count is not None and n == count):
            break
        if n:
            out.write(b",\n")
            written += 2
        out.write(line)
        written += len(line)
        n += 1
    out.write(FOOTER)
    return n


def generated(make, rng: random.Random) -> Iterator[bytes]:
    i = 0
    while True:
        yield make(rng, i).encode("utf-8")
        i += 1


def write_p1(out: BinaryIO, rng: random.Random) -> None:
    """Valid features, then the last one cut in half and no closing brackets."""
    out.write(HEADER)
    for i in range(SMALL_POISON_FEATURES):
        out.write((b",\n" if i else b"") + s1(rng, i).encode())
    last = s1(rng, SMALL_POISON_FEATURES).encode()
    out.write(b",\n" + last[: len(last) // 2])


def p2(rng: random.Random, i: int) -> str:
    kind = i % 3
    x, y = round(X0 + rng.random() * SPAN, 7), round(Y0 + rng.random() * SPAN, 7)
    if kind == 0:
        geom = {"type": "Point", "coordinates": [x, y]}
    elif kind == 1:
        geom = {"type": "LineString", "coordinates": [[x, y], [round(x + 0.0002, 7), round(y + 0.0001, 7)]]}
    else:
        geom = polygon(footprint_ring(rng))
    return feature(geom, ms_properties(i))


def p3(rng: random.Random, i: int) -> str:
    h = round(3 + rng.random() * 30, 1)
    return feature(polygon(footprint_ring(rng)), {P3_FIELDS[0]: h, P3_FIELDS[1]: round(h * 3.28084, 1)})


def p6(rng: random.Random, i: int) -> bytes:
    # 0xFF can never appear in UTF-8, so every string holding it is invalid.
    line = s1(rng, i).encode()
    return line.replace(b'"synthetic-', b'"synthetic-\xff\xfe-', 1)


def write_p4(out: BinaryIO, rng: random.Random, target_bytes: int) -> None:
    """One polygon whose single feature line is larger than target_bytes, streamed in chunks."""
    radius, cx, cy = 1.0, -77.02, 38.90   # spacing between vertices stays well above 1e-7
    n = target_bytes // 22 + 1            # each "[-77.xxxxxxx,38.xxxxxxx]," is ~23-25 bytes
    out.write(HEADER)
    out.write(b'{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[')
    first, buf = None, []
    for k in range(n):
        a, r = 2 * math.pi * k / n, radius * (0.95 + 0.1 * rng.random())
        pos = f"[{round(cx + r * math.cos(a), 7)},{round(cy + r * math.sin(a), 7)}]"
        first = first or pos
        buf.append(pos)
        if len(buf) == 100_000:
            out.write((",".join(buf) + ",").encode())
            buf = []
    out.write((",".join(buf + [first])).encode())   # close the ring
    out.write(b']]},"properties":' + dumps(ms_properties(0)).encode() + b"}")
    out.write(FOOTER)


def generate(kind: str, seed: int, out: Path, target_mb: float | None) -> Path:
    # Each kind gets its own random stream for a seed; S5 reuses S1's, so its shapes are S1's.
    rng = random.Random(f"{'S1' if kind == 'S5' else kind}-{seed}")
    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("wb") as f:
        if kind in SHAPES:
            write_features(f, generated(SHAPES[kind], rng), int(target_mb * MB), None)
        elif kind == "P1":
            write_p1(f, rng)
        elif kind in ("P2", "P3"):
            write_features(f, generated(p2 if kind == "P2" else p3, rng), None, SMALL_POISON_FEATURES)
        elif kind == "P4":
            write_p4(f, rng, int(target_mb * MB))
        elif kind == "P5":
            write_features(f, generated(s1, rng), int(target_mb * MB), None)
        elif kind == "P6":
            write_features(f, (p6(rng, i) for i in range(SMALL_POISON_FEATURES)), None, None)
        else:
            raise ValueError(f"unknown kind {kind}")
    return out


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    which = p.add_mutually_exclusive_group(required=True)
    which.add_argument("--shape", choices=sorted(SHAPES))
    which.add_argument("--poison", choices=[f"P{i}" for i in range(1, 7)])
    p.add_argument("--seed", type=int, default=11)
    p.add_argument("--target-mb", type=float, help="file size in MiB (S1-S6, P4, P5)")
    p.add_argument("--out", type=Path, required=True)
    a = p.parse_args(argv)
    kind = a.shape or a.poison
    target = a.target_mb
    if kind == "P5" and target is None:
        p.error("--poison P5 needs --target-mb (its size comes from Phase 2)")
    if target is not None and not (kind in SHAPES or kind in ("P4", "P5")):
        p.error(f"{kind} has a fixed size; --target-mb applies to S1-S6, P4 and P5")
    if target is None:
        target = DEFAULT_TARGET_MB["S"] if kind in SHAPES else DEFAULT_TARGET_MB.get(kind)
    out = generate(kind, a.seed, a.out, target)
    print(out, out.stat().st_size)
    return 0


if __name__ == "__main__":
    sys.exit(main())
