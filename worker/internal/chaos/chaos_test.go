package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Faults stay off unless CHAOS_ENABLED=1, including when the job carries every flag.
// A normal run must not crash, pause, or drop a heartbeat because a trace has a chaos column.
func TestChaosDisabledByDefault(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes"} {
		t.Run(fmt.Sprintf("CHAOS_ENABLED=%q", v), func(t *testing.T) {
			t.Setenv("CHAOS_ENABLED", v)
			g, err := New(os.Getenv, json.RawMessage(`{"crash_at":0.5,"pause_s":30,"slow":4}`), 1)
			if err != nil {
				t.Fatal(err)
			}
			if g.Enabled() {
				t.Fatal("chaos is active")
			}
			g.sleep = func(context.Context, time.Duration) error {
				t.Fatal("slept while chaos is off")
				return nil
			}
			if err := g.Hold(context.Background()); err != nil {
				t.Fatal(err)
			}
			g.OnProgress(0)
			g.OnProgress(0.5)
			g.OnProgress(1)
			beats := 0
			if err := g.Beat(context.Background(), func(context.Context) error { beats++; return nil }); err != nil || beats != 1 {
				t.Fatalf("heartbeat while chaos is off: beats=%d err=%v", beats, err)
			}
		})
	}
}

// crash_at 0.5 must kill the process once convert has started and before it finishes.
// The child is this test re-exec'd: exiting is the fault, so the parent asserts the death.
func TestCrashAtHalfKillsMidConvert(t *testing.T) {
	if os.Getenv("CHAOS_CRASH_CHILD") == "1" {
		crashChild(t)
		return
	}

	marker := filepath.Join(t.TempDir(), "progress")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashAtHalfKillsMidConvert$", "-test.count=1")
	cmd.Env = append(withoutChaosEnv(os.Environ()), "CHAOS_CRASH_CHILD=1", "CHAOS_ENABLED=1", "CHAOS_MARKER="+marker)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 137 {
		t.Fatalf("child exit %v, want 137 (killed mid-convert)\n%s", err, out)
	}
	got := strings.Fields(string(mustRead(t, marker)))
	want := []string{"0", "0.25", "0.5"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("progress before death %v, want %v (started, reached half, did not finish)", got, want)
	}
}

// crash_at rides on the queue message, which SQS redelivers unchanged. The second claim
// is epoch 2, not a second receive count. Same flag, CHAOS_ENABLED=1: convert finishes.
func TestCrashAtHalfLetsLaterEpochFinish(t *testing.T) {
	if os.Getenv("CHAOS_CRASH_CHILD") == "1" {
		laterEpochChild(t)
		return
	}

	marker := filepath.Join(t.TempDir(), "progress")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashAtHalfLetsLaterEpochFinish$", "-test.count=1")
	cmd.Env = append(withoutChaosEnv(os.Environ()), "CHAOS_CRASH_CHILD=1", "CHAOS_ENABLED=1", "CHAOS_MARKER="+marker)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("epoch 2 child exit %v, want success (crash_at kills only epoch 1)\n%s", err, out)
	}
	got := strings.Fields(string(mustRead(t, marker)))
	want := []string{"0", "0.25", "0.5", "0.75", "1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("progress %v, want %v (epoch 2 converts through crash_at)", got, want)
	}
}

func laterEpochChild(t *testing.T) {
	t.Helper()
	g, err := New(os.Getenv, json.RawMessage(`{"crash_at":0.5}`), 2)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	marker := os.Getenv("CHAOS_MARKER")
	for _, frac := range []float64{0, 0.25, 0.5, 0.75, 1} {
		if err := appendLine(marker, fmt.Sprintf("%g", frac)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		g.OnProgress(frac)
	}
}

func crashChild(t *testing.T) {
	t.Helper()
	g, err := New(os.Getenv, json.RawMessage(`{"crash_at":0.5}`), 1)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	marker := os.Getenv("CHAOS_MARKER")
	for _, frac := range []float64{0, 0.25, 0.5, 0.75, 1} {
		if err := appendLine(marker, fmt.Sprintf("%g", frac)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		g.OnProgress(frac)
	}
	t.Fatal("convert finished; crash_at 0.5 should have killed the process at half")
}

// pause_s freezes the job and, for that interval, heartbeats must not be sent.
// slow is the opposite: the job takes longer and heartbeats keep going. The lease
// loop (not this step) calls Beat, so a pause can outlast a lease and a slow job cannot.
func TestPauseStopsHeartbeats(t *testing.T) {
	t.Setenv("CHAOS_ENABLED", "1")
	g, err := New(os.Getenv, json.RawMessage(`{"pause_s":90}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	beats := 0
	send := func(context.Context) error { beats++; return nil }
	if err := g.Beat(context.Background(), send); err != nil || beats != 1 {
		t.Fatalf("heartbeat before pause: beats=%d err=%v", beats, err)
	}
	slept := false
	g.sleep = func(_ context.Context, d time.Duration) error {
		slept = true
		if d != 90*time.Second {
			t.Errorf("paused %s, want 90s", d)
		}
		before := beats
		if err := g.Beat(context.Background(), send); err != nil {
			t.Errorf("beat during pause: %v", err)
		}
		if beats != before {
			t.Errorf("heartbeats during pause: %d -> %d", before, beats)
		}
		return nil
	}
	if err := g.Hold(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slept {
		t.Fatal("pause_s did not hold the job")
	}
	if err := g.Beat(context.Background(), send); err != nil || beats != 2 {
		t.Fatalf("heartbeat after pause: beats=%d err=%v", beats, err)
	}
}

// slow stretches the gaps between convert ticks and does not suppress heartbeats.
// true is the flag form of factor 2.
func TestSlowStretchesConvertWithoutStoppingHeartbeats(t *testing.T) {
	t.Setenv("CHAOS_ENABLED", "1")
	gTrue, err := New(os.Getenv, json.RawMessage(`{"slow":true}`), 1)
	if err != nil || gTrue.flags.Slow != 2 {
		t.Fatalf("slow true = %v, err %v; want factor 2", gTrue.flags.Slow, err)
	}
	g, err := New(os.Getenv, json.RawMessage(`{"slow":2}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	var slept time.Duration
	g.sleep = func(_ context.Context, d time.Duration) error {
		slept += d
		return nil
	}
	g.OnProgress(0)
	if slept != 0 {
		t.Fatalf("slept %s on the first tick", slept)
	}
	g.last = time.Now().Add(-time.Second)
	g.OnProgress(0.5)
	if slept < 900*time.Millisecond || slept > 1100*time.Millisecond {
		t.Fatalf("stretch %s, want about 1s (factor 2 over 1s of convert)", slept)
	}
	beats := 0
	if err := g.Beat(context.Background(), func(context.Context) error { beats++; return nil }); err != nil || beats != 1 {
		t.Fatalf("slow stopped heartbeats: beats=%d err=%v", beats, err)
	}
}

func withoutChaosEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if strings.EqualFold(k, "CHAOS_ENABLED") || strings.EqualFold(k, "CHAOS_CRASH_CHILD") || strings.EqualFold(k, "CHAOS_MARKER") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		return err
	}
	return f.Sync()
}
