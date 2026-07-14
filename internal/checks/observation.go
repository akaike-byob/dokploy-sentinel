// Package checks evaluates the enabled check catalog (docs/plan/04-checks.md)
// against a collected Sample + the previous Snapshot, emitting tri-state
// Observations. Checks are pure: they read the Sample and prev state and never
// perform I/O or read the clock — everything is injected via EvalContext.
package checks

import (
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// Observation is one check's verdict for one scope. It carries health (never
// mere presence/absence) plus everything the renderer and state machine need.
type Observation struct {
	Check     string        `json:"check"`
	Scope     string        `json:"scope"`
	Health    health.Health `json:"health"`
	Tier      config.Tier   `json:"tier"`
	Title     string        `json:"title"`
	Measured  string        `json:"measured,omitempty"`
	Threshold string        `json:"threshold,omitempty"`
	Fix       string        `json:"fix,omitempty"`

	// Numeric measured value, so a per-container exception threshold can
	// re-decide health without re-running the check.
	MeasuredValue float64 `json:"measured_value,omitempty"`

	// Warming marks a rate check with no baseline yet (first run / post-reboot):
	// reported as OK with a note, never fired.
	Warming bool `json:"warming,omitempty"`

	// Container linkage — empty for host-scoped checks; used by the exceptions
	// engine to match rules.
	ContainerID   string            `json:"container_id,omitempty"`
	ContainerName string            `json:"container_name,omitempty"`
	ServiceName   string            `json:"service_name,omitempty"`
	Image         string            `json:"image,omitempty"`
	Labels        map[string]string `json:"-"`

	// Suppression — set by the exceptions engine; a suppressed observation is
	// still written to report.json (never hidden).
	Suppressed       bool   `json:"suppressed,omitempty"`
	SuppressedReason string `json:"suppressed_reason,omitempty"`
}

// Key is the dedup identity the alert state machine keys on: "check:scope".
func (o Observation) Key() string { return o.Check + ":" + o.Scope }

// hostScoped reports whether this observation is for a host-level check (no
// container to match against exceptions).
func (o Observation) hostScoped() bool {
	return config.HostScopedCheckIDs[o.Check]
}
