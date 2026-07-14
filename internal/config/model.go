package config

// Config is the fully-decoded, defaulted, and validated configuration for one
// run. It mirrors /etc/dokploy-sentinel/config.toml (see docs/plan/06-config.md).
type Config struct {
	HostLabel    string `toml:"host_label"`
	ReportPath   string `toml:"report_path"`
	StatePath    string `toml:"state_path"`
	HeartbeatURL string `toml:"heartbeat_url"`

	Docker     DockerConfig            `toml:"docker"`
	Targets    map[string]TargetConfig `toml:"targets"`
	Routing    RoutingConfig           `toml:"routing"`
	Alerting   AlertingConfig          `toml:"alerting"`
	Checks     ChecksConfig            `toml:"checks"`
	Exceptions []ExceptionRule         `toml:"exceptions"`
}

// DockerConfig controls how the Docker socket is read.
type DockerConfig struct {
	Socket             string   `toml:"socket"`
	InspectConcurrency int      `toml:"inspect_concurrency"`
	CollectDeadline    Duration `toml:"collect_deadline"`
}

// TargetConfig is a single Slack incoming webhook destination.
type TargetConfig struct {
	URL     string `toml:"url"`
	MinTier string `toml:"min_tier"` // e.g. "ALERT"; empty = no floor
	Mention string `toml:"mention"`  // e.g. "<@U123>"; only rendered on PAGE
}

// RoutingConfig maps each tier to the named targets that receive it.
type RoutingConfig struct {
	WARN  []string `toml:"WARN"`
	ALERT []string `toml:"ALERT"`
	PAGE  []string `toml:"PAGE"`
}

// TargetsFor returns the configured target names for a tier.
func (r RoutingConfig) TargetsFor(t Tier) []string {
	switch t {
	case TierWARN:
		return r.WARN
	case TierALERT:
		return r.ALERT
	case TierPAGE:
		return r.PAGE
	default:
		return nil
	}
}

// AlertingConfig holds the global alert-hygiene defaults (overridable per check).
type AlertingConfig struct {
	FlapSamplesWarn  int      `toml:"flap_samples_warn"`
	FlapSamplesAlert int      `toml:"flap_samples_alert"`
	FlapSamplesPage  int      `toml:"flap_samples_page"`
	ResolveSamples   int      `toml:"resolve_samples"`
	CooldownWarn     Duration `toml:"cooldown_warn"`
	CooldownAlert    Duration `toml:"cooldown_alert"`
	CooldownPage     Duration `toml:"cooldown_page"`
}

// FlapSamples returns the global flap threshold for a tier.
func (a AlertingConfig) FlapSamples(t Tier) int {
	switch t {
	case TierWARN:
		return a.FlapSamplesWarn
	case TierALERT:
		return a.FlapSamplesAlert
	case TierPAGE:
		return a.FlapSamplesPage
	default:
		return a.FlapSamplesWarn
	}
}

// Cooldown returns the global cooldown for a tier.
func (a AlertingConfig) Cooldown(t Tier) Duration {
	switch t {
	case TierWARN:
		return a.CooldownWarn
	case TierALERT:
		return a.CooldownAlert
	case TierPAGE:
		return a.CooldownPage
	default:
		return a.CooldownWarn
	}
}

// CheckOverrides are optional per-check overrides of alerting hygiene.
type CheckOverrides struct {
	FlapSamples *int      `toml:"flap_samples"`
	Cooldown    *Duration `toml:"cooldown"`
	Tier        *string   `toml:"tier"`
}

// ChecksConfig holds the per-check configuration blocks (Phase 1).
type ChecksConfig struct {
	MemPressure        MemPressureConfig        `toml:"mem_pressure"`
	CommittedAS        CommittedASConfig        `toml:"committed_as"`
	DeclaredOvercommit DeclaredOvercommitConfig `toml:"declared_overcommit"`
	UnboundedMem       UnboundedMemConfig       `toml:"unbounded_mem"`
	DiskFill           DiskFillConfig           `toml:"disk_fill"`
	DiskInodes         DiskInodesConfig         `toml:"disk_inodes"`
	SwapThrash         SwapThrashConfig         `toml:"swap_thrash"`
	Crashloop          CrashloopConfig          `toml:"crashloop"`
}

