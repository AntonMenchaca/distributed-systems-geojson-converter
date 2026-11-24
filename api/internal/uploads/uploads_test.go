package uploads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

const mib = 1 << 20

// fakeS3 counts bytes and calls; it reads every body straight into io.Discard.
type fakeS3 struct {
	puts, parts, completes, aborts int
	stored                         int64
	key                            string
	failPart                       bool
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.puts++
	f.key = aws.ToString(in.Key)
	n, err := io.Copy(io.Discard, in.Body)
	f.stored += n
	return &s3.PutObjectOutput{}, err
}

func (f *fakeS3) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	f.key = aws.ToString(in.Key)
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("mpu-1")}, nil
}

func (f *fakeS3) UploadPart(_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if f.failPart {
		return nil, errors.New("s3 unavailable")
	}
	f.parts++
	n, err := io.Copy(io.Discard, in.Body)
	f.stored += n
	return &s3.UploadPartOutput{ETag: aws.String("etag")}, err
}

func (f *fakeS3) CompleteMultipartUpload(_ context.Context, _ *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	f.completes++
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (f *fakeS3) AbortMultipartUpload(_ context.Context, _ *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.aborts++
	return &s3.AbortMultipartUploadOutput{}, nil
}

type fakePresigner struct{ in *s3.PutObjectInput }

func (p *fakePresigner) PresignPutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	p.in = in
	return &v4.PresignedHTTPRequest{URL: "https://s3.example/" + aws.ToString(in.Bucket) + "/" + aws.ToString(in.Key) + "?X-Amz-Signature=sig", Method: http.MethodPut}, nil
}

// zeros is a body of n zero bytes that allocates nothing, so a test can measure the
// handler's allocations alone.
type zeros struct{ left int64 }

func (z *zeros) Read(p []byte) (int, error) {
	if z.left == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > z.left {
		p = p[:z.left]
	}
	clear(p)
	z.left -= int64(len(p))
	return len(p), nil
}

func newServer(mode string, s *fakeS3, p *fakePresigner) *gin.Engine {
	r := gin.New()
	New(Config{Mode: mode, Bucket: "gc-test", S3: s, Presigner: p,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}).Register(r)
	return r
}

func create(t *testing.T, r *gin.Engine, size int64) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/uploads", strings.NewReader(`{"bytes":`+jsonInt(size)+`}`))
	req.Host = "api.example"
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func put(r *gin.Engine, id string, body io.Reader, size int64) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/uploads/"+id, body)
	req.ContentLength = size
	r.ServeHTTP(w, req)
	return w
}

func TestPresignedReturnsPutURL(t *testing.T) {
	s, p := &fakeS3{}, &fakePresigner{}
	w, out := create(t, newServer("presigned", s, p), 20*mib)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /uploads: %d %s", w.Code, w.Body)
	}
	id, _ := out["upload_id"].(string)
	url, _ := out["put_url"].(string)
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("upload_id %q is not a UUID", id)
	}
	if url != "https://s3.example/gc-test/uploads/"+id+".geojson?X-Amz-Signature=sig" {
		t.Fatalf("put_url %q is not the presigned S3 URL", url)
	}
	// The size is signed into the URL, so S3 refuses a PUT of any other length.
	if aws.ToInt64(p.in.ContentLength) != 20*mib {
		t.Fatalf("presigned ContentLength %d, want %d", aws.ToInt64(p.in.ContentLength), 20*mib)
	}
	if s.puts+s.parts != 0 {
		t.Fatal("presigned mode wrote to S3 itself; the client should")
	}
}

func TestProxyModesReturnTheAPIsOwnPutURL(t *testing.T) {
	for _, mode := range []string{"proxy-buffer", "proxy-stream"} {
		w, out := create(t, newServer(mode, &fakeS3{}, &fakePresigner{}), mib)
		if w.Code != http.StatusCreated || out["put_url"] != "http://api.example/uploads/"+out["upload_id"].(string) {
			t.Errorf("%s: %d %v", mode, w.Code, out)
		}
	}
}

