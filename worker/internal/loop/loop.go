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

// Message is one received SQS message. QueueURL is the queue it came from, so the worker
// deletes it there when a worker reads more than one queue (SCHED_POLICY).
type Message struct {
	ID            string
	Body          string
	ReceiptHandle string
	QueueURL      string
}

type Config struct {
	SQS         SQSAPI
	Policy      Policy // SCHED_POLICY: picks the queue for each receive; nil reads QueueURL only
	QueueURL    string
	Concurrency int   // WORKER_CONCURRENCY=fixed-<n>
	WaitSeconds int32 // long-poll wait; 20 is SQS's maximum
	// VisibilityTimeoutS overrides the queue's visibility for received messages; 0 keeps the
	// queue default (30 s, LEASE_MODE=fixed-30s). LEASE_MODE=fixed-12h sets 43200 here: SQS
	// allows 12 h only at receive time and rejects extending a message past 12 h from receipt.
	VisibilityTimeoutS int32
	Handle             func(context.Context, Message)
	Logger             *slog.Logger
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
	if cfg.Policy == nil {
		cfg.Policy = fcfs{cfg.QueueURL}
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

// multiQueueWaitS is the long-poll wait per queue when a worker reads two queues. It stays
// above 0 because a 0 s short poll samples only some SQS servers and can come back empty while
// messages exist, which would send small-first to the large queue too early; and it stays short
// because a 20 s wait on an empty small queue would leave large jobs waiting.
const multiQueueWaitS = 1

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
	// SCHED_POLICY: try the policy's queues in order and take the first that has messages.
	order := l.cfg.Policy.Order()
	wait := l.cfg.WaitSeconds
	if len(order) > 1 {
		wait = multiQueueWaitS
	}
	var out *sqs.ReceiveMessageOutput
	var queue string
	for _, queue = range order {
		var err error
		out, err = l.cfg.SQS.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queue),
			MaxNumberOfMessages: int32(min(free, sqsMaxBatch)),
			WaitTimeSeconds:     wait,
			VisibilityTimeout:   l.cfg.VisibilityTimeoutS,
		})
		if err != nil {
			return err
		}
		if len(out.Messages) > 0 {
			break
		}
	}
	for _, m := range out.Messages {
		l.slots <- struct{}{} // never blocks: only this goroutine acquires, and we asked for <= free
		l.wg.Add(1)
		msg := Message{ID: aws.ToString(m.MessageId), Body: aws.ToString(m.Body), ReceiptHandle: aws.ToString(m.ReceiptHandle), QueueURL: queue}
		go func() {
			defer func() { <-l.slots; l.wg.Done() }()
			l.cfg.Handle(ctx, msg)
		}()
	}
	return nil
}
