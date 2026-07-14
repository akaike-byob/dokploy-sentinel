# CLAUDE.md — dokploy-sentinel

Host-level watchdog for Dokploy / Docker VMs. A short-lived Go process on a systemd
timer that samples `/proc` + cgroup v2 + the Docker socket, evaluates a catalog of
checks, and fires tiered Slack alerts **before** memory over-subscription (or disk /
swap / crash-loop trouble) takes the box down. Observe-only in v1 — it never
restarts or kills anything.

Design lives in `docs/plan/` (read `docs/plan/README.md` first). The incident that
motivated it is `docs/problem-statement.md`.

## Build / test / run

```bash
go build ./...                       # build everything
go test ./...                        # unit + integration (fast, no I/O)
go test -tags live ./internal/collect/   # live collectors vs this box's /proc + docker
gofmt -l . && go vet ./...           # must be clean (CI enforces)

# run against a real host (dry-run: evaluate + print, never send / mutate state)
go run ./cmd/dokploy-sentinel run --config /path/to/config.toml --dry-run
go run ./cmd/dokploy-sentinel check    --config /path/to/config.toml   # validate config
go run ./cmd/dokploy-sentinel selftest --config /path/to/config.toml   # on-host probes
```

Module path: `github.com/akaike-byob/dokploy-sentinel` (the real repo; the installer
and `go install` resolve from here). It is the import prefix for every internal
package — changing repos means one `sed` over the tree + the `go.mod` module line.

## Architecture (dependency order)

- `internal/health` — the `OK | BAD | UNKNOWN` tri-state. The core invariant:
  **"couldn't measure" is NOT "healthy."** Collectors emit OK/UNKNOWN; observations
  add BAD.
- `internal/clock` — injectable time source (`Clock`), so flap/cooldown/rate math is
  deterministic in tests. **Nothing in checks/alert/state may call `time.Now()`.**
- `internal/config` — TOML model + defaults + semantic validation (named errors).
  `Tier`, `Duration`, `ByteSize` value types live here.
- `internal/collect` — reads all raw signals into one `Sample` (procfs, statvfs disk,
  HTTP-over-unix Docker client with version negotiation + bounded inspect fan-out
  under a deadline, cgroup v2 resolved via `/proc/<pid>/cgroup`). Roots are injectable
  (`ProcRoot`, `CgroupRoot`, socket path) for fixture tests.
- `internal/state` — `state.json` model + atomic write (tmp+fsync+rename); per-key
  alert memory, disk fill-rate rings, per-service delta history, boot-id reset detect.
- `internal/checks` — the `Check` interface + Phase 1 catalog (one file per check) +
  the exceptions engine (`except.go`). Checks are **pure**: read `Sample` + prev
  `Snapshot`, emit `Observation`s. `baseline.go` advances delta baselines after eval.
- `internal/alert` — neutral `Alert` → `Renderer` (Slack Block Kit only in v1) +
  the signal-not-spam state machine (`statemachine.go`) + routing + 429-aware sender
  + dead-man's-switch heartbeat.
- `internal/report` — `report.json` writer with **enforced secret redaction**.
- `internal/app` — `Runner.RunOnce` wires the whole lifecycle; injectable collector /
  sender / clock so the CLI and the integration test drive the same path.
- `cmd/dokploy-sentinel` — CLI (`run` / `check` / `selftest` / `version`).

## Non-obvious rules (don't regress these)

- **UNKNOWN freezes a key** — it never fires, resolves, or advances `consecutive_ok`.
  Auto-resolve requires `resolve_samples` consecutive **OK** (never "absent"). This
  makes a false all-clear structurally impossible; it's the whole point of the tool.
- **PAGE fires on first breach** (`flap_samples_page = 1`); WARN/ALERT flap-damp.
- **Per-container state keys on the service label**, not the container id, so swarm
  reschedules (fresh ids) don't lose history.
- **Exceptions**: `mute` relabels, never hides — a muted breach still lands in
  `report.json` under `suppressed`. Only per-container checks are exceptable;
  `exclude_from_budget` is consulted *inside* the aggregating checks.
- **Slack golden files** are in `internal/alert/testdata/`. Regenerate intentionally
  with `UPDATE_GOLDEN=1 go test ./internal/alert/`.
- **systemd units**: comments must be on their own line — systemd has no inline `#`.
  Do NOT set `ProtectControlGroups=true`, `ProcSubset=pid`, or `PrivateNetwork` (it
  needs `/proc` + cgroup + `docker.sock` + Slack).

## Scope

Phase 1 checks implemented: `mem_pressure`, `committed_as`, `declared_overcommit`,
`unbounded_mem`, `disk_fill` (level + rate), `disk_inodes`, `swap_thrash`, `crashloop`.
Phase 2+ (`oom_kill`, `boot_storm`, digest, `cpu_throttle`, `container_health`, …) are
documented in `docs/plan/` but not yet built. Delivery is **Slack-only** in v1 behind
a `Renderer` interface (Discord/generic = a later renderer, no other change).
