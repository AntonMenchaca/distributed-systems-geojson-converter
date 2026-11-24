package main

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"JOBS_TABLE": "jobs", "QUEUE_URL_JOBS": "http://q/jobs"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QueueLayout != "single" || cfg.CommitMode != "blind" || cfg.Port != "8080" || cfg.UploadMode != "proxy-buffer" {
		t.Fatalf("defaults %+v, want single / blind / 8080 / proxy-buffer", cfg)
	}
}

func TestLoadConfigRejectsBadKnobs(t *testing.T) {
	base := map[string]string{"JOBS_TABLE": "jobs", "QUEUE_URL_JOBS": "http://q/jobs"}
	for k, v := range map[string]string{"QUEUE_LAYOUT": "round-robin", "COMMIT_MODE": "yolo", "UPLOAD_MODE": "ftp", "JOBS_TABLE": ""} {
		m := map[string]string{}
		for kk, vv := range base {
			m[kk] = vv
		}
		m[k] = v
		if _, err := loadConfig(env(m)); err == nil {
			t.Errorf("%s=%q accepted; want a startup error", k, v)
		}
	}
}
