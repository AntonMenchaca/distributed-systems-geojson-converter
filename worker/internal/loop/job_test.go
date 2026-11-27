package loop

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/gdal"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/measure"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type trace struct{ events []string }

func (tr *trace) add(e string) { tr.events = append(tr.events, e) }

type fakeStore struct {
	jobstore.Store
	tr        *trace
	claimErr  error
	commitErr error
	failErr   error
	result    jobstore.Result
	// failPermanent is the permanent flag of the last Fail call.
	failPermanent bool
}

func (f *fakeStore) Claim(_ context.Context, id, task string, _ time.Time) (int64, error) {
	f.tr.add("claim:" + id + ":" + task)
	return 1, f.claimErr
}

func (f *fakeStore) Commit(_ context.Context, id string, epoch int64, r jobstore.Result) error {
	f.tr.add("commit:" + id)
	f.result = r
	return f.commitErr
}

func (f *fakeStore) Fail(_ context.Context, id string, epoch int64, code string, permanent bool) error {
	f.tr.add("fail:" + id + ":" + code)
	f.failPermanent = permanent
	return f.failErr
}

type fakeObjects struct {
	tr          *trace
	meta        map[string]map[string]string // upload key -> metadata
	bodies      map[string]string            // upload key -> file contents
	downloadErr error
	deleteErr   error
}

func (o fakeObjects) Download(_ context.Context, key, path string) error {
	o.tr.add("download:" + key)
	if o.downloadErr != nil {
		return o.downloadErr
	}
	return os.WriteFile(path, []byte(`{"type":"FeatureCollection","features":[]}`), 0o644)
}

func (o fakeObjects) Delete(_ context.Context, key string) error {
	o.tr.add("rm:" + key)
	return o.deleteErr
}

func (o fakeObjects) Upload(_ context.Context, key, path string, meta map[string]string) error {
	o.tr.add("upload:" + key)
	if o.meta != nil {
		o.meta[key] = meta
	}
	if o.bodies != nil {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		o.bodies[key] = string(b)
	}
	return nil
}

type recSQS struct {
	SQSAPI
	tr *trace
}

func (q recSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	q.tr.add("delete:" + *in.ReceiptHandle)
	return &sqs.DeleteMessageOutput{}, nil
}

func (q recSQS) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	q.tr.add("visibility:" + strconv.Itoa(int(in.VisibilityTimeout)))
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

// fakeConvert writes a 3-byte gpkg, or a shapefile directory, and reports 1000 features.
func fakeConvert(tr *trace, err error) ConvertFunc {
	return func(_ context.Context, in, outDir, format, profile string, tg gdal.Target) (gdal.Stats, error) {
		tr.add("convert:" + format + ":" + profile + ":" + tg.Name + ":" + tg.Layer)
		if err != nil {
			return gdal.Stats{}, err
		}
		out := gdal.OutputPath(outDir, format, tg)
		if format == "gpkg" {
			_ = os.MkdirAll(outDir, 0o755)
			if err := os.WriteFile(out, []byte("gpk"), 0o644); err != nil {
				return gdal.Stats{}, err
			}
		} else {
			_ = os.MkdirAll(out, 0o755)
			for _, ext := range []string{".shp", ".dbf"} {
				if err := os.WriteFile(filepath.Join(out, tg.Layer+ext), []byte("x"), 0o644); err != nil {
					return gdal.Stats{}, err
				}
			}
		}
		return gdal.Stats{WallMS: 10, CPUS: 0.01, PeakRSSMB: 30, PeakDiskMB: 1, FeatureCount: 1000}, nil
	}
}

const body = `{"job_id":"j1","run_id":"local-x","input_key":"datasets/dc.geojson","input_bytes":10,` +
	`"output_format":"gpkg","size_class":"small","est_work_s":1.1,"est_mem_mb":154,"submitted_at":"2026-11-14T18:30:00.000Z"}`

func newProcessor(t *testing.T, tr *trace, store *fakeStore, convErr error) *Processor {
	return &Processor{
		Store: store, SQS: recSQS{tr: tr}, QueueURL: "q", Objects: fakeObjects{tr: tr},
		Convert: fakeConvert(tr, convErr), WorkDir: t.TempDir(), TaskID: "task-1",
		Profile: "defaults", Now: time.Now,
	}
}

