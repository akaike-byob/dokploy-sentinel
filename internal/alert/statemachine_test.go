package alert

import (
	"testing"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/checks"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

// smConfig builds a config with deterministic routing + hygiene for the state
// machine tests.
func smConfig() *config.Config {
	c := config.Default()
	c.Targets = map[string]config.TargetConfig{
		"low":    {URL: "http://low"},
		"team":   {URL: "http://team"},
		"oncall": {URL: "http://oncall", Mention: "<@U1>"},
	}
	c.Routing = config.RoutingConfig{
		WARN:  []string{"low"},
		ALERT: []string{"team"},
		PAGE:  []string{"team", "oncall"},
	}
	c.Alerting.FlapSamplesWarn = 3
	c.Alerting.FlapSamplesAlert = 3
	c.Alerting.FlapSamplesPage = 1
	c.Alerting.ResolveSamples = 2
	c.Alerting.CooldownWarn = config.Duration(24 * time.Hour)
	c.Alerting.CooldownAlert = config.Duration(6 * time.Hour)
	c.Alerting.CooldownPage = config.Duration(2 * time.Hour)
	return c
}

type driver struct {
	cfg   *config.Config
	snap  *state.Snapshot
	now   time.Time
	runID int64
}

func newDriver() *driver {
	return &driver{cfg: smConfig(), snap: state.New(), now: time.Unix(1_700_000_000, 0).UTC()}
}

func (d *driver) advance(step time.Duration, o checks.Observation) []Decision {
	d.now = d.now.Add(step)
	d.runID++
	return Advance(d.cfg, d.snap, []checks.Observation{o}, d.now, "host", d.runID)
}

func bad(check string, tier config.Tier) checks.Observation {
	return checks.Observation{Check: check, Scope: "host", Health: health.BAD, Tier: tier, Title: "T", Measured: "m", Threshold: "t", Fix: "f"}
}
func ok(check string) checks.Observation {
	return checks.Observation{Check: check, Scope: "host", Health: health.OK, Title: "T", Measured: "m"}
}
func unknown(check string) checks.Observation {
	return checks.Observation{Check: check, Scope: "host", Health: health.UNKNOWN, Title: "T"}
}

func kinds(ds []Decision) []Kind {
	out := make([]Kind, len(ds))
	for i, d := range ds {
		out[i] = d.Alert.Kind
	}
	return out
}

func TestFlapDampThenFire(t *testing.T) {
	d := newDriver()
	if got := d.advance(time.Minute, bad("mem_pressure", config.TierWARN)); len(got) != 0 {
		t.Fatalf("run1: expected no send while damping, got %v", kinds(got))
	}
	if got := d.advance(time.Minute, bad("mem_pressure", config.TierWARN)); len(got) != 0 {
		t.Fatalf("run2: expected no send, got %v", kinds(got))
	}
	got := d.advance(time.Minute, bad("mem_pressure", config.TierWARN))
	if len(got) != 1 || got[0].Alert.Kind != KindFire {
		t.Fatalf("run3: expected one FIRE, got %v", kinds(got))
	}
	if want := []string{"low"}; !eqStr(got[0].Targets, want) {
		t.Fatalf("fire targets = %v, want %v", got[0].Targets, want)
	}
	ks := d.snap.Alerts["mem_pressure:host"]
	if ks == nil || ks.Status != state.StatusFiring {
		t.Fatalf("key should be firing, got %+v", ks)
	}
	if !eqStr(ks.NotifiedTargets, []string{"low"}) {
		t.Fatalf("notified_targets = %v", ks.NotifiedTargets)
	}
}

func TestPendingSpikeClearsSilently(t *testing.T) {
	d := newDriver()
	d.advance(time.Minute, bad("mem_pressure", config.TierWARN))
	d.advance(time.Minute, bad("mem_pressure", config.TierWARN))
	if got := d.advance(time.Minute, ok("mem_pressure")); len(got) != 0 {
		t.Fatalf("expected silent forget, got %v", kinds(got))
	}
	if _, exists := d.snap.Alerts["mem_pressure:host"]; exists {
		t.Fatalf("pending key should be deleted after clearing")
	}
}

func TestPageFiresFirstBreach(t *testing.T) {
	d := newDriver()
	got := d.advance(time.Minute, bad("mem_pressure", config.TierPAGE))
	if len(got) != 1 || got[0].Alert.Kind != KindFire {
		t.Fatalf("PAGE should fire on first breach, got %v", kinds(got))
	}
	if !eqStr(got[0].Targets, []string{"team", "oncall"}) {
		t.Fatalf("PAGE fan-out = %v", got[0].Targets)
	}
}

func TestEscalationWarnAlertPage(t *testing.T) {
	d := newDriver()
	// WARN fires after 3.
	d.advance(time.Minute, bad("mem_pressure", config.TierWARN))
	d.advance(time.Minute, bad("mem_pressure", config.TierWARN))
	if k := kinds(d.advance(time.Minute, bad("mem_pressure", config.TierWARN))); len(k) != 1 || k[0] != KindFire {
		t.Fatalf("expected WARN fire, got %v", k)
	}
	// Worsen to ALERT → single escalate (not resolve+refire).
	got := d.advance(time.Minute, bad("mem_pressure", config.TierALERT))
	if len(got) != 1 || got[0].Alert.Kind != KindEscalate || got[0].Alert.Tier != config.TierALERT {
		t.Fatalf("expected ALERT escalate, got %v", kinds(got))
	}
	// Worsen to PAGE → escalate again.
	got = d.advance(time.Minute, bad("mem_pressure", config.TierPAGE))
	if len(got) != 1 || got[0].Alert.Kind != KindEscalate || got[0].Alert.Tier != config.TierPAGE {
		t.Fatalf("expected PAGE escalate, got %v", kinds(got))
	}
	// notified_targets should be the union of low+team+oncall.
	ks := d.snap.Alerts["mem_pressure:host"]
	if !eqStr(ks.NotifiedTargets, []string{"low", "team", "oncall"}) {
		t.Fatalf("union notified_targets = %v", ks.NotifiedTargets)
	}
}

func TestDeEscalationNoResolve(t *testing.T) {
	d := newDriver()
	d.advance(time.Minute, bad("mem_pressure", config.TierPAGE)) // fires immediately
	got := d.advance(time.Minute, bad("mem_pressure", config.TierALERT))
	if len(got) != 0 {
		t.Fatalf("de-escalation should be silent (no false resolve), got %v", kinds(got))
	}
	ks := d.snap.Alerts["mem_pressure:host"]
	if ks.Tier != config.TierALERT || ks.Status != state.StatusFiring {
		t.Fatalf("de-escalated key = %+v", ks)
	}
}

func TestCooldownSuppressesThenReminds(t *testing.T) {
	d := newDriver()
	if k := kinds(d.advance(time.Minute, bad("mem_pressure", config.TierPAGE))); k[0] != KindFire {
		t.Fatalf("expected fire")
	}
	// Within the 2h PAGE cooldown → silent.
	if got := d.advance(30*time.Minute, bad("mem_pressure", config.TierPAGE)); len(got) != 0 {
		t.Fatalf("within cooldown should be silent, got %v", kinds(got))
	}
	// After the cooldown → reminder.
	got := d.advance(2*time.Hour, bad("mem_pressure", config.TierPAGE))
	if len(got) != 1 || got[0].Alert.Kind != KindReminder {
		t.Fatalf("expected reminder after cooldown, got %v", kinds(got))
	}
}

func TestAutoResolveOnlyAfterConsecutiveOK(t *testing.T) {
	d := newDriver()
	d.advance(time.Minute, bad("mem_pressure", config.TierPAGE)) // fire
	if got := d.advance(time.Minute, ok("mem_pressure")); len(got) != 0 {
		t.Fatalf("one OK should not resolve (resolve_samples=2), got %v", kinds(got))
	}
	got := d.advance(time.Minute, ok("mem_pressure"))
	if len(got) != 1 || got[0].Alert.Kind != KindResolved {
		t.Fatalf("expected RESOLVED after 2 OK, got %v", kinds(got))
	}
	if !eqStr(got[0].Targets, []string{"team", "oncall"}) {
		t.Fatalf("RESOLVED should follow notified_targets, got %v", got[0].Targets)
	}
	if _, exists := d.snap.Alerts["mem_pressure:host"]; exists {
		t.Fatalf("resolved key should be deleted")
	}
}

func TestUnknownFreezesFiringKey(t *testing.T) {
	d := newDriver()
	d.advance(time.Minute, bad("mem_pressure", config.TierPAGE)) // fire
	d.advance(time.Minute, ok("mem_pressure"))                   // consecutive_ok = 1

	// UNKNOWN must neither resolve nor advance consecutive_ok.
	got := d.advance(time.Minute, unknown("mem_pressure"))
	if len(got) != 1 || got[0].Alert.Kind != KindDegraded {
		t.Fatalf("first UNKNOWN on firing key should surface degraded once, got %v", kinds(got))
	}
	ks := d.snap.Alerts["mem_pressure:host"]
	if ks == nil || ks.Status != state.StatusFiring {
		t.Fatalf("UNKNOWN must not resolve a firing key: %+v", ks)
	}
	// UNKNOWN freezes consecutive_ok — it neither advances nor resets it.
	if ks.ConsecutiveOK != 1 {
		t.Fatalf("UNKNOWN must freeze consecutive_ok at 1, got %d", ks.ConsecutiveOK)
	}
	// A second UNKNOWN does not re-surface degraded and keeps the freeze.
	if got := d.advance(time.Minute, unknown("mem_pressure")); len(got) != 0 {
		t.Fatalf("degraded should surface only once, got %v", kinds(got))
	}
	// One more OK (frozen 1 → 2) reaches resolve_samples and resolves.
	if got := d.advance(time.Minute, ok("mem_pressure")); len(got) != 1 || got[0].Alert.Kind != KindResolved {
		t.Fatalf("expected resolve after the frozen OK count reaches 2, got %v", kinds(got))
	}
}

func TestNegativeTimeDeltaClamped(t *testing.T) {
	d := newDriver()
	d.advance(time.Minute, bad("mem_pressure", config.TierPAGE)) // fire, last_notified = now
	// Clock steps backwards by an hour (NTP step). Reminder must not fire early,
	// and last_notified must be clamped so it can't suppress forever.
	got := d.advance(-time.Hour, bad("mem_pressure", config.TierPAGE))
	if len(got) != 0 {
		t.Fatalf("backwards clock must not page early, got %v", kinds(got))
	}
	ks := d.snap.Alerts["mem_pressure:host"]
	if ks.LastNotified.After(d.now) {
		t.Fatalf("last_notified should be clamped to now, got %v > %v", ks.LastNotified, d.now)
	}
}

func TestSuppressedSkipsStateMachine(t *testing.T) {
	d := newDriver()
	o := bad("unbounded_mem", config.TierWARN)
	o.Scope = "svc"
	o.Suppressed = true
	for i := 0; i < 5; i++ {
		if got := d.advance(time.Minute, o); len(got) != 0 {
			t.Fatalf("suppressed observation must never notify, got %v", kinds(got))
		}
	}
	if len(d.snap.Alerts) != 0 {
		t.Fatalf("suppressed observation must not create alert state")
	}
}

func crashBad() checks.Observation {
	return checks.Observation{Check: "crashloop", Scope: "web", Health: health.BAD, Tier: config.TierALERT, Title: "Crash loop", Measured: "m", Threshold: "t", Fix: "f"}
}

// TestMutedFiringKeyResolvesSilently: a key that fired, then gets muted, must
// still advance to RESOLVED and be cleared from state (no leak) — silently.
func TestMutedFiringKeyResolvesSilently(t *testing.T) {
	d := newDriver()
	d.advance(time.Minute, crashBad())
	d.advance(time.Minute, crashBad())
	if k := kinds(d.advance(time.Minute, crashBad())); len(k) != 1 || k[0] != KindFire {
		t.Fatalf("expected ALERT fire, got %v", k)
	}

	okMuted := checks.Observation{Check: "crashloop", Scope: "web", Health: health.OK, Title: "Crash loop", Suppressed: true}
	if got := d.advance(time.Minute, okMuted); len(got) != 0 {
		t.Fatalf("muted key must not send, got %v", kinds(got))
	}
	if d.snap.Alerts["crashloop:web"] == nil {
		t.Fatal("still firing after one OK")
	}
	if got := d.advance(time.Minute, okMuted); len(got) != 0 {
		t.Fatalf("muted resolve must be silent, got %v", kinds(got))
	}
	if _, exists := d.snap.Alerts["crashloop:web"]; exists {
		t.Fatal("muted firing key must resolve + delete (no state leak)")
	}
}

// TestMutedBreachWithNoHistoryStillSkips preserves the design: a mute on a
// container that never fired never enters the state machine.
func TestMutedBreachWithNoHistoryStillSkips(t *testing.T) {
	d := newDriver()
	b := crashBad()
	b.Suppressed = true
	for i := 0; i < 5; i++ {
		if got := d.advance(time.Minute, b); len(got) != 0 {
			t.Fatalf("muted-from-the-start breach must never notify, got %v", kinds(got))
		}
	}
	if len(d.snap.Alerts) != 0 {
		t.Fatal("muted breach with no history must not create state")
	}
}

// TestDockerOutageDegradesFiringServiceKeys: on a full Docker outage the check
// emits one coarse UNKNOWN scoped "docker"; firing per-service keys must still
// get a one-time "monitoring degraded" notice and must NOT resolve.
func TestDockerOutageDegradesFiringServiceKeys(t *testing.T) {
	d := newDriver()
	d.advance(time.Minute, crashBad())
	d.advance(time.Minute, crashBad())
	d.advance(time.Minute, crashBad()) // fires crashloop:web

	down := checks.Observation{Check: "crashloop", Scope: "docker", Health: health.UNKNOWN, Title: "Crash loop"}
	got := d.advance(time.Minute, down)
	if len(got) != 1 || got[0].Alert.Kind != KindDegraded || got[0].Alert.Scope != "web" {
		t.Fatalf("expected one degraded notice for crashloop:web, got %v", got)
	}
	if ks := d.snap.Alerts["crashloop:web"]; ks == nil || ks.Status != state.StatusFiring {
		t.Fatalf("firing key must freeze (not resolve) during outage: %+v", ks)
	}
	// Repeated outage runs do not re-surface the degraded notice.
	if got := d.advance(time.Minute, down); len(got) != 0 {
		t.Fatalf("degraded should surface once, got %v", kinds(got))
	}
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
