package router

import (
	"math"
	"testing"
)

func TestSizeClassUsesThreshold(t *testing.T) {
	est, err := LoadEstimates("../../../config/estimates.json")
	if err != nil {
		t.Fatal(err)
	}
	if est.Version != "v1" {
		t.Fatalf("version %q; this test pins the fitted v1 estimates", est.Version)
	}
	if est.FittedAt == nil || *est.FittedAt == "" {
		t.Fatal("fitted_at: want a non-empty timestamp on v1")
	}
	for _, tc := range []struct {
		bytes int64
		want  string
	}{
		{1_000_000, "small"},
		{3_000_000_000, "large"},
	} {
		class, work, mem := SizeClass(tc.bytes, "gpkg", est)
		if class != tc.want {
			t.Errorf("%d bytes gpkg: class %s (est %.1fs), want %s", tc.bytes, class, work, tc.want)
		}
		if work <= 0 || mem <= 0 {
			t.Errorf("%d bytes: estimates work=%v mem=%v, want positive", tc.bytes, work, mem)
		}
	}
}

const mib = 1 << 20

// testEstimates: gpkg work = 1 + 0.1 × MiB, so the 30 s threshold falls at exactly 290 MiB.
func testEstimates() Estimates {
	f := FormatEstimate{WorkS: Model{1, 0.1}, MemMB: Model{150, 4}}
	return Estimates{Version: "test", SmallThresholdS: 30, Formats: map[string]FormatEstimate{"gpkg": f, "shp": f, "fgdb": f}}
}

func TestBySizeRoutesUnderThresholdToSmall(t *testing.T) {
	for _, tc := range []struct {
		bytes                int64
		wantClass, wantQueue string
	}{
		{1, "small", "jobs-small"},
		{289 * mib, "small", "jobs-small"}, // 29.9 s: under the threshold
		{290 * mib, "large", "jobs-large"}, // exactly 30 s: not under it
		{4000 * mib, "large", "jobs-large"},
	} {
		d, err := Route("by-size", tc.bytes, "gpkg", testEstimates())
		if err != nil {
			t.Fatal(err)
		}
		if d.Class != tc.wantClass || d.Queue != tc.wantQueue {
			t.Errorf("%d bytes: class %s queue %s (est %.2fs), want %s %s",
				tc.bytes, d.Class, d.Queue, d.EstWorkS, tc.wantClass, tc.wantQueue)
		}
	}
}

func TestSingleIgnoresClass(t *testing.T) {
	for _, bytes := range []int64{1, 4000 * mib} { // one small job, one large job
		d, err := Route("single", bytes, "gpkg", testEstimates())
		if err != nil {
			t.Fatal(err)
		}
		if d.Queue != "jobs" {
			t.Errorf("%d bytes (class %s): queue %s, want jobs", bytes, d.Class, d.Queue)
		}
	}
	// The class is still recorded under single, so runs can compare small-job turnaround.
	if d, _ := Route("single", 4000*mib, "gpkg", testEstimates()); d.Class != "large" {
		t.Errorf("class %s, want large", d.Class)
	}
}

func TestMessageCarriesEstWorkAndMem(t *testing.T) {
	d, err := Route("by-size", 100*mib, "gpkg", testEstimates())
	if err != nil {
		t.Fatal(err)
	}
	// 100 MiB: work = 1 + 0.1 × 100 = 11 s, memory = 150 + 4 × 100 = 550 MB.
	if math.Abs(d.EstWorkS-11) > 1e-9 || math.Abs(d.EstMemMB-550) > 1e-9 {
		t.Errorf("est_work_s %v est_mem_mb %v, want 11 and 550", d.EstWorkS, d.EstMemMB)
	}
}

func TestBySizeRejectsUnknownClassAndLayout(t *testing.T) {
	if _, err := Queue("by-size", "medium"); err == nil {
		t.Error("by-size accepted class medium; size_class is small or large (schemas/queue-message.json)")
	}
	if _, err := Queue("round-robin", "small"); err == nil {
		t.Error("accepted QUEUE_LAYOUT=round-robin")
	}
}
