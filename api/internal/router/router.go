// Package router decides where a job goes: its size class and its queue.
package router

import (
	"encoding/json"
	"fmt"
	"os"
)

// Model is a linear model in input MB: value = a + b × input_mb.
type Model [2]float64

func (m Model) At(inputMB float64) float64 { return m[0] + m[1]*inputMB }

// FormatEstimate is one format's entry in config/estimates.json.
type FormatEstimate struct {
	WorkS    Model    `json:"work_s"`
	MemMB    Model    `json:"mem_mb"`
	DiskMB   Model    `json:"disk_mb"`
	OutRatio float64  `json:"out_ratio"`
	FitR2    *float64 `json:"fit_r2"`
}

// Estimates is config/estimates.json. v0 holds placeholder values, not yet fitted from measurements.
type Estimates struct {
	Version         string                    `json:"version"`
	FittedAt        *string                   `json:"fitted_at"`
	SmallThresholdS float64                   `json:"small_threshold_s"`
	Formats         map[string]FormatEstimate `json:"formats"`
}

func LoadEstimates(path string) (Estimates, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Estimates{}, fmt.Errorf("estimates: %w", err)
	}
	var est Estimates
	if err := json.Unmarshal(raw, &est); err != nil {
		return Estimates{}, fmt.Errorf("estimates %s: %w", path, err)
	}
	for _, f := range []string{"gpkg", "shp", "fgdb"} {
		if _, ok := est.Formats[f]; !ok {
			return Estimates{}, fmt.Errorf("estimates %s: missing format %q", path, f)
		}
	}
	if est.SmallThresholdS <= 0 {
		return Estimates{}, fmt.Errorf("estimates %s: small_threshold_s must be positive", path)
	}
	return est, nil
}

// bytesPerMB: estimates are fitted in decimal megabytes.
const bytesPerMB = 1_000_000

// SizeClass predicts a job's work and peak memory and classes it small when the predicted
// work is under small_threshold_s. Callers validate format first; an unknown format is
// classed large with zero estimates.
func SizeClass(inputBytes int64, format string, est Estimates) (class string, estWorkS, estMemMB float64) {
	f, ok := est.Formats[format]
	if !ok {
		return "large", 0, 0
	}
	mb := float64(inputBytes) / bytesPerMB
	estWorkS, estMemMB = f.WorkS.At(mb), f.MemMB.At(mb)
	if estWorkS < est.SmallThresholdS {
		return "small", estWorkS, estMemMB
	}
	return "large", estWorkS, estMemMB
}

// Queue returns the queue name for a size class under QUEUE_LAYOUT.
func Queue(layout, class string) (string, error) {
	switch layout {
	case "single":
		return "jobs", nil
	case "by-size":
		return "", fmt.Errorf("QUEUE_LAYOUT=by-size: not implemented yet")
	default:
		return "", fmt.Errorf("QUEUE_LAYOUT=%q: want single or by-size", layout)
	}
}
