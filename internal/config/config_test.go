package config

import (
	"strings"
	"testing"
	"time"
)

// validTOML is a minimal valid base. It deliberately omits [routing] and
// [checks.*] so individual cases can add those tables without redefining them
// (BurntSushi rejects a duplicate table).
const validTOML = `
host_label = "box-1"
report_path = "/tmp/r.json"
state_path  = "/tmp/s.json"

[docker]
socket = "/var/run/docker.sock"
inspect_concurrency = 8
collect_deadline = "15s"

[targets.team]
url = "https://hooks.slack.com/services/T/B/team"
`

func loadValid(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	cfg, undecoded, err := Decode([]byte(validTOML + extra))
	if err != nil {
		return nil, err
	}
	return cfg, cfg.Validate(undecoded)
}

func TestValidConfigRoundTrips(t *testing.T) {
	cfg, err := loadValid(t, "")
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.Docker.CollectDeadline.D() != 15*time.Second {
		t.Errorf("collect_deadline = %v", cfg.Docker.CollectDeadline.D())
	}
	// Defaults fill in unset checks.
	if !cfg.Checks.CommittedAS.Enabled || cfg.Checks.CommittedAS.Alert != 2.0 {
		t.Errorf("committed_as defaults not applied: %+v", cfg.Checks.CommittedAS)
	}
	if cfg.Checks.DeclaredOvercommit.HeadroomFloor.Bytes() != 512<<20 {
		t.Errorf("headroom_floor default = %d", cfg.Checks.DeclaredOvercommit.HeadroomFloor.Bytes())
	}
	if cfg.Alerting.FlapSamplesPage != 1 {
		t.Errorf("flap_samples_page default = %d", cfg.Alerting.FlapSamplesPage)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		wantSub string
	}{
		{"warn_not_below_alert", "\n[checks.mem_pressure]\nwarn = 95\nalert = 90\npage = 99\n", "warn (95) must be < alert"},
		{"alert_not_below_page", "\n[checks.mem_pressure]\nwarn = 80\nalert = 96\npage = 95\n", "alert (96) must be < page"},
		{"missing_routing_target", "\n[routing]\nWARN = [\"ghost\"]\n", "undefined target \"ghost\""},
		{"unknown_check_key", "\n[checks.mem_pressure]\nwarnn = 80\n", "unknown config key"},
		{"empty_url", "\n[targets.bad]\nurl = \"\"\n", "empty url"},
		{"bad_min_tier", "\n[targets.badtier]\nurl = \"http://x\"\nmin_tier = \"NOPE\"\n", "unknown tier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadValid(t, tc.extra)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestBadDurationAndByteSize(t *testing.T) {
	if _, _, err := Decode([]byte(validTOML + "\n[docker]\ncollect_deadline = \"20flurbs\"\n")); err == nil {
		t.Fatal("expected duration parse error")
	}
	if _, _, err := Decode([]byte(validTOML + "\n[checks.declared_overcommit]\nheadroom_floor = \"512x\"\n")); err == nil {
		t.Fatal("expected byte-size parse error")
	}
}

func TestExceptionValidation(t *testing.T) {
	cases := []struct {
		name    string
		extra   string
		wantSub string
	}{
		{"empty_match", "\n[[exceptions]]\nreason = \"x\"\nmute = [\"crashloop\"]\n", "match must specify"},
		{"empty_reason", "\n[[exceptions]]\nmatch = { name = \"a*\" }\nmute = [\"crashloop\"]\n", "reason must not be empty"},
		{"no_action", "\n[[exceptions]]\nreason = \"x\"\nmatch = { name = \"a*\" }\n", "no action"},
		{"host_scoped_check", "\n[[exceptions]]\nreason = \"x\"\nmatch = { name = \"a*\" }\nmute = [\"mem_pressure\"]\n", "host-scoped"},
		{"unknown_check", "\n[[exceptions]]\nreason = \"x\"\nmatch = { name = \"a*\" }\nmute = [\"nope\"]\n", "unknown check id"},
		{"bad_expires", "\n[[exceptions]]\nreason = \"x\"\nmatch = { name = \"a*\" }\nmute = [\"crashloop\"]\nexpires = \"2026-13-40\"\n", "invalid expires"},
		{"bad_label", "\n[[exceptions]]\nreason = \"x\"\nmatch = { label = \"novalue\" }\nmute = [\"crashloop\"]\n", "must be \"key=value\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadValid(t, tc.extra)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestValidExceptionAccepted(t *testing.T) {
	extra := `
[[exceptions]]
reason = "buildkit is meant to be unbounded"
match = { name = "buildkit-*" }
mute = ["unbounded_mem", "crashloop"]

[[exceptions]]
reason = "clickhouse is memory-hungry by design"
match = { service = "clickhouse" }
exclude_from_budget = true
  [exceptions.retier]
  container_health = "WARN"
  [exceptions.thresholds.crashloop]
  restarts = 20
`
	if _, err := loadValid(t, extra); err != nil {
		t.Fatalf("valid exceptions rejected: %v", err)
	}
}

func TestByteSizeParsing(t *testing.T) {
	cases := map[string]int64{
		"512m": 512 << 20, "2g": 2 << 30, "1024": 1024, "1k": 1024, "512mb": 512 << 20,
	}
	for in, want := range cases {
		got, err := ParseByteSize(in)
		if err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d,%v want %d", in, got, err, want)
		}
	}
}
