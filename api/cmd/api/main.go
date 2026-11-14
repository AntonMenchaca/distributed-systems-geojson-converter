// Command api serves the job API on port 8080 (api.yaml).
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/api"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/jobs"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/router"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// config is everything the API reads from the environment. Knobs are validated here, so an
// unknown value is a startup error, never a silent default (knobs.md).
type config struct {
	Port          string
	JobsTable     string
	QueueLayout   string // QUEUE_LAYOUT knob
	CommitMode    string // COMMIT_MODE knob
	QueueURLs     map[string]string
	EstimatesPath string
	DatasetsCSV   string
	Bucket        string
	S3PathStyle   bool
}

func loadConfig(getenv func(string) string) (config, error) {
	or := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	cfg := config{
		Port:          or("PORT", "8080"),
		JobsTable:     getenv("JOBS_TABLE"),
		QueueLayout:   or("QUEUE_LAYOUT", "single"),
		CommitMode:    or("COMMIT_MODE", "blind"),
		EstimatesPath: or("ESTIMATES_PATH", "config/estimates.json"),
		DatasetsCSV:   or("DATASETS_CSV", "datasets/local/datasets.csv"),
		Bucket:        getenv("BUCKET"),
		S3PathStyle:   getenv("S3_FORCE_PATH_STYLE") == "true",
		QueueURLs:     map[string]string{},
	}
	for name, key := range map[string]string{"jobs": "QUEUE_URL_JOBS", "jobs-small": "QUEUE_URL_JOBS_SMALL", "jobs-large": "QUEUE_URL_JOBS_LARGE"} {
		if v := getenv(key); v != "" {
			cfg.QueueURLs[name] = v
		}
	}
	if cfg.JobsTable == "" {
		return config{}, fmt.Errorf("JOBS_TABLE is required")
	}
	if _, err := router.Queue(cfg.QueueLayout, "small"); err != nil {
		return config{}, err
	}
	switch jobstore.CommitMode(cfg.CommitMode) {
	case jobstore.CommitBlind, jobstore.CommitFenced:
	default:
		return config{}, fmt.Errorf("COMMIT_MODE=%q: want blind or fenced", cfg.CommitMode)
	}
	return cfg, nil
}

func main() {
	// JSON logs to stdout. On ECS, the awslogs driver sends stdout to
	// CloudWatch, where Logs Insights parses the JSON fields.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	est, err := router.LoadEstimates(cfg.EstimatesPath)
	if err != nil {
		log.Fatal(err)
	}
	catalog, err := jobs.LoadCatalog(cfg.DatasetsCSV)
	if err != nil {
		log.Fatal(err)
	}
	// Credentials, region and (locally) AWS_ENDPOINT_URL come from the environment.
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("loading AWS config: %v", err)
	}
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = cfg.S3PathStyle })
	handlers := jobs.New(jobs.Config{
		Store:   jobstore.NewDynamoStore(dynamodb.NewFromConfig(awsCfg), cfg.JobsTable, jobstore.CommitMode(cfg.CommitMode)),
		Queue:   jobs.SQSEnqueuer{Client: sqs.NewFromConfig(awsCfg), URLs: cfg.QueueURLs},
		Catalog: catalog,
		Est:     est,
		Layout:  cfg.QueueLayout,
		Outputs: jobs.S3OutputURLs{Presigner: s3.NewPresignClient(s3Client), Bucket: cfg.Bucket, Expires: time.Hour},
		Logger:  logger,
	})
	logger.Info("starting api", "queue_layout", cfg.QueueLayout, "estimates_version", est.Version)
	if err := api.SetupRouter(logger, handlers).Run(":" + cfg.Port); err != nil {
		log.Fatal(err)
	}
}
