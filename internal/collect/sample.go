// Package collect reads all raw host + Docker + cgroup signals for one run into
// a single Sample. Collection is centralized so ten checks don't re-read
// /proc/meminfo ten times (docs/plan/02-architecture.md §2.3). Every source
// records a collection-health status (OK | UNKNOWN); a source that errors or
// times out yields UNKNOWN, never absence.
package collect

import (
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// Sample is the complete set of raw signals for one run. Checks read from it and
// never perform I/O themselves.
type Sample struct {
	RunID       int64     `json:"run_id"`
	Timestamp   time.Time `json:"timestamp"`
	BootID      string    `json:"boot_id"`
	SwapPresent bool      `json:"swap_present"`

	Mem    MemInfo    `json:"mem"`
	Vmstat VmstatInfo `json:"vmstat"`
	Load   LoadInfo   `json:"load"`
	Disks  []DiskInfo `json:"disks"`
	Docker DockerInfo `json:"docker"`
}

// MemInfo holds /proc/meminfo fields (values in kB, as the kernel reports them).
type MemInfo struct {
	Health         health.Health `json:"health"`
	MemTotalKB     uint64        `json:"mem_total_kb"`
	MemFreeKB      uint64        `json:"mem_free_kb"`
	MemAvailableKB uint64        `json:"mem_available_kb"`
	CommittedASKB  uint64        `json:"committed_as_kb"`
	SwapTotalKB    uint64        `json:"swap_total_kb"`
	SwapFreeKB     uint64        `json:"swap_free_kb"`
	Err            string        `json:"err,omitempty"`
}

// UsedPct is live real memory usage: (MemTotal − MemAvailable)/MemTotal × 100.
// Uses MemAvailable, not MemFree, so it reflects true pressure, not cache.
func (m MemInfo) UsedPct() float64 {
	if m.MemTotalKB == 0 {
		return 0
	}
	// MemAvailable can marginally exceed MemTotal (it is a kernel estimate); clamp
	// so used% never goes negative.
	if m.MemAvailableKB >= m.MemTotalKB {
		return 0
	}
	used := float64(m.MemTotalKB) - float64(m.MemAvailableKB)
	return used / float64(m.MemTotalKB) * 100
}

// CommittedRatio is Committed_AS / MemTotal (a healthy Linux box sits above 1.0).
func (m MemInfo) CommittedRatio() float64 {
	if m.MemTotalKB == 0 {
		return 0
	}
	return float64(m.CommittedASKB) / float64(m.MemTotalKB)
}

// VmstatInfo holds monotonic /proc/vmstat counters (delta required for rates).
type VmstatInfo struct {
	Health     health.Health `json:"health"`
	Pswpin     uint64        `json:"pswpin"`     // pages swapped in
	Pswpout    uint64        `json:"pswpout"`    // pages swapped out
	Pgmajfault uint64        `json:"pgmajfault"` // major faults
	Err        string        `json:"err,omitempty"`
}

// LoadInfo holds /proc/loadavg figures and the core count.
type LoadInfo struct {
	Health health.Health `json:"health"`
	Load1  float64       `json:"load1"`
	Load5  float64       `json:"load5"`
	Load15 float64       `json:"load15"`
	NumCPU int           `json:"num_cpu"`
	Err    string        `json:"err,omitempty"`
}

// DiskInfo holds statvfs results for one filesystem path.
type DiskInfo struct {
	Path        string        `json:"path"`
	Health      health.Health `json:"health"`
	TotalBytes  int64         `json:"total_bytes"`
	UsedBytes   int64         `json:"used_bytes"`
	AvailBytes  int64         `json:"avail_bytes"`
	UsedPct     float64       `json:"used_pct"`
	InodesTotal uint64        `json:"inodes_total"`
	InodesUsed  uint64        `json:"inodes_used"`
	InodePct    float64       `json:"inode_pct"`
	Device      uint64        `json:"device"` // st_dev, for de-duplicating mounts
	Err         string        `json:"err,omitempty"`
}

// DockerInfo holds the Docker daemon reachability + the enumerated containers.
type DockerInfo struct {
	Health     health.Health `json:"health"`
	Reachable  bool          `json:"reachable"`
	APIVersion string        `json:"api_version,omitempty"`
	Containers []Container   `json:"containers"`
	Err        string        `json:"err,omitempty"`
	Hint       string        `json:"hint,omitempty"` // e.g. "run as root / add to docker group"
}

// Container is a single enumerated + inspected container, plus its cgroup stats.
type Container struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Image          string            `json:"image"`
	Labels         map[string]string `json:"labels,omitempty"`
	State          string            `json:"state"` // running, exited, ...
	Status         string            `json:"status"`
	Pid            int               `json:"pid"`
	MemoryLimit    int64             `json:"memory_limit"` // HostConfig.Memory; 0 = unbounded
	OOMKilled      bool              `json:"oom_killed"`
	ExitCode       int               `json:"exit_code"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	RestartCount   int               `json:"restart_count"`
	RestartPolicy  string            `json:"restart_policy"`
	HasHealthcheck bool              `json:"has_healthcheck"`
	HealthStatus   string            `json:"health_status,omitempty"`
	Privileged     bool              `json:"privileged"`
	Inspected      bool              `json:"inspected"`
	Cgroup         CgroupStats       `json:"cgroup"`
}

// Running reports whether the container is in the running state.
func (c Container) Running() bool { return c.State == "running" }

// ServiceKey is the stable identity to group/key on: the swarm/compose service
// label if present, else the container name. Persisted per-container state keys
// on this so history survives a swarm reschedule (new container id).
func (c Container) ServiceKey() string {
	if c.Labels != nil {
		if v := c.Labels["com.docker.swarm.service.name"]; v != "" {
			return v
		}
		proj := c.Labels["com.docker.compose.project"]
		svc := c.Labels["com.docker.compose.service"]
		if svc != "" {
			if proj != "" {
				return proj + "_" + svc
			}
			return svc
		}
	}
	return c.Name
}

// ServiceName is the short service name (without project prefix) used for display
// and for the `service` exception match shorthand.
func (c Container) ServiceName() string {
	if c.Labels != nil {
		if v := c.Labels["com.docker.swarm.service.name"]; v != "" {
			return v
		}
		if v := c.Labels["com.docker.compose.service"]; v != "" {
			return v
		}
	}
	return c.Name
}

// CgroupStats holds the per-container cgroup v2 memory + cpu numbers.
type CgroupStats struct {
	Health        health.Health `json:"health"`
	Resolved      bool          `json:"resolved"`
	Path          string        `json:"path,omitempty"`
	Current       int64         `json:"current"`
	Max           int64         `json:"max"` // -1 = unlimited ("max")
	InactiveFile  int64         `json:"inactive_file"`
	Anon          int64         `json:"anon"`
	WorkingSet    int64         `json:"working_set"` // current − inactive_file
	OOMKillCount  uint64        `json:"oom_kill_count"`
	NrThrottled   uint64        `json:"nr_throttled"`
	ThrottledUsec uint64        `json:"throttled_usec"`
	Err           string        `json:"err,omitempty"`
}
