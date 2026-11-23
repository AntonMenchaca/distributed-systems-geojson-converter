package loop

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AntonMenchaca/distributed-systems-geojson-converter/jobstore"
	"github.com/AntonMenchaca/distributed-systems-geojson-converter/worker/internal/gdal"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type trace struct{ events []string }

func (tr *trace) add(e string) { tr.events = append(tr.events, e) }

type fakeStore struct {
	jobstore.Store
	tr       *trace
	claimErr error
	result   jobstore.Result
}

func (f *fakeStore) Claim(_ context.Context, id, task string, _ time.Time) (int64, error) {
	f.tr.add("claim:" + id + ":" + task)
	return 1, f.claimErr
}

func (f *fakeStore) Commit(_ context.Context, id string, epoch int64, r jobstore.Result) error {
	f.tr.add("commit:" + id)
	f.result = r
	return nil
}

func (f *fakeStore) Fail(_ context.Context, id string, epoch int64, code string, permanent bool) error {
	f.tr.add("fail:" + id + ":" + code)
	return nil
}

type fakeObjects struct {
	tr   *trace
	meta map[string]map[string]string // upload key -> metadata
}

func (o fakeObjects) Download(_ context.Context, key, path string) error {
	o.tr.add("download:" + key)
	return os.WriteFile(path, []byte(`{"type":"FeatureCollection","features":[]}`), 0o644)
}

func (o fakeObjects) Upload(_ context.Context, key, _ string, meta map[string]string) error {
	o.tr.add("upload:" + key)
	if o.meta != nil {
		o.meta[key] = meta
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
