// Package uploads serves POST /uploads and PUT /uploads/{id} (api.yaml) under the
// UPLOAD_MODE knob:
//
//	proxy-buffer  the API reads the whole body into memory, then writes it to S3 (baseline)
//	proxy-stream  the API streams the body to S3 in fixed-size multipart parts
//	presigned     the client PUTs straight to S3 with a presigned URL; the API never sees the bytes
//
// An upload is stored at uploads/<upload_id>.geojson in the run bucket.
package uploads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Modes are the allowed UPLOAD_MODE values (knobs.md).
var Modes = map[string]bool{"proxy-buffer": true, "proxy-stream": true, "presigned": true}

// MaxBytes is the largest upload accepted: 5 GiB, the most a single S3 PUT (and so a
// presigned PUT) can carry.
const MaxBytes int64 = 5 << 30

// DefaultPartSize is proxy-stream's multipart part size (S3's minimum part is 5 MiB). The API
// holds one part in memory at a time, whatever the upload's size.
const DefaultPartSize = 8 << 20

// S3API is the part of *s3.Client the proxy modes use.
type S3API interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, opts ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, in *s3.UploadPartInput, opts ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, opts ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, opts ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

// Presigner is the part of *s3.PresignClient presigned mode uses.
type Presigner interface {
	PresignPutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

type Config struct {
	Mode      string // UPLOAD_MODE
	Bucket    string
	S3        S3API
	Presigner Presigner
	Expires   time.Duration // how long a presigned URL stays valid (default 1 h)
	PartSize  int           // proxy-stream part size (default DefaultPartSize)
	MaxBytes  int64         // default MaxBytes
	Logger    *slog.Logger
}

type Handlers struct{ cfg Config }

func New(cfg Config) *Handlers {
	if cfg.Expires == 0 {
		cfg.Expires = time.Hour
	}
	if cfg.PartSize == 0 {
		cfg.PartSize = DefaultPartSize
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = MaxBytes
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Handlers{cfg: cfg}
}

func (h *Handlers) Register(r gin.IRoutes) {
	r.POST("/uploads", h.create)
	r.PUT("/uploads/:id", h.put)
}

// Key is where an upload is stored in the run bucket.
func Key(uploadID string) string { return "uploads/" + uploadID + ".geojson" }

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func (h *Handlers) fail(c *gin.Context, status int, code, message, details string) {
	c.JSON(status, errorBody{Error: code, Message: message, Details: details})
}

func (h *Handlers) overLimit(c *gin.Context) {
	h.fail(c, http.StatusUnprocessableEntity, "OVER_LIMIT", "Upload is too large",
		fmt.Sprintf("the largest upload is %d bytes (one S3 PUT)", h.cfg.MaxBytes))
}

func (h *Handlers) create(c *gin.Context) {
	var req struct {
		Bytes int64 `json:"bytes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Bytes < 1 {
		h.fail(c, http.StatusBadRequest, "INVALID_BYTES", "The provided input data is invalid", "bytes must be an integer >= 1")
		return
	}
	if req.Bytes > h.cfg.MaxBytes {
		h.overLimit(c)
		return
	}
	id := uuid.NewString()
	putURL := fmt.Sprintf("%s://%s/uploads/%s", scheme(c.Request), c.Request.Host, id)
	if h.cfg.Mode == "presigned" {
		// ContentLength is signed, so S3 refuses a PUT of any other size.
		signed, err := h.cfg.Presigner.PresignPutObject(c.Request.Context(), &s3.PutObjectInput{
			Bucket: aws.String(h.cfg.Bucket), Key: aws.String(Key(id)), ContentLength: aws.Int64(req.Bytes),
		}, s3.WithPresignExpires(h.cfg.Expires))
		if err != nil {
			h.cfg.Logger.Error("presign failed", "error", err.Error())
			h.fail(c, http.StatusInternalServerError, "PRESIGN_FAILED", "Internal server error", "")
			return
		}
		putURL = signed.URL
	}
	c.JSON(http.StatusCreated, gin.H{"upload_id": id, "put_url": putURL})
}

func scheme(r *http.Request) string {
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func (h *Handlers) put(c *gin.Context) {
	if h.cfg.Mode == "presigned" {
		h.fail(c, http.StatusNotFound, "NOT_FOUND", "No such upload",
			"with UPLOAD_MODE=presigned, PUT the file to the put_url from POST /uploads")
		return
	}
	id := c.Param("id")
	if _, err := uuid.Parse(id); err != nil {
		h.fail(c, http.StatusNotFound, "NOT_FOUND", "No such upload", "upload IDs come from POST /uploads")
		return
	}
	if c.Request.ContentLength > h.cfg.MaxBytes {
		h.overLimit(c)
		return
	}
	body := http.MaxBytesReader(c.Writer, c.Request.Body, h.cfg.MaxBytes)
	var n int64
	var err error
	if h.cfg.Mode == "proxy-stream" {
		n, err = h.stream(c.Request.Context(), body, Key(id))
	} else {
		n, err = h.buffer(c.Request.Context(), body, Key(id))
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		h.overLimit(c)
	case err != nil:
		h.cfg.Logger.Error("upload failed", "upload_id", id, "mode", h.cfg.Mode, "error", err.Error())
		h.fail(c, http.StatusInternalServerError, "UPLOAD_FAILED", "Internal server error", "")
	default:
		c.JSON(http.StatusCreated, gin.H{"upload_id": id, "bytes": n})
	}
}

// buffer is proxy-buffer: the whole body is held in memory before one PUT to S3.
func (h *Handlers) buffer(ctx context.Context, r io.Reader, key string) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	_, err = h.cfg.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(h.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))),
	})
	return int64(len(data)), err
}

// stream is proxy-stream: the body goes to S3 one part at a time through a single reused
// buffer, so memory stays at about one part (PartSize) for any upload size. A body that fits
// in one part is a plain PUT.
func (h *Handlers) stream(ctx context.Context, r io.Reader, key string) (int64, error) {
	buf := make([]byte, h.cfg.PartSize)
	n, rerr := io.ReadFull(r, buf)
	if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
		_, err := h.cfg.S3.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(h.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(buf[:n]), ContentLength: aws.Int64(int64(n)),
		})
		return int64(n), err
	}
	if rerr != nil {
		return 0, rerr
	}

	created, err := h.cfg.S3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(h.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		return 0, err
	}
	abort := func(cause error) (int64, error) {
		_, _ = h.cfg.S3.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket: aws.String(h.cfg.Bucket), Key: aws.String(key), UploadId: created.UploadId,
		})
		return 0, cause
	}
	var parts []types.CompletedPart
	var total int64
	for part := int32(1); n > 0; part++ {
		out, err := h.cfg.S3.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String(h.cfg.Bucket), Key: aws.String(key), UploadId: created.UploadId,
			PartNumber: aws.Int32(part), Body: bytes.NewReader(buf[:n]), ContentLength: aws.Int64(int64(n)),
		})
		if err != nil {
			return abort(err)
		}
		parts = append(parts, types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(part)})
		total += int64(n)
		if rerr == io.ErrUnexpectedEOF { // that was the last, short part
			break
		}
		n, rerr = io.ReadFull(r, buf)
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return abort(rerr)
		}
	}
	if _, err := h.cfg.S3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(h.cfg.Bucket), Key: aws.String(key), UploadId: created.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	}); err != nil {
		return abort(err)
	}
	return total, nil
}
