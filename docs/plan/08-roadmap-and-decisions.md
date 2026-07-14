# 8. Roadmap, decisions & open questions

← [Index](README.md)

## 8.1 Phased roadmap

Ordered by "would have caught the incident earliest, cheapest first." Heartbeat +
config-validation are core reliability and are **in Phase 1**; the digest scheduler is
deferred to Phase 2.

- **Phase 1 (MVP):** collection framework (bounded fan-out, deadlines, tri-state health)
  + config + **config validation** + state machine + **Slack delivery** + report.json +
  **heartbeat** + installer + systemd. Checks: `mem_pressure`, `committed_as`,
  `declared_overcommit`, `unbounded_mem`, `disk_fill` (incl. rate), `disk_inodes`,
  `swap_thrash`, `crashloop`. Cooldown + auto-resolve (OK-gated). Default timer 60 s;
  PAGE checks `flap_samples=1`. **Covers 4 of the 5 incident contributors.**
- **Phase 2:** `oom_kill` (cgroup `memory.events` + exit-137), `boot_storm`, **daily
  digest** for chronic-but-known issues (e.g. `unbounded_mem`, `hygiene`), `docker
  events` audit ledger.
- **Phase 3:** `cpu_throttle`, `container_health`, `load_high`, `single_replica`,
  `no_restart_policy`, `no_healthcheck`, `hygiene`; richer digest.
- **Phase 4 (optional):** on-disk trend history (small time series), a local status JSON
  endpoint a separate uptime monitor can scrape, `.deb` packaging, **Discord + generic
  webhook renderers** (new `Renderer` implementations behind the existing interface).

**Out of scope for v1 (explicit):** auto-remediation (restart/kill). Observe and warn
until the signal is trusted.

## 8.2 Decisions taken

- **Repo home = `github.com/akaiketech/dokploy-sentinel`** — module path, installer
  URL, and GitHub Releases location.
- **Go / TOML / GitHub Releases + `install.sh` (+minisign) / systemd oneshot+timer /
  root default.**
- **`committed_as` defaults `warn 1.5 / alert 2.0` + a swap-aware guard**
  (`Committed_AS > (MemTotal+SwapTotal)`) — healthy Linux overcommits above `1.0`, so a
  `1.2` default would be noisy; the incident was `2.2`.
- **`swap_thrash` stays an absolute `pswpin_pages_per_sec`** for v1 (documented as
  per-host tunable); a fraction-of-RAM formulation is a possible later refinement.
- **Multi-host disambiguation via `host_label` in every payload** for v1; per-host
  routing deferred.
- **Per-container exceptions** ([06-config.md](06-config.md) §6.3) — match by
  name/image/label/service; `mute` / `retier` / per-container `thresholds` /
  `exclude_from_budget`. Only per-container checks are exceptable. A mute **relabels,
  never hides** (breach still in `report.json` `suppressed`), and optional `expires`
  time-boxes a rule so mutes can't rot into blind spots. Applied post-check, pre-state
  (step 6b).
- **Slack test delivery:** a throwaway webhook will be provided at build time for the
  live smoke test ([09-testing.md](09-testing.md) §9.7); until then, `--dry-run` +
  golden-file payload tests.
- **Slack-only delivery in v1**, behind a `Renderer` interface (Discord/generic = Phase 4).
- **`mem_overcommit` split** into `committed_as` (latent, host-only) +
  `declared_overcommit`; **unbounded headroom = `max(floor 512m, working-set)`** so idle
  unbounded containers still count.
- **Collection tri-state (OK/BAD/UNKNOWN); resolve only on OK** — a false all-clear is
  structurally impossible.
- **Default interval 60 s; PAGE `flap_samples=1`** — single timer, single state file.
  ~60 s to first page accepted as the timer-based floor; Phase 4 may add an optional
  fast sub-timer if 60 s proves too slow in practice.
- **Docker socket configurable; explicit UNKNOWN-freeze failure semantics.**
- **Heartbeat + config-validation in Phase 1.**
- **cgroup resolved via `/proc/<pid>/cgroup`**, construct-by-id only as fallback.
- **Secrets `0600`; never written to report.json/logs;** redaction enforced in the report
  writer.
- **cgroup v2 only fully-supported** (v1 detected + degraded, not fully parsed).

## 8.3 Open questions (small, non-blocking)

- Whether `declared_overcommit` should also PAGE (not just ALERT) past a hard multiple
  (e.g. declared > 2× usable RAM), or stay ALERT-only. Leaning ALERT-only for v1.
- Whether the daily digest (Phase 2) needs its own `OnCalendar` timer or the state-file
  gate is sufficient. Leaning state-file gate (simpler, survives missed runs).

*(Resolved and moved to §8.2: `committed_as` default, `swap_thrash` scaling, multi-host
UX, repo home, Slack test delivery. Testing strategy is now
[09-testing.md](09-testing.md).)*

## 8.4 Review log (design reviews incorporated)

**v0.1 → v0.2** — a skeptical design review raised 4 P0 / 10 P1 / 9 P2 findings:
- **P0-1 false all-clear from missing data** → tri-state OK/BAD/UNKNOWN; resolve only on
  consecutive OK; UNKNOWN freezes.
- **P0-2 headline check undefined + mis-modeled** → split into `committed_as` +
  `declared_overcommit`; headroom `max(floor, working-set)`.
- **P0-3 no Docker-socket failure behavior** → configurable path + UNKNOWN-freeze on
  missing/EACCES/refused/timeout; per-call + per-run deadlines.
- **P0-4 6-min time-to-page** → PAGE `flap_samples=1` + 60 s default interval; single
  timer retained.
- **P1** notified-targets in state; per-check dedup scope; heartbeat → Phase 1; config
  validation + installer gate + last-known-good; bounded inspect fan-out + deadline;
  cgroup resolved via `/proc/pid/cgroup`; `0600` + report redaction; minisign +
  first-class manual install path; supported-matrix + configurable socket.
- **P2** hung-run deadline; NTP clamp; first-run "warming up" surfaced; de-escalation
  path; state pruning; cgroup-vs-API locked-decision refinement noted; `selftest` defined.

**v0.2 → v0.3** — per follow-up direction:
- **Split** the monolithic plan into this `docs/plan/` directory.
- **Simplified to Slack-only** delivery (Discord/generic moved to Phase 4, behind a
  `Renderer` interface).
- **Expanded the check catalog** ([04-checks.md](04-checks.md)) so every check documents
  what it detects, how, and **why it adds value** (tied to the incident contributors).

**v0.3 → v0.4** — locked open defaults + testing:
- **Repo home** set to `github.com/akaiketech/dokploy-sentinel` (module path, installer
  URL, releases baked in).
- **`committed_as` default corrected** from a noisy `1.2` to `warn 1.5 / alert 2.0` +
  swap-aware guard (healthy Linux overcommits above 1.0).
- **`swap_thrash` scaling, multi-host UX** locked (§8.2).
- **Added [09-testing.md](09-testing.md)** — deterministic-core rule (inject clock +
  I/O roots), unit/fixture/live/integration layers, golden-file Slack payloads, CI
  (shellcheck, `systemd-analyze security`, GoReleaser snapshot), `selftest` vs tests,
  and the live-webhook smoke test.

**Locked-decision refinement note:** problem-statement §6 locks "data source = Docker
API directly." This plan reads cgroup files directly for per-container memory (much
cheaper) while still using the Docker API for listing/inspect/stats. This is an
intentional performance refinement, not a reversal — recorded for traceability.
