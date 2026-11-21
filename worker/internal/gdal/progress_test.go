package gdal

import "testing"

func TestProgressFractionsFromOGRTicks(t *testing.T) {
	const text = "0...10...20...30...40...50...60...70...80...90...100 - done.\n"
	want := make([]float64, 0, 11)
	for n := 0; n <= 100; n += 10 {
		want = append(want, float64(n)/100)
	}

	// One byte at a time: a trailing "1" must not be reported as 1% before "10" or "100" arrives.
	var got []float64
	w := newProgressWriter(func(f float64) { got = append(got, f) })
	for i := 0; i < len(text); i++ {
		if _, err := w.Write([]byte{text[i]}); err != nil {
			t.Fatal(err)
		}
	}
	assertFractions(t, got, want)

	var once []float64
	if _, err := newProgressWriter(func(f float64) { once = append(once, f) }).Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	assertFractions(t, once, want)
	if once[5] != 0.5 {
		t.Fatalf("tick 50 = %v, want 0.5 (crash_at 0.5)", once[5])
	}
}

func assertFractions(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("fractions %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fractions %v, want %v", got, want)
		}
	}
}
