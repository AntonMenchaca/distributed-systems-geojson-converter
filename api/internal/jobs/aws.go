package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// SQSEnqueuer sends messages to queues by name; URLs come from QUEUE_URL_* env vars.
type SQSEnqueuer struct {
	Client *sqs.Client
	URLs   map[string]string // queue name -> URL
}

func (q SQSEnqueuer) Send(ctx context.Context, queue string, m Message) error {
	url, ok := q.URLs[queue]
	if !ok {
		return fmt.Errorf("no URL configured for queue %q", queue)
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = q.Client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(url), MessageBody: aws.String(string(body))})
	return err
}

// S3OutputURLs presigns GET URLs for outputs in Bucket.
type S3OutputURLs struct {
	Presigner *s3.PresignClient
	Bucket    string
	Expires   time.Duration
}

func (p S3OutputURLs) OutputURL(ctx context.Context, key string) (string, error) {
	req, err := p.Presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(p.Bucket), Key: aws.String(key)},
		s3.WithPresignExpires(p.Expires))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}
