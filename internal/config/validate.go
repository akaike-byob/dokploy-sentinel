package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExpiresLayout is the date format for an exception rule's expires field.
const ExpiresLayout = "2006-01-02"

// Validate runs the semantic pass beyond TOML parse (docs/plan/06-config.md §6.1).
// undecoded is the list of unrecognized keys returned by Decode. All problems are
// collected and returned as one joined, named error.
func (c *Config) Validate(undecoded []string) error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	for _, k := range undecoded {
		add("unknown config key %q (typo, or a check not supported in this version)", k)
	}

	// ---- host roots ----
	if c.ProcRoot == "" {
		add("proc_root must not be empty")
	}
	if c.CgroupRoot == "" {
		add("cgroup_root must not be empty")
	}

	// ---- docker ----
	if c.Docker.Socket == "" {
		add("docker.socket must not be empty")
	}
	if c.Docker.InspectConcurrency <= 0 {
		add("docker.inspect_concurrency must be > 0 (got %d)", c.Docker.InspectConcurrency)
	}
	if c.Docker.CollectDeadline.D() <= 0 {
		add("docker.collect_deadline must be > 0 (got %s)", c.Docker.CollectDeadline.D())
	}

	// ---- targets ----
	for name, t := range c.Targets {
		if strings.TrimSpace(t.URL) == "" {
			add("target %q has an empty url", name)
		}
		if t.MinTier != "" {
			if _, err := ParseTier(t.MinTier); err != nil {
				add("target %q: %v", name, err)
			}
		}
	}

	// ---- routing references must exist ----
	for _, tier := range AllTiers {
		for _, tgt := range c.Routing.TargetsFor(tier) {
			if _, ok := c.Targets[tgt]; !ok {
				add("routing.%s references undefined target %q", tier, tgt)
			}
		}
	}

	// ---- alerting hygiene ----
	if c.Alerting.ResolveSamples < 1 {
		add("alerting.resolve_samples must be >= 1 (got %d)", c.Alerting.ResolveSamples)
	}
	for _, tier := range AllTiers {
		if c.Alerting.FlapSamples(tier) < 1 {
			add("alerting.flap_samples_%s must be >= 1", strings.ToLower(tier.String()))
		}
		if c.Alerting.Cooldown(tier).D() < 0 {
			add("alerting.cooldown_%s must be >= 0", strings.ToLower(tier.String()))
		}
	}

	// ---- checks ----
	c.validateChecks(add)

	// ---- exceptions ----
	c.validateExceptions(add)

	return errors.Join(errs...)
}

func (c *Config) validateChecks(add func(string, ...any)) {
	mp := c.Checks.MemPressure
	if mp.Enabled {
		requirePercent(add, "mem_pressure.warn", mp.Warn)
		requireOrdered(add, "mem_pressure", "warn", mp.Warn, "alert", mp.Alert)
		requireOrdered(add, "mem_pressure", "alert", mp.Alert, "page", mp.Page)
		requirePercent(add, "mem_pressure.page", mp.Page)
	}

	ca := c.Checks.CommittedAS
	if ca.Enabled {
		requireOrdered(add, "committed_as", "warn", ca.Warn, "alert", ca.Alert)
		requirePositive(add, "committed_as.warn", ca.Warn)
		if ca.CommitVsSwapRatio < 0 {
			add("committed_as.commit_vs_swap_ratio must be >= 0")
		}
	}

	do := c.Checks.DeclaredOvercommit
	if do.Enabled {
		if do.HeadroomReservePct < 0 || do.HeadroomReservePct >= 100 {
			add("declared_overcommit.headroom_reserve_pct must be in [0,100) (got %g)", do.HeadroomReservePct)
		}
		if do.HeadroomFloor.Bytes() < 0 {
			add("declared_overcommit.headroom_floor must be >= 0")
		}
	}

	df := c.Checks.DiskFill
	if df.Enabled {
		if len(df.Paths) == 0 {
			add("disk_fill.paths must list at least one filesystem path")
		}
		requirePercent(add, "disk_fill.warn", df.Warn)
		requireOrdered(add, "disk_fill", "warn", df.Warn, "alert", df.Alert)
		requirePercent(add, "disk_fill.alert", df.Alert)
		if df.DaysToFullAlert < 0 {
			add("disk_fill.days_to_full_alert must be >= 0")
		}
	}

	di := c.Checks.DiskInodes
	if di.Enabled {
		requirePercent(add, "disk_inodes.alert", di.Alert)
		requirePositive(add, "disk_inodes.alert", di.Alert)
	}

	st := c.Checks.SwapThrash
	if st.Enabled && st.PswpinPagesPerSec <= 0 {
		add("swap_thrash.pswpin_pages_per_sec must be > 0 (got %g)", st.PswpinPagesPerSec)
	}

	cl := c.Checks.Crashloop
	if cl.Enabled {
		if cl.Restarts <= 0 {
			add("crashloop.restarts must be > 0 (got %d)", cl.Restarts)
		}
		if cl.Window.D() <= 0 {
			add("crashloop.window must be > 0")
		}
	}

	// per-check tier overrides must parse
	for id, ov := range c.checkOverrides() {
		if ov.Tier != nil {
			if _, err := ParseTier(*ov.Tier); err != nil {
				add("checks.%s.tier: %v", id, err)
			}
		}
		if ov.FlapSamples != nil && *ov.FlapSamples < 1 {
			add("checks.%s.flap_samples must be >= 1", id)
		}
		if ov.Cooldown != nil && ov.Cooldown.D() < 0 {
			add("checks.%s.cooldown must be >= 0", id)
		}
	}
}

