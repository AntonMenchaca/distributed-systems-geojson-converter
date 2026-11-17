package main

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var required = map[string]string{"JOBS_TABLE": "jobs", "QUEUE_URL_JOBS": "http://q/jobs", "BUCKET": "b"}

func with(k, v string) map[string]string {
	m := map[string]string{k: v}
	for kk, vv := range required {
		if kk != k {
			m[kk] = vv
		}
	}
	return m
}

func TestLoadConfigBaselineDefaults(t *testing.T) {
	cfg, err := loadConfig(env(required))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 4 || cfg.LeaseMode != "fixed-30s" || cfg.CommitMode != "blind" || cfg.Profile != "defaults" {
		t.Fatalf("defaults %+v, want fixed-4 / fixed-30s / blind / defaults (knobs.md baselines)", cfg)
	}
}

func TestLoadConfigParsesFixedConcurrency(t *testing.T) {
	cfg, err := loadConfig(env(with("WORKER_CONCURRENCY", "fixed-2")))
	if err != nil || cfg.Concurrency != 2 {
		t.Fatalf("fixed-2: %+v, %v", cfg, err)
	}
}

func TestLoadConfigRejectsUnknownOrUnbuiltKnobs(t *testing.T) {
	for k, v := range map[string]string{
		"WORKER_CONCURRENCY": "fixed-0", "LEASE_MODE": "heartbeat", "COMMIT_MODE": "yolo",
		"GDAL_PROFILE": "tuned", "BUCKET": "",
	} {
		if _, err := loadConfig(env(with(k, v))); err == nil {
			t.Errorf("%s=%q accepted; want a startup error", k, v)
		}
	}
	if _, err := loadConfig(env(with("WORKER_CONCURRENCY", "mem-budget"))); err == nil {
		t.Error("mem-budget accepted though not implemented")
	}
}

func TestLeaseModeSetsReceiveVisibility(t *testing.T) {
	for mode, want := range map[string]int32{"fixed-30s": 0, "fixed-12h": 43200} {
		cfg, err := loadConfig(env(with("LEASE_MODE", mode)))
		if err != nil || cfg.VisibilityTimeoutS != want {
			t.Errorf("LEASE_MODE=%s: VisibilityTimeoutS %d, err %v; want %d", mode, cfg.VisibilityTimeoutS, err, want)
		}
	}
}
