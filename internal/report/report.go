// Package report writes report.json: the last full evaluation, as an audit trail
// and a surface a future uptime monitor can scrape. It NEVER contains webhook
// URLs or tokens — redaction is enforced here (docs/plan/01-tech-stack.md).
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/checks"
	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// Report is the serialized snapshot of one run.
type Report struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Host        string             `json:"host"`
	RunID       int64              `json:"run_id"`
	BootID      string             `json:"boot_id"`
	NoBaseline  bool               `json:"no_baseline"`
	Collection  CollectionHealth   `json:"collection"`
	Host_       HostSummary        `json:"host_summary"`
	Disks       []collect.DiskInfo `json:"disks"`
	Containers  []ContainerSummary `json:"containers"`
	Findings    []Finding          `json:"findings"`
	Suppressed  []Finding          `json:"suppressed"`
	Alerts      []AlertSummary     `json:"alerts"`
}

// CollectionHealth is the per-source OK/UNKNOWN status for this run.
type CollectionHealth struct {
	Mem    string `json:"mem"`
	Vmstat string `json:"vmstat"`
	Load   string `json:"load"`
	Docker string `json:"docker"`
}

// HostSummary is the top-line host numbers.
type HostSummary struct {
	MemUsedPct     float64 `json:"mem_used_pct"`
	MemTotalBytes  int64   `json:"mem_total_bytes"`
	CommittedRatio float64 `json:"committed_ratio"`
	SwapPresent    bool    `json:"swap_present"`
	Load5          float64 `json:"load5"`
	NumCPU         int     `json:"num_cpu"`
}

// ContainerSummary is a redaction-safe view of one container.
type ContainerSummary struct {
	Name        string `json:"name"`
	Service     string `json:"service"`
	Image       string `json:"image"`
	State       string `json:"state"`
	MemoryLimit int64  `json:"memory_limit"`
	WorkingSet  int64  `json:"working_set,omitempty"`
	Restarts    int    `json:"restarts"`
}

// Finding is one observation, rendered for the report.
type Finding struct {
	Check     string `json:"check"`
	Scope     string `json:"scope"`
	Health    string `json:"health"`
	Tier      string `json:"tier,omitempty"`
	Title     string `json:"title"`
	Measured  string `json:"measured,omitempty"`
	Threshold string `json:"threshold,omitempty"`
	Fix       string `json:"fix,omitempty"`
	Reason    string `json:"suppressed_reason,omitempty"`
}

// AlertSummary is a delivered decision (target names only, never URLs).
type AlertSummary struct {
	Key     string   `json:"key"`
	Kind    string   `json:"kind"`
	Tier    string   `json:"tier"`
	Targets []string `json:"targets"`
}

// Build assembles a Report from a run's inputs. decisions is a slice of
// {Key, Kind, Tier, Targets} tuples supplied by the caller (kept as a light
// interface to avoid importing the alert package here).
func Build(host string, now time.Time, s *collect.Sample, obs []checks.Observation, alerts []AlertSummary, noBaseline bool) *Report {
	r := &Report{
		GeneratedAt: now,
		Host:        host,
		RunID:       s.RunID,
		BootID:      s.BootID,
		NoBaseline:  noBaseline,
		Collection: CollectionHealth{
			Mem:    s.Mem.Health.String(),
			Vmstat: s.Vmstat.Health.String(),
			Load:   s.Load.Health.String(),
			Docker: s.Docker.Health.String(),
		},
		Host_: HostSummary{
			MemUsedPct:     round1(s.Mem.UsedPct()),
			MemTotalBytes:  int64(s.Mem.MemTotalKB) * 1024,
			CommittedRatio: round2(s.Mem.CommittedRatio()),
			SwapPresent:    s.SwapPresent,
			Load5:          s.Load.Load5,
			NumCPU:         s.Load.NumCPU,
		},
		Disks:  s.Disks,
		Alerts: alerts,
	}
	for _, c := range s.Docker.Containers {
		r.Containers = append(r.Containers, ContainerSummary{
			Name:        c.Name,
			Service:     c.ServiceName(),
			Image:       c.Image,
			State:       c.State,
			MemoryLimit: c.MemoryLimit,
			WorkingSet:  c.Cgroup.WorkingSet,
			Restarts:    c.RestartCount,
		})
	}
	for _, o := range obs {
		f := findingFrom(o)
		if o.Suppressed {
			r.Suppressed = append(r.Suppressed, f)
		} else {
			r.Findings = append(r.Findings, f)
		}
	}
	return r
}

func findingFrom(o checks.Observation) Finding {
	f := Finding{
		Check: o.Check, Scope: o.Scope, Health: o.Health.String(),
		Title: o.Title, Measured: o.Measured, Threshold: o.Threshold, Fix: o.Fix,
		Reason: o.SuppressedReason,
	}
	if o.Health == health.BAD {
		f.Tier = o.Tier.String()
	}
	return f
}

// webhookRe matches Slack incoming-webhook URLs, redacted defensively even though
// the report is constructed without them.
var webhookRe = regexp.MustCompile(`https://hooks\.slack\.com/services/[A-Za-z0-9/_-]+`)

// Write atomically writes the report to path (0600), applying a defensive
// redaction pass so a secret can never leak into the audit trail.
func Write(path string, r *Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	data = webhookRe.ReplaceAll(data, []byte("[REDACTED]"))

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }
func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
