package gdal

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

const fixture = "../../testdata/fixture-1000.geojson"

// requireGDAL skips when ogr2ogr is missing or broken (e.g. a laptop); CI runs these in the GDAL image.
func requireGDAL(t *testing.T) {
	t.Helper()
	if err := exec.Command("ogr2ogr", "--version").Run(); err != nil {
		t.Skipf("ogr2ogr not usable here (%v); run in the worker test image: docker build -f worker/Dockerfile --target test .", err)
	}
}

var ogrinfoCountRE = regexp.MustCompile(`Feature Count: (\d+)`)

// ogrinfoCount asks ogrinfo, independently of Convert, how many features the output holds.
func ogrinfoCount(t *testing.T, path string) int {
	t.Helper()
	out, err := exec.Command("ogrinfo", "-so", "-al", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ogrinfo %s: %v\n%s", path, err, out)
	}
	total := 0
	for _, m := range ogrinfoCountRE.FindAllSubmatch(out, -1) {
		n, _ := strconv.Atoi(string(m[1]))
		total += n
	}
	return total
}

func TestConvertEachFormat(t *testing.T) {
	requireGDAL(t)
	for _, format := range []string{"gpkg", "shp", "fgdb"} {
		t.Run(format, func(t *testing.T) {
			outDir := t.TempDir()
			stats, err := Convert(context.Background(), fixture, outDir, format, "defaults")
			if err != nil {
				t.Fatal(err)
			}
			if got := ogrinfoCount(t, OutputPath(outDir, format)); got != 1000 {
				t.Fatalf("ogrinfo counts %d features, want 1000", got)
			}
			if stats.FeatureCount != 1000 {
				t.Fatalf("Stats.FeatureCount = %d, want 1000", stats.FeatureCount)
			}
		})
	}
}

func TestConvertReportsPeakRSS(t *testing.T) {
	requireGDAL(t)
	stats, err := Convert(context.Background(), fixture, t.TempDir(), "gpkg", "defaults")
	if err != nil {
		t.Fatal(err)
	}
	if stats.PeakRSSMB <= 0 {
		t.Fatalf("PeakRSSMB = %d, want > 0", stats.PeakRSSMB)
	}
	if stats.WallMS <= 0 || stats.PeakDiskMB < 0 {
		t.Fatalf("stats %+v: want positive wall time", stats)
	}
}

func TestConvertRejectsUnknownFormatAndProfile(t *testing.T) {
	if _, err := Convert(context.Background(), fixture, t.TempDir(), "kml", "defaults"); err == nil {
		t.Error("format kml accepted")
	}
	if _, err := Convert(context.Background(), fixture, t.TempDir(), "gpkg", "turbo"); err == nil {
		t.Error("profile turbo accepted")
	}
}
