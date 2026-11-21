// Package measure samples a job's resource use: the job directory's size over time and the
// conversion child's CPU time and peak memory.
package measure

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProfileInterval is how often PROFILE=1 reads the conversion child's RSS.
// One job runs at a time in that mode, so each sample belongs to that child.
const ProfileInterval = 250 * time.Millisecond

// DirSize sums the sizes of the regular files under path. Missing paths count as 0.
func DirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a file vanished mid-walk (ogr2ogr renames temp files); keep going
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// SampleDisk records DirSize(path) every interval until stop is called. stop takes one
// final sample, so a job shorter than the interval still reports its output size.
func SampleDisk(ctx context.Context, path string, every time.Duration) (stop func() (peakBytes int64)) {
	ctx, cancel := context.WithCancel(ctx)
	peak := make(chan int64, 1)
	go func() {
		var max int64
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				peak <- max
				return
			case <-t.C:
				if n := DirSize(path); n > max {
					max = n
				}
			}
		}
	}()
	return func() int64 {
		cancel()
		max := <-peak
		if n := DirSize(path); n > max {
			max = n
		}
		return max
	}
}

// CPUSeconds is user + system CPU time from a child's rusage.
func CPUSeconds(ru *syscall.Rusage) float64 {
	return float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e9
}

// PeakRSSBytes converts ru_maxrss to bytes: Linux reports kilobytes, macOS bytes.
func PeakRSSBytes(ru *syscall.Rusage) int64 {
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}

// SampleRSS calls read every interval until stop. stop returns the peak and each
// successful reading, in order. A read error is skipped: one bad /proc sample
// should not end the profile or look like a zero-byte process.
func SampleRSS(ctx context.Context, read func() (int64, error), every time.Duration) (stop func() (peak int64, samples []int64)) {
	ctx, cancel := context.WithCancel(ctx)
	type result struct {
		peak    int64
		samples []int64
	}
	done := make(chan result, 1)
	go func() {
		var peak int64
		var samples []int64
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- result{peak, samples}
				return
			case <-t.C:
				n, err := read()
				if err != nil {
					continue
				}
				samples = append(samples, n)
				if n > peak {
					peak = n
				}
			}
		}
	}()
	return func() (int64, []int64) {
		cancel()
		r := <-done
		return r.peak, r.samples
	}
}

// ReadProcRSS reads VmRSS for pid from /proc/<pid>/status. Linux reports kilobytes.
func ReadProcRSS(pid int) (int64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	return parseProcStatusRSS(string(b))
}

func parseProcStatusRSS(status string) (int64, error) {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("measure: bad VmRSS line %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("measure: bad VmRSS line %q", line)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("measure: VmRSS missing")
}

// TODO(2.1): Define profile.csv's header here, as a []string, once the column
// names are copied from geojson-plan-design.md's Phase 2 table. Order in that
// slice is the file's column order. Leave it unset until those names exist;
// WriteProfile refuses an empty header so a guessed schema cannot be written.

// WriteProfile writes one profile.csv record: the header, then one already-measured
// row. header is the column list from the project plan's Phase 2 table. This
// function does not choose those names. An empty header is refused so a guessed
// schema cannot be written. len(row) must match len(header).
func WriteProfile(w io.Writer, header, row []string) error {
	if len(header) == 0 {
		return fmt.Errorf("measure: profile.csv header is unset; it comes from the project plan Phase 2 table")
	}
	if len(row) != len(header) {
		return fmt.Errorf("measure: profile row has %d values for %d columns", len(row), len(header))
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	if err := cw.Write(row); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}
