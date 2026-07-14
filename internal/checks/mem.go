package checks

import (
	"fmt"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// memPressure — live RAM usage crossed a percentage of physical RAM. PAGE at the
// top threshold. Uses MemAvailable, so it reflects true pressure not cache.
type memPressure struct{ cfg config.MemPressureConfig }

func (memPressure) ID() string { return "mem_pressure" }

func (c memPressure) Evaluate(ec *EvalContext) []Observation {
	m := ec.Sample.Mem
	if m.Health != health.OK {
		return []Observation{hostUnknown("mem_pressure", "Memory pressure", "could not read /proc/meminfo")}
	}
	used := m.UsedPct()
	// MemAvailable is a kernel estimate and can marginally exceed MemTotal; guard
	// the uint64 subtraction so it can't underflow to a huge value.
	var usedKB uint64
	if m.MemTotalKB > m.MemAvailableKB {
		usedKB = m.MemTotalKB - m.MemAvailableKB
	}
	usedBytes := int64(usedKB) * 1024
	totalBytes := int64(m.MemTotalKB) * 1024
	measured := fmt.Sprintf("%s (%s/%s)", pct(used), humanBytes(usedBytes), humanBytes(totalBytes))

	fix := "restart or cap the largest unbounded container now; check swap_thrash for active paging"
	switch {
	case used >= c.cfg.Page:
		return []Observation{hostBad("mem_pressure", "Memory pressure", config.TierPAGE, measured, pct(c.cfg.Page), fix, used)}
	case used >= c.cfg.Alert:
		return []Observation{hostBad("mem_pressure", "Memory pressure", config.TierALERT, measured, pct(c.cfg.Alert), fix, used)}
	case used >= c.cfg.Warn:
		return []Observation{hostBad("mem_pressure", "Memory pressure", config.TierWARN, measured, pct(c.cfg.Warn), fix, used)}
	default:
		return []Observation{hostOK("mem_pressure", "Memory pressure", measured, used)}
	}
}

// committedAS — the kernel has promised far more memory than exists. Host-only,
// nearly free (no container walk). Fires on the Committed_AS/MemTotal ratio and
// on a swap-aware guard (promise exceeds usable virtual memory even with swap).
type committedAS struct{ cfg config.CommittedASConfig }

func (committedAS) ID() string { return "committed_as" }

func (c committedAS) Evaluate(ec *EvalContext) []Observation {
	m := ec.Sample.Mem
	if m.Health != health.OK || m.CommittedASKB == 0 {
		return []Observation{hostUnknown("committed_as", "Memory over-commit", "could not read Committed_AS")}
	}
	r := m.CommittedRatio()
	committedBytes := int64(m.CommittedASKB) * 1024
	totalBytes := int64(m.MemTotalKB) * 1024
	usableKB := m.MemTotalKB + m.SwapTotalKB
	swapGuard := c.cfg.CommitVsSwapRatio > 0 &&
		float64(m.CommittedASKB) > float64(usableKB)*c.cfg.CommitVsSwapRatio

	measured := fmt.Sprintf("%s (%s committed / %s RAM)", ratio(r), humanBytes(committedBytes), humanBytes(totalBytes))

	tier := config.TierWARN
	bad := false
	threshold := ratio(c.cfg.Warn)
	switch {
	case r >= c.cfg.Alert:
		tier, bad, threshold = config.TierALERT, true, ratio(c.cfg.Alert)
	case r >= c.cfg.Warn:
		tier, bad, threshold = config.TierWARN, true, ratio(c.cfg.Warn)
	}
	if swapGuard {
		// Promise exceeds RAM+swap — a stronger "this can actually OOM" signal.
		tier, bad = config.TierALERT, true
		threshold = fmt.Sprintf("%s committed vs %s RAM+swap", humanBytes(committedBytes), humanBytes(int64(usableKB)*1024))
	}
	if !bad {
		return []Observation{hostOK("committed_as", "Memory over-commit", measured, r)}
	}
	fix := "right-size or move a stack off-box; add per-container mem_limits so the promise fits RAM"
	if swapGuard {
		fix = "over-committed beyond RAM+swap — move a stack off-box or add memory now"
	}
	return []Observation{hostBad("committed_as", "Memory over-commit", tier, measured, threshold, fix, r)}
}
