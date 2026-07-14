package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// SelftestResult is one runtime probe outcome for the operator.
type SelftestResult struct {
	Name   string
	OK     bool
	Detail string
}

// Selftest runs the on-host readiness probes (docs/plan/09-testing.md §9.6):
// config valid (already loaded), state dir writable, Docker reachable + version
// negotiated, and each Slack target returns 2xx to a test payload.
func Selftest(ctx context.Context, cfg *config.Config) []SelftestResult {
	var out []SelftestResult

	// State dir writable.
	out = append(out, checkWritable("state dir writable", cfg.StatePath))
	out = append(out, checkWritable("report dir writable", cfg.ReportPath))

	// Docker reachable + version negotiated.
	out = append(out, checkDocker(ctx, cfg))

	// Each Slack target returns 2xx to a test payload.
	names := make([]string, 0, len(cfg.Targets))
	for n := range cfg.Targets {
		names = append(names, n)
	}
	sort.Strings(names)
	host := hostLabel(cfg)
	for _, name := range names {
		out = append(out, checkTarget(ctx, name, cfg.Targets[name], host))
	}
	return out
}

func checkWritable(name, path string) SelftestResult {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SelftestResult{name, false, err.Error()}
	}
	f, err := os.CreateTemp(dir, ".selftest-*.tmp")
	if err != nil {
		return SelftestResult{name, false, err.Error()}
	}
	f.Close()
	os.Remove(f.Name())
	return SelftestResult{name, true, dir}
}

func checkDocker(ctx context.Context, cfg *config.Config) SelftestResult {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dc := collect.NewDockerClient(cfg.Docker.Socket, 5*time.Second)
	if hint, err := dc.Ping(dctx); err != nil {
		detail := err.Error()
		if hint != "" {
			detail += " — " + hint
		}
		return SelftestResult{"docker socket reachable", false, detail}
	}
	return SelftestResult{"docker socket reachable", true, "API v" + dc.APIVersion()}
}

func checkTarget(ctx context.Context, name string, t config.TargetConfig, host string) SelftestResult {
	label := "slack target " + name
	payload, _ := json.Marshal(map[string]string{
		"text": fmt.Sprintf(":white_check_mark: dokploy-sentinel selftest from %s — target %q reachable", host, name),
	})
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, t.URL, bytes.NewReader(payload))
	if err != nil {
		return SelftestResult{label, false, err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return SelftestResult{label, false, err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SelftestResult{label, false, fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return SelftestResult{label, true, "2xx"}
}
