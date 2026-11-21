// Package router decides where a job goes: its size class and its queue.
package router

import (
	"encoding/json"
	"fmt"
	"os"
)

// Model is a linear model in input MiB: value = a + b × input_mib.
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

// bytesPerMiB: estimates are fitted in MiB (bytes / 2^20), the unit tools/gen_trace.py uses
// and the one 2.3 fits config/estimates.json in (agreed on issue #11).
const bytesPerMiB = 1 << 20

// SizeClass predicts a job's work and peak memory and classes it small when the predicted
// work is under small_threshold_s. Callers validate format first; an unknown format is
// classed large with zero estimates.
func SizeClass(inputBytes int64, format string, est Estimates) (class string, estWorkS, estMemMB float64) {
	f, ok := est.Formats[format]
	if !ok {
		return "large", 0, 0
	}
	mib := float64(inputBytes) / bytesPerMiB
	estWorkS, estMemMB = f.WorkS.At(mib), f.MemMB.At(mib)
	if estWorkS < est.SmallThresholdS {
		return "small", estWorkS, estMemMB
	}
	return "large", estWorkS, estMemMB
}

// Queue returns the queue name for a size class under QUEUE_LAYOUT. single sends every job
// to "jobs" (the baseline: one FCFS line, so small jobs wait behind large ones); by-size sends
// small jobs to "jobs-small" and large ones to "jobs-large", the names terraform/modules/sqs
// and local/init.sh create.
func Queue(layout, class string) (string, error) {
	switch layout {
	case "single":
		return "jobs", nil
	case "by-size":
		switch class {
		case "small":
			return "jobs-small", nil
		case "large":
			return "jobs-large", nil
		default:
			return "", fmt.Errorf("size class %q: want small or large", class)
		}
	default:
		return "", fmt.Errorf("QUEUE_LAYOUT=%q: want single or by-size", layout)
	}
}

// Decision is where one job goes and what it is predicted to cost. The API copies it into
// the job record and the queue message (schemas/queue-message.json: size_class, est_work_s,
// est_mem_mb), so workers and analysis see the same prediction the router used.
type Decision struct {
	Class    string
	Queue    string
	EstWorkS float64
	EstMemMB float64
}

// Route classes a job and picks its queue under QUEUE_LAYOUT.
func Route(layout string, inputBytes int64, format string, est Estimates) (Decision, error) {
	class, work, mem := SizeClass(inputBytes, format, est)
	queue, err := Queue(layout, class)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Class: class, Queue: queue, EstWorkS: work, EstMemMB: mem}, nil
}
