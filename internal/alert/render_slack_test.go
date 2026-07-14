package alert

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// fixedAlert builds a deterministic alert for golden rendering.
func fixedAlert(tier config.Tier, kind Kind) Alert {
	return Alert{
		Tier:      tier,
		Kind:      kind,
		Title:     "Memory pressure",
		Host:      "dokploy-01",
		Scope:     "host",
		Measured:  "96% (21.1/22 GiB)",
		Threshold: "95%",
		Fix:       "restart or cap the largest unbounded container now; swap is thrashing (see swap_thrash).",
		Key:       "mem_pressure:host",
		Check:     "mem_pressure",
		Timestamp: time.Date(2026, 7, 14, 9, 42, 3, 0, time.UTC),
		RunID:     1187,
	}
}

// TestSlackGolden renders one payload per tier + RESOLVED and compares to a
// committed golden file. Run `UPDATE_GOLDEN=1 go test ./internal/alert/` to
// regenerate after an intentional payload change.
func TestSlackGolden(t *testing.T) {
	cases := []struct {
		name    string
		alert   Alert
		mention string
	}{
		{"warn", fixedAlert(config.TierWARN, KindFire), ""},
		{"alert", fixedAlert(config.TierALERT, KindFire), ""},
		{"page", fixedAlert(config.TierPAGE, KindFire), "<@U123ONCALL>"},
		{"page_no_mention", fixedAlert(config.TierPAGE, KindFire), ""},
		{"escalate", fixedAlert(config.TierPAGE, KindEscalate), "<@U123ONCALL>"},
		{"resolved", fixedAlert(config.TierPAGE, KindResolved), "<@U123ONCALL>"},
		{"degraded", fixedAlert(config.TierALERT, KindDegraded), ""},
	}
	r := SlackRenderer{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Render(tc.alert, tc.mention)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "slack_"+tc.name+".json")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("payload drift for %s:\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, want)
			}
		})
	}
}

// TestMentionOnlyOnPage verifies mentions appear only on PAGE and never on RESOLVED.
func TestMentionOnlyOnPage(t *testing.T) {
	cfg := smConfig()
	cases := []struct {
		alert    Alert
		target   string
		wantMent bool
	}{
		{fixedAlert(config.TierWARN, KindFire), "oncall", false},
		{fixedAlert(config.TierALERT, KindFire), "oncall", false},
		{fixedAlert(config.TierPAGE, KindFire), "oncall", true},
		{fixedAlert(config.TierPAGE, KindResolved), "oncall", false},
		{fixedAlert(config.TierPAGE, KindFire), "team", false}, // team has no mention configured
	}
	for _, tc := range cases {
		got := mentionFor(cfg, tc.target, tc.alert)
		if (got != "") != tc.wantMent {
			t.Errorf("mentionFor(%s, tier=%s, kind=%s) = %q, wantMention=%v", tc.target, tc.alert.Tier, tc.alert.Kind, got, tc.wantMent)
		}
	}
}
