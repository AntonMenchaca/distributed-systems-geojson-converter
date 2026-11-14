// Package loop is the worker's SQS long-poll loop and per-job pipeline.
package loop

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// SQSAPI is the subset of the SQS client the worker uses.
type SQSAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// Message is one received SQS message.
type Message struct {
	ID            string
	Body          string
	ReceiptHandle string
}

type Config struct {
	SQS         SQSAPI
	QueueURL    string
	Concurrency int   // WORKER_CONCURRENCY=fixed-<n>
	WaitSeconds int32 // long-poll wait; 20 is SQS's maximum
	Handle      func(context.Context, Message)
	Logger      *slog.Logger
}

type Loop struct {
	cfg   Config
	slots chan struct{} // one token per running job
	wg    sync.WaitGroup
}

func New(cfg Config) *Loop {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Loop{cfg: cfg, slots: make(chan struct{}, cfg.Concurrency)}
}

// Run polls until ctx is cancelled, then waits for running jobs. SHUTDOWN_MODE=ignore: on
// SIGTERM nothing drains; ECS kills the task mid-job after its stop timeout. SHUTDOWN_MODE=drain is not implemented yet.
func (l *Loop) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := l.receiveOnce(ctx); err != nil && ctx.Err() == nil {
			l.cfg.Logger.Error("receive failed", "error", err.Error())
			time.Sleep(time.Second)
		}
	}
	l.wg.Wait()
}

// sqsMaxBatch is the most messages one ReceiveMessage call can return.
const sqsMaxBatch = 10

// receiveOnce asks SQS for at most as many messages as there are free slots,
// so the worker never holds a message, and its lease, that it cannot start.
func (l *Loop) receiveOnce(ctx context.Context) error {
	free := cap(l.slots) - len(l.slots)
	if free == 0 {
		// Wait for any job to finish, then let the next call recount.
		select {
		case l.slots <- struct{}{}:
			<-l.slots
		case <-ctx.Done():
		}
		return nil
	}
	out, err := l.cfg.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(l.cfg.QueueURL),
		MaxNumberOfMessages: int32(min(free, sqsMaxBatch)),
		WaitTimeSeconds:     l.cfg.WaitSeconds,
	})
	if err != nil {
		return err
	}
	for _, m := range out.Messages {
		l.slots <- struct{}{} // never blocks: only this goroutine acquires, and we asked for <= free
		l.wg.Add(1)
		msg := Message{ID: aws.ToString(m.MessageId), Body: aws.ToString(m.Body), ReceiptHandle: aws.ToString(m.ReceiptHandle)}
		go func() {
			defer func() { <-l.slots; l.wg.Done() }()
			l.cfg.Handle(ctx, msg)
		}()
	}
	return nil
}
