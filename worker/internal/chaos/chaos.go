// Package chaos injects the per-job faults carried in a queue message's chaos object
// (schemas/queue-message.json, the trace CSV's chaos column). Nothing here runs unless
// CHAOS_ENABLED=1, so one trace is either a normal run or a repeatable failure run.
//
//	crash_at  fraction of the conversion, in (0, 1]. At that progress the process exits
//	          137, the conventional status of SIGKILL, on the first claim only. First
//	          means epoch == 1, the epoch Claim returns for that claim. A redelivery
//	          claims epoch 2 and converts through. It does not mean the SQS receive
//	          count. The flag stays on the message. The killed attempt stays RUNNING
//	          because a dead process cannot record a failure.
//	pause_s   seconds the job freezes. Heartbeats are not sent during the freeze, so a
//	          pause longer than the lease expires it.
//	slow      factor the conversion is stretched by (2, or true, means twice as long).
//	          Heartbeats keep being sent, which is what makes slow different from pause.
//
// The heartbeat lease is a later step. That loop must call Beat, and a nil return during
// a pause means the beat was not sent. This package does not renew a lease.
package chaos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Enabled reports whether fault injection is armed. Only the value 1 arms it.
func Enabled(getenv func(string) string) bool {
	return getenv("CHAOS_ENABLED") == "1"
}

// slowFlag is the slow flag: a factor >= 1, or true for 2x. false and 0 are off.
type slowFlag float64

func (s *slowFlag) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case "true":
		*s = 2
		return nil
	case "false", "null":
		*s = 0
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("chaos: slow must be a factor >= 1 or true")
	}
	*s = slowFlag(f)
	return nil
}

type flags struct {
	CrashAt *float64 `json:"crash_at,omitempty"`
	PauseS  float64  `json:"pause_s,omitempty"`
	Slow    slowFlag `json:"slow,omitempty"`
}

func (f flags) validate() error {
	if f.CrashAt != nil && (*f.CrashAt <= 0 || *f.CrashAt > 1) {
		return fmt.Errorf("chaos: crash_at %v, want a fraction in (0, 1]", *f.CrashAt)
	}
	if f.PauseS < 0 {
		return fmt.Errorf("chaos: pause_s %v, want seconds >= 0", f.PauseS)
	}
	if f.Slow < 0 || (f.Slow > 0 && f.Slow < 1) {
		return fmt.Errorf("chaos: slow %v, want a factor >= 1 or true", float64(f.Slow))
	}
	return nil
}

// Guard is one job's faults. A disabled guard ignores its flags, whatever they say.
// epoch is the claim this guard was built for: 1 is the first claim, 2 the redelivery.
type Guard struct {
	enabled bool
	epoch   int64
	flags   flags
	paused  atomic.Bool
	last    time.Time
	sleep   func(context.Context, time.Duration) error
}

// New reads raw, the queue message's chaos object, for the attempt Claim returned as
// epoch. When chaos is not armed, raw is ignored, including when it is malformed: a
// normal run must not fail a job over a flag it is not going to apply.
func New(getenv func(string) string, raw json.RawMessage, epoch int64) (*Guard, error) {
	g := &Guard{enabled: Enabled(getenv), epoch: epoch, sleep: defaultSleep}
	if !g.enabled || len(raw) == 0 || string(raw) == "null" {
		return g, nil
	}
	if err := json.Unmarshal(raw, &g.flags); err != nil {
		return nil, fmt.Errorf("chaos: %w", err)
	}
	if err := g.flags.validate(); err != nil {
		return nil, err
	}
	return g, nil
}

// Enabled reports whether this guard will apply its flags.
func (g *Guard) Enabled() bool { return g != nil && g.enabled }

// WantsProgress reports whether convert should report progress. crash_at and slow are
// both measured against that progress; pause_s is a single freeze and needs none.
func (g *Guard) WantsProgress() bool {
	return g != nil && g.enabled && (g.flags.CrashAt != nil || g.flags.Slow > 1)
}

// Hold freezes the job for pause_s. Heartbeats are suppressed until it returns.
func (g *Guard) Hold(ctx context.Context) error {
	if g == nil || !g.enabled || g.flags.PauseS <= 0 {
		return nil
	}
	g.paused.Store(true)
	defer g.paused.Store(false)
	d := time.Duration(g.flags.PauseS * float64(time.Second))
	return g.sleeper()(ctx, d)
}

// Beat sends one heartbeat, unless a pause is in effect. A nil error during a pause
// means the beat was skipped, not that a lease was renewed.
func (g *Guard) Beat(ctx context.Context, send func(context.Context) error) error {
	if g != nil && g.paused.Load() {
		return nil
	}
	if send == nil {
		return nil
	}
	return send(ctx)
}

// OnProgress is called with convert progress in [0, 1]. At crash_at the process exits
// 137 when this guard's epoch is 1. A later epoch converts through. slow stretches the
// time between ticks by that factor on every epoch and does not touch heartbeats.
func (g *Guard) OnProgress(frac float64) {
	if g == nil || !g.enabled {
		return
	}
	if g.flags.Slow > 1 {
		if !g.last.IsZero() {
			extra := time.Duration(float64(time.Since(g.last)) * (float64(g.flags.Slow) - 1))
			if extra > 0 {
				_ = g.sleeper()(context.Background(), extra)
			}
		}
		g.last = time.Now()
	}
	if g.epoch == 1 && g.flags.CrashAt != nil && frac >= *g.flags.CrashAt {
		os.Exit(137)
	}
}

func (g *Guard) sleeper() func(context.Context, time.Duration) error {
	if g.sleep != nil {
		return g.sleep
	}
	return defaultSleep
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
