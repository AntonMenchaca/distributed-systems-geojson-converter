package measure

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// A job shorter than the sampling interval must still report its output size.
func TestSampleDiskTakesFinalSample(t *testing.T) {
	dir := t.TempDir()
	stop := SampleDisk(context.Background(), dir, time.Hour)
	if err := os.MkdirAll(filepath.Join(dir, "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out", "a.dbf"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stop(); got != 5000 {
		t.Fatalf("peak %d bytes, want 5000", got)
	}
}

func TestDirSizeMissingPathIsZero(t *testing.T) {
	if got := DirSize(filepath.Join(t.TempDir(), "nope")); got != 0 {
		t.Fatalf("DirSize = %d, want 0", got)
	}
}

// PROFILE=1 reads the child RSS on a 250 ms cadence. A fake clock makes the
// interval exact: three reads across 751 ms, at 250, 500 and 750 ms.
func TestProfileSamplesAt250ms(t *testing.T) {
	if ProfileInterval != 250*time.Millisecond {
		t.Fatalf("ProfileInterval = %s, want 250ms", ProfileInterval)
	}
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var gotAt []time.Duration
		stop := SampleRSS(context.Background(), func() (int64, error) {
			gotAt = append(gotAt, time.Since(start))
			return int64(len(gotAt)) * 1000, nil
		}, ProfileInterval)
		time.Sleep(751 * time.Millisecond)
		peak, samples := stop()

		wantAt := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, 750 * time.Millisecond}
		if len(gotAt) != len(wantAt) {
			t.Fatalf("reads at %v, want %v", gotAt, wantAt)
		}
		for i, at := range wantAt {
			if gotAt[i] != at {
				t.Fatalf("read %d at %s, want %s", i, gotAt[i], at)
			}
		}
		if len(samples) != 3 || samples[0] != 1000 || samples[1] != 2000 || samples[2] != 3000 {
			t.Fatalf("samples %v, want 1000, 2000, 3000", samples)
		}
		if peak != 3000 {
			t.Fatalf("peak %d, want 3000", peak)
		}
	})
}

func TestParseProcStatusRSS(t *testing.T) {
	got, err := parseProcStatusRSS("Name:\togr2ogr\nVmRSS:\t  2048 kB\nVmSize:\t  4096 kB\n")
	if err != nil || got != 2048*1024 {
		t.Fatalf("VmRSS = %d, %v; want %d bytes", got, err, 2048*1024)
	}
	if _, err := parseProcStatusRSS("Name:\togr2ogr\n"); err == nil {
		t.Fatal("missing VmRSS accepted")
	}
}

func TestWriteProfileRefusesUnsetHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteProfile(&buf, nil, nil); err == nil {
		t.Fatal("empty header accepted")
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %q with no header", buf.String())
	}
}

func TestWriteProfileWritesCallerHeader(t *testing.T) {
	var buf bytes.Buffer
	header := []string{"from-the-plan", "also-from-the-plan"}
	if err := WriteProfile(&buf, header, []string{"1", "2"}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "from-the-plan,also-from-the-plan\n1,2\n" {
		t.Fatalf("csv %q", got)
	}
	if err := WriteProfile(&buf, header, []string{"only-one"}); err == nil {
		t.Fatal("short row accepted")
	}
	if strings.Count(buf.String(), "\n") != 2 {
		t.Fatalf("short row was written: %q", buf.String())
	}
}
