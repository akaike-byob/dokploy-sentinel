// Package alert turns tri-state Observations into Slack notifications through a
// signal-not-spam state machine (docs/plan/05-alerting.md). The internal Alert
// object is provider-neutral behind a Renderer interface; Slack is the only
// renderer in v1.
package alert

import (
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// Kind is the transition that produced an Alert (drives the title prefix).
type Kind string

const (
	KindFire     Kind = "fire"
	KindEscalate Kind = "escalate"
	KindReminder Kind = "reminder"
	KindResolved Kind = "resolved"
	KindDegraded Kind = "degraded" // sustained UNKNOWN on a firing key
)

// Alert is the provider-neutral event a Renderer turns into a payload.
type Alert struct {
	Tier      config.Tier `json:"tier"`
	Kind      Kind        `json:"kind"`
	Title     string      `json:"title"`
	Host      string      `json:"host"`
	Scope     string      `json:"scope"`
	Measured  string      `json:"measured"`
	Threshold string      `json:"threshold"`
	Fix       string      `json:"fix"`
	Key       string      `json:"key"`
	Check     string      `json:"check"`
	Timestamp time.Time   `json:"ts"`
	RunID     int64       `json:"run_id"`
}

// resolved reports whether this alert should render in the green RESOLVED style.
func (a Alert) resolved() bool { return a.Kind == KindResolved }

// Decision is one state-machine outcome that must be delivered: an Alert plus the
// resolved target names it fans out to.
type Decision struct {
	Alert   Alert
	Targets []string
}