func TestProcessCommitsThenDeletes(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	newProcessor(t, tr, store, nil).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	want := "claim:j1:task-1,download:datasets/dc.geojson,convert:gpkg:defaults:local-x_dc_j1:dc," +
		"upload:outputs/local-x/j1/local-x_dc_j1.gpkg,commit:j1,delete:rh"
	if got := strings.Join(tr.events, ","); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	r := store.result
	if r.OutputKey != "outputs/local-x/j1/local-x_dc_j1.gpkg" || r.OutputBytes != 3 || r.FeatureCount != 1000 || r.PeakRSSMB != 30 || r.CommittedAt.IsZero() {
		t.Fatalf("committed result %+v", r)
	}
}

// A shapefile is a directory of files; it is uploaded as one .shp.zip so every job has one
// output object and one download link.
func TestProcessUploadsShapefileAsOneZip(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	shpBody := strings.Replace(body, `"output_format":"gpkg"`, `"output_format":"shp"`, 1)
	newProcessor(t, tr, store, nil).Handle(context.Background(), Message{Body: shpBody, ReceiptHandle: "rh"})
	var uploads []string
	for _, e := range tr.events {
		if strings.HasPrefix(e, "upload:") {
			uploads = append(uploads, strings.TrimPrefix(e, "upload:"))
		}
	}
	key := "outputs/local-x/j1/local-x_dc_j1.shp.zip"
	if len(uploads) != 1 || uploads[0] != key {
		t.Fatalf("uploads %v, want exactly [%s]", uploads, key)
	}
	if store.result.OutputKey != key || store.result.OutputBytes <= 0 {
		t.Fatalf("committed result %+v, want output_key %s and the zip's size", store.result, key)
	}
}

// Each output object carries its run, job, dataset, format and attempt epoch, so a file
// found in S3 can be traced without the job record, and a stale upload can be told from
// the attempt that succeeded. Claim returns epoch 1 here.
func TestProcessTagsOutputWithMetadata(t *testing.T) {
	tr := &trace{}
	p := newProcessor(t, tr, &fakeStore{tr: tr}, nil)
	objs := fakeObjects{tr: tr, meta: map[string]map[string]string{}}
	p.Objects = objs
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	want := map[string]string{"run-id": "local-x", "job-id": "j1", "dataset-id": "dc", "output-format": "gpkg", "epoch": "1"}
	if got := objs.meta["outputs/local-x/j1/local-x_dc_j1.gpkg"]; !maps.Equal(got, want) {
		t.Fatalf("metadata %v, want %v", got, want)
	}
}

func TestOutputTarget(t *testing.T) {
	for _, c := range []struct {
		qm          QueueMessage
		name, layer string
	}{
		{QueueMessage{JobID: "9b2f6c1e-4a7d-4f0e-8c3b-2d1e5f6a7b8c", RunID: "p1-base-anton-s11", InputKey: "datasets/dc.geojson"},
			"p1-base-anton-s11_dc_9b2f6c1e", "dc"},
		// FileGDB layer names allow only letters, digits and underscores, starting with a letter.
		{QueueMessage{JobID: "j1", RunID: "local-x", InputKey: "datasets/polys-10k.geojson"}, "local-x_polys-10k_j1", "polys_10k"},
		{QueueMessage{JobID: "j1", RunID: "local-x", InputKey: "uploads/2024.geojson"}, "local-x_2024_j1", "l_2024"},
	} {
		tg := outputTarget(c.qm)
		if tg.Name != c.name || tg.Layer != c.layer {
			t.Errorf("outputTarget(%s) = %+v, want name %s layer %s", c.qm.InputKey, tg, c.name, c.layer)
		}
	}
}

func TestProcessConvertFailureLeavesMessage(t *testing.T) {
	tr := &trace{}
	newProcessor(t, tr, &fakeStore{tr: tr}, errors.New("boom")).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	got := strings.Join(tr.events, ",")
	if !strings.Contains(got, "fail:j1:CONVERT_FAILED") || strings.Contains(got, "delete:") || strings.Contains(got, "commit:") {
		t.Fatalf("events %s: want a non-permanent Fail, no commit and no delete (SQS redelivers)", got)
	}
}

// geojsonParseErr is what Convert returns for the P1 truncated file (GDAL 3.13 message).
func geojsonParseErr() error {
	return &gdal.ConvertError{Format: "gpkg", Stderr: []byte("ERROR 4: Failed to read GeoJSON data\n"), Err: errors.New("exit status 1")}
}

