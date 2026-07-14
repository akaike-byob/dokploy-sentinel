package checks

import (
	"fmt"
	"strings"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// ExceptionSet is the compiled, time-aware view of the [[exceptions]] rules
// (docs/plan/06-config.md §6.3). It is applied AFTER checks emit observations and
// BEFORE the state machine, except for exclude_from_budget which the aggregating
// checks consult directly.
type ExceptionSet struct {
	rules []compiledRule
	now   time.Time
}

type compiledRule struct {
	raw     config.ExceptionRule
	idx     int
	expired bool
}

// NewExceptionSet compiles the rules, resolving expiry against now.
func NewExceptionSet(rules []config.ExceptionRule, now time.Time) *ExceptionSet {
	es := &ExceptionSet{now: now}
	for i, r := range rules {
		es.rules = append(es.rules, compiledRule{raw: r, idx: i, expired: ruleExpired(r, now)})
	}
	return es
}

// ruleExpired reports whether an exception's expires date has passed. The rule
// applies through the whole of its expires day; it is skipped strictly after.
func ruleExpired(r config.ExceptionRule, now time.Time) bool {
	if r.Expires == "" {
		return false
	}
	d, err := time.Parse(config.ExpiresLayout, r.Expires)
	if err != nil {
		return false // validated elsewhere; be permissive here
	}
	// Expired once now is on a later calendar day (UTC) than the expires date.
	end := d.AddDate(0, 0, 1)
	return !now.UTC().Before(end)
}

// matchTarget is the container identity an exception matches against.
type matchTarget struct {
	name       string
	image      string
	service    string
	serviceKey string
	labels     map[string]string
}

func targetFromObs(o Observation) matchTarget {
	return matchTarget{name: o.ContainerName, image: o.Image, service: o.ServiceName, serviceKey: o.ServiceName, labels: o.Labels}
}

func targetFromContainer(c collect.Container) matchTarget {
	return matchTarget{name: c.Name, image: c.Image, service: c.ServiceName(), serviceKey: c.ServiceKey(), labels: c.Labels}
}

// matchSpec reports whether all provided match keys match (logical AND).
func matchSpec(m config.MatchSpec, t matchTarget) bool {
	if m.Name != "" && !globMatch(m.Name, t.name) {
		return false
	}
	if m.Image != "" && !globMatch(m.Image, t.image) {
		return false
	}
	if m.Service != "" && !(globMatch(m.Service, t.service) || globMatch(m.Service, t.serviceKey)) {
		return false
	}
	if m.Label != "" {
		k, v, ok := strings.Cut(m.Label, "=")
		if !ok {
			return false
		}
		got, present := t.labels[k]
		if !present || !globMatch(v, got) {
			return false
		}
	}
	return true
}

// ExcludedFromBudget reports whether a container is dropped from the aggregating
// checks' sums (declared_overcommit + unbounded_mem) by a live exclude rule.
func (es *ExceptionSet) ExcludedFromBudget(c collect.Container) bool {
	if es == nil {
		return false
	}
	t := targetFromContainer(c)
	for _, r := range es.rules {
		if r.expired || !r.raw.ExcludeFromBudget {
			continue
		}
		if matchSpec(r.raw.Match, t) {
			return true
		}
	}
	return false
}

// Apply runs the post-check transform: mute/retier/rethreshold for matching
// container-scoped observations, plus one expiry WARN per expired rule. Host
// observations pass through untouched (nothing to match per-container).
func (es *ExceptionSet) Apply(obs []Observation) []Observation {
	if es == nil {
		return obs
	}
	for i := range obs {
		o := &obs[i]
		if o.hostScoped() {
			continue
		}
		t := targetFromObs(*o)
		for _, r := range es.rules {
			if r.expired || !matchSpec(r.raw.Match, t) {
				continue
			}
			applyRule(r.raw, o)
		}
	}
	// Expiry warnings so a "temporary" mute can't rot into a permanent blind spot.
	for _, r := range es.rules {
		if r.expired {
			obs = append(obs, expiryWarn(r))
		}
	}
	return obs
}

// applyRule mutates one observation per a matching rule.
func applyRule(r config.ExceptionRule, o *Observation) {
	// mute: relabel, never hide — suppression is recorded, the observation stays.
	for _, id := range r.Mute {
		if id == "*" || id == o.Check {
			o.Suppressed = true
			o.SuppressedReason = r.Reason
		}
	}
	// per-container threshold override: re-decide health from the measured value.
	if th, ok := r.Thresholds[o.Check]; ok {
		redecideThreshold(o, th)
	}
	// retier: rewrite the tier for a still-bad observation (usually a downgrade).
	if t, ok := r.Retier[o.Check]; ok && o.Health == health.BAD {
		if tier, err := config.ParseTier(t); err == nil {
			o.Tier = tier
		}
	}
}

// redecideThreshold re-evaluates a numeric check's health against a per-container
// threshold. Only checks with a single greater-is-bad primary threshold are
// supported (crashloop.restarts in Phase 1).
func redecideThreshold(o *Observation, th map[string]any) {
	key := primaryThresholdKey(o.Check)
	if key == "" {
		return
	}
	newT, ok := toFloat(th[key])
	if !ok {
		return
	}
	if o.MeasuredValue >= newT {
		o.Health = health.BAD
		o.Threshold = fmt.Sprintf("%g (per-container override)", newT)
	} else {
		o.Health = health.OK
		o.Tier = config.TierWARN
		o.Fix = ""
		o.Threshold = fmt.Sprintf("%g (per-container override)", newT)
	}
}

// primaryThresholdKey maps a check to its single greater-is-bad threshold key.
func primaryThresholdKey(check string) string {
	switch check {
	case "crashloop":
		return "restarts"
	default:
		return ""
	}
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

// expiryWarn builds the one-line WARN emitted for an expired exception rule.
func expiryWarn(r compiledRule) Observation {
	return Observation{
		Check:     "exception_expired",
		Scope:     fmt.Sprintf("rule-%d", r.idx),
		Health:    health.BAD,
		Tier:      config.TierWARN,
		Title:     "Exception expired",
		Measured:  fmt.Sprintf("%q expired %s", r.raw.Reason, r.raw.Expires),
		Threshold: r.raw.Expires,
		Fix:       "remove or renew the [[exceptions]] rule; the muted checks are armed again",
	}
}

// globMatch matches a shell-style glob supporting '*' (any run, including empty)
// and '?' (any one char). Unlike path.Match it does not treat '/' specially, so
// image refs like "registry.io/foo" match "*foo*".
func globMatch(pattern, s string) bool {
	// Iterative match with backtracking for '*'.
	var star int = -1
	var starMatch int
	pi, si := 0, 0
	for si < len(s) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == s[si]) {
			pi++
			si++
		} else if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			starMatch = si
			pi++
		} else if star != -1 {
			pi = star + 1
			starMatch++
			si = starMatch
		} else {
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
