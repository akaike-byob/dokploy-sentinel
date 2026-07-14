package alert

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/config"
)

// spacing is the minimum gap between sends so a burst stays well under Slack's
// ~1 msg/sec/channel limit.
const spacing = 250 * time.Millisecond

// Sender delivers rendered decisions to Slack targets. It is 429-aware and never
// blocks the run: on failure it records the error and moves on.
type Sender struct {
	hc       *http.Client
	renderer Renderer
	sleep    func(time.Duration)
}

// NewSender builds a Sender. If hc is nil a default 10s-timeout client is used;
// if sleep is nil time.Sleep is used (tests inject a no-op).
func NewSender(hc *http.Client, r Renderer, sleep func(time.Duration)) *Sender {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	return &Sender{hc: hc, renderer: r, sleep: sleep}
}

// SendResult records the outcome of one POST (one target for one decision).
type SendResult struct {
	Target     string
	Key        string
	Kind       Kind
	StatusCode int
	Err        error
}

// Deliver renders and posts every decision to its routed targets, spacing sends.
func (s *Sender) Deliver(ctx context.Context, cfg *config.Config, decisions []Decision) []SendResult {
	var results []SendResult
	first := true
	for _, d := range decisions {
		for _, name := range d.Targets {
			t, ok := cfg.Targets[name]
			if !ok || t.URL == "" {
				continue
			}
			if !first {
				s.sleep(spacing)
			}
			first = false

			res := SendResult{Target: name, Key: d.Alert.Key, Kind: d.Alert.Kind}
			body, err := s.renderer.Render(d.Alert, mentionFor(cfg, name, d.Alert))
			if err != nil {
				res.Err = fmt.Errorf("render: %w", err)
				results = append(results, res)
				continue
			}
			code, err := s.post(ctx, t.URL, body)
			res.StatusCode = code
			res.Err = err
			results = append(results, res)
		}
	}
	return results
}

// post sends one payload, honoring a single 429 Retry-After retry.
func (s *Sender) post(ctx context.Context, url string, body []byte) (int, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", s.renderer.ContentType())
		resp, err := s.hc.Do(req)
		if err != nil {
			return 0, err
		}
		code := resp.StatusCode
		retryAfter := resp.Header.Get("Retry-After")
		resp.Body.Close()

		if code == http.StatusTooManyRequests && attempt == 0 {
			s.sleep(parseRetryAfter(retryAfter))
			continue
		}
		if code < 200 || code >= 300 {
			return code, fmt.Errorf("slack POST returned %d", code)
		}
		return code, nil
	}
	return http.StatusTooManyRequests, fmt.Errorf("slack POST rate-limited after retry")
}

// parseRetryAfter reads a Retry-After seconds value, defaulting to 1s.
func parseRetryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		if n > 30 {
			n = 30 // never block the run for long
		}
		return time.Duration(n) * time.Second
	}
	return time.Second
}
