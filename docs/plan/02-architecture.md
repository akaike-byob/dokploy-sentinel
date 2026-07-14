# 2. Architecture

← [Index](README.md)

## 2.1 Run lifecycle (one timer firing)

```
timer fires → dokploy-sentinel run --config /etc/dokploy-sentinel/config.toml
  1. Load + VALIDATE config (see 06-config.md §validation). On parse/semantic error:
       - if a last-known-good snapshot exists → run with it + emit a loud ALERT
         "config invalid, using last-good" to all ALERT targets;
       - else exit non-zero → heartbeat /fail. Never silently exit into permanence.
  2. Heartbeat: POST <heartbeat_url>/start   (best-effort, -m 10, ignore failure).
  3. Capture ONE run timestamp + boot_id (/proc/sys/kernel/random/boot_id).
  4. Read previous state.json (missing/corrupt/first-run → "no baseline": no rates,
     mark rate-checks "warming up", still evaluate level checks).
  5. COLLECT, per source, each recording a collection-health status (OK | UNKNOWN):
       - host:   /proc/meminfo, /proc/vmstat, /proc/loadavg, /proc/swaps, statvfs(paths)
       - docker: /_ping → version negotiate; /containers/json?all=true; inspect
                 running+recent-exited via a BOUNDED worker pool (default 12) under a
                 total collection deadline (default 20s).
       - cgroup: resolve each container's cgroup from /proc/<State.Pid>/cgroup, read
                 memory.current/max/stat/events(.local), cpu.stat.
       - expensive (only if a cheap signal already flagged): /containers/{id}/stats,
                 /system/df.
     Any source that errors/times out → its checks get UNKNOWN, not absence.
  6. Evaluate enabled checks → Observations {key, health: OK|BAD|UNKNOWN, tier,
     measured, threshold, fix}.
  6b. Apply per-container exceptions (06-config.md §6.3): for observations whose
     container matches a rule, mute / retier / re-threshold. Muted observations are
     moved to report.json's `suppressed` list (NOT dropped) and skip the state machine.
     `exclude_from_budget` is honored inside the aggregating checks in step 6.
  7. Alert state machine (05-alerting.md): flap-damp → fire → escalate/de-escalate →
     cooldown-remind → resolve-only-on-OK. UNKNOWN freezes a key.
  8. Route notifications by tier to Slack targets; render; POST (bounded, 429-aware).
  9. Write report.json (full snapshot, secrets redacted) and state.json (atomic).
 10. Heartbeat: POST <heartbeat_url> on success (EVEN IF problems were FOUND — a
     healthy monitor); POST <heartbeat_url>/fail only on monitor-internal error.
```

Rates are always computed from measured `now − prev` timestamps, never the nominal
interval (systemd timers drift/coalesce). A hung Docker call cannot stall the process:
the per-run deadline (step 5) bounds total wall time well under the timer interval, so
`Type=oneshot` overlap (systemd won't start a second instance while one is active)
cannot silently stop the monitor.

## 2.2 Component / package layout

```
dokploy-sentinel/
  cmd/dokploy-sentinel/main.go      # CLI: run | check (dry-run, no send) | selftest | version
  internal/
    config/        # TOML load + semantic validation + defaults
    collect/       # procfs.go disk.go docker.go cgroup.go — each returns (data, health)
    checks/        # one file per check; Evaluate(sample, prev) -> []Observation
                   #   except.go: match containers + apply exceptions (06-config §6.3)
    state/         # state.json model, atomic rw, pruning, boot-id reset detection
    alert/
      statemachine.go   # flap-damp / fire / escalate / cooldown / resolve
      render_slack.go   # neutral Alert -> Slack payload (only renderer in v1)
      renderer.go       # Renderer interface (Discord/generic slot in here later)
      route.go          # tier -> targets fan-out
      heartbeat.go      # dead-man's-switch pings
    report/        # report.json writer (redaction enforced here)
  packaging/       # install.sh uninstall.sh systemd/*.{service,timer} config.example.toml
  .goreleaser.yaml .github/workflows/release.yml
```

## 2.3 The check interface (extension point)

Every check implements:

```go
type Check interface {
    ID() string
    Evaluate(sample *Sample, prev *state.Snapshot) []Observation
}
```

- **Collection is centralized and shared** — the `Sample` holds already-collected raw
  data, so ten checks don't re-read `/proc/meminfo` ten times. Checks never do I/O.
- Adding a signal from the catalog = one new file implementing `Check` + a config
  block. This is how the phased roadmap stays cheap.
- Each `Observation` carries `health ∈ {OK, BAD, UNKNOWN}` — never mere
  presence/absence (this is the false-all-clear fix; see [05-alerting.md](05-alerting.md)).

`selftest` asserts: config valid, Docker socket reachable + version negotiated, each
configured Slack target returns 2xx to a test payload, state dir writable.
