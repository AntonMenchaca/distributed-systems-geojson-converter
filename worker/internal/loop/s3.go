package loop

import (
	"context"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Objects implements Objects against one bucket.
type S3Objects struct {
	Client *s3.Client
	Bucket string
}

func (o S3Objects) Download(ctx context.Context, key, path string) error {
	out, err := o.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(o.Bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, out.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (o S3Objects) Delete(ctx context.Context, key string) error {
	_, err := o.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(o.Bucket), Key: aws.String(key)})
	return err
}

func (o S3Objects) Upload(ctx context.Context, key, path string, meta map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = o.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(o.Bucket), Key: aws.String(key), Body: f, Metadata: meta})
	return err
}
