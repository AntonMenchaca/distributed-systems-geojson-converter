package main

import (
	"syscall"
	"testing"
	"time"
)

// SHUTDOWN_MODE=ignore: SIGTERM changes nothing; ECS's SIGKILL after the stop timeout ends
// the task mid-job. Jobs must not be cancelled at SIGTERM.
func TestIgnoreModeSurvivesSIGTERM(t *testing.T) {
	ctx, stop := runContext("ignore")
	defer stop()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("context cancelled by SIGTERM in ignore mode: %v", ctx.Err())
	}
}

// SHUTDOWN_MODE=drain: SIGTERM cancels the stop context, which ends polling and starts
// the drain. Running jobs keep their own context (see loop.Run).
func TestDrainModeSIGTERMStopsReceiving(t *testing.T) {
	ctx, stop := runContext("drain")
	defer stop()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM did not stop receiving in drain mode")
	}
}

func TestShutdownModeDrainAccepted(t *testing.T) {
	cfg, err := loadConfig(env(with("SHUTDOWN_MODE", "drain")))
	if err != nil || cfg.ShutdownMode != "drain" {
		t.Fatalf("SHUTDOWN_MODE=drain: mode %q, err %v; want drain accepted", cfg.ShutdownMode, err)
	}
	cfg, err = loadConfig(env(required))
	if err != nil || cfg.ShutdownMode != "ignore" {
		t.Fatalf("default SHUTDOWN_MODE %q, err %v; want ignore", cfg.ShutdownMode, err)
	}
}

// STOP_TIMEOUT_S is ECS's stopTimeout in seconds. Fargate allows up to 120, and the worker
// needs 10 s of that to return messages, so 11 is the smallest useful value.
func TestStopTimeoutBounds(t *testing.T) {
	cfg, err := loadConfig(env(required))
	if err != nil || cfg.StopTimeoutS != 120 {
		t.Fatalf("default STOP_TIMEOUT_S %d, err %v; want 120", cfg.StopTimeoutS, err)
	}
	for _, v := range []string{"10", "121", "soon", "0"} {
		if _, err := loadConfig(env(with("STOP_TIMEOUT_S", v))); err == nil {
			t.Errorf("STOP_TIMEOUT_S=%s accepted; want an error", v)
		}
	}
	if cfg, err := loadConfig(env(with("STOP_TIMEOUT_S", "11"))); err != nil || cfg.StopTimeoutS != 11 {
		t.Errorf("STOP_TIMEOUT_S=11: %d, err %v; want accepted", cfg.StopTimeoutS, err)
	}
}