func (c *Config) validateExceptions(add func(string, ...any)) {
	for i, ex := range c.Exceptions {
		where := fmt.Sprintf("exceptions[%d]", i)
		if strings.TrimSpace(ex.Reason) == "" {
			add("%s: reason must not be empty", where)
		}
		if ex.Match.Empty() {
			add("%s: match must specify at least one of name/image/service/label", where)
		}
		if ex.Match.Label != "" && !strings.Contains(ex.Match.Label, "=") {
			add("%s: match.label must be \"key=value\" (got %q)", where, ex.Match.Label)
		}
		if !ex.HasAction() {
			add("%s: rule has no action (need mute, retier, thresholds, or exclude_from_budget)", where)
		}
		// mute ids
		for _, id := range ex.Mute {
			if id == "*" {
				continue
			}
			checkExceptableID(add, where+".mute", id)
		}
		for id := range ex.Retier {
			checkExceptableID(add, where+".retier", id)
		}
		for id, t := range ex.Retier {
			if _, err := ParseTier(t); err != nil {
				add("%s.retier.%s: %v", where, id, err)
			}
		}
		for id := range ex.Thresholds {
			checkExceptableID(add, where+".thresholds", id)
		}
		if ex.Expires != "" {
			if _, err := time.Parse(ExpiresLayout, ex.Expires); err != nil {
				add("%s: invalid expires date %q (want YYYY-MM-DD)", where, ex.Expires)
			}
		}
	}
}

// checkOverrides returns the per-check override blocks keyed by check id.
func (c *Config) checkOverrides() map[string]CheckOverrides {
	return map[string]CheckOverrides{
		"mem_pressure":        c.Checks.MemPressure.CheckOverrides,
		"committed_as":        c.Checks.CommittedAS.CheckOverrides,
		"declared_overcommit": c.Checks.DeclaredOvercommit.CheckOverrides,
		"unbounded_mem":       c.Checks.UnboundedMem.CheckOverrides,
		"disk_fill":           c.Checks.DiskFill.CheckOverrides,
		"disk_inodes":         c.Checks.DiskInodes.CheckOverrides,
		"swap_thrash":         c.Checks.SwapThrash.CheckOverrides,
		"crashloop":           c.Checks.Crashloop.CheckOverrides,
	}
}

func checkExceptableID(add func(string, ...any), where, id string) {
	if !KnownCheckID(id) {
		add("%s: unknown check id %q", where, id)
		return
	}
	if HostScopedCheckIDs[id] {
		add("%s: check %q is host-scoped and cannot be excepted per-container", where, id)
	}
}

func requireOrdered(add func(string, ...any), check, loName string, lo float64, hiName string, hi float64) {
	if !(lo < hi) {
		add("checks.%s: %s (%g) must be < %s (%g)", check, loName, lo, hiName, hi)
	}
}

func requirePercent(add func(string, ...any), name string, v float64) {
	if v <= 0 || v > 100 {
		add("%s must be in (0,100] (got %g)", name, v)
	}
}

func requirePositive(add func(string, ...any), name string, v float64) {
	if v <= 0 {
		add("%s must be > 0 (got %g)", name, v)
	}
}
