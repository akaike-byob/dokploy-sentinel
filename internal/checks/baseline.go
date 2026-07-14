package checks

import (
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

// UpdateBaselines advances the persisted delta baselines using this run's sample,
// AFTER evaluation (checks read the *previous* baseline). It records the vmstat
// counters, appends to the disk fill-rate rings, and updates per-service restart
// / exit-event / oom history. Only OK sources update their baseline, so a
// transient collection failure never corrupts the next run's deltas.
func UpdateBaselines(snap *state.Snapshot, s *collect.Sample, now time.Time, crashWindow time.Duration) {
	snap.RunCount = s.RunID
	snap.BootID = s.BootID
	snap.LastRun = now

	if s.Vmstat.Health == health.OK {
		snap.Vmstat = state.VmstatBaseline{
			Pswpin:     s.Vmstat.Pswpin,
			Pswpout:    s.Vmstat.Pswpout,
			Pgmajfault: s.Vmstat.Pgmajfault,
			Timestamp:  now,
			BootID:     s.BootID,
		}
	}

	for _, d := range s.Disks {
		if d.Health == health.OK {
			snap.Ring(d.Path).Add(state.DiskPoint{Timestamp: now, UsedBytes: d.UsedBytes})
		}
	}

	if s.Docker.Health == health.OK {
		cutoff := now.Add(-crashWindow)
		for key, cts := range groupByService(s.Docker.Containers) {
			st := snap.Service(key)
			st.LastSeenRun = s.RunID
			st.BootID = s.BootID
			// Only overwrite the restart baseline from an inspected container: an
			// un-inspected running container reports RestartCount=0, which would
			// clobber a real high count and manufacture a false delta next run.
			if rc, ok := maxInspectedRestartCount(cts); ok {
				st.LastRestartCount = rc
			}
			st.LastOOMKill = maxOOMKill(cts)
			st.ExitEvents = mergeExitEvents(st.ExitEvents, cts, cutoff)
		}
	}
	snap.PruneServices(s.RunID)
}

func maxOOMKill(cts []collect.Container) uint64 {
	var m uint64
	for _, c := range cts {
		if c.Cgroup.Health == health.OK && c.Cgroup.OOMKillCount > m {
			m = c.Cgroup.OOMKillCount
		}
	}
	return m
}

// maxInspectedRestartCount returns the highest RestartCount among inspected
// containers of a service, and whether any were inspected. Callers must not
// overwrite a stored baseline when ok is false.
func maxInspectedRestartCount(cts []collect.Container) (int, bool) {
	max, any := 0, false
	for _, c := range cts {
		if !c.Inspected {
			continue
		}
		any = true
		if c.RestartCount > max {
			max = c.RestartCount
		}
	}
	return max, any
}

// mergeExitEvents keeps crash exits within the window, adds newly-visible
// crash corpses (de-duplicated by container id), and drops the rest.
func mergeExitEvents(prev []state.ExitEvent, cts []collect.Container, cutoff time.Time) []state.ExitEvent {
	byID := map[string]state.ExitEvent{}
	for _, e := range prev {
		if !e.At.Before(cutoff) {
			byID[e.ContainerID] = e
		}
	}
	for _, ct := range cts {
		if isCrashExit(ct) && !ct.FinishedAt.Before(cutoff) {
			byID[ct.ID] = state.ExitEvent{ContainerID: ct.ID, At: ct.FinishedAt}
		}
	}
	out := make([]state.ExitEvent, 0, len(byID))
	for _, e := range byID {
		out = append(out, e)
	}
	return out
}

// isCrashExit reports whether an exited container is a crash (non-zero exit or
// OOM-killed) rather than a clean completion — so periodic/one-shot containers
// that exit 0 are not counted as crash-loop restarts.
func isCrashExit(c collect.Container) bool {
	return c.State == "exited" && c.Inspected && !c.FinishedAt.IsZero() &&
		(c.ExitCode != 0 || c.OOMKilled)
}
