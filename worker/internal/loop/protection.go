package loop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Protection holds ECS scale-in protection on this task while at least one job runs
// (SHUTDOWN_MODE=drain). ECS lets a protection expire after ExpiresMin minutes, so while
// a job runs the protection is renewed every RenewEvery. The endpoint is the ECS agent's
// task-protection API, $ECS_AGENT_URI/task-protection/v1/state.
type Protection struct {
	Endpoint   string        // ECS_AGENT_URI; empty means not on ECS, and nothing is sent
	ExpiresMin int           // protection lifetime per request, in minutes
	RenewEvery time.Duration // how often a busy task renews it; must be under ExpiresMin
	HTTP       *http.Client
	Logger     *slog.Logger

	mu    sync.Mutex // guards busy and the renewer; held across the HTTP calls so they stay ordered
	busy  int        // jobs running on this task
	stop  chan struct{}
	ended chan struct{}
}

// NewProtection returns a Protection that asks for 10 minutes at a time and renews every 5.
func NewProtection(endpoint string, logger *slog.Logger) *Protection {
	if logger == nil {
		logger = slog.Default()
	}
	return &Protection{
		Endpoint:   endpoint,
		ExpiresMin: 10,
		RenewEvery: 5 * time.Minute,
		HTTP:       &http.Client{Timeout: 5 * time.Second},
		Logger:     logger,
	}
}

// Acquire is called when a job starts. The first job on the task turns protection on and
// starts the renewer. A failed request is returned, but the job count still goes up, so
// every Acquire is balanced by one Release.
func (p *Protection) Acquire() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy++
	if p.busy > 1 {
		return nil
	}
	err := p.set(true)
	p.stop, p.ended = make(chan struct{}), make(chan struct{})
	go p.renew(p.stop, p.ended)
	return err
}

// Release is called when a job ends. The last job turns protection off, after the renewer
// has stopped, so a late renewal cannot switch it back on.
func (p *Protection) Release() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy--
	if p.busy > 0 {
		return nil
	}
	close(p.stop)
	<-p.ended
	return p.set(false)
}

func (p *Protection) renew(stop <-chan struct{}, ended chan<- struct{}) {
	defer close(ended)
	t := time.NewTicker(p.RenewEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if err := p.set(true); err != nil {
				p.Logger.Error("scale-in protection renewal failed", "error", err.Error())
			}
		}
	}
}

// set sends one PUT to the task-protection endpoint. An empty endpoint (local runs) does nothing.
func (p *Protection) set(on bool) error {
	if p.Endpoint == "" {
		return nil
	}
	body, err := json.Marshal(map[string]any{"ProtectionEnabled": on, "ExpiresInMinutes": p.ExpiresMin})
	if err != nil {
		return err
	}
	url := strings.TrimRight(p.Endpoint, "/") + "/task-protection/v1/state"
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("task protection: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("task protection: HTTP %d", resp.StatusCode)
	}
	return nil
}