// A permanent failure ends the job as FAILED, and only then is the message dropped: no
// redelivery, so the DLQ never sees it.
func TestPermanentConvertFailureFailsJobAndDeletes(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	newProcessor(t, tr, store, geojsonParseErr()).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	want := "claim:j1:task-1,download:datasets/dc.geojson,convert:gpkg:defaults:local-x_dc_j1:dc," +
		"fail:j1:CONVERT_FAILED,delete:rh"
	if got := strings.Join(tr.events, ","); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	if !store.failPermanent {
		t.Fatal("Fail was not marked permanent")
	}
}

// If this attempt is no longer the current one, its failure is not recorded, so the message
// must stay: the newer attempt owns the job and will finish it.
func TestPermanentFailureStaleEpochKeepsMessage(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr, failErr: jobstore.ErrStaleEpoch}
	newProcessor(t, tr, store, geojsonParseErr()).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if got := strings.Join(tr.events, ","); strings.Contains(got, "delete:") {
		t.Fatalf("events %s: a stale attempt must not delete the message", got)
	}
}

// If the failure cannot be recorded at all, the job is not FAILED, so the message stays too.
func TestPermanentFailureStoreErrorKeepsMessage(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr, failErr: errors.New("dynamodb unavailable")}
	newProcessor(t, tr, store, geojsonParseErr()).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if got := strings.Join(tr.events, ","); strings.Contains(got, "delete:") {
		t.Fatalf("events %s: an unrecorded failure must not delete the message", got)
	}
}

// A redelivery of a job that already FAILED is dropped without converting again.
func TestAlreadyFailedDeletesWithoutConverting(t *testing.T) {
	tr := &trace{}
	newProcessor(t, tr, &fakeStore{tr: tr, claimErr: jobstore.ErrAlreadyFailed}, nil).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if got := strings.Join(tr.events, ","); got != "claim:j1:task-1,delete:rh" {
		t.Fatalf("events %s, want claim then delete", got)
	}
}

// An ogr2ogr killed for memory has no GDAL message, so it is transient: the message stays and
// SQS retries it (the DLQ catches it if it keeps failing).
func TestOOMConvertFailureIsTransient(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	oom := &gdal.ConvertError{Format: "gpkg", Err: errors.New("signal: killed")}
	newProcessor(t, tr, store, oom).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	got := strings.Join(tr.events, ",")
	if store.failPermanent || strings.Contains(got, "delete:") {
		t.Fatalf("events %s, failPermanent %v: an OOM kill must stay transient", got, store.failPermanent)
	}
}

func profileRecord(t *testing.T, body string) map[string]string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(body))
	header, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(header, measure.ProfileHeader) {
		t.Fatalf("header %v", header)
	}
	row, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(); err != io.EOF {
		t.Fatal("profile object has more than one row")
	}
	if len(row) != len(header) {
		t.Fatalf("row %v", row)
	}
	got := make(map[string]string, len(header))
	for i, name := range header {
		got[name] = row[i]
	}
	return got
}

func TestProcessProfileUploadsOneRow(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	p := newProcessor(t, tr, store, nil)
	bodies := map[string]string{}
	p.Objects = fakeObjects{tr: tr, bodies: bodies}
	p.MeasureProfile = true
	p.Convert = func(_ context.Context, in, outDir, format, profile string, tg gdal.Target) (gdal.Stats, error) {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return gdal.Stats{}, err
		}
		if err := os.WriteFile(gdal.OutputPath(outDir, format, tg), []byte("gpk"), 0o644); err != nil {
			return gdal.Stats{}, err
		}
		return gdal.Stats{
			WallMS: 10.9, CPUS: 0.25, FeatureCount: 10000,
			OgrinfoMS: 4.5, ProfilePeakRSSBytes: 2<<20 + 1,
		}, nil
	}
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})

	got := profileRecord(t, bodies["profiles/local-x/j1.csv"])
	want := map[string]string{
		"run_id": "local-x", "job_id": "j1", "dataset_id": "dc", "output_format": "gpkg",
		"gdal_profile": "defaults", "input_bytes": "10", "output_bytes": "3",
		"max_shp_component_bytes": "", "output_features": "10000", "wall_ms": "10", "cpu_s": "0.25",
		"peak_rss_mb": "3", "peak_disk_mb": "1", "ogrinfo_ms": "4.5", "error": "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got["package_ms"] == "" {
		t.Error("package_ms empty")
	}
	if store.result.OutputKey == "" || strings.Contains(strings.Join(tr.events, ","), "delete:rh") == false {
		t.Fatalf("profile upload must not replace the output commit: %+v %s", store.result, tr.events)
	}
}

