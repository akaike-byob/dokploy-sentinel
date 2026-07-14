package checks

import (
	"strings"
	"testing"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

const kib = 1024

func baseCtx(cfg *config.Config, s *collect.Sample, prev *state.Snapshot, had bool, now time.Time) *EvalContext {
	if prev == nil {
		prev = state.New()
	}
	return &EvalContext{
		Sample: s, Prev: prev, HadBaseline: had, Cfg: cfg, Now: now,
		HostLabel: "h", BootChanged: prev.BootChanged(s.BootID),
		Exceptions: NewExceptionSet(cfg.Exceptions, now),
	}
}

func okMem(totalKB, availKB, committedKB, swapKB uint64) collect.MemInfo {
	return collect.MemInfo{
		Health: health.OK, MemTotalKB: totalKB, MemAvailableKB: availKB,
		MemFreeKB: availKB, CommittedASKB: committedKB, SwapTotalKB: swapKB,
	}
}

func TestCommittedASSwapGuard(t *testing.T) {
	cfg := config.Default()
	now := time.Unix(1000, 0)

	// ratio 1.6 (WARN by ratio) but committed exceeds RAM+swap → ALERT via guard.
	s := &collect.Sample{Mem: okMem(1000, 500, 1600, 0)}
	obs := committedAS{cfg.Checks.CommittedAS}.Evaluate(baseCtx(cfg, s, nil, false, now))
	if obs[0].Health != health.BAD || obs[0].Tier != config.TierALERT {
		t.Fatalf("swap-guard should force ALERT, got %s %s", obs[0].Health, obs[0].Tier)
	}

	// Same ratio but plenty of swap → guard off → WARN by ratio only.
	s = &collect.Sample{Mem: okMem(1000, 500, 1600, 2000)}
	obs = committedAS{cfg.Checks.CommittedAS}.Evaluate(baseCtx(cfg, s, nil, false, now))
	if obs[0].Health != health.BAD || obs[0].Tier != config.TierWARN {
		t.Fatalf("with swap headroom expected WARN, got %s %s", obs[0].Health, obs[0].Tier)
	}
}

func runningContainer(name string, limit, workingSet int64) collect.Container {
	return collect.Container{
		ID: name, Name: name, Image: "img:latest", State: "running", Inspected: true,
		MemoryLimit: limit,
		Cgroup:      collect.CgroupStats{Health: health.OK, WorkingSet: workingSet},
	}
}

func TestDeclaredOvercommitHeadroom(t *testing.T) {
	cfg := config.Default()
	cfg.Checks.DeclaredOvercommit.HeadroomReservePct = 0 // usable == total for easy math
	now := time.Unix(1000, 0)
	oneGiB := int64(1) << 30
	mem := okMem(uint64(oneGiB/kib), uint64(oneGiB/kib), 0, 0) // total = 1 GiB

	// One unbounded container whose working set (1.2 GiB) exceeds the 512m floor:
	// headroom must be the working set, pushing declared over usable → BAD.
	s := &collect.Sample{Mem: mem, Docker: collect.DockerInfo{Health: health.OK,
		Containers: []collect.Container{runningContainer("hot", 0, oneGiB+oneGiB/5)}}}
	obs := declaredOvercommit{cfg.Checks.DeclaredOvercommit}.Evaluate(baseCtx(cfg, s, nil, false, now))
	if obs[0].Health != health.BAD || obs[0].Tier != config.TierALERT {
		t.Fatalf("working-set headroom should exceed usable → ALERT, got %s: %s", obs[0].Health, obs[0].Measured)
	}

	// A bounded container well under usable → OK.
	s = &collect.Sample{Mem: mem, Docker: collect.DockerInfo{Health: health.OK,
		Containers: []collect.Container{runningContainer("small", oneGiB/4, oneGiB/8)}}}
	obs = declaredOvercommit{cfg.Checks.DeclaredOvercommit}.Evaluate(baseCtx(cfg, s, nil, false, now))
	if obs[0].Health != health.OK {
		t.Fatalf("under-budget should be OK, got %s: %s", obs[0].Health, obs[0].Measured)
	}
}

func TestDeclaredOvercommitUninspectedIsUnknown(t *testing.T) {
	cfg := config.Default()
	now := time.Unix(1000, 0)
	c := runningContainer("x", 0, 0)
	c.Inspected = false
	s := &collect.Sample{Mem: okMem(1000, 1000, 0, 0),
		Docker: collect.DockerInfo{Health: health.OK, Containers: []collect.Container{c}}}
	obs := declaredOvercommit{cfg.Checks.DeclaredOvercommit}.Evaluate(baseCtx(cfg, s, nil, false, now))
	if obs[0].Health != health.UNKNOWN {
		t.Fatalf("un-inspected running container must yield UNKNOWN (incomplete budget), got %s", obs[0].Health)
	}
}

func TestUnboundedMemDetection(t *testing.T) {
	cfg := config.Default()
	now := time.Unix(1000, 0)
	s := &collect.Sample{Docker: collect.DockerInfo{Health: health.OK, Containers: []collect.Container{
		runningContainer("bounded", 512<<20, 0),
		runningContainer("free", 0, 0),
	}}}
	obs := unboundedMem{cfg.Checks.UnboundedMem}.Evaluate(baseCtx(cfg, s, nil, false, now))
	var badScopes []string
	for _, o := range obs {
		if o.Health == health.BAD {
			badScopes = append(badScopes, o.Scope)
			if o.Tier != config.TierWARN {
				t.Errorf("unbounded_mem should be WARN, got %s", o.Tier)
			}
		}
	}
	if len(badScopes) != 1 || badScopes[0] != "free" {
		t.Fatalf("expected only 'free' flagged, got %v", badScopes)
	}
}

func TestDiskFillRateDaysToFull(t *testing.T) {
	cfg := config.Default()
	now := time.Unix(2_000_000, 0)
	oneGiB := int64(1) << 30

	// 95/100 GiB used, +8 GiB/day → ~0.6 days to full → ALERT.
	disk := collect.DiskInfo{Path: "/", Health: health.OK,
		TotalBytes: 100 * oneGiB, UsedBytes: 95 * oneGiB, UsedPct: 95}
	prev := state.New()
	prev.Ring("/").Add(state.DiskPoint{Timestamp: now.Add(-24 * time.Hour), UsedBytes: 87 * oneGiB})
	s := &collect.Sample{Disks: []collect.DiskInfo{disk}}

	obs := diskFill{cfg.Checks.DiskFill}.Evaluate(baseCtx(cfg, s, prev, true, now))
	if obs[0].Health != health.BAD || obs[0].Tier != config.TierALERT {
		t.Fatalf("fill trajectory should ALERT, got %s: %s", obs[0].Health, obs[0].Measured)
	}
	if !strings.Contains(obs[0].Measured, "full in") {
		t.Errorf("measured should mention days-to-full, got %q", obs[0].Measured)
	}
}

func TestDiskFillFirstRunWarming(t *testing.T) {
	cfg := config.Default()
	now := time.Unix(2_000_000, 0)
	oneGiB := int64(1) << 30
	disk := collect.DiskInfo{Path: "/", Health: health.OK, TotalBytes: 100 * oneGiB, UsedBytes: 50 * oneGiB, UsedPct: 50}
	s := &collect.Sample{Disks: []collect.DiskInfo{disk}}
	obs := diskFill{cfg.Checks.DiskFill}.Evaluate(baseCtx(cfg, s, state.New(), false, now))
	if obs[0].Health != health.OK || !obs[0].Warming {
		t.Fatalf("first run must warm up (no false alert), got %+v", obs[0])
	}
}

func TestSwapThrashRate(t *testing.T) {
	cfg := config.Default()
	now := time.Unix(3_000_000, 0)

	prev := state.New()
	prev.BootID = "boot-1"
	prev.Vmstat = state.VmstatBaseline{Pswpin: 1000, Timestamp: now.Add(-60 * time.Second), BootID: "boot-1"}

	// +70000 pages over 60s ≈ 1166 pages/s > 1000 → PAGE.
	s := &collect.Sample{SwapPresent: true, BootID: "boot-1",
		Vmstat: collect.VmstatInfo{Health: health.OK, Pswpin: 71000}}
	obs := swapThrash{cfg.Checks.SwapThrash}.Evaluate(baseCtx(cfg, s, prev, true, now))
	if obs[0].Health != health.BAD || obs[0].Tier != config.TierPAGE {
		t.Fatalf("high page-in rate should PAGE, got %s: %s", obs[0].Health, obs[0].Measured)
	}

	// No swap → gated off (OK, not a false page).
	s2 := &collect.Sample{SwapPresent: false, BootID: "boot-1", Vmstat: collect.VmstatInfo{Health: health.OK, Pswpin: 71000}}
	obs = swapThrash{cfg.Checks.SwapThrash}.Evaluate(baseCtx(cfg, s2, prev, true, now))
	if obs[0].Health != health.OK {
		t.Fatalf("swap-less host should not thrash-alert, got %s", obs[0].Health)
	}

	// Reboot (boot-id change) → warming, never a delta across a counter reset.
	s3 := &collect.Sample{SwapPresent: true, BootID: "boot-2", Vmstat: collect.VmstatInfo{Health: health.OK, Pswpin: 5}}
	obs = swapThrash{cfg.Checks.SwapThrash}.Evaluate(baseCtx(cfg, s3, prev, true, now))
	if obs[0].Health != health.OK || !obs[0].Warming {
		t.Fatalf("boot-id change must warm up, got %+v", obs[0])
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"buildkit-*", "buildkit-abc", true},
		{"buildkit-*", "other", false},
		{"*foo*", "registry.io/foo:latest", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*", "anything", true},
		{"exact", "exact", true},
	}
	for _, c := range cases {
		if got := globMatch(c.pat, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pat, c.s, got, c.want)
		}
	}
}

