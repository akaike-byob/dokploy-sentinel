package alert

import (
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// routeTargets returns the target names that should receive an event at the given
// tier, honoring each target's min_tier floor. Order follows the routing table.
func routeTargets(cfg *config.Config, tier config.Tier) []string {
	var out []string
	for _, name := range cfg.Routing.TargetsFor(tier) {
		t, ok := cfg.Targets[name]
		if !ok {
			continue // validated away, but stay defensive
		}
		if min, has := t.MinTierParsed(); has && tier < min {
			continue // this channel never receives below its floor
		}
		out = append(out, name)
	}
	return out
}

// mentionFor returns the mention string to include for a target on this alert.
// Mentions are added only on PAGE (and never on RESOLVED), to avoid pinging
// humans for WARN/ALERT.
func mentionFor(cfg *config.Config, name string, a Alert) string {
	if a.resolved() || a.Tier != config.TierPAGE {
		return ""
	}
	if t, ok := cfg.Targets[name]; ok {
		return t.Mention
	}
	return ""
}

// unionTargets appends new names to existing, preserving order and de-duplicating.
func unionTargets(existing, add []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, n := range existing {
		seen[n] = true
	}
	for _, n := range add {
		if !seen[n] {
			existing = append(existing, n)
			seen[n] = true
		}
	}
	return existing
}

// flapSamplesFor returns the flap threshold for a check+tier, honoring a
// per-check override.
func flapSamplesFor(cfg *config.Config, ov config.CheckOverrides, tier config.Tier) int {
	if ov.FlapSamples != nil {
		return *ov.FlapSamples
	}
	return cfg.Alerting.FlapSamples(tier)
}

// cooldownFor returns the cooldown for a check+tier, honoring a per-check override.
func cooldownFor(cfg *config.Config, ov config.CheckOverrides, tier config.Tier) time.Duration {
	if ov.Cooldown != nil {
		return ov.Cooldown.D()
	}
	return cfg.Alerting.Cooldown(tier).D()
}

// overridesFor returns the per-check override block for a check id (zero value if
// the check is not one we track overrides for, e.g. exception_expired).
func overridesFor(cfg *config.Config, check string) config.CheckOverrides {
	c := cfg.Checks
	switch check {
	case "mem_pressure":
		return c.MemPressure.CheckOverrides
	case "committed_as":
		return c.CommittedAS.CheckOverrides
	case "declared_overcommit":
		return c.DeclaredOvercommit.CheckOverrides
	case "unbounded_mem":
		return c.UnboundedMem.CheckOverrides
	case "disk_fill":
		return c.DiskFill.CheckOverrides
	case "disk_inodes":
		return c.DiskInodes.CheckOverrides
	case "swap_thrash":
		return c.SwapThrash.CheckOverrides
	case "crashloop":
		return c.Crashloop.CheckOverrides
	default:
		return config.CheckOverrides{}
	}
}
