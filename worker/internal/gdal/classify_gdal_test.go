package gdal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These run the real ogr2ogr, so they check Permanent against GDAL's own output, not only the
// captured strings in classify_test.go. They skip on a laptop without GDAL (see requireGDAL).
func TestRealGDALFailuresClassify(t *testing.T) {
	requireGDAL(t)
	dir := t.TempDir()

	// One point and one line: fine for GeoPackage, refused by a shapefile or FileGDB layer.
	valid := `{"type":"FeatureCollection","features":[` +
		`{"type":"Feature","properties":{"name":"a"},"geometry":{"type":"Point","coordinates":[-77.0,38.9]}},` +
		`{"type":"Feature","properties":{"name":"b"},"geometry":{"type":"LineString","coordinates":[[-77.0,38.9],[-77.1,38.95]]}}]}`
	mixed := filepath.Join(dir, "mixed.geojson")
	truncated := filepath.Join(dir, "truncated.geojson")
	missing := filepath.Join(dir, "missing.geojson")
	mustWrite(t, mixed, valid)
	mustWrite(t, truncated, valid[:len(valid)-40]) // cut off in the middle of the last feature

	ctx := context.Background()
	for _, c := range []struct {
		name, input, format string
		want                bool
	}{
		{"truncated JSON, gpkg", truncated, "gpkg", true},
		{"truncated JSON, shp", truncated, "shp", true},
		{"mixed geometry, shp", mixed, "shp", true},
		{"mixed geometry, fgdb", mixed, "fgdb", true},
		// A file that is not there yet is a transient problem, not a bad input.
		{"missing input, gpkg", missing, "gpkg", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Convert(ctx, c.input, t.TempDir(), c.format, "defaults", target)
			if err == nil {
				t.Fatal("Convert succeeded; want a failure")
			}
			if got := Permanent(err); got != c.want {
				t.Fatalf("Permanent = %v, want %v for error: %v", got, c.want, err)
			}
		})
	}
}

// The other half of the mixed-geometry rule: GeoPackage converts the same file, so it is not
// a failure at all and nothing is classified.
func TestRealGDALMixedGeometryConvertsToGeoPackage(t *testing.T) {
	requireGDAL(t)
	dir := t.TempDir()
	mixed := filepath.Join(dir, "mixed.geojson")
	mustWrite(t, mixed, `{"type":"FeatureCollection","features":[`+
		`{"type":"Feature","properties":{"name":"a"},"geometry":{"type":"Point","coordinates":[-77.0,38.9]}},`+
		`{"type":"Feature","properties":{"name":"b"},"geometry":{"type":"LineString","coordinates":[[-77.0,38.9],[-77.1,38.95]]}}]}`)
	stats, err := Convert(context.Background(), mixed, t.TempDir(), "gpkg", "defaults", target)
	if err != nil {
		t.Fatalf("gpkg conversion of mixed geometry: %v", err)
	}
	if stats.FeatureCount != 2 {
		t.Fatalf("FeatureCount = %d, want 2", stats.FeatureCount)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