func TestExceptionsMuteRetierThreshold(t *testing.T) {
	now := time.Unix(1000, 0)
	rules := []config.ExceptionRule{
		{Reason: "buildkit unbounded ok", Match: config.MatchSpec{Name: "buildkit-*"}, Mute: []string{"unbounded_mem"}},
		{Reason: "downgrade ch crashloop", Match: config.MatchSpec{Service: "clickhouse"},
			Retier:     map[string]string{"crashloop": "WARN"},
			Thresholds: map[string]map[string]any{"crashloop": {"restarts": int64(20)}}},
	}
	es := NewExceptionSet(rules, now)

	obs := []Observation{
		{Check: "unbounded_mem", Scope: "buildkit-x", Health: health.BAD, Tier: config.TierWARN, ContainerName: "buildkit-x", ServiceName: "buildkit-x"},
		{Check: "crashloop", Scope: "clickhouse", Health: health.BAD, Tier: config.TierALERT, ServiceName: "clickhouse", MeasuredValue: 6},
	}
	out := es.Apply(obs)

	// mute → suppressed but still present.
	if !out[0].Suppressed || out[0].SuppressedReason == "" {
		t.Errorf("muted observation should be suppressed with a reason, got %+v", out[0])
	}
	// thresholds: measured 6 < override 20 → re-decided OK; retier is moot once OK.
	if out[1].Health != health.OK {
		t.Errorf("per-container threshold override should flip to OK, got %s", out[1].Health)
	}
}

