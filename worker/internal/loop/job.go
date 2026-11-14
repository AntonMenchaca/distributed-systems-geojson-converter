package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/gdal"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Objects moves files between the job directory and S3.
type Objects interface {
	Download(ctx context.Context, key, path string) error
	Upload(ctx context.Context, key, path string) error
}

type ConvertFunc func(ctx context.Context, in, outDir, format, profile string) (gdal.Stats, error)

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

// Processor runs one job: claim, download, convert, upload, commit, delete. In the
// baseline (COMMIT_MODE=blind) outputs go straight to their final keys, one object per file.
type Processor struct {
	Store     jobstore.Store
	SQS       SQSAPI
	QueueURL  string
	Objects   Objects
	Convert   ConvertFunc
	WorkDir   string
	TaskID    string
	Profile   string // GDAL_PROFILE
	LeaseMode string // LEASE_MODE
	Logger    *slog.Logger
	Now       func() time.Time
}

// maxVisibilityS is SQS's 12-hour visibility ceiling, used by LEASE_MODE=fixed-12h.
const maxVisibilityS = 12 * 60 * 60

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

	if p.LeaseMode == "fixed-12h" {
		if _, err := p.SQS.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(p.QueueURL), ReceiptHandle: aws.String(m.ReceiptHandle), VisibilityTimeout: maxVisibilityS,
		}); err != nil {
			logger.Error("extend visibility failed", "error", err.Error())
		}
	}

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
		p.fail(ctx, qm.JobID, epoch, "WORKDIR_FAILED", err, logger)
		return
	}

	t := time.Now()
	in := filepath.Join(jobDir, "in.geojson")
	if err := p.Objects.Download(ctx, qm.InputKey, in); err != nil {
		p.fail(ctx, qm.JobID, epoch, "DOWNLOAD_FAILED", err, logger)
		return
	}
	downloadMS := time.Since(t).Milliseconds()

	outDir := filepath.Join(jobDir, "out")
	stats, err := p.Convert(ctx, in, outDir, qm.OutputFormat, p.Profile)
	if err != nil {
		p.fail(ctx, qm.JobID, epoch, "CONVERT_FAILED", err, logger)
		return
	}

	t = time.Now()
	prefix := "outputs/" + qm.JobID + "/"
	var outBytes int64
	err = filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(outDir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		outBytes += info.Size()
		return p.Objects.Upload(ctx, prefix+filepath.ToSlash(rel), path)
	})
	if err != nil {
		p.fail(ctx, qm.JobID, epoch, "UPLOAD_FAILED", err, logger)
		return
	}
	uploadMS := time.Since(t).Milliseconds()

	err = p.Store.Commit(ctx, qm.JobID, epoch, jobstore.Result{
		OutputKey:    outputKey(prefix, outDir, qm.OutputFormat),
		OutputBytes:  outBytes,
		FeatureCount: stats.FeatureCount,
		DownloadMS:   downloadMS,
		ConvertMS:    int64(stats.WallMS),
		UploadMS:     uploadMS,
		PeakRSSMB:    float64(stats.PeakRSSMB),
		CPUS:         stats.CPUS,
		PeakDiskMB:   float64(stats.PeakDiskMB),
		CommittedAt:  p.Now(),
	})
	if err != nil {
		logger.Error("commit failed", "error", err.Error())
		return
	}
	p.deleteMessage(ctx, m, logger)
	logger.Info("job succeeded", "features", stats.FeatureCount, "convert_ms", stats.WallMS, "peak_rss_mb", stats.PeakRSSMB)
}

// outputKey is the output file's key (gpkg) or, for directory outputs (shp, fgdb), the
// key prefix ending in "/".
func outputKey(prefix, outDir, format string) string {
	path := gdal.OutputPath(outDir, format)
	rel, _ := filepath.Rel(outDir, path)
	key := prefix + filepath.ToSlash(rel)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		key += "/"
	}
	return key
}

// fail records a transient failure. The message stays on the queue for redelivery.
func (p *Processor) fail(ctx context.Context, jobID string, epoch int64, code string, cause error, logger *slog.Logger) {
	logger.Error("job attempt failed", "code", code, "error", cause.Error())
	if err := p.Store.Fail(ctx, jobID, epoch, code, false); err != nil {
		logger.Error("record failure failed", "error", err.Error())
	}
}

func (p *Processor) deleteMessage(ctx context.Context, m Message, logger *slog.Logger) {
	if _, err := p.SQS.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(p.QueueURL), ReceiptHandle: aws.String(m.ReceiptHandle)}); err != nil {
		logger.Error("delete message failed", "error", err.Error())
	}
}
