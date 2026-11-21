// Package gdal runs one ogr2ogr child process per job. The child is the crash boundary and
// gives exact per-job CPU and peak memory, which become config/estimates.json.
package gdal

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/measure"
)

type Stats struct {
	WallMS, CPUS          float64
	PeakRSSMB, PeakDiskMB int64
	FeatureCount          int64
	// OgrinfoMS is the separate `ogrinfo -so -al` pass. It is not part of WallMS,
	// which stays the ogr2ogr conversion the job record stores as convert_ms.
	OgrinfoMS float64
	// ProfilePeakRSSBytes is for profile.csv peak_rss_mb under ConvertProfile: the
	// larger of the /proc RSS sample peak (measure.ProfileInterval) and the child's
	// rusage peak. Short jobs can finish before the first /proc tick, so rusage
	// keeps fit.py from seeing zeros. PeakRSSMB stays the rusage figure alone for
	// the job record / attempts.csv.
	ProfilePeakRSSBytes int64
}

// Target names one conversion's output: Name is the output's base name (Package's file is
// Name plus the format's extension) and Layer is the layer inside it.
type Target struct{ Name, Layer string }

// For each output format: the ogr2ogr driver (-f), the suffix ogr2ogr writes to (a file for
// gpkg, a directory for shp and fgdb), and the suffix of the single file Package returns.
// GDAL opens .shp.zip and .gdb.zip directly, so the packaged file needs no unzipping.
var formats = map[string]struct{ driver, out, packaged string }{
	"gpkg": {"GPKG", ".gpkg", ".gpkg"},
	"shp":  {"ESRI Shapefile", "", ".shp.zip"},
	"fgdb": {"OpenFileGDB", ".gdb", ".gdb.zip"},
}

// profiles is the GDAL_PROFILE knob. gt1 commits one transaction per feature, reproducing a
// hand-written write loop; tuned (per-format settings) is not implemented yet.
var profiles = map[string][]string{
	"defaults": nil,
	"gt1":      {"-gt", "1"},
}

// OutputPath is where Convert writes format's output inside outDir (a file for gpkg, a
// directory for shp and fgdb).
func OutputPath(outDir, format string, t Target) string {
	return filepath.Join(outDir, t.Name+formats[format].out)
}

const mb = 1 << 20

// Convert runs ogr2ogr on in, writing t's output into outDir, and measures it.
func Convert(ctx context.Context, in, outDir, format, profile string, t Target) (Stats, error) {
	return convert(ctx, in, outDir, format, profile, t, false)
}

// ConvertProfile is Convert with PROFILE=1 measurement: while ogr2ogr runs, the
// child's RSS is read from /proc every measure.ProfileInterval. Disk is still
// sampled once a second, the same as Convert.
func ConvertProfile(ctx context.Context, in, outDir, format, profile string, t Target) (Stats, error) {
	return convert(ctx, in, outDir, format, profile, t, true)
}

func convert(ctx context.Context, in, outDir, format, profile string, t Target, measureProfile bool) (Stats, error) {
	f, ok := formats[format]
	if !ok {
		return Stats{}, fmt.Errorf("gdal: unknown format %q", format)
	}
	extra, ok := profiles[profile]
	if !ok {
		if profile == "tuned" {
			return Stats{}, fmt.Errorf("gdal: GDAL_PROFILE=tuned is not implemented yet")
		}
		return Stats{}, fmt.Errorf("gdal: unknown GDAL_PROFILE %q", profile)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Stats{}, err
	}
	out := OutputPath(outDir, format, t)
	args := append([]string{"-f", f.driver, out, in, "-nln", t.Layer}, extra...)
	// -progress only when a caller asked for it (chaos crash_at / slow). The text goes to
	// stdout, which Convert otherwise leaves unused, so a normal job's command is unchanged.
	// Stdout is set only when that writer exists: a nil pointer in the io.Writer interface
	// is not a nil interface, and os/exec would call Write on it.
	fn := progressFrom(ctx)
	if fn != nil {
		args = append(args, "-progress")
	}
	cmd := exec.CommandContext(ctx, "ogr2ogr", args...)
	if fn != nil {
		cmd.Stdout = newProgressWriter(fn)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stopDisk := measure.SampleDisk(ctx, outDir, time.Second)
	start := time.Now()
	runErr := cmd.Start()
	var stopRSS func() (int64, []int64)
	if runErr == nil && measureProfile && cmd.Process != nil {
		pid := cmd.Process.Pid
		stopRSS = measure.SampleRSS(ctx, func() (int64, error) {
			return measure.ReadProcRSS(pid)
		}, measure.ProfileInterval)
	}
	if runErr == nil {
		runErr = cmd.Wait()
	}
	wall := time.Since(start)
	peakDisk := stopDisk()

	var stats Stats
	stats.WallMS = float64(wall.Microseconds()) / 1000
	stats.PeakDiskMB = ceilMB(peakDisk)
	if ru, ok := sysUsage(cmd); ok {
		stats.CPUS = measure.CPUSeconds(ru)
		rusageRSS := measure.PeakRSSBytes(ru)
		stats.PeakRSSMB = ceilMB(rusageRSS)
		if measureProfile {
			stats.ProfilePeakRSSBytes = procRSS
			if rusageRSS > stats.ProfilePeakRSSBytes {
				stats.ProfilePeakRSSBytes = rusageRSS
			}
		}
	} else if measureProfile {
		stats.ProfilePeakRSSBytes = procRSS
	}
	if runErr != nil {
		return stats, fmt.Errorf("gdal: ogr2ogr %s: %w: %s", format, runErr, lastBytes(stderr.Bytes(), 2048))
	}
	// Timed apart from WallMS. This is the same ogrinfo -so -al pass that counts
	// features; it does not run inside the ogr2ogr child.
	infoAt := time.Now()
	n, err := FeatureCount(ctx, out)
	stats.OgrinfoMS = float64(time.Since(infoAt).Microseconds()) / 1000
	if err != nil {
		return stats, err
	}
	stats.FeatureCount = n
	return stats, nil
}

func sysUsage(cmd *exec.Cmd) (*syscall.Rusage, bool) {
	if cmd.ProcessState == nil {
		return nil, false
	}
	ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	return ru, ok
}

func ceilMB(b int64) int64 { return (b + mb - 1) / mb }

func lastBytes(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

var featureCountRE = regexp.MustCompile(`Feature Count: (\d+)`)

// FeatureCount sums ogrinfo's per-layer feature counts for path.
func FeatureCount(ctx context.Context, path string) (int64, error) {
	out, err := exec.CommandContext(ctx, "ogrinfo", "-ro", "-so", "-al", path).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("gdal: ogrinfo %s: %w: %s", path, err, lastBytes(out, 2048))
	}
	var total int64
	for _, m := range featureCountRE.FindAllSubmatch(out, -1) {
		n, _ := strconv.ParseInt(string(m[1]), 10, 64)
		total += n
	}
	return total, nil
}
