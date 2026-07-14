package alert

import (
	"strings"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/checks"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

// Advance runs the per-key state machine for one run and returns the decisions
// that must be delivered. It mutates snap.Alerts in place. A suppressed (muted)
// observation never SENDS, but if it already has tracked state it is still
// advanced so it can resolve and be cleared — a mute stops the notification, not
// the observation. A muted breach with no prior state never enters the machine.
//
// The invariants (docs/plan/05-alerting.md §5.3):
//   - flap damp: hold pending until consecutive_bad ≥ flap_samples (PAGE = 1)
//   - fire once; escalate on worse tier; de-escalate silently on better-but-bad
//   - cooldown suppresses re-notify of an unchanged firing key
//   - auto-resolve only after resolve_samples consecutive OK (never on "absent")
//   - UNKNOWN freezes the key: no fire, no resolve, no counter advance
func Advance(cfg *config.Config, snap *state.Snapshot, obs []checks.Observation, now time.Time, host string, runID int64) []Decision {
	var decisions []Decision
	for _, o := range obs {
		key := o.Key()
		ks := snap.Alerts[key]
		// A muted observation with no prior state never enters the machine (a mute
		// on a container that has not yet fired). But a muted observation for a key
		// that is ALREADY tracked (it fired before the mute was added) must still be
		// advanced — otherwise it can never resolve and leaks in state.json. In that
		// case we run the transition but drop the send: mute stops the notification,
		// not the observation (docs/plan/06-config.md §6.3).
		if o.Suppressed && ks == nil {
			continue
		}
		var d Decision
		var ok bool
		switch o.Health {
		case health.UNKNOWN:
			d, ok = handleUnknown(cfg, ks, o, host, now, runID)
		case health.BAD:
			d, ok = handleBad(cfg, snap, ks, o, host, now, runID)
		case health.OK:
			d, ok = handleOK(cfg, snap, ks, o, now, host, runID)
		}
		if ok && !o.Suppressed {
			decisions = append(decisions, d)
		}
	}
	decisions = append(decisions, sweepDegraded(snap, obs, host, now, runID)...)
	return decisions
}

// sweepDegraded surfaces "monitoring degraded" for firing keys whose check's
// whole source went UNKNOWN this run and therefore collapsed to a single coarse
// (scope="docker") observation instead of a per-service one. Without this, a
// firing per-service key (e.g. crashloop:web) would silently freeze during a
// Docker outage with no signal that the monitor lost visibility.
func sweepDegraded(snap *state.Snapshot, obs []checks.Observation, host string, now time.Time, runID int64) []Decision {
	sourceDown := map[string]bool{}
	for _, o := range obs {
		if o.Health == health.UNKNOWN && o.Scope == "docker" {
			sourceDown[o.Check] = true
		}
	}
	if len(sourceDown) == 0 {
		return nil
	}
	var decisions []Decision
	for key, ks := range snap.Alerts {
		if ks.Status != state.StatusFiring || ks.LastSeenRun == runID {
			continue // not firing, or already handled this run
		}
		check := key
		if i := strings.IndexByte(key, ':'); i >= 0 {
			check = key[:i]
		}
		if !sourceDown[check] {
			continue
		}
		ks.LastSeenRun = runID // freeze: never resolve on missing data
		if ks.DegradedNotified {
			continue
		}
		ks.DegradedNotified = true
		scope := key
		if i := strings.IndexByte(key, ':'); i >= 0 {
			scope = key[i+1:]
		}
		a := Alert{
			Tier: ks.Tier, Kind: KindDegraded, Title: "Monitoring degraded",
			Host: host, Scope: scope,
			Measured: "monitoring degraded — source unavailable this run",
			Key:      key, Check: check, Timestamp: now, RunID: runID,
		}
		decisions = append(decisions, Decision{Alert: a, Targets: append([]string(nil), ks.NotifiedTargets...)})
	}
	return decisions
}

// handleUnknown freezes an existing key and surfaces "monitoring degraded" once
// for a firing key. A key with no prior state is ignored (nothing to freeze).
func handleUnknown(cfg *config.Config, ks *state.KeyState, o checks.Observation, host string, now time.Time, runID int64) (Decision, bool) {
	if ks == nil {
		return Decision{}, false
	}
	ks.LastSeenRun = runID
	if ks.Status == state.StatusFiring && !ks.DegradedNotified {
		ks.DegradedNotified = true
		a := buildAlert(o, host, ks.Tier, KindDegraded, now, runID)
		a.Measured = "monitoring degraded — could not measure this run"
		return Decision{Alert: a, Targets: append([]string(nil), ks.NotifiedTargets...)}, true
	}
	return Decision{}, false
}

// handleBad advances flap damping and fires / escalates / reminds.
func handleBad(cfg *config.Config, snap *state.Snapshot, ks *state.KeyState, o checks.Observation, host string, now time.Time, runID int64) (Decision, bool) {
	tier := o.Tier
	ov := overridesFor(cfg, o.Check)

	if ks == nil {
		ks = &state.KeyState{Status: state.StatusPending, Tier: tier, FirstDetected: now}
		snap.Alerts[o.Key()] = ks
	}
	clampClock(ks, now)
	ks.LastSeenRun = runID
	ks.LastMeasured = o.Measured
	ks.DegradedNotified = false
	ks.ConsecutiveOK = 0
	ks.ConsecutiveBad++

	if ks.Status == state.StatusPending {
		if ks.ConsecutiveBad < flapSamplesFor(cfg, ov, tier) {
			return Decision{}, false // still damping
		}
		// FIRE
		ks.Status = state.StatusFiring
		ks.Tier = tier
		ks.FiredAt = now
		ks.LastNotified = now
		ks.LastTierNotified = tier
		targets := routeTargets(cfg, tier)
		ks.NotifiedTargets = unionTargets(ks.NotifiedTargets, targets)
		return Decision{Alert: buildAlert(o, host, tier, KindFire, now, runID), Targets: targets}, len(targets) > 0
	}

	// Already firing.
	switch {
	case tier > ks.Tier:
		// ESCALATE — one alert, not a false resolve + new fire.
		ks.Tier = tier
		ks.LastTierNotified = tier
		ks.LastNotified = now
		targets := routeTargets(cfg, tier)
		ks.NotifiedTargets = unionTargets(ks.NotifiedTargets, targets)
		return Decision{Alert: buildAlert(o, host, tier, KindEscalate, now, runID), Targets: targets}, len(targets) > 0
	case tier < ks.Tier:
		// DE-ESCALATE — improving but still bad. Record; route future reminders to
		// the lower tier. No send, no false resolve. Reset the cooldown clock so the
		// next reminder waits a full lower-tier cooldown from here, not from the
		// original higher-tier fire.
		ks.Tier = tier
		ks.LastTierNotified = tier
		ks.LastNotified = now
		return Decision{}, false
	default:
		// Unchanged firing — remind only after the cooldown.
		if now.Sub(ks.LastNotified) >= cooldownFor(cfg, ov, ks.Tier) {
			ks.LastNotified = now
			targets := routeTargets(cfg, ks.Tier)
			ks.NotifiedTargets = unionTargets(ks.NotifiedTargets, targets)
			return Decision{Alert: buildAlert(o, host, ks.Tier, KindReminder, now, runID), Targets: targets}, len(targets) > 0
		}
		return Decision{}, false
	}
}

// handleOK forgets a pending spike and auto-resolves a firing key after enough
// consecutive OK runs.
func handleOK(cfg *config.Config, snap *state.Snapshot, ks *state.KeyState, o checks.Observation, now time.Time, host string, runID int64) (Decision, bool) {
	if ks == nil {
		return Decision{}, false
	}
	clampClock(ks, now)
	ks.LastSeenRun = runID
	ks.DegradedNotified = false

	if ks.Status == state.StatusPending {
		// A pending spike that clears is silently forgotten.
		delete(snap.Alerts, o.Key())
		return Decision{}, false
	}

	ks.ConsecutiveBad = 0
	ks.ConsecutiveOK++
	if ks.ConsecutiveOK < cfg.Alerting.ResolveSamples {
		return Decision{}, false // wait for confirmed recovery
	}
	// RESOLVE — green, fanned out to exactly the channels that were notified.
	targets := append([]string(nil), ks.NotifiedTargets...)
	a := buildAlert(o, host, ks.Tier, KindResolved, now, runID)
	delete(snap.Alerts, o.Key())
	return Decision{Alert: a, Targets: targets}, len(targets) > 0
}

// clampClock defends against an NTP step: a negative elapsed time can neither
// page early nor suppress forever.
func clampClock(ks *state.KeyState, now time.Time) {
	if now.Before(ks.LastNotified) {
		ks.LastNotified = now
	}
	if now.Before(ks.FirstDetected) {
		ks.FirstDetected = now
	}
}

func buildAlert(o checks.Observation, host string, tier config.Tier, kind Kind, now time.Time, runID int64) Alert {
	return Alert{
		Tier:      tier,
		Kind:      kind,
		Title:     o.Title,
		Host:      host,
		Scope:     o.Scope,
		Measured:  o.Measured,
		Threshold: o.Threshold,
		Fix:       o.Fix,
		Key:       o.Key(),
		Check:     o.Check,
		Timestamp: now,
		RunID:     runID,
	}
}
