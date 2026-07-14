package alert

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Heartbeat is the dead-man's-switch (docs/plan/05-alerting.md §5.5). An external
// service expects a regular ping and alerts when the pings stop — so a crashed
// monitor is not indistinguishable from "everything healthy". All pings are
// best-effort: a flaky network must never fail the run.
type Heartbeat struct {
	url   string
	hc    *http.Client
	sleep func(time.Duration)
}

// NewHeartbeat builds a heartbeat for url. An empty url disables it (all methods
// become no-ops).
func NewHeartbeat(url string, sleep func(time.Duration)) *Heartbeat {
	if sleep == nil {
		sleep = time.Sleep
	}
	return &Heartbeat{
		url:   strings.TrimRight(url, "/"),
		hc:    &http.Client{Timeout: 10 * time.Second},
		sleep: sleep,
	}
}

// Enabled reports whether a heartbeat URL is configured.
func (h *Heartbeat) Enabled() bool { return h != nil && h.url != "" }

// Start pings <url>/start at run begin.
func (h *Heartbeat) Start(ctx context.Context) { h.ping(ctx, "/start") }

// Success pings <url> on a successful run — even when problems were found (a
// healthy monitor doing its job).
func (h *Heartbeat) Success(ctx context.Context) { h.ping(ctx, "") }

// Fail pings <url>/fail only on a monitor-internal error.
func (h *Heartbeat) Fail(ctx context.Context) { h.ping(ctx, "/fail") }

// ping POSTs with up to 3 attempts so a transient blip doesn't read as down.
func (h *Heartbeat) ping(ctx context.Context, suffix string) {
	if !h.Enabled() {
		return
	}
	url := h.url + suffix
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return
		}
		resp, err := h.hc.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
		}
		if attempt < 2 {
			h.sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
	}
}
