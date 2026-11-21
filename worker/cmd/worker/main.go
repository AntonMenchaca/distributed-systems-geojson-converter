// Command worker pulls conversion jobs from SQS and runs ogr2ogr for each.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/chaos"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/gdal"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/loop"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// config is everything the worker reads from the environment. Knobs are validated here, so
// an unknown or not-yet-built value is a startup error, never a silent default (knobs.md).
type config struct {
	JobsTable    string
	QueueURL     string   // the first queue read; the fallback for deleting a message
	QueueURLs    []string // the queues this worker reads, in the order SCHED_POLICY needs
	QueueLayout  string   // QUEUE_LAYOUT
	SchedPolicy  string   // SCHED_POLICY
	Pool         string   // WORKER_POOL: shared, or small / large under POOL_LAYOUT=split (wiring)
	Bucket       string
	WorkDir      string
	S3PathStyle  bool
	Concurrency  int    // WORKER_CONCURRENCY=fixed-<n>
	LeaseMode    string // LEASE_MODE
	CommitMode   string // COMMIT_MODE
	Profile      string // GDAL_PROFILE
	ShutdownMode string // SHUTDOWN_MODE
	// MeasureProfile is PROFILE=1: the loop runs one job at a time and ConvertProfile
	// samples that child's RSS. It is not GDAL_PROFILE, and it is not a knobs.md knob.
	MeasureProfile bool
	// VisibilityTimeoutS is set on each ReceiveMessage: 0 keeps the queue's 30 s default
	// (fixed-30s); fixed-12h asks for SQS's 12 h maximum, allowed only at receive time.
	VisibilityTimeoutS int32
}

func loadConfig(getenv func(string) string) (config, error) {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	cfg := config{
		JobsTable:    getenv("JOBS_TABLE"),
		QueueLayout:  or("QUEUE_LAYOUT", "single"),
		SchedPolicy:  or("SCHED_POLICY", "fcfs"),
		Pool:         or("WORKER_POOL", "shared"),
		Bucket:       getenv("BUCKET"),
		WorkDir:      or("WORK_DIR", os.TempDir()),
		S3PathStyle:  getenv("S3_FORCE_PATH_STYLE") == "true",
		LeaseMode:    or("LEASE_MODE", "fixed-30s"),
		CommitMode:   or("COMMIT_MODE", "blind"),
		Profile:      or("GDAL_PROFILE", "defaults"),
		ShutdownMode: or("SHUTDOWN_MODE", "ignore"),
	}
	for k, v := range map[string]string{"JOBS_TABLE": cfg.JobsTable, "BUCKET": cfg.Bucket} {
		if v == "" {
			return config{}, fmt.Errorf("%s is required", k)
		}
	}
	queues, err := workerQueues(cfg.QueueLayout, cfg.Pool)
	if err != nil {
		return config{}, err
	}
	for _, key := range queues {
		url := getenv(key)
		if url == "" {
			return config{}, fmt.Errorf("%s is required with QUEUE_LAYOUT=%s, WORKER_POOL=%s", key, cfg.QueueLayout, cfg.Pool)
		}
		cfg.QueueURLs = append(cfg.QueueURLs, url)
	}
	cfg.QueueURL = cfg.QueueURLs[0]
	if _, err := loop.NewPolicy(cfg.SchedPolicy, cfg.QueueURLs); err != nil {
		return config{}, err
	}

	conc := or("WORKER_CONCURRENCY", "fixed-4")
	switch {
	case conc == "mem-budget":
		return config{}, fmt.Errorf("WORKER_CONCURRENCY=mem-budget: not implemented yet")
	case strings.HasPrefix(conc, "fixed-"):
		n, err := strconv.Atoi(strings.TrimPrefix(conc, "fixed-"))
		if err != nil || n < 1 {
			return config{}, fmt.Errorf("WORKER_CONCURRENCY=%q: want fixed-<n> with n >= 1", conc)
		}
		cfg.Concurrency = n
	default:
		return config{}, fmt.Errorf("WORKER_CONCURRENCY=%q: want fixed-<n> or mem-budget", conc)
	}

	// PROFILE=1 forces a single slot even when WORKER_CONCURRENCY is higher, so a
	// profiled conversion does not share the task with another job.
	switch profileMode := or("PROFILE", "0"); profileMode {
	case "0":
	case "1":
		cfg.MeasureProfile = true
		cfg.Concurrency = 1
	default:
		return config{}, fmt.Errorf("PROFILE=%q: want 0 or 1", profileMode)
	}

	switch cfg.LeaseMode {
	case "fixed-30s":
	case "fixed-12h":
		cfg.VisibilityTimeoutS = 12 * 60 * 60
	case "heartbeat":
		return config{}, fmt.Errorf("LEASE_MODE=heartbeat: not implemented yet")
	default:
		return config{}, fmt.Errorf("LEASE_MODE=%q: want fixed-30s, fixed-12h or heartbeat", cfg.LeaseMode)
	}
	switch jobstore.CommitMode(cfg.CommitMode) {
	case jobstore.CommitBlind, jobstore.CommitFenced:
	default:
		return config{}, fmt.Errorf("COMMIT_MODE=%q: want blind or fenced", cfg.CommitMode)
	}
	switch cfg.ShutdownMode {
	case "ignore":
	case "drain":
		return config{}, fmt.Errorf("SHUTDOWN_MODE=drain: not implemented yet")
	default:
		return config{}, fmt.Errorf("SHUTDOWN_MODE=%q: want ignore or drain", cfg.ShutdownMode)
	}
	switch cfg.Profile {
	case "defaults", "gt1":
	case "tuned":
		return config{}, fmt.Errorf("GDAL_PROFILE=tuned: not implemented yet")
	default:
		return config{}, fmt.Errorf("GDAL_PROFILE=%q: want defaults, gt1 or tuned", cfg.Profile)
	}
	return cfg, nil
}

