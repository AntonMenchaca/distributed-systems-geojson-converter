// Package jobs serves POST /jobs and GET /jobs/{id} (api.yaml). The API only records and
// enqueues jobs; it never touches file bytes or calls workers.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/router"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Message is the queue message body (schemas/queue-message.json).
type Message struct {
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

// Enqueuer sends a message to a named queue (jobs, jobs-small, jobs-large).
type Enqueuer interface {
	Send(ctx context.Context, queue string, m Message) error
}

// Dataset is one pre-staged input from the catalog (datasets.csv).
type Dataset struct {
	Key   string
	Bytes int64
}

type Catalog interface {
	Lookup(datasetID string) (Dataset, bool)
}

// OutputURLer turns an output key into a download URL. Optional.
type OutputURLer interface {
	OutputURL(ctx context.Context, key string) (string, error)
}

type Config struct {
	Store   jobstore.Store
	Queue   Enqueuer
	Catalog Catalog
	Est     router.Estimates
	Layout  string // QUEUE_LAYOUT
	Outputs OutputURLer
	Logger  *slog.Logger
}

type Handlers struct {
	cfg Config
	now func() time.Time
}

func New(cfg Config) *Handlers { return &Handlers{cfg: cfg, now: time.Now} }

func (h *Handlers) Register(r gin.IRoutes) {
	r.POST("/jobs", h.create)
	r.GET("/jobs/:id", h.get)
}

var formats = map[string]bool{"shp": true, "gpkg": true, "fgdb": true}

// runIDPattern matches the run_id pattern in api.yaml and schemas/*.json. The worker puts
// run_id into every output's S3 key and file name, so it must be safe in both.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

type jobRequest struct {
	Input struct {
		DatasetID string `json:"dataset_id"`
		UploadID  string `json:"upload_id"`
	} `json:"input"`
	OutputFormat string          `json:"output_format"`
	RunID        string          `json:"run_id"`
	Chaos        json.RawMessage `json:"chaos,omitempty"`
}

const tsLayout = "2006-01-02T15:04:05.000Z"

func (h *Handlers) create(c *gin.Context) {
	var req jobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "INVALID_INPUT", err.Error())
		return
	}
	if !formats[req.OutputFormat] {
		badRequest(c, "UNKNOWN_FORMAT", "output_format must be shp, gpkg or fgdb")
		return
	}
	if len(req.Chaos) > 0 {
		var flags map[string]any
		if err := json.Unmarshal(req.Chaos, &flags); err != nil || flags == nil {
			badRequest(c, "INVALID_INPUT", "chaos must be a JSON object, e.g. {\"crash_at\":0.5}")
			return
		}
	}
	if !runIDPattern.MatchString(req.RunID) {
		badRequest(c, "INVALID_INPUT", "run_id must be 1-63 lowercase letters, digits or dashes, starting with a letter or digit")
		return
	}
	if req.Input.UploadID != "" {
		badRequest(c, "UNSUPPORTED_INPUT", "upload_id inputs are not implemented yet; use dataset_id")
		return
	}
	ds, ok := h.cfg.Catalog.Lookup(req.Input.DatasetID)
	if !ok {
		badRequest(c, "UNKNOWN_DATASET", "no dataset "+req.Input.DatasetID+" in the catalog")
		return
	}

	class, work, mem := router.SizeClass(ds.Bytes, req.OutputFormat, h.cfg.Est)
	queue, err := router.Queue(h.cfg.Layout, class)
	if err != nil {
		h.internal(c, "ROUTE_FAILED", err)
		return
	}
	job := jobstore.Job{
		JobID:        uuid.NewString(),
		RunID:        req.RunID,
		OutputFormat: req.OutputFormat,
		SizeClass:    class,
		InputKey:     ds.Key,
		InputBytes:   ds.Bytes,
		SubmittedAt:  h.now().UTC(),
	}
	ctx := c.Request.Context()
	if err := h.cfg.Store.Create(ctx, job); err != nil {
		h.internal(c, "RECORD_FAILED", err)
		return
	}
	// QUEUED is written before the send: once the message is on the queue a worker may
	// claim it at once, and a later MarkQueued would regress RUNNING back to QUEUED.
	if err := h.cfg.Store.MarkQueued(ctx, job.JobID, queue, h.now().UTC()); err != nil {
		h.internal(c, "RECORD_FAILED", err)
		return
	}
	msg := Message{
		JobID: job.JobID, RunID: job.RunID, InputKey: job.InputKey, InputBytes: job.InputBytes,
		OutputFormat: job.OutputFormat, SizeClass: class, EstWorkS: work, EstMemMB: mem,
		SubmittedAt: job.SubmittedAt.Format(tsLayout), Chaos: req.Chaos,
	}
	if err := h.cfg.Queue.Send(ctx, queue, msg); err != nil {
		// The record stays QUEUED with no message; verify.py reports it as never finished.
		h.internal(c, "ENQUEUE_FAILED", err)
		return
	}
	jobsAccepted.WithLabelValues(job.OutputFormat, class).Inc()
	c.JSON(http.StatusAccepted, gin.H{"job_id": job.JobID, "status": jobstore.StatusQueued, "size_class": class})
}

func (h *Handlers) get(c *gin.Context) {
	ctx := c.Request.Context()
	j, err := h.cfg.Store.Get(ctx, c.Param("id"))
	if errors.Is(err, jobstore.ErrNotFound) {
		c.JSON(http.StatusNotFound, errorBody{Error: "NOT_FOUND", Message: "no such job"})
		return
	}
	if err != nil {
		h.internal(c, "READ_FAILED", err)
		return
	}
	resp := toResponse(j)
	if j.Status == jobstore.StatusSucceeded && j.OutputKey != "" && h.cfg.Outputs != nil {
		url, err := h.cfg.Outputs.OutputURL(ctx, j.OutputKey)
		if err != nil {
			h.internal(c, "PRESIGN_FAILED", err)
			return
		}
		resp.OutputURL = url
	}
	c.JSON(http.StatusOK, resp)
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func badRequest(c *gin.Context, code, details string) {
	c.JSON(http.StatusBadRequest, errorBody{Error: code, Message: "The provided input data is invalid", Details: details})
}

func (h *Handlers) internal(c *gin.Context, code string, err error) {
	h.cfg.Logger.Error("request failed", "code", code, "error", err.Error())
	c.JSON(http.StatusInternalServerError, errorBody{Error: code, Message: "Internal server error"})
}
