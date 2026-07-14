package config

import "time"

// Default returns a Config populated with the shipped Phase 1 defaults
// (docs/plan/06-config.md). Load decodes the operator's TOML on top of this, so
// any key the operator omits keeps its default here.
func Default() *Config {
	return &Config{
		HostLabel:    "",
		ReportPath:   "/var/lib/dokploy-sentinel/report.json",
		StatePath:    "/var/lib/dokploy-sentinel/state.json",
		HeartbeatURL: "",
		Docker: DockerConfig{
			Socket:             "/var/run/docker.sock",
			InspectConcurrency: 12,
			CollectDeadline:    Duration(20 * time.Second),
		},
		Targets: map[string]TargetConfig{},
		Routing: RoutingConfig{},
		Alerting: AlertingConfig{
			FlapSamplesWarn:  3,
			FlapSamplesAlert: 3,
			FlapSamplesPage:  1, // PAGE fires on first breach
			ResolveSamples:   2,
			CooldownWarn:     Duration(24 * time.Hour),
			CooldownAlert:    Duration(6 * time.Hour),
			CooldownPage:     Duration(2 * time.Hour),
		},
		Checks: ChecksConfig{
			MemPressure: MemPressureConfig{
				Enabled: true, Warn: 80, Alert: 90, Page: 95,
			},
			CommittedAS: CommittedASConfig{
				Enabled: true, Warn: 1.5, Alert: 2.0, CommitVsSwapRatio: 1.0,
			},
			DeclaredOvercommit: DeclaredOvercommitConfig{
				Enabled: true, HeadroomReservePct: 15, HeadroomFloor: ByteSize(512 << 20),
			},
			UnboundedMem: UnboundedMemConfig{Enabled: true},
			DiskFill: DiskFillConfig{
				Enabled: true, Paths: []string{"/", "/var/lib/docker"},
				Warn: 80, Alert: 90, DaysToFullAlert: 7,
			},
			DiskInodes: DiskInodesConfig{Enabled: true, Alert: 90},
			SwapThrash: SwapThrashConfig{Enabled: true, PswpinPagesPerSec: 1000},
			Crashloop:  CrashloopConfig{Enabled: true, Restarts: 5, Window: Duration(10 * time.Minute)},
		},
	}
}
