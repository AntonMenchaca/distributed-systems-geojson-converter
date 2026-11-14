// Package measure samples a job's resource use: the job directory's size over time and the
// conversion child's CPU time and peak memory.
package measure

import (
	"context"
	"io/fs"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

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