func TestProcessProfileRecordsMaxShpComponent(t *testing.T) {
	tr := &trace{}
	p := newProcessor(t, tr, &fakeStore{tr: tr}, nil)
	bodies := map[string]string{}
	p.Objects = fakeObjects{tr: tr, bodies: bodies}
	p.MeasureProfile = true
	p.Convert = func(_ context.Context, in, outDir, format, profile string, tg gdal.Target) (gdal.Stats, error) {
		out := gdal.OutputPath(outDir, format, tg)
		if err := os.MkdirAll(out, 0o755); err != nil {
			return gdal.Stats{}, err
		}
		if err := os.WriteFile(filepath.Join(out, tg.Layer+".shp"), make([]byte, 100), 0o644); err != nil {
			return gdal.Stats{}, err
		}
		if err := os.WriteFile(filepath.Join(out, tg.Layer+".dbf"), make([]byte, 500), 0o644); err != nil {
			return gdal.Stats{}, err
		}
		return gdal.Stats{WallMS: 5, CPUS: 0.1, FeatureCount: 10, ProfilePeakRSSBytes: 1 << 20}, nil
	}
	shpBody := strings.Replace(body, `"output_format":"gpkg"`, `"output_format":"shp"`, 1)
	p.Handle(context.Background(), Message{Body: shpBody, ReceiptHandle: "rh"})

	got := profileRecord(t, bodies["profiles/local-x/j1.csv"])
	if got["max_shp_component_bytes"] != "500" {
		t.Fatalf("max_shp_component_bytes = %q, want 500", got["max_shp_component_bytes"])
	}
	if got["output_bytes"] == "" || got["output_bytes"] == "500" {
		t.Fatalf("output_bytes = %q, want the zip size (not the component)", got["output_bytes"])
	}
}

func TestProcessProfileWritesFailureRow(t *testing.T) {
	tr := &trace{}
	p := newProcessor(t, tr, &fakeStore{tr: tr}, nil)
	bodies := map[string]string{}
	p.Objects = fakeObjects{tr: tr, bodies: bodies}
	p.MeasureProfile = true
	p.Convert = func(context.Context, string, string, string, string, gdal.Target) (gdal.Stats, error) {
		return gdal.Stats{WallMS: 8, CPUS: 0.5, ProfilePeakRSSBytes: 1}, errors.New("boom")
	}
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})

	events := strings.Join(tr.events, ",")
	if strings.Contains(events, "commit:") || strings.Contains(events, "delete:") || !strings.Contains(events, "fail:j1:CONVERT_FAILED") {
		t.Fatalf("events %s", events)
	}
	got := profileRecord(t, bodies["profiles/local-x/j1.csv"])
	want := map[string]string{
		"run_id": "local-x", "job_id": "j1", "dataset_id": "dc", "output_format": "gpkg",
		"gdal_profile": "defaults", "input_bytes": "10", "output_bytes": "",
		"max_shp_component_bytes": "", "output_features": "", "wall_ms": "8", "package_ms": "", "cpu_s": "0.5",
		"peak_rss_mb": "1", "peak_disk_mb": "1", "ogrinfo_ms": "", "error": "CONVERT_FAILED",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestProcessProfileDownloadFailureLeavesConvertEmpty(t *testing.T) {
	tr := &trace{}
	p := newProcessor(t, tr, &fakeStore{tr: tr}, nil)
	bodies := map[string]string{}
	p.Objects = fakeObjects{tr: tr, bodies: bodies, downloadErr: errors.New("missing")}
	p.MeasureProfile = true
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})

	got := profileRecord(t, bodies["profiles/local-x/j1.csv"])
	for _, col := range []string{"output_bytes", "max_shp_component_bytes", "output_features", "wall_ms", "package_ms", "cpu_s", "peak_rss_mb", "ogrinfo_ms"} {
		if got[col] != "" {
			t.Errorf("%s = %q, want empty", col, got[col])
		}
	}
	if got["error"] != "DOWNLOAD_FAILED" || got["input_bytes"] != "10" || got["dataset_id"] != "dc" || got["peak_disk_mb"] != "0" {
		t.Fatalf("row %v", got)
	}
}

func TestProcessAlreadyDoneDeletesWithoutConverting(t *testing.T) {
	tr := &trace{}
	newProcessor(t, tr, &fakeStore{tr: tr, claimErr: jobstore.ErrAlreadyDone}, nil).Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if got := strings.Join(tr.events, ","); got != "claim:j1:task-1,delete:rh" {
		t.Fatalf("events %s, want claim then delete", got)
	}
}

