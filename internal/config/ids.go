package config

// Check-id registries used by config validation and the exceptions engine.
// The full catalog is listed (docs/plan/04-checks.md) so an exception or config
// referencing a not-yet-implemented check id validates; only Phase 1 checks are
// actually wired into the run in v1.

// HostScopedCheckIDs are checks with no container to match — they are NOT
// exceptable per-container (docs/plan/06-config.md §6.3).
var HostScopedCheckIDs = map[string]bool{
	"mem_pressure":        true,
	"committed_as":        true,
	"declared_overcommit": true,
	"disk_fill":           true,
	"disk_inodes":         true,
	"swap_thrash":         true,
	"load_high":           true, // phase 3
	"boot_storm":          true, // phase 2
}

// PerContainerCheckIDs are checks scoped to a container/service and therefore
// exceptable.
var PerContainerCheckIDs = map[string]bool{
	"unbounded_mem":     true,
	"crashloop":         true,
	"oom_kill":          true, // phase 2
	"cpu_throttle":      true, // phase 3
	"container_health":  true, // phase 3
	"single_replica":    true, // phase 3
	"no_restart_policy": true, // phase 3
	"no_healthcheck":    true, // phase 3
	"hygiene":           true, // phase 3
}

// KnownCheckID reports whether id is any recognized check in the catalog.
func KnownCheckID(id string) bool {
	return HostScopedCheckIDs[id] || PerContainerCheckIDs[id]
}

// Phase1CheckIDs are the checks actually implemented and run in v1.
var Phase1CheckIDs = []string{
	"mem_pressure",
	"committed_as",
	"declared_overcommit",
	"unbounded_mem",
	"disk_fill",
	"disk_inodes",
	"swap_thrash",
	"crashloop",
}
