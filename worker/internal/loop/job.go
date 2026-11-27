package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
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
	// Delete removes key. A stale fenced attempt uses it to discard its own output.
	Delete(ctx context.Context, key string) error
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
	Store          jobstore.Store
	SQS            SQSAPI
	QueueURL       string // fallback for messages that don't record their queue
	Objects        Objects
	Convert        ConvertFunc
	WorkDir        string
	TaskID         string
	Profile        string // GDAL_PROFILE
	MeasureProfile bool   // PROFILE=1: upload one profile row for this job
	// CommitMode is COMMIT_MODE. fenced writes each attempt's output under its own
	// attempt-{epoch}/ prefix and deletes it if the commit is rejected as stale; blind
	// keeps one shared key per job.
	CommitMode jobstore.CommitMode
	// LeaseMode is LEASE_MODE. heartbeat starts a Lease for each job; fixed modes do not.
	LeaseMode  string
	LeaseS     int32         // LEASE_S when LeaseMode=heartbeat
	LeaseEvery time.Duration // HEARTBEAT_EVERY_S
	Logger     *slog.Logger
	Now        func() time.Time
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
	// SUCCEEDED and FAILED are both terminal: no attempt can change either, so the message
	// has nothing left to do.
	if errors.Is(err, jobstore.ErrAlreadyDone) || errors.Is(err, jobstore.ErrAlreadyFailed) {
		p.deleteMessage(ctx, m, logger)
		return
	}
	if err != nil {
		logger.Error("claim failed", "error", err.Error())
		return
	}
	logger = logger.With("epoch", epoch)

	jobCtx, jobCancel := context.WithCancel(ctx)
	defer jobCancel()

	// PROFILE=1 records one row for this attempt. The upload defer is registered
	// before finishDisk (so the disk sample is taken first) and before chaos
	// validation, so a rejected payload still writes its row. Upload does not
	// read the job directory, which is removed by a later defer.
	var peakDisk int64
	diskDone := false
	var obs *profileObs
	if p.MeasureProfile {
		obs = &profileObs{dataset: outputTarget(qm).Dataset}
		defer func() {
			if obs.write {
				p.uploadProfile(ctx, qm, obs, peakDisk, diskDone, logger)
			}
		}()
	}

	guard, err := chaos.New(os.Getenv, qm.Chaos, epoch)
	if err != nil {
		if p.fail(ctx, qm.JobID, epoch, "CHAOS_INVALID", err, true, logger, obs) {
			p.deleteMessage(ctx, m, logger)
		}
		return
	}
	if guard.Enabled() && len(qm.Chaos) > 0 && string(qm.Chaos) != "null" {
		logger.Info("chaos", "flags", string(qm.Chaos))
	}

	leaseErr := make(chan error, 1)
	if p.LeaseMode == "heartbeat" {
		queue := m.QueueURL
		if queue == "" {
			queue = p.QueueURL
		}
		lease := &Lease{
			SQS:           p.SQS,
			QueueURL:      queue,
			ReceiptHandle: m.ReceiptHandle,
			LeaseS:        p.LeaseS,
			Every:         p.LeaseEvery,
			Beat:          guard.Beat,
			Heartbeat: func(c context.Context) error {
				return p.Store.Heartbeat(c, qm.JobID, epoch, p.Now())
			},
			Kill: jobCancel,
		}
		go func() { leaseErr <- lease.Run(jobCtx) }()
	}

	jobDir := filepath.Join(p.WorkDir, fmt.Sprintf("%s-%d", qm.JobID, epoch))
	defer os.RemoveAll(jobDir)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		p.fail(ctx, qm.JobID, epoch, "WORKDIR_FAILED", err, false, logger, obs)
		return
	}

	// Peak disk is the whole job directory (input + output + ogr2ogr temp files + the zip),
	// sampled from before the download until packaging ends. disk_mb in estimates.json
	// predicts this number.
	stopDisk := measure.SampleDisk(jobCtx, jobDir, time.Second)
	finishDisk := func() {
		if !diskDone {
			peakDisk, diskDone = stopDisk(), true
		}
	}
	defer finishDisk()

	t := time.Now()
	in := filepath.Join(jobDir, "in.geojson")
	if err := p.Objects.Download(jobCtx, qm.InputKey, in); err != nil {
		p.fail(ctx, qm.JobID, epoch, "DOWNLOAD_FAILED", err, false, logger, obs)
		return
	}
	downloadMS := time.Since(t).Milliseconds()

	// pause_s freezes the claimed job, and heartbeats with it, before ogr2ogr starts.
	// crash_at then kills at a fraction of convert progress on epoch 1 only; a later
	// claim converts through. slow stretches the gaps between those ticks without
	// stopping heartbeats.
	if err := guard.Hold(jobCtx); err != nil {
		logger.Error("chaos pause interrupted", "error", err.Error())
		return
	}
	convertCtx := jobCtx
	if guard.WantsProgress() {
		convertCtx = gdal.WithProgress(jobCtx, guard.OnProgress)
	}

	outDir := filepath.Join(jobDir, "out")
	target := outputTarget(qm)
	stats, err := p.Convert(convertCtx, in, outDir, qm.OutputFormat, p.Profile, target.Target)
	if obs != nil {
		obs.sawConvert = true
		obs.stats = stats
	}
	if err != nil {
		finishDisk()
		// A lost lease cancels convert; do not Fail/Commit — another delivery owns the job.
		if jobCtx.Err() != nil {
			select {
			case e := <-leaseErr:
				logger.Error("lease lost; leaving message for redelivery", "error", e.Error())
			default:
				logger.Error("job cancelled; leaving message for redelivery", "error", err.Error())
			}
			return
		}
		// A permanent failure ends the job as FAILED and drops the message; a transient one
		// leaves both for SQS to redeliver (and for the DLQ to catch if it keeps failing).
		permanent := gdal.Permanent(err)
		if p.fail(ctx, qm.JobID, epoch, "CONVERT_FAILED", err, permanent, logger, obs) {
			p.deleteMessage(ctx, m, logger)
		}
		return
	}
	if obs != nil {
		obs.convertOK = true
	}

	// Before zipping: SHP_LIMIT_MODE=split-2gb cares about the largest .shp/.dbf,
	// not the zip that output_bytes will record.
	outPath := gdal.OutputPath(outDir, qm.OutputFormat, target.Target)
	if obs != nil && qm.OutputFormat == "shp" {
		if n, err := gdal.LargestShpComponent(outPath); err != nil {
			logger.Error("largest shp component", "error", err.Error())
		} else {
			obs.sawMaxShp = true
			obs.maxShpComponentBytes = n
		}
	}

	// Packaging is timed apart from convert_ms, which is what the format comparisons use.
	t = time.Now()
	file, err := gdal.Package(outPath, qm.OutputFormat)
	finishDisk()
	if err != nil {
		p.fail(ctx, qm.JobID, epoch, "PACKAGE_FAILED", err, false, logger, obs)
		return
	}
	packageMS := time.Since(t).Milliseconds()
	info, err := os.Stat(file)
	if err != nil {
		p.fail(ctx, qm.JobID, epoch, "PACKAGE_FAILED", err, false, logger, obs)
		return
	}
	if obs != nil {
		obs.sawPackage = true
		obs.packageMS = packageMS
		obs.outputBytes = info.Size()
	}

	t = time.Now()
	key := p.outputKey(qm, epoch, filepath.Base(file))
	// epoch is this claim's. verify compares it with the SUCCEEDED attempt so a stale
	// upload that overwrote the object is visible. S3 user-metadata values are strings.
	meta := map[string]string{
		"run-id": qm.RunID, "job-id": qm.JobID, "dataset-id": target.Dataset,
		"output-format": qm.OutputFormat, "epoch": strconv.FormatInt(epoch, 10),
	}
	if err := p.Objects.Upload(ctx, key, file, meta); err != nil {
		p.fail(ctx, qm.JobID, epoch, "UPLOAD_FAILED", err, false, logger, obs)
		return
	}
	uploadMS := time.Since(t).Milliseconds()

	if obs != nil {
		obs.write = true
	}
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
		PeakDiskMB:   float64(measure.CeilMiB(peakDisk)),
		CommittedAt:  p.Now(),
	})
	if errors.Is(err, jobstore.ErrStaleEpoch) {
		// A newer claim owns the job. Its message and its output are the live ones: drop
		// only this attempt's object, and leave the message alone (this receipt handle is
		// stale, and the newer attempt deletes the message when it commits).
		logger.Warn("commit rejected: epoch has moved on; discarding this attempt's output", "key", key)
		p.discardOutput(ctx, key, logger)
		return
	}
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

