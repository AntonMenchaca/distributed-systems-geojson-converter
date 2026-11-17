package router

import "testing"

func TestSizeClassUsesThreshold(t *testing.T) {
	est, err := LoadEstimates("../../../config/estimates.json")
	if err != nil {
		t.Fatal(err)
	}
	if est.Version != "v0" {
		t.Fatalf("version %q; this test pins the v0 stub", est.Version)
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
