package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/chaos"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/gdal"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/measure"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Objects moves files between the job directory and S3.
type Objects interface {
	Download(ctx context.Context, key, path string) error
	// Upload stores path at key with meta as the object's user metadata.
	Upload(ctx context.Context, key, path string, meta map[string]string) error
}

type ConvertFunc func(ctx context.Context, in, outDir, format, profile string, t gdal.Target) (gdal.Stats, error)

// QueueMessage is schemas/queue-message.json.
type QueueMessage struct {
	JobID        string          `json:"job_id"`
	RunID        string          `json:"run_id"`
	InputKey     string          `json:"input_key"`
	InputBytes   int64           `json:"input_bytes"`
	OutputFormat string          `json:"output_format"`
	SizeClass    string          `json:"size_class"`
	EstWorkS     float64         `json:"est_work_s"`
	EstMemMB     float64         `json:"est_mem_mb"`
	SubmittedAt  string          `json:"submitted_at"`
	Chaos        json.RawMessage `json:"chaos,omitempty"`
}

// Processor runs one job: claim, download, convert, package, upload, commit, delete. In the
// baseline (COMMIT_MODE=blind) the output goes straight to its final key.
type Processor struct {
	Store    jobstore.Store
	SQS      SQSAPI
	QueueURL string
	Objects  Objects
	Convert  ConvertFunc
	WorkDir  string
	TaskID   string
	Profile  string // GDAL_PROFILE
	Logger   *slog.Logger
	Now      func() time.Time
}

func (p *Processor) log() *slog.Logger {
	if p.Logger == nil {
		return slog.Default()
	}
	return p.Logger
}

// Handle never returns an error: a message it does not delete comes back after its
// visibility timeout, which is how SQS retries.
func (p *Processor) Handle(ctx context.Context, m Message) {
	var qm QueueMessage
	if err := json.Unmarshal([]byte(m.Body), &qm); err != nil {
		// Left on the queue: with no DLQ (the baseline) it returns forever, like any poison job.
		p.log().Error("bad message body", "message_id", m.ID, "error", err.Error())
		return
	}
	logger := p.log().With("job_id", qm.JobID, "run_id", qm.RunID, "format", qm.OutputFormat)

	epoch, err := p.Store.Claim(ctx, qm.JobID, p.TaskID, p.Now())
	if errors.Is(err, jobstore.ErrAlreadyDone) {
		p.deleteMessage(ctx, m, logger)
		return
	}
	if err != nil {
		logger.Error("claim failed", "error", err.Error())
		return
	}
	logger = logger.With("epoch", epoch)

	jobDir := filepath.Join(p.WorkDir, fmt.Sprintf("%s-%d", qm.JobID, epoch))
	defer os.RemoveAll(jobDir)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		p.fail(ctx, qm.JobID, epoch, "WORKDIR_FAILED", err, false, logger)
		return
	}

	// Peak disk is the whole job directory (input + output + ogr2ogr temp files + the zip),
	// sampled from before the download until packaging ends. disk_mb in estimates.json
	// predicts this number.
	stopDisk := measure.SampleDisk(ctx, jobDir, time.Second)
	var peakDisk int64
	diskDone := false
	finishDisk := func() {
		if !diskDone {
			peakDisk, diskDone = stopDisk(), true
		}
	}
	defer finishDisk()

	t := time.Now()
	in := filepath.Join(jobDir, "in.geojson")
	if err := p.Objects.Download(ctx, qm.InputKey, in); err != nil {
		p.fail(ctx, qm.JobID, epoch, "DOWNLOAD_FAILED", err, false, logger)
		return
	}
	downloadMS := time.Since(t).Milliseconds()

	guard, err := chaos.New(os.Getenv, qm.Chaos)
	if err != nil {
		p.fail(ctx, qm.JobID, epoch, "CHAOS_INVALID", err, true, logger)
		return
	}
	if guard.Enabled() && len(qm.Chaos) > 0 && string(qm.Chaos) != "null" {
		logger.Info("chaos", "flags", string(qm.Chaos))
	}
	// pause_s freezes the claimed job, and heartbeats with it, before ogr2ogr starts.
	// crash_at then kills at a fraction of convert progress; slow stretches the gaps
	// between those ticks without stopping heartbeats.
	if err := guard.Hold(ctx); err != nil {
		logger.Error("chaos pause interrupted", "error", err.Error())
		return
	}
	convertCtx := ctx
	if guard.WantsProgress() {
		convertCtx = gdal.WithProgress(ctx, guard.OnProgress)
	}

	outDir := filepath.Join(jobDir, "out")
	target := outputTarget(qm)
	stats, err := p.Convert(convertCtx, in, outDir, qm.OutputFormat, p.Profile, target.Target)
	if err != nil {
		finishDisk()
		p.fail(ctx, qm.JobID, epoch, "CONVERT_FAILED", err, false, logger)
		return
	}

	// Packaging is timed apart from convert_ms, which is what the format comparisons use.
	t = time.Now()
	file, err := gdal.Package(gdal.OutputPath(outDir, qm.OutputFormat, target.Target), qm.OutputFormat)
	finishDisk()
	if err != nil {
		p.fail(ctx, qm.JobID, epoch, "PACKAGE_FAILED", err, false, logger)
		return
	}
	packageMS := time.Since(t).Milliseconds()
	info, err := os.Stat(file)
	if err != nil {
		p.fail(ctx, qm.JobID, epoch, "PACKAGE_FAILED", err, false, logger)
		return
	}

	t = time.Now()
	key := "outputs/" + qm.RunID + "/" + qm.JobID + "/" + filepath.Base(file)
	meta := map[string]string{"run-id": qm.RunID, "job-id": qm.JobID, "dataset-id": target.Dataset, "output-format": qm.OutputFormat}
	if err := p.Objects.Upload(ctx, key, file, meta); err != nil {
		p.fail(ctx, qm.JobID, epoch, "UPLOAD_FAILED", err, false, logger)
		return
	}
	uploadMS := time.Since(t).Milliseconds()

	err = p.Store.Commit(ctx, qm.JobID, epoch, jobstore.Result{
		OutputKey:    key,
		OutputBytes:  info.Size(),
		FeatureCount: stats.FeatureCount,
		DownloadMS:   downloadMS,
		ConvertMS:    int64(stats.WallMS),
		PackageMS:    packageMS,
		UploadMS:     uploadMS,
		PeakRSSMB:    float64(stats.PeakRSSMB),
		CPUS:         stats.CPUS,
		PeakDiskMB:   float64((peakDisk + (1<<20 - 1)) >> 20),
		CommittedAt:  p.Now(),
	})
	if err != nil {
		logger.Error("commit failed", "error", err.Error())
		return
	}
	p.deleteMessage(ctx, m, logger)
	logger.Info("job succeeded", "features", stats.FeatureCount, "convert_ms", stats.WallMS, "peak_rss_mb", stats.PeakRSSMB)
}

