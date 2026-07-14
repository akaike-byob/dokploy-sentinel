// Package state models dokploy-sentinel's cross-run memory (state.json): the run
// counter, delta baselines (vmstat, disk fill-rate rings, per-service restart /
// exit history), and the alert state machine's per-key memory. The process
// itself is short-lived and stateless; everything that must persist lives here.
package state

import (
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// Version is the state.json schema version. A mismatch is treated as no baseline
// (safe: rate checks warm up, level checks still run).
const Version = 1

// DiskRingMax caps the number of points kept per filesystem so state.json can't
// grow unbounded on a churny host.
const DiskRingMax = 64

// Snapshot is the whole persisted state for the tool.
type Snapshot struct {
	Version  int       `json:"version"`
	RunCount int64     `json:"run_count"`
	BootID   string    `json:"boot_id"`
	LastRun  time.Time `json:"last_run"`

	Vmstat   VmstatBaseline           `json:"vmstat"`
	Disks    map[string]*DiskRing     `json:"disks"`    // keyed by mount path
	Services map[string]*ServiceState `json:"services"` // keyed by ServiceKey
	Alerts   map[string]*KeyState     `json:"alerts"`   // keyed by "check:scope"

	// SurfacedInfos records one-time INFO notices already delivered (e.g. socket
	// missing), so they aren't repeated every run.
	SurfacedInfos map[string]bool `json:"surfaced_infos,omitempty"`
}

// New returns an empty, initialized snapshot (the first-run / no-baseline state).
func New() *Snapshot {
	return &Snapshot{
		Version:       Version,
		Disks:         map[string]*DiskRing{},
		Services:      map[string]*ServiceState{},
		Alerts:        map[string]*KeyState{},
		SurfacedInfos: map[string]bool{},
	}
}

// ensureMaps makes a decoded snapshot safe to write to.
func (s *Snapshot) ensureMaps() {
	if s.Disks == nil {
		s.Disks = map[string]*DiskRing{}
	}
	if s.Services == nil {
		s.Services = map[string]*ServiceState{}
	}
	if s.Alerts == nil {
		s.Alerts = map[string]*KeyState{}
	}
	if s.SurfacedInfos == nil {
		s.SurfacedInfos = map[string]bool{}
	}
}

// VmstatBaseline is the previous run's monotonic swap counters + when they were
// read, for computing page-in rates.
type VmstatBaseline struct {
	Pswpin     uint64    `json:"pswpin"`
	Pswpout    uint64    `json:"pswpout"`
	Pgmajfault uint64    `json:"pgmajfault"`
	Timestamp  time.Time `json:"timestamp"`
	BootID     string    `json:"boot_id"`
}

// DiskPoint is a single (timestamp, used_bytes) reading in a fill-rate ring.
type DiskPoint struct {
	Timestamp time.Time `json:"ts"`
	UsedBytes int64     `json:"used_bytes"`
}

// DiskRing is a bounded history of a filesystem's usage for trend estimation.
type DiskRing struct {
	Points []DiskPoint `json:"points"`
}

// Add appends a point and trims to DiskRingMax.
func (r *DiskRing) Add(p DiskPoint) {
	r.Points = append(r.Points, p)
	if len(r.Points) > DiskRingMax {
		r.Points = r.Points[len(r.Points)-DiskRingMax:]
	}
}

// ExitEvent records one observed short-lived exited container (for swarm
// crash-loop counting, where reschedules produce fresh container ids).
type ExitEvent struct {
	ContainerID string    `json:"container_id"`
	At          time.Time `json:"at"`
}

// ServiceState is per-service delta memory, keyed on the service label so it
// survives swarm reschedules.
type ServiceState struct {
	LastRestartCount int         `json:"last_restart_count"`
	LastOOMKill      uint64      `json:"last_oom_kill"`
	ExitEvents       []ExitEvent `json:"exit_events,omitempty"`
	LastSeenRun      int64       `json:"last_seen_run"`
	BootID           string      `json:"boot_id"`
}

// KeyState is the alert state machine's per-key memory (docs/plan/05-alerting.md
// §5.3). The key is "check:scope"; tier is an attribute, not part of the key.
type KeyState struct {
	Status           string      `json:"status"` // "pending" | "firing"
	Tier             config.Tier `json:"tier"`
	ConsecutiveBad   int         `json:"consecutive_bad"`
	ConsecutiveOK    int         `json:"consecutive_ok"`
	FirstDetected    time.Time   `json:"first_detected"`
	FiredAt          time.Time   `json:"fired_at,omitempty"`
	LastNotified     time.Time   `json:"last_notified,omitempty"`
	LastTierNotified config.Tier `json:"last_tier_notified"`
	NotifiedTargets  []string    `json:"notified_targets,omitempty"`
	LastMeasured     string      `json:"last_measured,omitempty"`
	LastSeenRun      int64       `json:"last_seen_run"`
	// DegradedNotified records that a "monitoring degraded" notice was already
	// sent for this firing key while it sat UNKNOWN, so it isn't repeated.
	DegradedNotified bool `json:"degraded_notified,omitempty"`
}

// Alert state constants.
const (
	StatusPending = "pending"
	StatusFiring  = "firing"
)
