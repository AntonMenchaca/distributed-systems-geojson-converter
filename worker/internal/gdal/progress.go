package gdal

import (
	"context"
	"strconv"
)

type progressKey struct{}

// WithProgress returns a context Convert uses to report ogr2ogr's -progress fraction,
// in [0, 1], as GDAL prints it (0...10...20...100 - done.). Jobs that are not
// measuring progress convert exactly as before.
func WithProgress(ctx context.Context, fn func(float64)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

func progressFrom(ctx context.Context) func(float64) {
	fn, _ := ctx.Value(progressKey{}).(func(float64))
	return fn
}

// progressWriter parses ogr2ogr -progress text. A number is only delivered once the
// following byte shows it is complete, so a chunk that ends on "1" is held until it
// can be told apart from "10" or "100".
type progressWriter struct {
	fn  func(float64)
	buf []byte
}

func newProgressWriter(fn func(float64)) *progressWriter {
	return &progressWriter{fn: fn}
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for len(w.buf) > 0 {
		i := 0
		for i < len(w.buf) && w.buf[i] >= '0' && w.buf[i] <= '9' {
			i++
		}
		if i == 0 {
			w.buf = w.buf[1:]
			continue
		}
		if i == len(w.buf) {
			break
		}
		n, err := strconv.Atoi(string(w.buf[:i]))
		w.buf = w.buf[i:]
		if err == nil && n >= 0 && n <= 100 {
			w.fn(float64(n) / 100)
		}
	}
	return len(p), nil
}
