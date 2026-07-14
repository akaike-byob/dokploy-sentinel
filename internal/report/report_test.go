package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/checks"
	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

func TestRedactsWebhookURLs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")

	// A finding whose fix text accidentally embeds a webhook URL must be redacted.
	r := &Report{
		GeneratedAt: time.Unix(1000, 0),
		Host:        "h",
		Findings: []Finding{{
			Check: "x", Scope: "host", Health: "BAD",
			Fix: "see https://hooks.slack.com/services/T00000/B11111/AbCdEfSecret for details",
		}},
	}
	if err := Write(path, r); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "AbCdEfSecret") {
		t.Fatalf("report leaked a webhook secret:\n%s", data)
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("expected [REDACTED] marker, got:\n%s", data)
	}
	// File must be 0600.
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("report mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestBuildSeparatesSuppressed(t *testing.T) {
	s := &collect.Sample{
		Mem:    collect.MemInfo{Health: health.OK, MemTotalKB: 1000, MemAvailableKB: 500, CommittedASKB: 600},
		Vmstat: collect.VmstatInfo{Health: health.OK},
		Load:   collect.LoadInfo{Health: health.OK, NumCPU: 4},
		Docker: collect.DockerInfo{Health: health.OK, Containers: []collect.Container{
			{Name: "web", Image: "web:latest", State: "running", MemoryLimit: 0},
		}},
	}
	obs := []checks.Observation{
		{Check: "unbounded_mem", Scope: "web", Health: health.BAD, Tier: config.TierWARN, Title: "T"},
		{Check: "crashloop", Scope: "svc", Health: health.BAD, Tier: config.TierALERT, Title: "T", Suppressed: true, SuppressedReason: "muted"},
	}
	r := Build("h", time.Unix(1000, 0), s, obs, nil, true)
	if len(r.Findings) != 1 || len(r.Suppressed) != 1 {
		t.Fatalf("expected 1 finding + 1 suppressed, got %d/%d", len(r.Findings), len(r.Suppressed))
	}
	if r.Suppressed[0].Reason != "muted" {
		t.Errorf("suppressed reason lost: %+v", r.Suppressed[0])
	}
	if len(r.Containers) != 1 || r.Containers[0].Name != "web" {
		t.Errorf("container summary wrong: %+v", r.Containers)
	}
	// Round-trips as JSON.
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}
