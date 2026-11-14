"""Write the local catalog's two synthetic datasets (stdlib only; runs inside LocalStack's init hook).

    python3 local/gen_synthetic.py OUT_DIR

pts-1k.geojson: 1,000 points. polys-10k.geojson: 10,000 small squares. Both around Washington,
DC (EPSG:4326), seeded so the bytes (and the sha256 in datasets/local/datasets.csv) never change.
"""
import json
import random
import sys
from pathlib import Path


def feature(geom: dict, i: int) -> dict:
    # Same attribute schema as the Microsoft footprints: no "id" (FileGDB rejects FID 0).
    return {"type": "Feature", "properties": {"release": 2, "capture_dates_range": f"synthetic-{i}"}, "geometry": geom}


def points(n: int, seed: int) -> dict:
    rng = random.Random(seed)
    feats = [feature({"type": "Point", "coordinates": [round(-77.12 + rng.random() * 0.2, 7), round(38.80 + rng.random() * 0.2, 7)]}, i)
             for i in range(n)]
    return {"type": "FeatureCollection", "features": feats}


def squares(n: int, seed: int) -> dict:
    rng = random.Random(seed)
    feats = []
    for i in range(n):
        x, y, d = -77.12 + rng.random() * 0.2, 38.80 + rng.random() * 0.2, 0.0001 + rng.random() * 0.0002
        ring = [[x, y], [x + d, y], [x + d, y + d], [x, y + d], [x, y]]
        feats.append(feature({"type": "Polygon", "coordinates": [[[round(a, 7), round(b, 7)] for a, b in ring]]}, i))
    return {"type": "FeatureCollection", "features": feats}


def main() -> None:
    out = Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)
    for name, doc in (("pts-1k.geojson", points(1000, 11)), ("polys-10k.geojson", squares(10000, 11))):
        (out / name).write_text(json.dumps(doc, separators=(",", ":")))


if __name__ == "__main__":
    main()
