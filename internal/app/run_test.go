package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/alert"
	"github.com/akaike-byob/dokploy-sentinel/internal/clock"
	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// memSample builds a sample where only mem_pressure can produce a finding; every
// other source is OK/UNKNOWN-and-ignored, so the test observes a single key.
func memSample(usedPct float64, ok bool, runID int64, now time.Time) *collect.Sample {
	m := collect.MemInfo{Health: health.OK, MemTotalKB: 100, MemAvailableKB: uint64(100 - usedPct)}
	if !ok {
		m = collect.MemInfo{Health: health.UNKNOWN}
	}
	return &collect.Sample{
		RunID: runID, Timestamp: now, BootID: "boot-1", SwapPresent: false,
		Mem:    m,
		Vmstat: collect.VmstatInfo{Health: health.OK},
		Load:   collect.LoadInfo{Health: health.OK},
		Docker: collect.DockerInfo{Health: health.UNKNOWN, Err: "no socket"},
	}
}

type mockSlack struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (m *mockSlack) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.bodies = append(m.bodies, body)
		m.mu.Unlock()
		w.WriteHeader(200)
	}
}

func (m *mockSlack) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies)
}

func newRunner(t *testing.T, srvURL string, script func(runID int64, now time.Time) *collect.Sample) (*Runner, *clock.Fake) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.StatePath = filepath.Join(dir, "state.json")
	cfg.ReportPath = filepath.Join(dir, "report.json")
	cfg.HostLabel = "test-box"
	cfg.Targets = map[string]config.TargetConfig{"team": {URL: srvURL}}
	cfg.Routing = config.RoutingConfig{WARN: []string{"team"}, ALERT: []string{"team"}, PAGE: []string{"team"}}

	fc := clock.NewFake(time.Unix(1_700_000_000, 0).UTC())
	r := &Runner{
		Cfg:   cfg,
		Clock: fc,
		Collect: func(_ context.Context, _ *config.Config, runID int64, now time.Time, _ string) *collect.Sample {
			return script(runID, now)
		},
		Sender: alert.NewSender(&http.Client{}, alert.SlackRenderer{}, func(time.Duration) {}),
	}
	return r, fc
}

// TestSequentialFireCooldownResolve reproduces docs/plan/09-testing.md §9.4:
// pending, pending, FIRE, silent(cooldown), pending-resolve, RESOLVED — exactly
// two POSTs.
func TestSequentialFireCooldownResolve(t *testing.T) {
	ms := &mockSlack{}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()

	// used 82% = WARN (>80); used 20% = OK.
	seq := []struct {
		used float64
		ok   bool
		step time.Duration
	}{
		{82, true, time.Minute}, // run1: pending
		{82, true, time.Minute}, // run2: pending
		{82, true, time.Minute}, // run3: FIRE (send #1)
		{82, true, time.Minute}, // run4: within 24h cooldown → silent
		{20, true, time.Minute}, // run5: OK → pending-resolve (need 2)
		{20, true, time.Minute}, // run6: OK → RESOLVED (send #2)
	}
	idx := 0
	r, fc := newRunner(t, srv.URL, func(runID int64, now time.Time) *collect.Sample {
		return memSample(seq[idx].used, seq[idx].ok, runID, now)
	})

	sends := []int{}
	for i := range seq {
		idx = i
		fc.Advance(seq[i].step)
		res, err := r.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("run %d error: %v", i+1, err)
		}
		sends = append(sends, len(res.Sends))
	}

	if ms.count() != 2 {
		t.Fatalf("expected exactly 2 Slack POSTs, got %d (per-run sends: %v)", ms.count(), sends)
	}
	// Send #1 is the WARN fire; send #2 is the green RESOLVED.
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if txt, _ := ms.bodies[0]["text"].(string); !contains(txt, "WARN") {
		t.Errorf("first send should be WARN, got %q", txt)
	}
	if txt, _ := ms.bodies[1]["text"].(string); !contains(txt, "RESOLVED") {
		t.Errorf("second send should be RESOLVED, got %q", txt)
	}
}

// TestUnknownMidFiringDoesNotResolve proves a socket blip mid-firing does not
// produce a false all-clear.
func TestUnknownMidFiringDoesNotResolve(t *testing.T) {
	ms := &mockSlack{}
	srv := httptest.NewServer(ms.handler())
	defer srv.Close()

	// PAGE fires on first breach (flap=1). used 96% = PAGE.
	seq := []struct {
		used float64
		ok   bool
	}{
		{96, true}, // run1: PAGE fire
		{0, false}, // run2: UNKNOWN → must NOT resolve
		{20, true}, // run3: OK (1)
		{20, true}, // run4: OK (2) → resolve
	}
	idx := 0
	r, fc := newRunner(t, srv.URL, func(runID int64, now time.Time) *collect.Sample {
		return memSample(seq[idx].used, seq[idx].ok, runID, now)
	})

	firingAfter := make([]bool, len(seq))
	for i := range seq {
		idx = i
		fc.Advance(time.Minute)
		if _, err := r.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Reload state to inspect the key.
		firingAfter[i] = keyExists(t, r.Cfg.StatePath)
	}

	if !firingAfter[1] {
		t.Fatal("key must still be firing after an UNKNOWN run (no false resolve)")
	}
	if firingAfter[3] {
		t.Fatal("key should be resolved+deleted after two OK runs following the UNKNOWN")
	}
}

func keyExists(t *testing.T, statePath string) bool {
	t.Helper()
	data, err := os.ReadFile(statePath)
	if err != nil {
		return false
	}
	var s struct {
		Alerts map[string]json.RawMessage `json:"alerts"`
	}
	json.Unmarshal(data, &s)
	return len(s.Alerts) > 0
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
