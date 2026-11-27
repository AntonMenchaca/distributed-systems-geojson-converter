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
	if cfg.Concurrency != 4 || cfg.LeaseMode != "fixed-30s" || cfg.CommitMode != "blind" || cfg.Profile != "defaults" || cfg.MeasureProfile {
		t.Fatalf("defaults %+v, want fixed-4 / fixed-30s / blind / defaults, PROFILE off (knobs.md baselines)", cfg)
	}
}

func TestProfileRunsOneJobAtATime(t *testing.T) {
	m := with("WORKER_CONCURRENCY", "fixed-4")
	m["PROFILE"] = "1"
	cfg, err := loadConfig(env(m))
	if err != nil || !cfg.MeasureProfile || cfg.Concurrency != 1 {
		t.Fatalf("PROFILE=1 with fixed-4: %+v, %v; want one job at a time", cfg, err)
	}
	if _, err := loadConfig(env(with("PROFILE", "yes"))); err == nil {
		t.Fatal("PROFILE=yes accepted; want a startup error")
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
		"WORKER_CONCURRENCY": "fixed-0", "LEASE_MODE": "forever", "COMMIT_MODE": "yolo",
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
	for mode, want := range map[string]int32{"fixed-30s": 0, "fixed-12h": 43200, "heartbeat": 60} {
		cfg, err := loadConfig(env(with("LEASE_MODE", mode)))
		if err != nil || cfg.VisibilityTimeoutS != want {
			t.Errorf("LEASE_MODE=%s: VisibilityTimeoutS %d, err %v; want %d", mode, cfg.VisibilityTimeoutS, err, want)
		}
	}
}

// plus returns the required env with extra keys added.
func plus(extra map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range required {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestLoadConfigPicksQueuesFromLayoutAndPool(t *testing.T) {
	urls := map[string]string{"QUEUE_URL_JOBS_SMALL": "http://q/small", "QUEUE_URL_JOBS_LARGE": "http://q/large"}
	for _, tc := range []struct {
		env  map[string]string
		want []string
	}{
		{map[string]string{}, []string{"http://q/jobs"}}, // the baseline: single, fcfs, shared
		{map[string]string{"QUEUE_LAYOUT": "by-size", "SCHED_POLICY": "small-first"}, []string{"http://q/small", "http://q/large"}},
		{map[string]string{"QUEUE_LAYOUT": "by-size", "SCHED_POLICY": "weighted-3:1"}, []string{"http://q/small", "http://q/large"}},
		{map[string]string{"QUEUE_LAYOUT": "by-size", "WORKER_POOL": "small"}, []string{"http://q/small"}},
		{map[string]string{"QUEUE_LAYOUT": "by-size", "WORKER_POOL": "large"}, []string{"http://q/large"}},
	} {
		env := plus(urls)
		for k, v := range tc.env {
			env[k] = v
		}
		cfg, err := loadConfig(func(k string) string { return env[k] })
		if err != nil {
			t.Errorf("%v: %v", tc.env, err)
			continue
		}
		if len(cfg.QueueURLs) != len(tc.want) || cfg.QueueURL != tc.want[0] {
			t.Errorf("%v: queues %v, want %v", tc.env, cfg.QueueURLs, tc.want)
			continue
		}
		for i := range tc.want {
			if cfg.QueueURLs[i] != tc.want[i] {
				t.Errorf("%v: queues %v, want %v", tc.env, cfg.QueueURLs, tc.want)
			}
		}
	}
}

func TestLoadConfigRejectsPolicyLayoutMismatch(t *testing.T) {
	urls := map[string]string{"QUEUE_URL_JOBS_SMALL": "http://q/small", "QUEUE_URL_JOBS_LARGE": "http://q/large"}
	for _, extra := range []map[string]string{
		{"SCHED_POLICY": "small-first"},                      // one queue: nothing to choose between
		{"QUEUE_LAYOUT": "by-size"},                          // fcfs with two queues has no single order
		{"WORKER_POOL": "small"},                             // a pool needs its own size queue
		{"QUEUE_LAYOUT": "by-size", "WORKER_POOL": "medium"}, // no such pool
		{"QUEUE_LAYOUT": "round-robin"},
	} {
		env := plus(urls)
		for k, v := range extra {
			env[k] = v
		}
		if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
			t.Errorf("%v accepted; want a startup error", extra)
		}
	}
	// by-size needs both size queues' URLs.
	env := plus(map[string]string{"QUEUE_LAYOUT": "by-size", "SCHED_POLICY": "small-first", "QUEUE_URL_JOBS_SMALL": "http://q/small"})
	if _, err := loadConfig(func(k string) string { return env[k] }); err == nil {
		t.Error("by-size without QUEUE_URL_JOBS_LARGE accepted")
	}
}
