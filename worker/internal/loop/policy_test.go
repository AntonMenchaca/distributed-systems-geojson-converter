package loop

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// queuesSQS holds a message count per queue URL (-1 = never runs out) and logs every receive.
type queuesSQS struct {
	SQSAPI
	left  map[string]int
	asked []string // every queue a receive was sent to, in order
	got   []string // the queue of every receive that returned a message
	waits []int32
}

func (f *queuesSQS) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	q := aws.ToString(in.QueueUrl)
	f.asked = append(f.asked, q)
	f.waits = append(f.waits, in.WaitTimeSeconds)
	if f.left[q] == 0 {
		return &sqs.ReceiveMessageOutput{}, nil
	}
	if f.left[q] > 0 {
		f.left[q]--
	}
	f.got = append(f.got, q)
	return &sqs.ReceiveMessageOutput{Messages: []types.Message{{MessageId: aws.String("m"), Body: aws.String("{}"), ReceiptHandle: aws.String("r")}}}, nil
}

func newPolicyLoop(t *testing.T, policy string, fake *queuesSQS, handled chan<- Message) *Loop {
	t.Helper()
	p, err := NewPolicy(policy, []string{"small", "large"})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{SQS: fake, Policy: p, Concurrency: 1000, WaitSeconds: 20,
		Handle: func(_ context.Context, m Message) { handled <- m }})
}

func TestSmallFirstReadsLargeOnlyWhenSmallEmpty(t *testing.T) {
	fake := &queuesSQS{left: map[string]int{"small": 5, "large": -1}}
	handled := make(chan Message, 100)
	l := newPolicyLoop(t, "small-first", fake, handled)
	for i := 0; i < 6; i++ {
		if err := l.receiveOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	l.wg.Wait()
	// Five receives drain small without ever asking large; the sixth finds small empty and reads large.
	want := []string{"small", "small", "small", "small", "small", "small", "large"}
	if len(fake.asked) != len(want) {
		t.Fatalf("receives went to %v, want %v", fake.asked, want)
	}
	for i := range want {
		if fake.asked[i] != want[i] {
			t.Fatalf("receives went to %v, want %v", fake.asked, want)
		}
	}
	// Each message remembers its queue, so the worker deletes it from the queue it came from.
	close(handled)
	for m := range handled {
		if m.QueueURL != "small" && m.QueueURL != "large" {
			t.Fatalf("message from queue %q", m.QueueURL)
		}
	}
}

func TestWeightedThreeToOne(t *testing.T) {
	fake := &queuesSQS{left: map[string]int{"small": -1, "large": -1}}
	handled := make(chan Message, 1000)
	l := newPolicyLoop(t, "weighted-3:1", fake, handled)
	for i := 0; i < 400; i++ {
		if err := l.receiveOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	l.wg.Wait()
	small := 0
	for _, q := range fake.got {
		if q == "small" {
			small++
		}
	}
	if len(fake.got) != 400 || small < 290 || small > 310 {
		t.Fatalf("%d of %d receives were small, want 300 ±10 of 400", small, len(fake.got))
	}
}

func TestWeightedReadsTheOtherQueueWhenTheChosenOneIsEmpty(t *testing.T) {
	fake := &queuesSQS{left: map[string]int{"small": 0, "large": -1}}
	l := newPolicyLoop(t, "weighted-3:1", fake, make(chan Message, 100))
	for i := 0; i < 8; i++ {
		_ = l.receiveOnce(context.Background())
	}
	l.wg.Wait()
	if len(fake.got) != 8 {
		t.Fatalf("got %d messages in 8 receives with large full, want 8 (no idle receive)", len(fake.got))
	}
}

// With two queues every receive waits at most 1 s: long polling (wait > 0) sees all of a
// queue's messages, unlike a 0 s short poll, while a 20 s wait on an empty small queue would
// stall large jobs.
func TestTwoQueuePoliciesUseShortLongPolls(t *testing.T) {
	fake := &queuesSQS{left: map[string]int{"small": 0, "large": 0}}
	l := newPolicyLoop(t, "small-first", fake, make(chan Message, 1))
	_ = l.receiveOnce(context.Background())
	for _, w := range fake.waits {
		if w != multiQueueWaitS {
			t.Fatalf("waits %v, want %d s on each queue", fake.waits, multiQueueWaitS)
		}
	}
}

func TestNewPolicyRejectsWrongQueueCounts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		queues []string
	}{
		{"fcfs", []string{"small", "large"}},
		{"small-first", []string{"jobs"}},
		{"weighted-3:1", []string{"jobs"}},
		{"round-robin", []string{"jobs"}},
	} {
		if _, err := NewPolicy(tc.name, tc.queues); err == nil {
			t.Errorf("SCHED_POLICY=%s with %d queue(s) accepted", tc.name, len(tc.queues))
		}
	}
}