// MemPressureConfig — live RAM usage percentage thresholds.
type MemPressureConfig struct {
	Enabled bool    `toml:"enabled"`
	Warn    float64 `toml:"warn"`
	Alert   float64 `toml:"alert"`
	Page    float64 `toml:"page"`
	CheckOverrides
}

// CommittedASConfig — Committed_AS/MemTotal ratio + swap-aware guard.
type CommittedASConfig struct {
	Enabled           bool    `toml:"enabled"`
	Warn              float64 `toml:"warn"`
	Alert             float64 `toml:"alert"`
	CommitVsSwapRatio float64 `toml:"commit_vs_swap_ratio"`
	CheckOverrides
}

// DeclaredOvercommitConfig — sum(limits)+headroom vs usable RAM.
type DeclaredOvercommitConfig struct {
	Enabled            bool     `toml:"enabled"`
	HeadroomReservePct float64  `toml:"headroom_reserve_pct"`
	HeadroomFloor      ByteSize `toml:"headroom_floor"`
	CheckOverrides
}

// UnboundedMemConfig — a running container with no mem_limit.
type UnboundedMemConfig struct {
	Enabled bool `toml:"enabled"`
	CheckOverrides
}

// DiskFillConfig — filesystem level + fill-rate trajectory.
type DiskFillConfig struct {
	Enabled         bool     `toml:"enabled"`
	Paths           []string `toml:"paths"`
	Warn            float64  `toml:"warn"`
	Alert           float64  `toml:"alert"`
	DaysToFullAlert float64  `toml:"days_to_full_alert"`
	CheckOverrides
}

// DiskInodesConfig — inode exhaustion.
type DiskInodesConfig struct {
	Enabled bool    `toml:"enabled"`
	Alert   float64 `toml:"alert"`
	CheckOverrides
}

// SwapThrashConfig — sustained page-in rate from swap.
type SwapThrashConfig struct {
	Enabled           bool    `toml:"enabled"`
	PswpinPagesPerSec float64 `toml:"pswpin_pages_per_sec"`
	CheckOverrides
}

// CrashloopConfig — restart-loop detection.
type CrashloopConfig struct {
	Enabled  bool     `toml:"enabled"`
	Restarts int      `toml:"restarts"`
	Window   Duration `toml:"window"`
	CheckOverrides
}

// MatchSpec is the per-container matcher for an exception rule. All non-empty
// keys must match (logical AND).
type MatchSpec struct {
	Name    string `toml:"name"`    // glob on container name
	Image   string `toml:"image"`   // glob on image reference
	Service string `toml:"service"` // compose/swarm service label shorthand
	Label   string `toml:"label"`   // "key=value"; value may glob
}

// Empty reports whether no match key was provided.
func (m MatchSpec) Empty() bool {
	return m.Name == "" && m.Image == "" && m.Service == "" && m.Label == ""
}

// ExceptionRule adjusts or silences per-container checks for matching containers
// (see docs/plan/06-config.md §6.3).
type ExceptionRule struct {
	Reason            string                    `toml:"reason"`
	Match             MatchSpec                 `toml:"match"`
	Mute              []string                  `toml:"mute"`
	Retier            map[string]string         `toml:"retier"`
	Thresholds        map[string]map[string]any `toml:"thresholds"`
	ExcludeFromBudget bool                      `toml:"exclude_from_budget"`
	Expires           string                    `toml:"expires"` // YYYY-MM-DD
}

// HasAction reports whether the rule carries at least one action.
func (e ExceptionRule) HasAction() bool {
	return len(e.Mute) > 0 || len(e.Retier) > 0 || len(e.Thresholds) > 0 || e.ExcludeFromBudget
}
