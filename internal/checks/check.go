package checks

import (
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

// EvalContext is everything a check needs, injected so checks stay pure and
// deterministic (docs/plan/09-testing.md §9.1).
type EvalContext struct {
	Sample      *collect.Sample
	Prev        *state.Snapshot
	HadBaseline bool
	Cfg         *config.Config
	Now         time.Time
	HostLabel   string
	BootChanged bool
	// Exceptions is the compiled exception set. Aggregating checks consult it for
	// exclude_from_budget; the post-check transform is applied separately via
	// ExceptionSet.Apply.
	Exceptions *ExceptionSet
}

// Check is the extension point: one file per check implements it (docs/plan/
// 02-architecture.md §2.3). Adding a signal = one new Check + a config block.
type Check interface {
	ID() string
	Evaluate(ec *EvalContext) []Observation
}

// BuildRegistry returns the enabled Phase 1 checks in a stable order.
func BuildRegistry(cfg *config.Config) []Check {
	var out []Check
	c := cfg.Checks
	if c.MemPressure.Enabled {
		out = append(out, memPressure{c.MemPressure})
	}
	if c.CommittedAS.Enabled {
		out = append(out, committedAS{c.CommittedAS})
	}
	if c.DeclaredOvercommit.Enabled {
		out = append(out, declaredOvercommit{c.DeclaredOvercommit})
	}
	if c.UnboundedMem.Enabled {
		out = append(out, unboundedMem{c.UnboundedMem})
	}
	if c.DiskFill.Enabled {
		out = append(out, diskFill{c.DiskFill})
	}
	if c.DiskInodes.Enabled {
		out = append(out, diskInodes{c.DiskInodes})
	}
	if c.SwapThrash.Enabled {
		out = append(out, swapThrash{c.SwapThrash})
	}
	if c.Crashloop.Enabled {
		out = append(out, crashloop{c.Crashloop})
	}
	return out
}

// EvaluateAll runs every enabled check and returns the flattened observations.
func EvaluateAll(ec *EvalContext) []Observation {
	var obs []Observation
	for _, ch := range BuildRegistry(ec.Cfg) {
		obs = append(obs, ch.Evaluate(ec)...)
	}
	return obs
}

// ---- shared observation constructors ----

// hostOK builds an OK host-scoped observation.
func hostOK(check, title, measured string, mv float64) Observation {
	return Observation{Check: check, Scope: "host", Health: health.OK, Title: title, Measured: measured, MeasuredValue: mv}
}

// hostBad builds a BAD host-scoped observation.
func hostBad(check, title string, tier config.Tier, measured, threshold, fix string, mv float64) Observation {
	return Observation{
		Check: check, Scope: "host", Health: health.BAD, Tier: tier,
		Title: title, Measured: measured, Threshold: threshold, Fix: fix, MeasuredValue: mv,
	}
}

// hostUnknown builds an UNKNOWN host-scoped observation.
func hostUnknown(check, title, reason string) Observation {
	return Observation{Check: check, Scope: "host", Health: health.UNKNOWN, Title: title, Measured: reason}
}
