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
	// Drain is SHUTDOWN_MODE=drain. When ctx is cancelled (SIGTERM) the loop stops
	// receiving and lets running jobs finish. After StopTimeout it cancels any job still
	// running and returns its message to the queue (visibility 0). Under drain, Handle
	// gets a context that the stop signal does not cancel.
	Drain       bool
	StopTimeout time.Duration
	// Protector, when set, is acquired before each job and released after it (scale-in
	// protection under drain). Its errors are logged; they do not stop the job.
	Protector Protector
}

// Protector is held while a job runs. Acquire and Release are called once per job.
type Protector interface {
	Acquire() error
	Release() error
}

type Loop struct {
	cfg   Config
	slots chan struct{} // one token per running job
	wg    sync.WaitGroup

	// jobCtx is the lifetime of jobs under drain. Only cancelJobs ends it, at the stop timeout.
	jobCtx     context.Context
	cancelJobs context.CancelFunc

	mu       sync.Mutex
	nextSeq  uint64
	inFlight map[uint64]Message // messages whose job has not returned
}

func New(cfg Config) *Loop {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Policy == nil {
		cfg.Policy = fcfs{cfg.QueueURL}
	}
	l := &Loop{cfg: cfg, slots: make(chan struct{}, cfg.Concurrency), inFlight: map[uint64]Message{}}
	l.jobCtx, l.cancelJobs = context.WithCancel(context.Background())
	return l
}

// Run polls until ctx is cancelled, then finishes the running jobs. SHUTDOWN_MODE=ignore:
// jobs share ctx, so cancelling it cancels them too, and the caller waits for them to return.
// SHUTDOWN_MODE=drain: see Config.Drain.
func (l *Loop) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := l.receiveOnce(ctx); err != nil && ctx.Err() == nil {
			l.cfg.Logger.Error("receive failed", "error", err.Error())
			time.Sleep(time.Second)
		}
	}
	if !l.cfg.Drain {
		l.wg.Wait()
		return
	}
	l.drain()
}

// drain waits up to StopTimeout for the running jobs. Those still running then are
// cancelled, and their messages go back to the queue. A cancelled job does not commit
// or delete (see Processor.Handle), so its message is released after it is cancelled,
// never before.
func (l *Loop) drain() {
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	timer := time.NewTimer(l.cfg.StopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}
	l.cfg.Logger.Warn("stop timeout reached; cancelling running jobs")
	l.cancelJobs()
	for _, m := range l.snapshot() {
		l.release(m)
	}
	<-done
}

// release returns a message to the queue at once: visibility 0.
func (l *Loop) release(m Message) {
	queue := m.QueueURL
	if queue == "" {
		queue = l.cfg.QueueURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := l.cfg.SQS.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(queue),
		ReceiptHandle:     aws.String(m.ReceiptHandle),
		VisibilityTimeout: 0,
	})
	if err != nil {
		// Expected if the job deleted its message just before the release.
		l.cfg.Logger.Error("release failed", "message_id", m.ID, "error", err.Error())
		return
	}
	l.cfg.Logger.Info("released message", "message_id", m.ID)
}

func (l *Loop) track(m Message) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextSeq++
	l.inFlight[l.nextSeq] = m
	return l.nextSeq
}

func (l *Loop) untrack(seq uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.inFlight, seq)
}

func (l *Loop) snapshot() []Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Message, 0, len(l.inFlight))
	for _, m := range l.inFlight {
		out = append(out, m)
	}
	return out
}

// runJob runs one job with its protection held, and clears its in-flight record when
// Handle returns.
func (l *Loop) runJob(ctx context.Context, seq uint64, m Message) {
	defer func() { l.untrack(seq); <-l.slots; l.wg.Done() }()
	if l.cfg.Protector != nil {
		if err := l.cfg.Protector.Acquire(); err != nil {
			l.cfg.Logger.Error("scale-in protection on failed", "error", err.Error())
		}
		defer func() {
			if err := l.cfg.Protector.Release(); err != nil {
				l.cfg.Logger.Error("scale-in protection off failed", "error", err.Error())
			}
		}()
	}
	l.cfg.Handle(ctx, m)
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
		jobCtx := ctx
		if l.cfg.Drain {
			jobCtx = l.jobCtx
		}
		go l.runJob(jobCtx, l.track(msg), msg)
	}
	return nil
}
