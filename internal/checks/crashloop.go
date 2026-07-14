package checks

import (
	"fmt"
	"sort"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

// crashloop — a container/service is restart-looping. Plain Docker uses the
// RestartCount delta; Swarm reschedules with new container ids (so RestartCount
// stays low), so we also count short-lived exited containers per service over a
// window. ALERT, scoped per service. State is keyed on the service label so
// history survives a reschedule.
type crashloop struct{ cfg config.CrashloopConfig }

func (crashloop) ID() string { return "crashloop" }

func (c crashloop) Evaluate(ec *EvalContext) []Observation {
	d := ec.Sample.Docker
	if d.Health != health.OK {
		return []Observation{{Check: "crashloop", Scope: "docker", Health: health.UNKNOWN,
			Title: "Crash loop", Measured: dockerUnknownReason(d)}}
	}

	cutoff := ec.Now.Add(-c.cfg.Window.D())
	byService := groupByService(d.Containers)

	keys := make([]string, 0, len(byService))
	for k := range byService {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic output

	var obs []Observation
	for _, key := range keys {
		obs = append(obs, c.evalService(ec, key, byService[key], cutoff))
	}
	return obs
}

func (c crashloop) evalService(ec *EvalContext, key string, cts []collect.Container, cutoff time.Time) Observation {
	o := Observation{Check: "crashloop", Scope: key, Title: "Crash loop"}
	// Link the representative container (prefer a running one, else the newest).
	rep := representative(cts)
	linkContainer(&o, rep)
	o.Scope = key // keep the service key as the dedup scope

	// Short-lived exited corpses in the window (swarm-aware), merged with history.
	windowCrashes := countWindowCrashes(ec.Prev.Services[key], cts, cutoff.Unix())

	// Plain-Docker in-place restart delta (skipped without a baseline / after reboot).
	restartDelta := 0
	if ec.HadBaseline && !ec.BootChanged {
		if prev := ec.Prev.Services[key]; prev != nil && prev.BootID == ec.Sample.BootID {
			cur := maxRestartCount(cts)
			if cur >= prev.LastRestartCount {
				restartDelta = cur - prev.LastRestartCount
			}
		}
	}

	crashes := windowCrashes
	if restartDelta > crashes {
		crashes = restartDelta
	}
	o.MeasuredValue = float64(crashes)

	// If the service has running containers but none could be inspected, we can't
	// read RestartCount → don't claim OK.
	if hasUninspectedRunning(cts) && windowCrashes == 0 {
		o.Health = health.UNKNOWN
		o.Measured = "running container not inspected"
		return o
	}

	measured := fmt.Sprintf("%d restart(s) in %s", crashes, c.cfg.Window.D())
	if crashes >= c.cfg.Restarts {
		o.Health = health.BAD
		o.Tier = config.TierALERT
		o.Measured = measured
		o.Threshold = fmt.Sprintf("%d in %s", c.cfg.Restarts, c.cfg.Window.D())
		o.Fix = fmt.Sprintf("inspect %s logs for the crash cause (bad deploy, OOM-restart, config error)", o.ServiceName)
		return o
	}
	// OK, but carry the tier + fix this check would fire at, so a per-container
	// `thresholds` override that re-decides this to BAD (except.go) promotes it at
	// the correct severity with remediation text rather than the zero-value WARN.
	o.Health = health.OK
	o.Tier = config.TierALERT
	o.Fix = fmt.Sprintf("inspect %s logs for the crash cause (bad deploy, OOM-restart, config error)", o.ServiceName)
	o.Measured = measured
	return o
}

func groupByService(cs []collect.Container) map[string][]collect.Container {
	m := map[string][]collect.Container{}
	for _, c := range cs {
		m[c.ServiceKey()] = append(m[c.ServiceKey()], c)
	}
	return m
}

// representative picks a container to name the service: a running one if any,
// else the most recently finished (falling back to most recently started).
func representative(cts []collect.Container) collect.Container {
	for _, c := range cts {
		if c.Running() {
			return c
		}
	}
	var rep collect.Container
	for i, c := range cts {
		if i == 0 ||
			c.FinishedAt.After(rep.FinishedAt) ||
			(c.FinishedAt.Equal(rep.FinishedAt) && c.StartedAt.After(rep.StartedAt)) {
			rep = c
		}
	}
	return rep
}

func maxRestartCount(cts []collect.Container) int {
	max := 0
	for _, c := range cts {
		if c.RestartCount > max {
			max = c.RestartCount
		}
	}
	return max
}

func hasUninspectedRunning(cts []collect.Container) bool {
	for _, c := range cts {
		if c.Running() && !c.Inspected {
			return true
		}
	}
	return false
}

// countWindowCrashes merges previously-recorded exit events (within the window)
// with the exited corpses currently visible, de-duplicated by container id.
func countWindowCrashes(prev *state.ServiceState, cts []collect.Container, cutoffUnix int64) int {
	seen := map[string]bool{}
	if prev != nil {
		for _, e := range prev.ExitEvents {
			if e.At.Unix() >= cutoffUnix {
				seen[e.ContainerID] = true
			}
		}
	}
	for _, ct := range cts {
		if isCrashExit(ct) && ct.FinishedAt.Unix() >= cutoffUnix {
			seen[ct.ID] = true
		}
	}
	return len(seen)
}