// profileObs is the part of one PROFILE=1 row this attempt has measured.
// A column stays empty until the stage that produces it has run.
type profileObs struct {
	write                bool
	dataset              string
	errCode              string
	sawConvert           bool
	convertOK            bool
	stats                gdal.Stats
	sawPackage           bool
	packageMS            int64
	outputBytes          int64
	sawMaxShp            bool
	maxShpComponentBytes int64
}

// uploadProfile writes this attempt's row to profiles/<run_id>/<job_id>.csv.
// A later attempt of the same job replaces the object. A failed upload is logged
// and does not change the job's outcome: the output commit is already decided.
func (p *Processor) uploadProfile(ctx context.Context, qm QueueMessage, obs *profileObs, peakDisk int64, diskDone bool, logger *slog.Logger) {
	var buf bytes.Buffer
	if err := measure.WriteProfile(&buf, measure.ProfileHeader, profileValues(qm, p.Profile, obs, peakDisk, diskDone)); err != nil {
		logger.Error("profile row", "error", err.Error())
		return
	}
	f, err := os.CreateTemp("", "profile-*.csv")
	if err != nil {
		logger.Error("profile row", "error", err.Error())
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		logger.Error("profile row", "error", err.Error())
		return
	}
	if err := f.Close(); err != nil {
		logger.Error("profile row", "error", err.Error())
		return
	}
	key := "profiles/" + qm.RunID + "/" + qm.JobID + ".csv"
	if err := p.Objects.Upload(ctx, key, name, nil); err != nil {
		logger.Error("profile upload failed", "key", key, "error", err.Error())
	}
}