// workerQueues names the env vars of the queues a worker reads. QUEUE_LAYOUT=single: everyone
// reads jobs. by-size: a shared pool reads jobs-small then jobs-large (SCHED_POLICY picks
// between them); under POOL_LAYOUT=split each pool reads only its own queue.
func workerQueues(layout, pool string) ([]string, error) {
	switch layout {
	case "single":
		if pool != "shared" {
			return nil, fmt.Errorf("WORKER_POOL=%s needs QUEUE_LAYOUT=by-size: a pool reads its own size queue", pool)
		}
		return []string{"QUEUE_URL_JOBS"}, nil
	case "by-size":
		switch pool {
		case "shared":
			return []string{"QUEUE_URL_JOBS_SMALL", "QUEUE_URL_JOBS_LARGE"}, nil
		case "small":
			return []string{"QUEUE_URL_JOBS_SMALL"}, nil
		case "large":
			return []string{"QUEUE_URL_JOBS_LARGE"}, nil
		default:
			return nil, fmt.Errorf("WORKER_POOL=%q: want shared, small or large", pool)
		}
	default:
		return nil, fmt.Errorf("QUEUE_LAYOUT=%q: want single or by-size", layout)
	}
}

// runContext is the worker's lifetime context, shared by the poll loop and every job.
// SHUTDOWN_MODE=ignore (the baseline): SIGTERM is ignored, so the worker keeps polling and
// its ogr2ogr children keep running until ECS sends SIGKILL after the stop timeout and the
// task dies mid-job. Only an interactive Ctrl-C (os.Interrupt) stops it. drain is not implemented yet.
func runContext(mode string) (context.Context, context.CancelFunc) {
	signal.Ignore(syscall.SIGTERM)
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	taskID, err := resolveTaskID(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := runContext(cfg.ShutdownMode)
	defer stop()

	// Credentials, region and (locally) AWS_ENDPOINT_URL come from the environment.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("loading AWS config: %v", err)
	}
	sqsClient := sqs.NewFromConfig(awsCfg)
	convert := gdal.Convert
	if cfg.MeasureProfile {
		convert = gdal.ConvertProfile
	}
	proc := &loop.Processor{
		Store:    jobstore.NewDynamoStore(dynamodb.NewFromConfig(awsCfg), cfg.JobsTable, jobstore.CommitMode(cfg.CommitMode)),
		SQS:      sqsClient,
		QueueURL: cfg.QueueURL,
		Objects:  loop.S3Objects{Client: s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = cfg.S3PathStyle }), Bucket: cfg.Bucket},
		Convert:  convert,
		WorkDir:  cfg.WorkDir,
		TaskID:   taskID,
		Profile:  cfg.Profile,
		Logger:   logger,
		Now:      time.Now,
	}
	policy, err := loop.NewPolicy(cfg.SchedPolicy, cfg.QueueURLs) // already validated by loadConfig
	if err != nil {
		log.Fatal(err)
	}
	logger.Info("starting worker", "concurrency", cfg.Concurrency, "lease_mode", cfg.LeaseMode,
		"commit_mode", cfg.CommitMode, "gdal_profile", cfg.Profile, "profile", cfg.MeasureProfile,
		"shutdown_mode", cfg.ShutdownMode,
		"queue_layout", cfg.QueueLayout, "sched_policy", cfg.SchedPolicy, "worker_pool", cfg.Pool,
		"chaos_enabled", chaos.Enabled(os.Getenv), "task_id", taskID)
	loop.New(loop.Config{
		SQS: sqsClient, Policy: policy, QueueURL: cfg.QueueURL, Concurrency: cfg.Concurrency, WaitSeconds: 20,
		VisibilityTimeoutS: cfg.VisibilityTimeoutS, Handle: proc.Handle, Logger: logger,
	}).Run(ctx)
}
