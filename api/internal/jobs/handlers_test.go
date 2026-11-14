package jobs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/api/internal/router"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// events records every store and queue call in order, shared by the fakes.
type events []string

type fakeStore struct {
	jobstore.Store // unimplemented methods panic, so a test notices an unexpected call
	log            *events
	jobs           map[string]jobstore.Job
}

func (f *fakeStore) Create(_ context.Context, j jobstore.Job) error {
	*f.log = append(*f.log, "create:"+j.JobID)
	f.jobs[j.JobID] = j
	return nil
}

func (f *fakeStore) MarkQueued(_ context.Context, id, queue string, _ time.Time) error {
	*f.log = append(*f.log, "mark_queued:"+id+":"+queue)
	return nil
}

func (f *fakeStore) Get(_ context.Context, id string) (jobstore.Job, error) {
	j, ok := f.jobs[id]
	if !ok {
		return jobstore.Job{}, jobstore.ErrNotFound
	}
	return j, nil
}

type fakeQueue struct {
	log  *events
	sent []Message
}

func (q *fakeQueue) Send(_ context.Context, queue string, m Message) error {
	*q.log = append(*q.log, "send:"+m.JobID+":"+queue)
	q.sent = append(q.sent, m)
	return nil
}

type mapCatalog map[string]Dataset

func (c mapCatalog) Lookup(id string) (Dataset, bool) { d, ok := c[id]; return d, ok }

func loadEstimates(t *testing.T) router.Estimates {
	t.Helper()
	est, err := router.LoadEstimates("../../../config/estimates.json")
	if err != nil {
		t.Fatal(err)
	}
	return est
}

func newServer(t *testing.T) (*gin.Engine, *fakeStore, *fakeQueue, *events) {
	t.Helper()
	log := &events{}
	store := &fakeStore{log: log, jobs: map[string]jobstore.Job{}}
	queue := &fakeQueue{log: log}
	h := New(Config{
		Store:   store,
		Queue:   queue,
		Catalog: mapCatalog{"dc": {Key: "datasets/dc.geojson", Bytes: 1_000_000}},
		Est:     loadEstimates(t),
		Layout:  "single",
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	r := gin.New()
	h.Register(r)
	return r, store, queue, log
}

func do(r http.Handler, method, target, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

const dcGPKG = `{"input":{"dataset_id":"dc"},"output_format":"gpkg","run_id":"local-x"}`

func TestPostJobReturns202WithJobID(t *testing.T) {
	r, _, _, _ := newServer(t)
	w := do(r, http.MethodPost, "/jobs", dcGPKG)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202; body %s", w.Code, w.Body)
	}
	var got struct {
		JobID     string `json:"job_id"`
		Status    string `json:"status"`
		SizeClass string `json:"size_class"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.JobID == "" || got.Status != jobstore.StatusQueued || got.SizeClass != "small" {
		t.Fatalf("body %s, want a job_id, status QUEUED and size_class small", w.Body)
	}
}

// The record is QUEUED before the message is sent, so a fast worker's RUNNING is never
// overwritten by a late MarkQueued.
func TestPostJobWritesSubmittedThenQueued(t *testing.T) {
	r, store, queue, log := newServer(t)
	w := do(r, http.MethodPost, "/jobs", dcGPKG)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d; body %s", w.Code, w.Body)
	}
	var id string
	for k := range store.jobs {
		id = k
	}
	want := []string{"create:" + id, "mark_queued:" + id + ":jobs", "send:" + id + ":jobs"}
	if strings.Join(*log, ",") != strings.Join(want, ",") {
		t.Fatalf("calls %v, want %v", *log, want)
	}
	j := store.jobs[id]
	if j.RunID != "local-x" || j.InputKey != "datasets/dc.geojson" || j.InputBytes != 1_000_000 || j.SubmittedAt.IsZero() {
		t.Fatalf("created job %+v lacks run_id, input or submitted_at", j)
	}
	m := queue.sent[0]
	if m.InputKey != j.InputKey || m.OutputFormat != "gpkg" || m.SizeClass != "small" || m.EstWorkS <= 0 || m.EstMemMB <= 0 {
		t.Fatalf("message %+v does not carry the job's input and estimates", m)
	}
}

func TestPostJobUnknownFormatReturns400(t *testing.T) {
	r, _, _, log := newServer(t)
	w := do(r, http.MethodPost, "/jobs", `{"input":{"dataset_id":"dc"},"output_format":"kml","run_id":"local-x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %s", w.Code, w.Body)
	}
	if len(*log) != 0 {
		t.Fatalf("an invalid job still reached the store or queue: %v", *log)
	}
}

func TestGetJobReturns404ForUnknown(t *testing.T) {
	r, _, _, _ := newServer(t)
	if w := do(r, http.MethodGet, "/jobs/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", w.Code, w.Body)
	}
}

func TestPostJobCountsAcceptedJobs(t *testing.T) {
	r, _, _, _ := newServer(t)
	before := testutil.ToFloat64(jobsAccepted.WithLabelValues("gpkg", "small"))
	if w := do(r, http.MethodPost, "/jobs", dcGPKG); w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	if got := testutil.ToFloat64(jobsAccepted.WithLabelValues("gpkg", "small")) - before; got != 1 {
		t.Fatalf("jobs_accepted_total{gpkg,small} rose by %v, want 1", got)
	}
}