// jobOutput is a job's output name and layer, plus the dataset they come from.
type jobOutput struct {
	gdal.Target
	Dataset string
}

// outputTarget names a job's output {run_id}_{dataset}_{job_id[:8]}, so a downloaded file
// alone says which run, dataset and job produced it. The dataset is the input file's name
// (datasets/dc.geojson -> dc). The layer is the dataset, reduced to the characters FileGDB
// allows in a layer name (letters, digits, underscores, starting with a letter).
func outputTarget(qm QueueMessage) jobOutput {
	base := path.Base(qm.InputKey)
	dataset := strings.TrimSuffix(base, path.Ext(base))
	short := qm.JobID
	if len(short) > 8 {
		short = short[:8]
	}
	layer := nonLayerChars.ReplaceAllString(dataset, "_")
	if layer == "" || !unicode.IsLetter(rune(layer[0])) {
		layer = "l_" + layer
	}
	return jobOutput{Target: gdal.Target{Name: qm.RunID + "_" + dataset + "_" + short, Layer: layer}, Dataset: dataset}
}

var nonLayerChars = regexp.MustCompile(`[^A-Za-z0-9_]`)

// fail records a failed attempt. permanent ends the job as FAILED (retrying will not
// help). The message stays on the queue for redelivery either way; a DLQ is a later step.
func (p *Processor) fail(ctx context.Context, jobID string, epoch int64, code string, cause error, permanent bool, logger *slog.Logger) {
	logger.Error("job attempt failed", "code", code, "error", cause.Error())
	if err := p.Store.Fail(ctx, jobID, epoch, code, permanent); err != nil {
		logger.Error("record failure failed", "error", err.Error())
	}
}

func (p *Processor) deleteMessage(ctx context.Context, m Message, logger *slog.Logger) {
	if _, err := p.SQS.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(p.QueueURL), ReceiptHandle: aws.String(m.ReceiptHandle)}); err != nil {
		logger.Error("delete message failed", "error", err.Error())
	}
}