func TestProxyStreamDoesNotBufferWholeBody(t *testing.T) {
	s := &fakeS3{}
	r := newServer("proxy-stream", s, nil)
	id := uuid.NewString()
	const size = 50 * mib
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	w := put(r, id, &zeros{left: size}, size)
	runtime.ReadMemStats(&after)
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	// TotalAlloc counts every byte allocated, so it bounds the peak too.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc >= 16*mib {
		t.Fatalf("allocated %d MiB streaming a 50 MiB body, want < 16 MiB", alloc/mib)
	}
	if s.stored != size || s.parts != 7 || s.completes != 1 || s.key != "uploads/"+id+".geojson" {
		t.Fatalf("stored %d bytes in %d parts (completed %d) at %s; want %d in 7 parts (8 MiB each)", s.stored, s.parts, s.completes, s.key, size)
	}
}

// The baseline does hold the whole body; this shows the allocation test can tell the difference.
func TestProxyBufferHoldsWholeBody(t *testing.T) {
	s := &fakeS3{}
	r := newServer("proxy-buffer", s, nil)
	const size = 50 * mib
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	w := put(r, uuid.NewString(), &zeros{left: size}, size)
	runtime.ReadMemStats(&after)
	if w.Code != http.StatusCreated || s.puts != 1 || s.stored != size {
		t.Fatalf("PUT: %d, %d puts, %d bytes", w.Code, s.puts, s.stored)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc < size {
		t.Fatalf("proxy-buffer allocated only %d MiB for a 50 MiB body", alloc/mib)
	}
}

func TestProxyModesRejectedWhenPresigned(t *testing.T) {
	s := &fakeS3{}
	w := put(newServer("presigned", s, &fakePresigner{}), uuid.NewString(), bytes.NewReader([]byte("{}")), 2)
	if w.Code != http.StatusNotFound {
		t.Fatalf("PUT /uploads/{id} with presigned: %d, want 404", w.Code)
	}
	if s.puts+s.parts != 0 {
		t.Fatal("presigned mode stored a proxied body")
	}
}

func TestSmallStreamIsOnePut(t *testing.T) {
	s := &fakeS3{}
	w := put(newServer("proxy-stream", s, nil), uuid.NewString(), &zeros{left: 3 * mib}, 3*mib)
	if w.Code != http.StatusCreated || s.puts != 1 || s.parts != 0 || s.stored != 3*mib {
		t.Fatalf("PUT 3 MiB: %d, %d puts, %d parts, %d bytes", w.Code, s.puts, s.parts, s.stored)
	}
}

func TestStreamAbortsWhenAPartFails(t *testing.T) {
	s := &fakeS3{failPart: true}
	w := put(newServer("proxy-stream", s, nil), uuid.NewString(), &zeros{left: 20 * mib}, 20*mib)
	if w.Code != http.StatusInternalServerError || s.aborts != 1 || s.completes != 0 {
		t.Fatalf("%d, aborts %d, completes %d; want 500 and one abort", w.Code, s.aborts, s.completes)
	}
}

func TestOverLimitAndBadRequests(t *testing.T) {
	r := newServer("proxy-stream", &fakeS3{}, nil)
	if w, _ := create(t, r, MaxBytes+1); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("POST over 5 GiB: %d, want 422", w.Code)
	}
	if w, _ := create(t, r, 0); w.Code != http.StatusBadRequest {
		t.Errorf("POST bytes 0: %d, want 400", w.Code)
	}
	if w := put(r, uuid.NewString(), &zeros{left: 1}, MaxBytes+1); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("PUT declaring over 5 GiB: %d, want 422", w.Code)
	}
	if w := put(r, "not-an-id", &zeros{left: 1}, 1); w.Code != http.StatusNotFound {
		t.Errorf("PUT unknown id: %d, want 404", w.Code)
	}
}
