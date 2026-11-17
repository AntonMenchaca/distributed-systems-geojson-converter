package loop

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type fakeSQS struct {
	SQSAPI
	maxAsked   []int32
	visibility []int32
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.maxAsked = append(f.maxAsked, in.MaxNumberOfMessages)
	f.visibility = append(f.visibility, in.VisibilityTimeout)
	return &sqs.ReceiveMessageOutput{}, nil
}

// A worker asks SQS only for as many messages as it has free slots, so it
// never holds a message (and its lease) it cannot start.
func TestLoopReceivesOnlyFreeSlots(t *testing.T) {
	fake := &fakeSQS{}
	l := New(Config{SQS: fake, QueueURL: "q", Concurrency: 4, Handle: func(context.Context, Message) {}})
	for i := 0; i < 3; i++ {
		l.slots <- struct{}{} // three jobs busy
	}
	if err := l.receiveOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.maxAsked) != 1 || fake.maxAsked[0] != 1 {
		t.Fatalf("asked SQS for %v messages, want exactly [1]", fake.maxAsked)
	}
}

func TestLoopCapsReceiveAtTen(t *testing.T) {
	fake := &fakeSQS{}
	l := New(Config{SQS: fake, QueueURL: "q", Concurrency: 16, Handle: func(context.Context, Message) {}})
	if err := l.receiveOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.maxAsked[0] != 10 {
		t.Fatalf("asked for %d, want SQS's maximum of 10", fake.maxAsked[0])
	}
}

// SQS caps visibility at 12 h from receipt and rejects ChangeMessageVisibility past it, so
// LEASE_MODE=fixed-12h must set the timeout on the receive call itself.
func TestFixed12hSetsVisibilityAtReceive(t *testing.T) {
	fake := &fakeSQS{}
	l := New(Config{SQS: fake, QueueURL: "q", Concurrency: 1, VisibilityTimeoutS: 43200, Handle: func(context.Context, Message) {}})
	if err := l.receiveOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.visibility[0] != 43200 {
		t.Fatalf("ReceiveMessage VisibilityTimeout = %d, want 43200", fake.visibility[0])
	}
}
