package checks

import (
	"fmt"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// linkContainer copies container identity onto an observation for exception
// matching + rendering.
func linkContainer(o *Observation, c collect.Container) {
	o.ContainerID = c.ID
	o.ContainerName = c.Name
	o.ServiceName = c.ServiceName()
	o.Image = c.Image
	o.Labels = c.Labels
}

// unboundedMem — a running container has no memory limit (HostConfig.Memory == 0),
// so it is entitled to the whole box. WARN, scoped per service.
type unboundedMem struct{ cfg config.UnboundedMemConfig }

func (unboundedMem) ID() string { return "unbounded_mem" }

func (c unboundedMem) Evaluate(ec *EvalContext) []Observation {
	d := ec.Sample.Docker
	if d.Health != health.OK {
		return []Observation{{Check: "unbounded_mem", Scope: "docker", Health: health.UNKNOWN,
			Title: "Unbounded container", Measured: dockerUnknownReason(d)}}
	}
	var obs []Observation
	for _, ct := range d.Containers {
		if !ct.Running() {
			continue
		}
		if ec.Exceptions.ExcludedFromBudget(ct) {
			continue // deliberately-unbounded helper; excluded from budget
		}
		o := Observation{Check: "unbounded_mem", Scope: ct.ServiceKey(), Title: "Unbounded container"}
		linkContainer(&o, ct)
		switch {
		case !ct.Inspected:
			o.Health = health.UNKNOWN
			o.Measured = "inspect failed (deadline or error)"
		case ct.MemoryLimit == 0:
			o.Health = health.BAD
			o.Tier = config.TierWARN
			o.Measured = fmt.Sprintf("%s has no mem_limit (can use all RAM)", ct.Name)
			o.Threshold = "any mem_limit"
			o.Fix = fmt.Sprintf("add a memory limit to %s (e.g. mem_limit: 512m)", ct.ServiceName())
			o.MeasuredValue = 1
		default:
			o.Health = health.OK
			o.Measured = fmt.Sprintf("limit %s", humanBytes(ct.MemoryLimit))
		}
		obs = append(obs, o)
	}
	return obs
}

// declaredOvercommit — the config's declared limits (plus realistic headroom per
// unbounded container) exceed usable RAM. ALERT, host-scoped, flap-damped so
// live-RSS jitter doesn't cause noise.
type declaredOvercommit struct {
	cfg config.DeclaredOvercommitConfig
}

func (declaredOvercommit) ID() string { return "declared_overcommit" }

func (c declaredOvercommit) Evaluate(ec *EvalContext) []Observation {
	m := ec.Sample.Mem
	d := ec.Sample.Docker
	if m.Health != health.OK {
		return []Observation{hostUnknown("declared_overcommit", "Declared over-commit", "could not read physical RAM")}
	}
	if d.Health != health.OK {
		return []Observation{hostUnknown("declared_overcommit", "Declared over-commit", dockerUnknownReason(d))}
	}

	floor := c.cfg.HeadroomFloor.Bytes()
	var declared int64
	var unbounded int
	for _, ct := range d.Containers {
		if !ct.Running() {
			continue
		}
		if ec.Exceptions.ExcludedFromBudget(ct) {
			continue
		}
		if !ct.Inspected {
			// An un-inspected running container makes the sum incomplete — we must
			// not under-report the budget, so freeze rather than guess.
			return []Observation{hostUnknown("declared_overcommit", "Declared over-commit",
				"a running container could not be inspected (incomplete budget)")}
		}
		if ct.MemoryLimit > 0 {
			declared += ct.MemoryLimit
			continue
		}
		// Unbounded: headroom = max(floor, working_set).
		unbounded++
		hr := floor
		if ct.Cgroup.Health == health.OK && ct.Cgroup.WorkingSet > floor {
			hr = ct.Cgroup.WorkingSet
		}
		declared += hr
	}

	usable := int64(float64(m.MemTotalKB) * 1024 * (1 - c.cfg.HeadroomReservePct/100))
	over := float64(declared) / float64(usable)
	measured := fmt.Sprintf("declared %s vs %s usable (%s over), %d unbounded",
		humanBytes(declared), humanBytes(usable), ratio(over), unbounded)

	if declared > usable {
		fix := "right-size or move a stack off-box; set mem_limits so declared+headroom fits RAM"
		return []Observation{hostBad("declared_overcommit", "Declared over-commit", config.TierALERT,
			measured, fmt.Sprintf("%s usable", humanBytes(usable)), fix, over)}
	}
	return []Observation{hostOK("declared_overcommit", "Declared over-commit", measured, over)}
}

// dockerUnknownReason renders a concise reason (+hint) for a degraded Docker source.
func dockerUnknownReason(d collect.DockerInfo) string {
	r := d.Err
	if r == "" {
		r = "docker unreachable"
	}
	if d.Hint != "" {
		r += " (" + d.Hint + ")"
	}
	return r
}