// sizedObjects downloads an input of inBytes.
type sizedObjects struct{ inBytes int }

func (o sizedObjects) Download(_ context.Context, _, path string) error {
	return os.WriteFile(path, make([]byte, o.inBytes), 0o644)
}
func (o sizedObjects) Upload(context.Context, string, string, map[string]string) error { return nil }
func (o sizedObjects) Delete(context.Context, string) error                            { return nil }

// Fenced mode gives each attempt its own folder, so a stale attempt never shares a key
// with the live one. Claim returns epoch 1 here.
func TestFencedOutputKeyIsPerAttempt(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	p := newProcessor(t, tr, store, nil)
	p.CommitMode = jobstore.CommitFenced
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	want := "outputs/local-x/j1/attempt-1/local-x_dc_j1.gpkg"
	if store.result.OutputKey != want || !strings.Contains(strings.Join(tr.events, ","), "upload:"+want+",commit:j1,delete:rh") {
		t.Fatalf("output_key %q events %v, want %s uploaded, committed, then the message deleted", store.result.OutputKey, tr.events, want)
	}
	if strings.Contains(strings.Join(tr.events, ","), "rm:") {
		t.Fatalf("events %v: a committed attempt must keep its object", tr.events)
	}
}

// A fenced worker whose commit is rejected as stale removes its own object and leaves the
// message: the newer attempt's object and message are the live ones.
func TestStaleWorkerDeletesItsObject(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr, commitErr: jobstore.ErrStaleEpoch}
	p := newProcessor(t, tr, store, nil)
	p.CommitMode = jobstore.CommitFenced
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	key := "outputs/local-x/j1/attempt-1/local-x_dc_j1.gpkg"
	want := "claim:j1:task-1,download:datasets/dc.geojson,convert:gpkg:defaults:local-x_dc_j1:dc," +
		"upload:" + key + ",commit:j1,rm:" + key
	if got := strings.Join(tr.events, ","); got != want {
		t.Fatalf("events\n got %s\nwant %s (no message delete, no Fail)", got, want)
	}
}

// A failed delete of the stale object is logged only: the commit was already rejected.
func TestStaleWorkerDeleteFailureIsHarmless(t *testing.T) {
	tr := &trace{}
	p := newProcessor(t, tr, &fakeStore{tr: tr, commitErr: jobstore.ErrStaleEpoch}, nil)
	p.CommitMode = jobstore.CommitFenced
	p.Objects = fakeObjects{tr: tr, deleteErr: errors.New("s3 down")}
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	got := strings.Join(tr.events, ",")
	if !strings.Contains(got, "rm:outputs/local-x/j1/attempt-1/") || strings.Contains(got, "delete:rh") || strings.Contains(got, "fail:") {
		t.Fatalf("events %s: want the delete attempted, then no Fail and no message delete", got)
	}
}

// Under blind the key is shared with the newer attempt, so a stale worker must never
// delete it, whatever the store returns.
func TestBlindNeverDeletesSharedOutput(t *testing.T) {
	tr := &trace{}
	p := newProcessor(t, tr, &fakeStore{tr: tr, commitErr: jobstore.ErrStaleEpoch}, nil)
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if got := strings.Join(tr.events, ","); strings.Contains(got, "rm:") {
		t.Fatalf("events %s: blind mode must not delete the shared object", got)
	}
}

// Peak disk is the job directory's peak size: input plus output, which is what disk_mb in
// estimates.json must predict for WORKER_CONCURRENCY=mem-budget.
func TestPeakDiskCoversInputAndOutput(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	p := newProcessor(t, tr, store, nil)
	p.Objects = sizedObjects{inBytes: 3 << 20}
	p.Convert = func(_ context.Context, in, outDir, format, profile string, tg gdal.Target) (gdal.Stats, error) {
		_ = os.MkdirAll(outDir, 0o755)
		if err := os.WriteFile(gdal.OutputPath(outDir, format, tg), make([]byte, 2<<20), 0o644); err != nil {
			return gdal.Stats{}, err
		}
		return gdal.Stats{PeakDiskMB: 2, FeatureCount: 1}, nil
	}
	p.Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if store.result.PeakDiskMB < 5 {
		t.Fatalf("PeakDiskMB = %v, want >= 5 (3 MiB input + 2 MiB output)", store.result.PeakDiskMB)
	}
}
