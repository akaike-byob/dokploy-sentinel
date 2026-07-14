package checks

import (
	"fmt"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// swapThrash — sustained paging *in* from swap: the real "grinding to a halt"
// signal (swap *usage* is benign; swap *thrash* is not). PAGE, host-scoped,
// gated off when no swap exists. Requires a delta, so it warms up on first run.
type swapThrash struct{ cfg config.SwapThrashConfig }

func (swapThrash) ID() string { return "swap_thrash" }

func (c swapThrash) Evaluate(ec *EvalContext) []Observation {
	v := ec.Sample.Vmstat
	if v.Health != health.OK {
		return []Observation{hostUnknown("swap_thrash", "Swap thrash", "could not read /proc/vmstat")}
	}
	if !ec.Sample.SwapPresent {
		return []Observation{hostOK("swap_thrash", "Swap thrash", "no swap configured", 0)}
	}

	prev := ec.Prev.Vmstat
	warming := !ec.HadBaseline || ec.BootChanged || prev.Timestamp.IsZero() ||
		ec.Sample.BootID != prev.BootID
	if !warming {
		dt := ec.Now.Sub(prev.Timestamp).Seconds()
		if dt <= 0 || ec.Sample.Vmstat.Pswpin < prev.Pswpin {
			warming = true // clamp non-positive dt / counter reset
		} else {
			rate := float64(ec.Sample.Vmstat.Pswpin-prev.Pswpin) / dt
			measured := fmt.Sprintf("%.0f pages/s paged in from swap", rate)
			if rate >= c.cfg.PswpinPagesPerSec {
				fix := "the working set no longer fits in RAM — cap or move a stack; adding swap only defers this"
				return []Observation{hostBad("swap_thrash", "Swap thrash", config.TierPAGE,
					measured, fmt.Sprintf("%.0f pages/s", c.cfg.PswpinPagesPerSec), fix, rate)}
			}
			return []Observation{hostOK("swap_thrash", "Swap thrash", measured, rate)}
		}
	}
	o := hostOK("swap_thrash", "Swap thrash", "rate warming up (no baseline yet)", 0)
	o.Warming = true
	return []Observation{o}
}
