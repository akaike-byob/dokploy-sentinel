package collect

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// Options configures a single Collect. Roots are injectable so tests can point
// them at fixture trees (docs/plan/09-testing.md §9.1).
type Options struct {
	ProcRoot           string        // default /proc
	CgroupRoot         string        // default /sys/fs/cgroup
	DiskPaths          []string      // filesystems to statvfs
	Docker             *DockerClient // nil ⇒ Docker source reported UNKNOWN
	InspectConcurrency int           // bounded inspect fan-out (default 12)
	Deadline           time.Duration // total Docker collection budget (default 20s)
	Now                time.Time     // single run timestamp
	RunID              int64
	BootID             string // if empty, read from ProcRoot
}

func (o *Options) applyDefaults() {
	if o.ProcRoot == "" {
		o.ProcRoot = "/proc"
	}
	if o.CgroupRoot == "" {
		o.CgroupRoot = "/sys/fs/cgroup"
	}
	if o.InspectConcurrency <= 0 {
		o.InspectConcurrency = 12
	}
	if o.Deadline <= 0 {
		o.Deadline = 20 * time.Second
	}
}

// Collect reads every configured source into a Sample. It never returns an
// error: a failed source yields UNKNOWN health on that part of the Sample so the
// evaluation stage can freeze the affected keys rather than see false absence.
func Collect(ctx context.Context, opts Options) *Sample {
	opts.applyDefaults()

	s := &Sample{
		RunID:     opts.RunID,
		Timestamp: opts.Now,
		BootID:    opts.BootID,
	}
	if s.BootID == "" {
		s.BootID = ReadBootID(opts.ProcRoot)
	}

	// ---- host: cheap /proc + statvfs reads ----
	s.Mem = readMeminfo(opts.ProcRoot)
	s.Vmstat = readVmstat(opts.ProcRoot)
	s.Load = readLoadavg(opts.ProcRoot)
	s.SwapPresent = swapPresent(opts.ProcRoot, s.Mem)
	s.Disks = collectDisks(opts.DiskPaths)

	// ---- docker + cgroup ----
	s.Docker = collectDocker(ctx, opts)

	return s
}

// collectDocker pings, lists, inspects (bounded), and resolves cgroups under a
// single collection deadline.
func collectDocker(ctx context.Context, opts Options) DockerInfo {
	var di DockerInfo
	if opts.Docker == nil {
		di.Health = health.UNKNOWN
		di.Err = "docker socket not configured"
		return di
	}

	dctx, cancel := context.WithTimeout(ctx, opts.Deadline)
	defer cancel()

	hint, err := opts.Docker.Ping(dctx)
	if err != nil {
		di.Health = health.UNKNOWN
		di.Err = err.Error()
		di.Hint = hint
		return di
	}
	di.Reachable = true
	di.APIVersion = opts.Docker.APIVersion()

	list, err := opts.Docker.ListContainers(dctx)
	if err != nil {
		di.Health = health.UNKNOWN
		di.Err = err.Error()
		return di
	}
	di.Health = health.OK

	containers := make([]Container, len(list))
	for i, it := range list {
		containers[i] = Container{
			ID:     it.ID,
			Name:   cleanName(it.Names),
			Image:  it.Image,
			Labels: it.Labels,
			State:  it.State,
			Status: it.Status,
		}
	}

	// Bounded inspect fan-out under the deadline.
	sem := make(chan struct{}, opts.InspectConcurrency)
	var wg sync.WaitGroup
	for i := range containers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-dctx.Done():
				return // deadline hit; leave Inspected=false
			}
			inspectInto(dctx, opts, &containers[idx])
		}(i)
	}
	wg.Wait()

	di.Containers = containers
	return di
}

// inspectInto fills inspect + cgroup data for one container in place.
func inspectInto(ctx context.Context, opts Options, c *Container) {
	ins, err := opts.Docker.Inspect(ctx, c.ID)
	if err != nil {
		return // Inspected stays false → dependent checks treat as UNKNOWN
	}
	c.Inspected = true
	c.Name = strings.TrimPrefix(ins.Name, "/")
	if ins.Config.Image != "" {
		c.Image = ins.Config.Image
	}
	if len(ins.Config.Labels) > 0 {
		c.Labels = ins.Config.Labels
	}
	c.State = ins.State.Status
	c.Pid = ins.State.Pid
	c.MemoryLimit = ins.HostConfig.Memory
	c.OOMKilled = ins.State.OOMKilled
	c.ExitCode = ins.State.ExitCode
	c.StartedAt = parseDockerTime(ins.State.StartedAt)
	c.FinishedAt = parseDockerTime(ins.State.FinishedAt)
	c.RestartCount = ins.RestartCount
	c.RestartPolicy = ins.HostConfig.RestartPolicy.Name
	c.Privileged = ins.HostConfig.Privileged
	c.HasHealthcheck = ins.hasHealthcheck()
	if ins.State.Health != nil {
		c.HealthStatus = ins.State.Health.Status
	}

	// Resolve cgroup only for running containers with a live pid.
	if c.State == "running" && c.Pid > 0 {
		if cgroupV2Available(opts.CgroupRoot) {
			c.Cgroup = readCgroup(opts.ProcRoot, opts.CgroupRoot, c.ID, c.Pid)
		} else {
			c.Cgroup = CgroupStats{Health: health.UNKNOWN, Err: "cgroup v2 not available (reduced fidelity)"}
		}
	}
}

// cleanName strips the leading slash Docker prepends to container names.
func cleanName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}
