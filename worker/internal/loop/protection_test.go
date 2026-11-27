package loop

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeAgent stands in for the ECS agent's task-protection endpoint and records each PUT.
type fakeAgent struct {
	mu      sync.Mutex
	paths   []string
	enabled []bool
}

func newFakeAgent(t *testing.T) (*fakeAgent, string) {
	t.Helper()
	a := &fakeAgent{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProtectionEnabled bool `json:"ProtectionEnabled"`
			ExpiresInMinutes  int  `json:"ExpiresInMinutes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ExpiresInMinutes <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.paths = append(a.paths, r.Method+" "+r.URL.Path)
		a.enabled = append(a.enabled, body.ProtectionEnabled)
		a.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return a, srv.URL
}

func (a *fakeAgent) calls() ([]bool, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]bool(nil), a.enabled...), append([]string(nil), a.paths...)
}

// Two overlapping jobs turn scale-in protection on once and off once, and every call
// goes to the task-protection endpoint.
func TestProtectionToggledAroundJob(t *testing.T) {
	agent, endpoint := newFakeAgent(t)
	prot := NewProtection(endpoint, slog.Default())
	prot.RenewEvery = time.Hour

	for _, step := range []func() error{prot.Acquire, prot.Acquire, prot.Release, prot.Release} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	enabled, paths := agent.calls()
	if len(enabled) != 2 || !enabled[0] || enabled[1] {
		t.Fatalf("protection PUTs %v, want [true false]", enabled)
	}
	for _, p := range paths {
		if p != "PUT /task-protection/v1/state" {
			t.Errorf("request %q, want PUT /task-protection/v1/state", p)
		}
	}
}

// While a job runs, protection is renewed before ECS's expiry; after the last job
// finishes, the renewals stop.
func TestProtectionRenewsWhileBusy(t *testing.T) {
	agent, endpoint := newFakeAgent(t)
	prot := NewProtection(endpoint, slog.Default())
	prot.RenewEvery = 10 * time.Millisecond

	if err := prot.Acquire(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := prot.Release(); err != nil {
		t.Fatal(err)
	}
	enabled, _ := agent.calls()
	on := 0
	for _, e := range enabled {
		if e {
			on++
		}
	}
	if on < 2 {
		t.Fatalf("protection renewed %d times in 100 ms, want at least 2 (PUTs %v)", on, enabled)
	}
	if enabled[len(enabled)-1] {
		t.Fatal("last PUT left protection on after the last job")
	}
	time.Sleep(50 * time.Millisecond)
	if after, _ := agent.calls(); len(after) != len(enabled) {
		t.Errorf("%d PUTs after release; renewals should have stopped", len(after)-len(enabled))
	}
}
