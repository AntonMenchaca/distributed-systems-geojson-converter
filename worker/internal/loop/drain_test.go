package loop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// drainSQS hands out its pending messages one per receive. After that a receive
// long-polls until its context is cancelled, as SQS does when the queue is empty.
type drainSQS struct {
	SQSAPI
	mu                sync.Mutex
	pending           []sqstypes.Message
	receivesAfterStop int // receives that started with the stop context already cancelled
	changes           []visibilityChange
}

type visibilityChange struct {
	queue, handle string
	timeout       int32
}

func (f *drainSQS) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	if ctx.Err() != nil {
		f.receivesAfterStop++
	}
	if len(f.pending) > 0 {
		m := f.pending[0]
		f.pending = f.pending[1:]
		f.mu.Unlock()
		return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{m}}, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *drainSQS) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, visibilityChange{
		queue:   aws.ToString(in.QueueUrl),
		handle:  aws.ToString(in.ReceiptHandle),
		timeout: in.VisibilityTimeout,
	})
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func testMessage(id string) sqstypes.Message {
	return sqstypes.Message{MessageId: aws.String(id), Body: aws.String("{}"), ReceiptHandle: aws.String("rh-" + id)}
}

// SHUTDOWN_MODE=drain: once the stop signal cancels the context, no new receive starts,
// but a job already running is let finish. It finishes inside the stop timeout, so its
// message is not released.
func TestDrainStopsReceiving(t *testing.T) {
	fake := &drainSQS{pending: []sqstypes.Message{testMessage("a")}}
	started := make(chan struct{})
	finish := make(chan struct{})
	cancelledMidJob := false
	handle := func(ctx context.Context, _ Message) {
		close(started)
		<-finish
		cancelledMidJob = ctx.Err() != nil
	}
	l := New(Config{
		SQS: fake, QueueURL: "q", Concurrency: 2, WaitSeconds: 1, Handle: handle,
		Drain: true, StopTimeout: 5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()

	<-started
	cancel() // SIGTERM
	select {
	case <-done:
		t.Fatal("Run returned while a job was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(finish)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the running job finished")
	}
	if fake.receivesAfterStop != 0 {
		t.Errorf("%d receives started after the stop; want 0", fake.receivesAfterStop)
	}
	if cancelledMidJob {
		t.Error("the stop cancelled a job that should have finished within the stop timeout")
	}
	if len(fake.changes) != 0 {
		t.Errorf("released %d messages of a job that finished in time; want 0", len(fake.changes))
	}
}

// Drain runs out of time: the job is cancelled and its message goes back to the queue
// with visibility 0, so another worker can take it at once. The loop does this for every
// LEASE_MODE, because it tracks receipt handles itself rather than relying on a Lease.
func TestDrainReleasesUnfinishedLeases(t *testing.T) {
	fake := &drainSQS{pending: []sqstypes.Message{testMessage("a")}}
	started := make(chan struct{})
	handle := func(ctx context.Context, _ Message) {
		close(started)
		<-ctx.Done() // cut off mid-convert: never committed, never deleted
	}
	l := New(Config{
		SQS: fake, QueueURL: "q", Concurrency: 1, WaitSeconds: 1, Handle: handle,
		Drain: true, StopTimeout: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()

	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the stop timeout")
	}
	want := []visibilityChange{{queue: "q", handle: "rh-a", timeout: 0}}
	if len(fake.changes) != len(want) || fake.changes[0] != want[0] {
		t.Fatalf("visibility changes %+v, want %+v", fake.changes, want)
	}
}

type recordProtector struct {
	mu     sync.Mutex
	events []string
}

func (p *recordProtector) add(e string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *recordProtector) Acquire() error { p.add("acquire"); return nil }
func (p *recordProtector) Release() error { p.add("release"); return nil }

// Protection is held for the whole run of a job, and released after Handle returns.
func TestJobHoldsProtectionForItsRun(t *testing.T) {
	fake := &drainSQS{pending: []sqstypes.Message{testMessage("a")}}
	prot := &recordProtector{}
	started := make(chan struct{})
	handle := func(context.Context, Message) {
		close(started)
		prot.add("handle")
	}
	l := New(Config{
		SQS: fake, QueueURL: "q", Concurrency: 1, WaitSeconds: 1, Handle: handle,
		Drain: true, StopTimeout: time.Second, Protector: prot,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()

	<-started
	cancel()
	<-done
	want := []string{"acquire", "handle", "release"}
	if len(prot.events) != len(want) {
		t.Fatalf("events %v, want %v", prot.events, want)
	}
	for i := range want {
		if prot.events[i] != want[i] {
			t.Fatalf("events %v, want %v", prot.events, want)
		}
	}
}
