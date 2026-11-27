package loop

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// fakeVisSQS records ChangeMessageVisibility calls and can reject them.
type fakeVisSQS struct {
	mu       sync.Mutex
	timeouts []int32
	errAfter int // reject starting at this 0-based call index; -1 never rejects
	calls    int
}

func (f *fakeVisSQS) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return &sqs.ReceiveMessageOutput{}, nil
}
func (f *fakeVisSQS) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return &sqs.DeleteMessageOutput{}, nil
}
func (f *fakeVisSQS) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.calls
	f.calls++
	f.timeouts = append(f.timeouts, in.VisibilityTimeout)
	if f.errAfter >= 0 && n >= f.errAfter {
		return nil, errors.New("sqs: ChangeMessageVisibility rejected")
	}
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *fakeVisSQS) snapshot() (calls int, timeouts []int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	timeouts = append([]int32(nil), f.timeouts...)
	return f.calls, timeouts
}

// TestHeartbeatRenewsVisibilityEveryThird checks LEASE_MODE=heartbeat: visibility is
// extended to LEASE_S on every HEARTBEAT_EVERY_S tick (lease/3 for the 60s/20s defaults).
func TestHeartbeatRenewsVisibilityEveryThird(t *testing.T) {
	sqsFake := &fakeVisSQS{errAfter: -1}
	ticks := make(chan time.Time)
	var hbCount atomic.Int64
	lease := &Lease{
		SQS:           sqsFake,
		QueueURL:      "q",
		ReceiptHandle: "rh",
		LeaseS:        3, // LEASE_S; every tick renews for this many seconds
		Every:         time.Second,
		Tick:          ticks,
		Heartbeat: func(context.Context) error {
			hbCount.Add(1)
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lease.Run(ctx) }()

	for i := 0; i < 3; i++ {
		ticks <- time.Now()
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	calls, timeouts := sqsFake.snapshot()
	if calls != 3 {
		t.Fatalf("ChangeMessageVisibility calls = %d, want 3 (one per lease/3 tick)", calls)
	}
	for i, to := range timeouts {
		if to != 3 {
			t.Fatalf("renewal %d VisibilityTimeout = %d, want LEASE_S=3", i, to)
		}
	}
	if hbCount.Load() != 3 {
		t.Fatalf("store Heartbeat calls = %d, want 3", hbCount.Load())
	}
}

// TestLostRenewalStopsJob: when SQS rejects ChangeMessageVisibility, the lease kills the
// child process and the job must not commit.
func TestLostRenewalStopsJob(t *testing.T) {
	sqsFake := &fakeVisSQS{errAfter: 0} // first renewal fails
	ticks := make(chan time.Time)
	cmd := sleepCmd(30 * time.Second)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	var committed atomic.Bool
	var killed atomic.Bool
	lease := &Lease{
		SQS:           sqsFake,
		QueueURL:      "q",
		ReceiptHandle: "rh",
		LeaseS:        60,
		Every:         time.Second,
		Tick:          ticks,
		Heartbeat:     func(context.Context) error { return nil },
		Kill: func() {
			killed.Store(true)
			_ = cmd.Process.Kill()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lease.Run(ctx) }()

	ticks <- time.Now()
	err := <-done
	if err == nil {
		t.Fatal("Run returned nil; want an error after a lost renewal")
	}
	if !killed.Load() {
		t.Fatal("Kill was not called after ChangeMessageVisibility failed")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("child process still running after lost renewal")
	}
	if committed.Load() {
		t.Fatal("job committed after a lost renewal; want nothing committed")
	}
}

func sleepCmd(d time.Duration) *exec.Cmd {
	sec := strconv.Itoa(int(d.Seconds()))
	if runtime.GOOS == "windows" {
		return exec.Command("timeout", "/T", sec, "/NOBREAK")
	}
	return exec.Command("sleep", sec)
}
