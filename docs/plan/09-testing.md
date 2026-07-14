# 9. Testing strategy

← [Index](README.md)

The load-bearing logic (state machine, delta math, config validation, Slack payloads)
must be tested deterministically. The collectors must be tested against both fixtures
and — where available — real kernel/Docker surfaces. This doc defines how.

## 9.1 The one enabling design rule: inject time and I/O

Nothing in `internal/checks`, `internal/alert`, or `internal/state` may call
`time.Now()` or read files directly. Instead:

- A **`Clock` interface** (`Now() time.Time`) is threaded through the run. Production
  uses a real clock; tests use a fake clock they advance by hand. This makes flap
  damping, cooldowns, rate math, and the digest window **deterministic**.
- Collectors take their **roots as parameters** (`procRoot`, `cgroupRoot`, docker
  `http.Client`), defaulting to `/proc`, `/sys/fs/cgroup`, and the unix-socket client.
  Tests point them at a temp fixture tree or an `httptest` server.

Without this rule, the interesting behaviors (does a spike within cooldown stay silent?
does UNKNOWN freeze a firing key?) can't be tested without sleeping — so it's a hard
requirement, not a nicety.

## 9.2 Unit tests (the core)

Table-driven, fast, no I/O:

- **State machine** (`internal/alert`) — the highest-value suite. Cases:
  flap-damp holds until `flap_samples`; `pending` spike clears silently; first fire;
  WARN→ALERT→PAGE escalation (one alert, not resolve+refire); PAGE→ALERT de-escalation;
  cooldown suppresses re-notify then re-notifies after the window; auto-resolve only
  after `resolve_samples` consecutive **OK**; **UNKNOWN freezes** (no fire, no resolve,
  no `consecutive_ok` advance); boot-id reset skips rate; negative time delta (NTP step)
  is clamped; `notified_targets` union drives RESOLVED fan-out.
- **Config validation** (`internal/config`) — each named error fires
  (`warn<alert<page` violated, missing routing target, unknown check id, empty url, bad
  duration/byte-size), and a valid config round-trips with correct defaults.
- **Delta math** (`internal/checks`) — disk fill-rate → `days_to_full` (incl. negative
  rate clamped, first-run "warming up"); swap `pswpin` rate; `oom_kill` counter delta;
  `RestartCount` delta; `declared_overcommit` headroom = `max(floor, working_set)`;
  `committed_as` swap-aware guard.
- **Exceptions** (`internal/checks`) — matcher (name/image/label/service globs; AND of
  keys; Swarm service shorthand) and the transform: `mute` moves a breach to
  `suppressed` (does **not** drop it), `retier` rewrites tier, per-container `thresholds`
  re-decide health from `measured`, `exclude_from_budget` removes a container from
  `declared_overcommit`, and an expired rule is skipped **and** emits the expiry WARN.
- **Slack renderer** (`internal/alert`) — **golden-file** tests: a fixed `Alert` →
  exact JSON committed under `testdata/`, one golden per tier + RESOLVED. Catches
  accidental payload drift (colors, blocks, mention only-on-PAGE).

## 9.3 Collector tests (fixtures + live)

- **Fixture tests** (always run): a `testdata/proc/` tree (real `meminfo`/`vmstat`/
  `loadavg`/`swaps` captured from hosts, including a swap-less and an over-committed
  sample), a `testdata/cgroup/` tree (a `docker-<id>.scope` with `memory.current/max/
  stat/events.local`, plus a `memory.max` of literal `max`), and a **fake Docker
  daemon** via `httptest.Server` over a unix listener returning canned `/_ping`,
  `/containers/json`, and inspect JSON (incl. swarm-labelled containers, an exited-137
  container, an unbounded one).
- **Live tests** (build tag `//go:build live`, run opt-in): exercise the real
  collectors against this box's actual `/proc`, cgroup v2 tree, and Docker socket —
  asserting they parse without error and return sane ranges, not exact values. Gated so
  CI on a socket-less runner still passes. This environment (Go 1.25, live socket,
  cgroup v2) can run them directly.

## 9.4 Integration test (sequential runs)

Drive `run()` N times with an injected clock + injected samples against a temp
state/report dir and a **mock Slack server** (`httptest`), asserting the *sequence*:
e.g. run1 BAD→pending (no send), run2 BAD→pending, run3 BAD→**fire** (send #1), run4 BAD
within cooldown→silent, run5 OK→pending-resolve, run6 OK→**RESOLVED** (send #2); assert
`state.json` transitions and that exactly two POSTs hit the mock with the expected
payloads. One UNKNOWN-injection variant proves a socket blip mid-firing does **not**
resolve.

## 9.5 Static + shell + packaging checks (CI)

- `go vet`, `staticcheck`, `gofmt -l` (fail on diff).
- **`shellcheck`** on `install.sh`/`uninstall.sh`.
- A CI job that installs the unit into a container and runs
  **`systemd-analyze security dokploy-sentinel.service`**, asserting the exposure score
  stays under a threshold (guards the sandboxing in [07-deployment.md](07-deployment.md)).
- **GoReleaser `--snapshot`** dry-run to prove the release build + checksums work per
  arch before tagging.

## 9.6 `selftest` vs tests (they're different)

`dokploy-sentinel selftest` is a **runtime** command for the operator on a real host
(config valid? socket reachable? each Slack target returns 2xx to a test payload? state
dir writable?). The suites above are **build-time** developer confidence. The installer
runs `dokploy-sentinel check` (config validation) before arming; the operator runs
`selftest` after editing config.

## 9.7 Manual smoke test (before a release, with the live webhook)

On this box (or any Dokploy host): install, point `[targets.*]` at the throwaway Slack
webhook, `systemctl start` once, and confirm — a report.json appears, a test/first
alert lands in Slack with correct formatting, `selftest` passes, and a deliberately
broken config is rejected by `check` (doesn't arm). This is the one step fixtures can't
replace and is why we want a real webhook at build time.
