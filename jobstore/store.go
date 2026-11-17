// Package jobstore is the only code that writes job records. Every status change goes
// through Store, so "who may write what, under which condition" lives in one place.
// Field names follow schemas/job-record.json.
package jobstore

import (
	"context"
	"errors"
	"time"
)

// CommitMode is the COMMIT_MODE knob (knobs.md).
type CommitMode string

const (
	// CommitBlind writes results unconditionally: the deliberately naive baseline.
	CommitBlind CommitMode = "blind"
	// CommitFenced commits only if the attempt's epoch is still current. Not implemented yet.
	CommitFenced CommitMode = "fenced"
)

// Status values, as in schemas/job-record.json.
const (
	StatusSubmitted    = "SUBMITTED"
	StatusQueued       = "QUEUED"
	StatusRunning      = "RUNNING"
	StatusSucceeded    = "SUCCEEDED"
	StatusFailed       = "FAILED"
	StatusDeadLettered = "DEAD_LETTERED"
	StatusRejected     = "REJECTED"
)

// Attempt outcomes, as in schemas/job-record.json.
const (
	OutcomeSucceeded = "SUCCEEDED"
	OutcomeFailed    = "FAILED"
	OutcomeError     = "ERROR"
)

var (
	ErrAlreadyDone = errors.New("jobstore: job already succeeded")
	ErrStaleEpoch  = errors.New("jobstore: epoch has moved on")
	ErrNotFound    = errors.New("jobstore: job not found")
	ErrJobExists   = errors.New("jobstore: job_id already exists")
)

type Store interface {
	Create(ctx context.Context, j Job) error                                                // status SUBMITTED; ErrJobExists if job_id exists
	MarkQueued(ctx context.Context, jobID, queue string, at time.Time) error                // status QUEUED, enqueued_at
	Claim(ctx context.Context, jobID, taskID string, at time.Time) (epoch int64, err error) // RUNNING, epoch+1; ErrAlreadyDone if SUCCEEDED
	Heartbeat(ctx context.Context, jobID string, epoch int64, at time.Time) error           // ErrStaleEpoch if epoch moved
	Commit(ctx context.Context, jobID string, epoch int64, r Result) error                  // blind: unconditional; fenced: ErrStaleEpoch if epoch moved
	Fail(ctx context.Context, jobID string, epoch int64, code string, permanent bool) error // FAILED if permanent
	Reject(ctx context.Context, j Job, code string) error                                   // REJECTED at admission
	Get(ctx context.Context, jobID string) (Job, error)
}

// Job is one DynamoDB item in the jobs table.
type Job struct {
	JobID        string `dynamodbav:"job_id"`
	RunID        string `dynamodbav:"run_id"`
	OutputFormat string `dynamodbav:"output_format"`
	SizeClass    string `dynamodbav:"size_class"`
	Queue        string `dynamodbav:"queue,omitempty"`
	InputKey     string `dynamodbav:"input_key"`
	InputBytes   int64  `dynamodbav:"input_bytes"`

	Status string `dynamodbav:"status"`
	Epoch  int64  `dynamodbav:"epoch"`

	SubmittedAt    time.Time `dynamodbav:"submitted_at"`
	EnqueuedAt     time.Time `dynamodbav:"enqueued_at"`
	FirstStartedAt time.Time `dynamodbav:"first_started_at"`
	StartedAt      time.Time `dynamodbav:"started_at"`
	HeartbeatAt    time.Time `dynamodbav:"heartbeat_at"`
	CommittedAt    time.Time `dynamodbav:"committed_at"`
	DownloadMS     int64     `dynamodbav:"download_ms,omitempty"`
	ConvertMS      int64     `dynamodbav:"convert_ms,omitempty"`
	PackageMS      int64     `dynamodbav:"package_ms,omitempty"`
	UploadMS       int64     `dynamodbav:"upload_ms,omitempty"`

	OutputKey    string `dynamodbav:"output_key,omitempty"`
	OutputBytes  int64  `dynamodbav:"output_bytes,omitempty"`
	OutputSHA256 string `dynamodbav:"output_sha256,omitempty"`
	FeatureCount int64  `dynamodbav:"feature_count,omitempty"`
	ErrorCode    string `dynamodbav:"error_code,omitempty"`

	Attempts []Attempt `dynamodbav:"attempts,omitempty"`
}

// Attempt is one claim of a job. attempts[i] always holds epoch i+1, because the epoch
// increment and the append happen in the same UpdateItem.
type Attempt struct {
	Epoch      int64     `dynamodbav:"epoch"`
	TaskID     string    `dynamodbav:"task_id"`
	StartedAt  time.Time `dynamodbav:"started_at"`
	EndedAt    time.Time `dynamodbav:"ended_at"`
	Outcome    string    `dynamodbav:"outcome,omitempty"`
	ErrorCode  string    `dynamodbav:"error_code,omitempty"`
	PeakRSSMB  float64   `dynamodbav:"peak_rss_mb,omitempty"`
	CPUS       float64   `dynamodbav:"cpu_s,omitempty"`
	PeakDiskMB float64   `dynamodbav:"peak_disk_mb,omitempty"`
}

// Result is what a worker commits for a successful attempt.
type Result struct {
	OutputKey    string
	OutputBytes  int64
	OutputSHA256 string
	FeatureCount int64
	DownloadMS   int64
	ConvertMS    int64
	PackageMS    int64
	UploadMS     int64
	PeakRSSMB    float64
	CPUS         float64
	PeakDiskMB   float64
	CommittedAt  time.Time
}
