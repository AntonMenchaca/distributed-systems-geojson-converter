package gdal

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
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

var target = Target{Name: "local-x_dc_j1", Layer: "dc"}

// Each format ends as one named file that GDAL opens directly, with the dataset as its layer.
func TestConvertAndPackageEachFormat(t *testing.T) {
	requireGDAL(t)
	for format, wantFile := range map[string]string{
		"gpkg": "local-x_dc_j1.gpkg", "shp": "local-x_dc_j1.shp.zip", "fgdb": "local-x_dc_j1.gdb.zip",
	} {
		t.Run(format, func(t *testing.T) {
			outDir := t.TempDir()
			stats, err := Convert(context.Background(), fixture, outDir, format, "defaults", target)
			if err != nil {
				t.Fatal(err)
			}
			if stats.FeatureCount != 1000 {
				t.Fatalf("Stats.FeatureCount = %d, want 1000", stats.FeatureCount)
			}
			file, err := Package(OutputPath(outDir, format, target), format)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(file) != wantFile {
				t.Fatalf("packaged file %s, want %s", filepath.Base(file), wantFile)
			}
			if got := ogrinfoCount(t, file); got != 1000 {
				t.Fatalf("ogrinfo counts %d features in %s, want 1000", got, file)
			}
			if out, _ := exec.Command("ogrinfo", "-so", "-al", file).CombinedOutput(); !strings.Contains(string(out), "Layer name: dc") {
				t.Fatalf("ogrinfo %s: no layer named dc\n%s", file, out)
			}
		})
	}
}

func TestConvertReportsPeakRSS(t *testing.T) {
	requireGDAL(t)
	stats, err := Convert(context.Background(), fixture, t.TempDir(), "gpkg", "defaults", target)
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

// Jobs under the 250 ms /proc interval still get a non-zero profile peak from rusage.
func TestConvertProfilePeakRSSUsesRusageFloor(t *testing.T) {
	requireGDAL(t)
	stats, err := ConvertProfile(context.Background(), fixture, t.TempDir(), "gpkg", "defaults", target)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ProfilePeakRSSBytes <= 0 {
		t.Fatalf("ProfilePeakRSSBytes = %d, want > 0 (rusage floor when /proc may miss)", stats.ProfilePeakRSSBytes)
	}
	if ceilMB(stats.ProfilePeakRSSBytes) < stats.PeakRSSMB {
		t.Fatalf("ProfilePeakRSSBytes %d MiB < PeakRSSMB %d; profile peak must be max(proc, rusage)",
			ceilMB(stats.ProfilePeakRSSBytes), stats.PeakRSSMB)
	}
}

func TestConvertRejectsUnknownFormatAndProfile(t *testing.T) {
	if _, err := Convert(context.Background(), fixture, t.TempDir(), "kml", "defaults", target); err == nil {
		t.Error("format kml accepted")
	}
	if _, err := Convert(context.Background(), fixture, t.TempDir(), "gpkg", "turbo", target); err == nil {
		t.Error("profile turbo accepted")
	}
}

func zipEntries(t *testing.T, path string) []string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	slices.Sort(names)
	return names
}

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLargestShpComponentPicksBiggerOfShpAndDbf(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "dc.shp", "dc.dbf", "dc.shx")
	if err := os.WriteFile(filepath.Join(dir, "dc.shp"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dc.dbf"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LargestShpComponent(dir)
	if err != nil || got != 500 {
		t.Fatalf("LargestShpComponent = %d, %v; want 500", got, err)
	}
}

// A .shp.zip holds the shapefile's files at the zip root, which is how GDAL reads it.
func TestPackageShapefileZipsFilesAtRoot(t *testing.T) {
	out := OutputPath(t.TempDir(), "shp", target)
	writeFiles(t, out, "dc.shp", "dc.dbf", "dc.shx", "dc.prj")
	file, err := Package(out, "shp")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(file) != "local-x_dc_j1.shp.zip" {
		t.Fatalf("packaged %s", file)
	}
	if got, want := zipEntries(t, file), []string{"dc.dbf", "dc.prj", "dc.shp", "dc.shx"}; !slices.Equal(got, want) {
		t.Fatalf("zip entries %v, want %v", got, want)
	}
}

// A .gdb.zip holds the .gdb directory itself, which is how GDAL reads it.
func TestPackageFileGDBZipsTheGDBDirectory(t *testing.T) {
	out := OutputPath(t.TempDir(), "fgdb", target)
	writeFiles(t, out, "a00000001.gdbtable", "gdb")
	file, err := Package(out, "fgdb")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(file) != "local-x_dc_j1.gdb.zip" {
		t.Fatalf("packaged %s", file)
	}
	want := []string{"local-x_dc_j1.gdb/a00000001.gdbtable", "local-x_dc_j1.gdb/gdb"}
	if got := zipEntries(t, file); !slices.Equal(got, want) {
		t.Fatalf("zip entries %v, want %v", got, want)
	}
}

func TestPackageLeavesGeoPackageAsIs(t *testing.T) {
	out := OutputPath(t.TempDir(), "gpkg", target)
	if err := os.WriteFile(out, []byte("gpkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := Package(out, "gpkg")
	if err != nil || file != out {
		t.Fatalf("Package = %s, %v; want %s unchanged", file, err, out)
	}
}
