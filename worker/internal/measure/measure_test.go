package measure

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
