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
	QueueURL     string
	Bucket       string
	WorkDir      string
	TaskID       string
	S3PathStyle  bool
	Concurrency  int    // WORKER_CONCURRENCY=fixed-<n>
	LeaseMode    string // LEASE_MODE
	CommitMode   string // COMMIT_MODE
	Profile      string // GDAL_PROFILE
	ShutdownMode string // SHUTDOWN_MODE
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
	host, _ := os.Hostname()
	cfg := config{
		JobsTable:    getenv("JOBS_TABLE"),
		QueueURL:     getenv("QUEUE_URL_JOBS"),
		Bucket:       getenv("BUCKET"),
		WorkDir:      or("WORK_DIR", os.TempDir()),
		TaskID:       or("TASK_ID", host),
		S3PathStyle:  getenv("S3_FORCE_PATH_STYLE") == "true",
		LeaseMode:    or("LEASE_MODE", "fixed-30s"),
		CommitMode:   or("COMMIT_MODE", "blind"),
		Profile:      or("GDAL_PROFILE", "defaults"),
		ShutdownMode: or("SHUTDOWN_MODE", "ignore"),
	}
	for k, v := range map[string]string{"JOBS_TABLE": cfg.JobsTable, "QUEUE_URL_JOBS": cfg.QueueURL, "BUCKET": cfg.Bucket} {
		if v == "" {
			return config{}, fmt.Errorf("%s is required", k)
		}
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
	ctx, stop := runContext(cfg.ShutdownMode)
	defer stop()

	// Credentials, region and (locally) AWS_ENDPOINT_URL come from the environment.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("loading AWS config: %v", err)
	}
	sqsClient := sqs.NewFromConfig(awsCfg)
	proc := &loop.Processor{
		Store:    jobstore.NewDynamoStore(dynamodb.NewFromConfig(awsCfg), cfg.JobsTable, jobstore.CommitMode(cfg.CommitMode)),
		SQS:      sqsClient,
		QueueURL: cfg.QueueURL,
		Objects:  loop.S3Objects{Client: s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = cfg.S3PathStyle }), Bucket: cfg.Bucket},
		Convert:  gdal.Convert,
		WorkDir:  cfg.WorkDir,
		TaskID:   cfg.TaskID,
		Profile:  cfg.Profile,
		Logger:   logger,
		Now:      time.Now,
	}
	logger.Info("starting worker", "concurrency", cfg.Concurrency, "lease_mode", cfg.LeaseMode,
		"commit_mode", cfg.CommitMode, "gdal_profile", cfg.Profile, "shutdown_mode", cfg.ShutdownMode, "task_id", cfg.TaskID)
	loop.New(loop.Config{
		SQS: sqsClient, QueueURL: cfg.QueueURL, Concurrency: cfg.Concurrency, WaitSeconds: 20,
		VisibilityTimeoutS: cfg.VisibilityTimeoutS, Handle: proc.Handle, Logger: logger,
	}).Run(ctx)
}