func profileValues(qm QueueMessage, gdalProfile string, obs *profileObs, peakDisk int64, diskDone bool) []string {
	wall, cpu, rss, features, ogrinfo := "", "", "", "", ""
	if obs.sawConvert {
		wall = strconv.FormatInt(int64(obs.stats.WallMS), 10)
		cpu = strconv.FormatFloat(obs.stats.CPUS, 'f', -1, 64)
		rss = strconv.FormatInt(measure.CeilMiB(obs.stats.ProfilePeakRSSBytes), 10)
	}
	if obs.convertOK {
		features = strconv.FormatInt(obs.stats.FeatureCount, 10)
		ogrinfo = strconv.FormatFloat(obs.stats.OgrinfoMS, 'f', -1, 64)
	}
	outBytes, maxShp, packageMS, disk := "", "", "", ""
	if obs.sawPackage {
		outBytes = strconv.FormatInt(obs.outputBytes, 10)
		packageMS = strconv.FormatInt(obs.packageMS, 10)
	}
	if obs.sawMaxShp {
		maxShp = strconv.FormatInt(obs.maxShpComponentBytes, 10)
	}
	if diskDone {
		disk = strconv.FormatInt(measure.CeilMiB(peakDisk), 10)
	}
	return []string{
		qm.RunID, qm.JobID, obs.dataset, qm.OutputFormat, gdalProfile,
		strconv.FormatInt(qm.InputBytes, 10), outBytes, maxShp, features,
		wall, packageMS, cpu, rss, disk, ogrinfo, obs.errCode,
	}
}

// fail records a failed attempt. permanent ends the job as FAILED (retrying will not help).
// It returns true only when the job is now FAILED, which is when the caller may delete the
// message. A transient failure, a stale epoch or a store error returns false, and the message
// stays on the queue for redelivery. obs, when PROFILE=1 is on, marks this attempt's profile
// row with code.
func (p *Processor) fail(ctx context.Context, jobID string, epoch int64, code string, cause error, permanent bool, logger *slog.Logger, obs *profileObs) bool {
	if obs != nil {
		obs.write = true
		obs.errCode = code
	}
	logger.Error("job attempt failed", "code", code, "permanent", permanent, "error", cause.Error())
	err := p.Store.Fail(ctx, jobID, epoch, code, permanent)
	if errors.Is(err, jobstore.ErrStaleEpoch) {
		logger.Warn("failure not recorded: epoch has moved on or the job already succeeded")
		return false
	}
	if err != nil {
		logger.Error("record failure failed", "error", err.Error())
		return false
	}
	return permanent
}

// outputKey is where this attempt's single output object goes. Blind keeps the one shared
// key, so a stale attempt can overwrite a newer one. Fenced gives each attempt its own
// attempt-{epoch}/ folder: attempts can no longer collide, and a stale one can delete its
// object without touching the live one.
func (p *Processor) outputKey(qm QueueMessage, epoch int64, name string) string {
	prefix := "outputs/" + qm.RunID + "/" + qm.JobID + "/"
	if p.CommitMode == jobstore.CommitFenced {
		prefix += "attempt-" + strconv.FormatInt(epoch, 10) + "/"
	}
	return prefix + name
}

// discardOutput deletes a stale attempt's own object. It only runs in fenced mode, where
// the key is unique to the attempt; under blind the key is shared with the newer attempt.
// A failed delete is logged and changes nothing: the commit was already rejected.
func (p *Processor) discardOutput(ctx context.Context, key string, logger *slog.Logger) {
	if p.CommitMode != jobstore.CommitFenced {
		return
	}
	if err := p.Objects.Delete(ctx, key); err != nil {
		logger.Error("delete stale output failed", "key", key, "error", err.Error())
	}
}

func (p *Processor) deleteMessage(ctx context.Context, m Message, logger *slog.Logger) {
	queue := m.QueueURL // the queue it came from (SCHED_POLICY reads up to two)
	if queue == "" {
		queue = p.QueueURL
	}
	if _, err := p.SQS.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queue), ReceiptHandle: aws.String(m.ReceiptHandle)}); err != nil {
		logger.Error("delete message failed", "error", err.Error())
	}
}
