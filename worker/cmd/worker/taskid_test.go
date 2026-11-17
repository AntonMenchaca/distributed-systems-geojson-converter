package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const arn = "arn:aws:ecs:us-east-1:123456789012:task/gc-c-cluster/0123456789abcdef0123456789abcdef"

// attempts[].task_id must join to tasks.csv and chaos-kill events, which use ECS task ARNs.
func TestTaskIDIsTheECSTaskARN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/task" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"Cluster":"gc-c-cluster","TaskARN":"` + arn + `"}`))
	}))
	defer srv.Close()
	got, err := resolveTaskID(env(map[string]string{"ECS_CONTAINER_METADATA_URI_V4": srv.URL}))
	if err != nil || got != arn {
		t.Fatalf("task id %q, %v; want %s", got, err, arn)
	}
}

func TestTaskIDFallsBackOutsideECS(t *testing.T) {
	host, _ := os.Hostname()
	if got, err := resolveTaskID(env(map[string]string{})); err != nil || got != host {
		t.Fatalf("task id %q, %v; want hostname %q", got, err, host)
	}
	if got, _ := resolveTaskID(env(map[string]string{"TASK_ID": "local-1"})); got != "local-1" {
		t.Fatalf("TASK_ID override ignored: %q", got)
	}
}
