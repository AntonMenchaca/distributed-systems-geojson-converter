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

func TestShutdownModeDrainNotBuiltYet(t *testing.T) {
	if _, err := loadConfig(env(with("SHUTDOWN_MODE", "drain"))); err == nil {
		t.Fatal("SHUTDOWN_MODE=drain accepted though not implemented")
	}
	cfg, err := loadConfig(env(required))
	if err != nil || cfg.ShutdownMode != "ignore" {
		t.Fatalf("default SHUTDOWN_MODE %q, err %v; want ignore", cfg.ShutdownMode, err)
	}
}