func TestExceptionsRetierDowngrade(t *testing.T) {
	now := time.Unix(1000, 0)
	rules := []config.ExceptionRule{{Reason: "downgrade", Match: config.MatchSpec{Service: "clickhouse"},
		Retier: map[string]string{"crashloop": "WARN"}}}
	es := NewExceptionSet(rules, now)
	obs := []Observation{{Check: "crashloop", Scope: "clickhouse", Health: health.BAD, Tier: config.TierALERT, ServiceName: "clickhouse", MeasuredValue: 6}}
	out := es.Apply(obs)
	if out[0].Health != health.BAD || out[0].Tier != config.TierWARN {
		t.Fatalf("retier should downgrade ALERT→WARN, got %s %s", out[0].Health, out[0].Tier)
	}
}

func TestExceptionExpiredSkippedAndWarns(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC) // after expires
	rules := []config.ExceptionRule{{Reason: "legacy worker", Match: config.MatchSpec{Name: "legacy-*"},
		Mute: []string{"crashloop"}, Expires: "2026-09-01"}}
	es := NewExceptionSet(rules, now)
	obs := []Observation{{Check: "crashloop", Scope: "legacy-1", Health: health.BAD, Tier: config.TierALERT, ContainerName: "legacy-1", ServiceName: "legacy-1"}}
	out := es.Apply(obs)

	// The expired rule must NOT mute the breach.
	if out[0].Suppressed {
		t.Errorf("expired rule must not suppress the breach")
	}
	// And it must emit an expiry WARN.
	var found bool
	for _, o := range out {
		if o.Check == "exception_expired" && o.Tier == config.TierWARN {
			found = true
		}
	}
	if !found {
		t.Errorf("expired rule should emit an exception_expired WARN, got %+v", out)
	}
}

func exitedContainer(id, svc string, exitCode int, oom bool, finishedAt time.Time) collect.Container {
	return collect.Container{
		ID: id, Name: id, State: "exited", Inspected: true, ExitCode: exitCode, OOMKilled: oom,
		FinishedAt: finishedAt, Labels: map[string]string{"com.docker.compose.service": svc},
	}
}

