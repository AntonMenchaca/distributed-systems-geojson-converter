# Test data

`fixture-1000.geojson`: 1,000 small polygons around Washington, DC (EPSG:4326), generated with seed 11 by a one-off script; attributes match the Microsoft footprints schema (`release`, `capture_dates_range`; no `id`, because GDAL would take an `id` of 0 as the FID and FileGDB rejects FID 0). Regenerating it is never needed: tests only rely on the feature count.
