package jobs

import (
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
)

// jobResponse is the Job schema in api.yaml. Unset timestamps are omitted, not zero.
type jobResponse struct {
	JobID        string            `json:"job_id"`
	RunID        string            `json:"run_id"`
	Status       string            `json:"status"`
	OutputFormat string            `json:"output_format"`
	SizeClass    string            `json:"size_class"`
	Timings      timings           `json:"timings"`
	Attempts     []attemptResponse `json:"attempts"`
	OutputURL    string            `json:"output_url,omitempty"`
	FeatureCount int64             `json:"feature_count,omitempty"`
	ErrorCode    string            `json:"error_code,omitempty"`
}

type timings struct {
	SubmittedAt    string `json:"submitted_at,omitempty"`
	EnqueuedAt     string `json:"enqueued_at,omitempty"`
	FirstStartedAt string `json:"first_started_at,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	CommittedAt    string `json:"committed_at,omitempty"`
	DownloadMS     int64  `json:"download_ms,omitempty"`
	ConvertMS      int64  `json:"convert_ms,omitempty"`
	PackageMS      int64  `json:"package_ms,omitempty"`
	UploadMS       int64  `json:"upload_ms,omitempty"`
}

type attemptResponse struct {
	Epoch      int64   `json:"epoch"`
	TaskID     string  `json:"task_id"`
	StartedAt  string  `json:"started_at"`
	EndedAt    string  `json:"ended_at,omitempty"`
	Outcome    string  `json:"outcome,omitempty"`
	PeakRSSMB  float64 `json:"peak_rss_mb,omitempty"`
	CPUS       float64 `json:"cpu_s,omitempty"`
	PeakDiskMB float64 `json:"peak_disk_mb,omitempty"`
}

func fmtTS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(tsLayout)
}

func toResponse(j jobstore.Job) jobResponse {
	r := jobResponse{
		JobID: j.JobID, RunID: j.RunID, Status: j.Status, OutputFormat: j.OutputFormat, SizeClass: j.SizeClass,
		FeatureCount: j.FeatureCount, ErrorCode: j.ErrorCode,
		Timings: timings{
			SubmittedAt: fmtTS(j.SubmittedAt), EnqueuedAt: fmtTS(j.EnqueuedAt),
			FirstStartedAt: fmtTS(j.FirstStartedAt), StartedAt: fmtTS(j.StartedAt), CommittedAt: fmtTS(j.CommittedAt),
			DownloadMS: j.DownloadMS, ConvertMS: j.ConvertMS, PackageMS: j.PackageMS, UploadMS: j.UploadMS,
		},
		Attempts: make([]attemptResponse, 0, len(j.Attempts)),
	}
	for _, a := range j.Attempts {
		r.Attempts = append(r.Attempts, attemptResponse{
			Epoch: a.Epoch, TaskID: a.TaskID, StartedAt: fmtTS(a.StartedAt), EndedAt: fmtTS(a.EndedAt),
			Outcome: a.Outcome, PeakRSSMB: a.PeakRSSMB, CPUS: a.CPUS, PeakDiskMB: a.PeakDiskMB,
		})
	}
	return r
}