func TestCrashloopIgnoresCleanExits(t *testing.T) {
	cfg := config.Default()
	cfg.Checks.Crashloop.Restarts = 2
	now := time.Unix(5_000_000, 0)
	fin := now.Add(-time.Minute)

	// Three clean (exit 0) completions of a periodic job → NOT a crash loop.
	clean := &collect.Sample{BootID: "b", Docker: collect.DockerInfo{Health: health.OK, Containers: []collect.Container{
		exitedContainer("j1", "cron", 0, false, fin),
		exitedContainer("j2", "cron", 0, false, fin),
		exitedContainer("j3", "cron", 0, false, fin),
	}}}
	obs := crashloop{cfg.Checks.Crashloop}.Evaluate(baseCtx(cfg, clean, nil, true, now))
	for _, o := range obs {
		if o.Health == health.BAD {
			t.Fatalf("clean exit-0 completions must not trip crashloop: %+v", o)
		}
	}

	// Three non-zero exits of the same service → crash loop.
	crash := &collect.Sample{BootID: "b", Docker: collect.DockerInfo{Health: health.OK, Containers: []collect.Container{
		exitedContainer("c1", "web", 1, false, fin),
		exitedContainer("c2", "web", 137, true, fin),
		exitedContainer("c3", "web", 1, false, fin),
	}}}
	obs = crashloop{cfg.Checks.Crashloop}.Evaluate(baseCtx(cfg, crash, nil, true, now))
	var bad bool
	for _, o := range obs {
		if o.Health == health.BAD && o.Tier == config.TierALERT {
			bad = true
		}
	}
	if !bad {
		t.Fatalf("three non-zero exits should trip crashloop ALERT, got %+v", obs)
	}
}

func TestBaselineDoesNotClobberRestartCount(t *testing.T) {
	now := time.Unix(6_000_000, 0)
	snap := state.New()
	snap.Services["svc"] = &state.ServiceState{LastRestartCount: 5, BootID: "b"}

	// The only container is running but un-inspected (inspect timed out) → its
	// RestartCount reads 0; the stored baseline of 5 must be preserved.
	c := collect.Container{ID: "x", Name: "x", State: "running", Inspected: false, RestartCount: 0,
		Labels: map[string]string{"com.docker.compose.service": "svc"}}
	s := &collect.Sample{RunID: 2, BootID: "b", Docker: collect.DockerInfo{Health: health.OK, Containers: []collect.Container{c}}}
	UpdateBaselines(snap, s, now, 10*time.Minute)
	if got := snap.Services["svc"].LastRestartCount; got != 5 {
		t.Fatalf("un-inspected container must not clobber restart baseline: got %d, want 5", got)
	}
}

func TestThresholdOverridePromotesWithTierAndFix(t *testing.T) {
	now := time.Unix(1000, 0)
	rules := []config.ExceptionRule{{Reason: "tighten", Match: config.MatchSpec{Service: "web"},
		Thresholds: map[string]map[string]any{"crashloop": {"restarts": int64(1)}}}}
	es := NewExceptionSet(rules, now)
	// Mimic the crashloop OK observation, which now carries the tier + fix it would
	// fire at, so the override promotes it correctly.
	obs := []Observation{{
		Check: "crashloop", Scope: "web", Health: health.OK, Tier: config.TierALERT,
		Fix: "inspect web logs", ServiceName: "web", MeasuredValue: 2,
	}}
	out := es.Apply(obs)
	if out[0].Health != health.BAD || out[0].Tier != config.TierALERT || out[0].Fix == "" {
		t.Fatalf("override should promote to BAD ALERT with a fix, got %+v", out[0])
	}
}

func TestMemPressureNoUnderflow(t *testing.T) {
	cfg := config.Default()
	now := time.Unix(1000, 0)
	// MemAvailable marginally exceeds MemTotal (kernel estimate).
	s := &collect.Sample{Mem: okMem(1000, 1001, 0, 0)}
	obs := memPressure{cfg.Checks.MemPressure}.Evaluate(baseCtx(cfg, s, nil, false, now))
	if obs[0].Health != health.OK {
		t.Fatalf("avail>total should read as OK, got %s", obs[0].Health)
	}
	if strings.Contains(obs[0].Measured, "unlimited") || strings.Contains(obs[0].Measured, "-") {
		t.Fatalf("measured must not underflow, got %q", obs[0].Measured)
	}
}

func TestExcludeFromBudget(t *testing.T) {
	now := time.Unix(1000, 0)
	rules := []config.ExceptionRule{{Reason: "helper", Match: config.MatchSpec{Service: "clickhouse"}, ExcludeFromBudget: true}}
	es := NewExceptionSet(rules, now)
	ch := collect.Container{Name: "ch-1", State: "running", Labels: map[string]string{"com.docker.compose.service": "clickhouse"}}
	other := collect.Container{Name: "web-1", State: "running"}
	if !es.ExcludedFromBudget(ch) {
		t.Errorf("clickhouse should be excluded from budget")
	}
	if es.ExcludedFromBudget(other) {
		t.Errorf("web should not be excluded")
	}
}
