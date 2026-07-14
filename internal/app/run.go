// Package app wires the whole run lifecycle (docs/plan/02-architecture.md §2.1)
// behind a Runner, so both the CLI and the integration test drive the exact same
// path with injected clock / collector / sender.
package app

import (
	"context"
	"os"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/alert"
	"github.com/akaike-byob/dokploy-sentinel/internal/checks"
	"github.com/akaike-byob/dokploy-sentinel/internal/clock"
	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/report"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

// CollectFunc gathers a Sample for one run. Production uses DefaultCollect; tests
// inject canned samples.
type CollectFunc func(ctx context.Context, cfg *config.Config, runID int64, now time.Time, bootID string) *collect.Sample

// Runner holds the injectable dependencies for a run.
type Runner struct {
	Cfg        *config.Config
	Clock      clock.Clock
	Collect    CollectFunc
	Sender     *alert.Sender    // nil ⇒ no delivery
	Heartbeat  *alert.Heartbeat // nil ⇒ no heartbeat
	DryRun     bool             // evaluate + report but do not send / mutate state
	ProcRoot   string           // default /proc
	CgroupRoot string           // default /sys/fs/cgroup
}

// RunResult carries what one run produced (for tests and dry-run printing).
type RunResult struct {
	Report     *report.Report
	Decisions  []alert.Decision
	Sends      []alert.SendResult
	NoBaseline bool
}

// RunOnce executes one timer firing end to end.
func (r *Runner) RunOnce(ctx context.Context) (*RunResult, error) {
	cfg := r.Cfg
	host := hostLabel(cfg)

	snap, hadBaseline, err := state.Load(cfg.StatePath)
	if err != nil {
		r.heartbeatFail(ctx)
		return nil, err
	}

	now := r.Clock.Now()
	runID := snap.RunCount + 1
	bootID := collect.ReadBootID(r.procRoot())

	if !r.DryRun {
		r.heartbeatStart(ctx)
	}

	sample := r.collect(ctx, cfg, runID, now, bootID)

	es := checks.NewExceptionSet(cfg.Exceptions, now)
	ec := &checks.EvalContext{
		Sample:      sample,
		Prev:        snap,
		HadBaseline: hadBaseline,
		Cfg:         cfg,
		Now:         now,
		HostLabel:   host,
		BootChanged: snap.BootChanged(sample.BootID),
		Exceptions:  es,
	}
	obs := checks.EvaluateAll(ec)
	obs = es.Apply(obs)

	decisions := alert.Advance(cfg, snap, obs, now, host, runID)

	checks.UpdateBaselines(snap, sample, now, crashWindow(cfg))
	snap.PrunePendingAlerts(runID)

	rep := report.Build(host, now, sample, obs, toAlertSummaries(decisions), !hadBaseline)

	res := &RunResult{Report: rep, Decisions: decisions, NoBaseline: !hadBaseline}

	if r.DryRun {
		return res, nil
	}

	// Persist state BEFORE delivering: the state machine has already recorded the
	// fires in snap (fired_at, last_notified, notified_targets). If Save fails we
	// must NOT have sent, or the next run sees the keys as brand-new and re-fires
	// them into an alert storm. So save first; only send once the fire is durable.
	if err := state.Save(cfg.StatePath, snap); err != nil {
		r.heartbeatFail(ctx)
		return res, err
	}
	if r.Sender != nil {
		res.Sends = r.Sender.Deliver(ctx, cfg, decisions)
	}
	if err := report.Write(cfg.ReportPath, rep); err != nil {
		r.heartbeatFail(ctx)
		return res, err
	}
	r.heartbeatSuccess(ctx)
	return res, nil
}

func (r *Runner) collect(ctx context.Context, cfg *config.Config, runID int64, now time.Time, bootID string) *collect.Sample {
	if r.Collect != nil {
		return r.Collect(ctx, cfg, runID, now, bootID)
	}
	return DefaultCollect(r.procRoot(), r.cgroupRoot())(ctx, cfg, runID, now, bootID)
}

// DefaultCollect builds the production collector: a unix-socket Docker client
// plus the configured disk paths and roots.
func DefaultCollect(procRoot, cgroupRoot string) CollectFunc {
	return func(ctx context.Context, cfg *config.Config, runID int64, now time.Time, bootID string) *collect.Sample {
		perCall := 5 * time.Second
		if d := cfg.Docker.CollectDeadline.D(); d > 0 && d < perCall {
			perCall = d
		}
		dc := collect.NewDockerClient(cfg.Docker.Socket, perCall)
		return collect.Collect(ctx, collect.Options{
			ProcRoot:           procRoot,
			CgroupRoot:         cgroupRoot,
			DiskPaths:          cfg.Checks.DiskFill.Paths,
			Docker:             dc,
			InspectConcurrency: cfg.Docker.InspectConcurrency,
			Deadline:           cfg.Docker.CollectDeadline.D(),
			Now:                now,
			RunID:              runID,
			BootID:             bootID,
		})
	}
}

func toAlertSummaries(decisions []alert.Decision) []report.AlertSummary {
	out := make([]report.AlertSummary, 0, len(decisions))
	for _, d := range decisions {
		out = append(out, report.AlertSummary{
			Key:     d.Alert.Key,
			Kind:    string(d.Alert.Kind),
			Tier:    d.Alert.Tier.String(),
			Targets: d.Targets,
		})
	}
	return out
}

func hostLabel(cfg *config.Config) string {
	if cfg.HostLabel != "" {
		return cfg.HostLabel
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown-host"
}

func crashWindow(cfg *config.Config) time.Duration {
	if w := cfg.Checks.Crashloop.Window.D(); w > 0 {
		return w
	}
	return 10 * time.Minute
}

func (r *Runner) procRoot() string {
	if r.ProcRoot != "" {
		return r.ProcRoot
	}
	return "/proc"
}

func (r *Runner) cgroupRoot() string {
	if r.CgroupRoot != "" {
		return r.CgroupRoot
	}
	return "/sys/fs/cgroup"
}

func (r *Runner) heartbeatStart(ctx context.Context) {
	if r.Heartbeat != nil {
		r.Heartbeat.Start(ctx)
	}
}

func (r *Runner) heartbeatSuccess(ctx context.Context) {
	if r.Heartbeat != nil {
		r.Heartbeat.Success(ctx)
	}
}

func (r *Runner) heartbeatFail(ctx context.Context) {
	if r.Heartbeat != nil {
		r.Heartbeat.Fail(ctx)
	}
}
