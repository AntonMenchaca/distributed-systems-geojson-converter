package gdal

import (
	"errors"
	"strings"
	"testing"
)

// The stderr text below is what GDAL 3.13 printed for the H.2 poison files (see classify.go).
const (
	p1Stderr = "ERROR 1: At line 1005, character 30: Unterminated object\n" +
		"ERROR 4: Failed to read GeoJSON data\n" +
		"ERROR 1: Unable to open datasource `/data/p1.geojson' with the following drivers.\n"
	p4Stderr = "ERROR 1: At line 5, character 4772603: GeoJSON object too complex/large. " +
		"You may define the OGR_GEOJSON_MAX_OBJ_SIZE configuration option to a value in megabytes " +
		"to allow for larger features, or 0 to remove any size limit.\n" +
		"ERROR 4: Failed to read GeoJSON data\n"
	p2ShpStderr = "Warning 6: Normalized/laundered field name: 'capture_dates_range' to 'capture_da'\n" +
		"ERROR 1: Attempt to write non-point (LINESTRING) geometry to point shapefile.\n" +
		"ERROR 1: Unable to write feature 1 from layer p2.\n"
	p2FGDBStderr = "ERROR 6: Unsupported geometry type\n" +
		"ERROR 1: Terminating translation prematurely after failed\n"
)

var exitStatus1 = errors.New("exit status 1")

func convertErr(format, stderr string) error {
	return &ConvertError{Format: format, Stderr: []byte(stderr), Err: exitStatus1}
}

func TestTruncatedJSONIsPermanent(t *testing.T) {
	for _, format := range []string{"gpkg", "shp", "fgdb"} {
		if !Permanent(convertErr(format, p1Stderr)) {
			t.Errorf("%s: truncated JSON should be permanent", format)
		}
	}
}

// The message must still be found when a lot of warning text comes before it, which is the
// case the 2 KB tail in Error() could not cover.
func TestTruncatedJSONIsPermanentWhenBuriedInWarnings(t *testing.T) {
	noise := strings.Repeat("Warning 6: Normalized/laundered field name: 'some_long_field_name' to 'some_lo'\n", 60)
	if len(noise) < 4096 {
		t.Fatalf("noise is %d bytes; the test needs more than the 2 KB tail", len(noise))
	}
	if !Permanent(convertErr("gpkg", noise+p1Stderr)) {
		t.Fatal("truncated JSON after 4 KB of warnings should still be permanent")
	}
}

func TestOOMIsNotPermanent(t *testing.T) {
	// An OOM kill ends ogr2ogr with a signal and no GDAL message.
	killed := &ConvertError{Format: "gpkg", Stderr: nil, Err: errors.New("signal: killed")}
	if Permanent(killed) {
		t.Fatal("an ogr2ogr killed for memory must be transient")
	}
	if Permanent(convertErr("gpkg", "")) {
		t.Fatal("an exit with no GDAL message must be transient")
	}
}

func TestWarningsAloneAreNotPermanent(t *testing.T) {
	warnOnly := "Warning 6: Normalized/laundered field name: 'capture_dates_range' to 'capture_da'\n"
	if Permanent(convertErr("shp", warnOnly)) {
		t.Fatal("a warning is not a failure; only the ERROR lines decide")
	}
}

func TestP4IsPermanentInEveryFormat(t *testing.T) {
	for _, format := range []string{"gpkg", "shp", "fgdb"} {
		if !Permanent(convertErr(format, p4Stderr)) {
			t.Errorf("%s: a GeoJSON object over the size limit should be permanent", format)
		}
	}
}

// P2 fails only for the single-geometry formats. GeoPackage accepts mixed types, so the same
// file's shapefile message must not make a GeoPackage job permanent.
func TestMixedGeometryIsPermanentOnlyForItsFormat(t *testing.T) {
	cases := []struct {
		format, stderr string
		want           bool
	}{
		{"shp", p2ShpStderr, true},
		{"gpkg", p2ShpStderr, false},
		{"fgdb", p2FGDBStderr, true},
		{"shp", p2FGDBStderr, false},
	}
	for _, c := range cases {
		if got := Permanent(convertErr(c.format, c.stderr)); got != c.want {
			t.Errorf("Permanent(%s, %q...) = %v, want %v", c.format, c.stderr[:20], got, c.want)
		}
	}
}

func TestNonConvertErrorsAreTransient(t *testing.T) {
	if Permanent(errors.New("boom")) {
		t.Fatal("a plain error (download, upload, ogrinfo) must be transient")
	}
	if Permanent(nil) {
		t.Fatal("no error is not permanent")
	}
}

func TestConvertErrorMessageKeepsTheLogTail(t *testing.T) {
	msg := convertErr("gpkg", p1Stderr).Error()
	if !strings.HasPrefix(msg, "gdal: ogr2ogr gpkg: exit status 1: ") || !strings.Contains(msg, "Failed to read GeoJSON data") {
		t.Fatalf("message %q", msg)
	}
}
