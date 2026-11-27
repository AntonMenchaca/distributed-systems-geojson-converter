package loop

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Heartbeat lease defaults for LEASE_MODE=heartbeat (knobs.md): 60 s visibility,
// renewed every 20 s (every third of the lease).
const (
	HeartbeatLeaseS int32 = 60
	HeartbeatEveryS       = 20
	heartbeatEvery        = HeartbeatEveryS * time.Second
)

// Lease renews an SQS message's visibility and the job's heartbeat_at while a job runs.
// Only LEASE_MODE=heartbeat starts one; fixed-30s and fixed-12h rely on the receive-time
// visibility alone.
type Lease struct {
	SQS           SQSAPI
	QueueURL      string
	ReceiptHandle string
	LeaseS        int32         // LEASE_S: VisibilityTimeout on each renewal
	Every         time.Duration // HEARTBEAT_EVERY_S
	// Heartbeat stamps jobstore heartbeat_at for this attempt's epoch. Optional.
	Heartbeat func(context.Context) error
	// Beat wraps each renewal (chaos.Guard.Beat): a nil return during pause_s skips
	// the send so the lease can expire. Nil means always send.
	Beat func(ctx context.Context, send func(context.Context) error) error
	// Kill stops the job's ogr2ogr child when a renewal is lost. Optional.
	Kill func()
	// Tick, when set, replaces the time.Ticker (tests drive renewals explicitly).
	Tick <-chan time.Time
}

// Run renews until ctx is cancelled or a renewal fails. A failed ChangeMessageVisibility
// (or Heartbeat) calls Kill and returns: the message becomes visible again and nothing
// from this attempt should commit.
func (l *Lease) Run(ctx context.Context) error {
	var ticker *time.Ticker
	var ch <-chan time.Time
	if l.Tick != nil {
		ch = l.Tick
	} else {
		every := l.Every
		if every <= 0 {
			every = heartbeatEvery
		}
		ticker = time.NewTicker(every)
		defer ticker.Stop()
		ch = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
			if err := l.renew(ctx); err != nil {
				if l.Kill != nil {
					l.Kill()
				}
				return err
			}
		}
	}
}

func (l *Lease) renew(ctx context.Context) error {
	send := func(ctx context.Context) error {
		timeout := l.LeaseS
		if timeout <= 0 {
			timeout = HeartbeatLeaseS
		}
		_, err := l.SQS.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(l.QueueURL),
			ReceiptHandle:     aws.String(l.ReceiptHandle),
			VisibilityTimeout: timeout,
		})
		if err != nil {
			return fmt.Errorf("lease: ChangeMessageVisibility: %w", err)
		}
		if l.Heartbeat != nil {
			if err := l.Heartbeat(ctx); err != nil {
				return fmt.Errorf("lease: heartbeat: %w", err)
			}
		}
		return nil
	}
	if l.Beat != nil {
		return l.Beat(ctx, send)
	}
	return send(ctx)
}
