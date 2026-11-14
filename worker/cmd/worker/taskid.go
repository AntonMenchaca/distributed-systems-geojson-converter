package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// resolveTaskID names this worker in attempts[].task_id. On ECS it is the task ARN from the
// task metadata endpoint, so attempts join to tasks.csv and chaos-kill events; the Fargate
// hostname is an ENI IP name that gets reused. TASK_ID overrides; locally the hostname.
func resolveTaskID(getenv func(string) string) (string, error) {
	if id := getenv("TASK_ID"); id != "" {
		return id, nil
	}
	if uri := getenv("ECS_CONTAINER_METADATA_URI_V4"); uri != "" {
		client := http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(uri + "/task")
		if err != nil {
			return "", fmt.Errorf("task metadata: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("task metadata: status %d", resp.StatusCode)
		}
		var meta struct {
			TaskARN string `json:"TaskARN"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil || meta.TaskARN == "" {
			return "", fmt.Errorf("task metadata: no TaskARN (%v)", err)
		}
		return meta.TaskARN, nil
	}
	return os.Hostname()
}
