package loop

import (
	"context"
	"errors"
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

type fakeObjects struct{ tr *trace }

func (o fakeObjects) Download(_ context.Context, key, path string) error {
	o.tr.add("download:" + key)
	return os.WriteFile(path, []byte(`{"type":"FeatureCollection","features":[]}`), 0o644)
}

func (o fakeObjects) Upload(_ context.Context, key, _ string) error {
	o.tr.add("upload:" + key)
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

// fakeConvert writes a 3-byte gpkg and reports 1000 features.
func fakeConvert(tr *trace, err error) ConvertFunc {
	return func(_ context.Context, in, outDir, format, profile string) (gdal.Stats, error) {
		tr.add("convert:" + format + ":" + profile)
		if err != nil {
			return gdal.Stats{}, err
		}
		_ = os.MkdirAll(outDir, 0o755)
		if err := os.WriteFile(filepath.Join(outDir, "out.gpkg"), []byte("gpk"), 0o644); err != nil {
			return gdal.Stats{}, err
		}
		return gdal.Stats{WallMS: 10, CPUS: 0.01, PeakRSSMB: 30, PeakDiskMB: 1, FeatureCount: 1000}, nil
	}
}

const body = `{"job_id":"j1","run_id":"local-x","input_key":"datasets/dc.geojson","input_bytes":10,` +
	`"output_format":"gpkg","size_class":"small","est_work_s":1.1,"est_mem_mb":154,"submitted_at":"2026-11-14T18:30:00.000Z"}`

func newProcessor(t *testing.T, tr *trace, store *fakeStore, convErr error, lease string) *Processor {
	return &Processor{
		Store: store, SQS: recSQS{tr: tr}, QueueURL: "q", Objects: fakeObjects{tr: tr},
		Convert: fakeConvert(tr, convErr), WorkDir: t.TempDir(), TaskID: "task-1",
		Profile: "defaults", LeaseMode: lease, Now: time.Now,
	}
}

func TestProcessCommitsThenDeletes(t *testing.T) {
	tr := &trace{}
	store := &fakeStore{tr: tr}
	newProcessor(t, tr, store, nil, "fixed-30s").Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	want := "claim:j1:task-1,download:datasets/dc.geojson,convert:gpkg:defaults,upload:outputs/j1/out.gpkg,commit:j1,delete:rh"
	if got := strings.Join(tr.events, ","); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	r := store.result
	if r.OutputKey != "outputs/j1/out.gpkg" || r.OutputBytes != 3 || r.FeatureCount != 1000 || r.PeakRSSMB != 30 || r.CommittedAt.IsZero() {
		t.Fatalf("committed result %+v", r)
	}
}

func TestProcessConvertFailureLeavesMessage(t *testing.T) {
	tr := &trace{}
	newProcessor(t, tr, &fakeStore{tr: tr}, errors.New("boom"), "fixed-30s").Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	got := strings.Join(tr.events, ",")
	if !strings.Contains(got, "fail:j1:CONVERT_FAILED") || strings.Contains(got, "delete:") || strings.Contains(got, "commit:") {
		t.Fatalf("events %s: want a non-permanent Fail, no commit and no delete (SQS redelivers)", got)
	}
}

func TestProcessAlreadyDoneDeletesWithoutConverting(t *testing.T) {
	tr := &trace{}
	newProcessor(t, tr, &fakeStore{tr: tr, claimErr: jobstore.ErrAlreadyDone}, nil, "fixed-30s").Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if got := strings.Join(tr.events, ","); got != "claim:j1:task-1,delete:rh" {
		t.Fatalf("events %s, want claim then delete", got)
	}
}

func TestFixed12hLeaseExtendsVisibilityFirst(t *testing.T) {
	tr := &trace{}
	newProcessor(t, tr, &fakeStore{tr: tr}, nil, "fixed-12h").Handle(context.Background(), Message{Body: body, ReceiptHandle: "rh"})
	if tr.events[0] != "visibility:43200" {
		t.Fatalf("first event %s, want visibility:43200", tr.events[0])
	}
}
