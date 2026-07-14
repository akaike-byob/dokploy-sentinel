# 6. Config model (TOML)

← [Index](README.md)

Single annotated file at `/etc/dokploy-sentinel/config.toml` (`0600`, root). Every
check block has `enabled` + thresholds + optional per-check `tier`, `cooldown`,
`flap_samples`. **Slack-only in v1** — a target's `url` is a Slack incoming webhook.

```toml
# ---- identity & delivery ----
host_label    = ""                     # defaults to hostname if empty
report_path   = "/var/lib/dokploy-sentinel/report.json"
state_path    = "/var/lib/dokploy-sentinel/state.json"
heartbeat_url = ""                     # dead-man's-switch (healthchecks.io-style); empty = off

[docker]
socket = "/var/run/docker.sock"        # configurable for rootless / Podman
inspect_concurrency = 12               # bounded fan-out
collect_deadline    = "20s"            # total per-run collection budget

# ---- Slack targets (name -> incoming webhook URL) ----
[targets.team]
url = "https://hooks.slack.com/services/T../B../team"
# min_tier = "ALERT"                   # this channel never receives below ALERT
# mention  = ""                        # e.g. "<@U123>" — only added on PAGE

[targets.oncall]
url     = "https://hooks.slack.com/services/T../B../pager"
mention = "<@U123ONCALL>"

[targets.lownoise]
url = "https://hooks.slack.com/services/T../B../lownoise"

# ---- tier -> which targets (fan-out). RESOLVED follows wherever the alert fired. ----
[routing]
WARN  = ["lownoise"]
ALERT = ["team"]
PAGE  = ["team", "oncall"]

# ---- global alert-hygiene defaults (overridable per check) ----
[alerting]
flap_samples_warn  = 3
flap_samples_alert = 3
flap_samples_page  = 1                 # PAGE fires on first breach (time-to-page)
resolve_samples    = 2
cooldown_warn  = "24h"
cooldown_alert = "6h"
cooldown_page  = "2h"

# ---- checks (Phase 1 shown; further [checks.*] per 04-checks.md) ----
[checks.mem_pressure]
enabled = true
warn = 80; alert = 90; page = 95        # percent of RAM

[checks.committed_as]
enabled = true
warn  = 1.5                             # Committed_AS / MemTotal (healthy Linux sits >1.0)
alert = 2.0                             # the incident was 2.2
commit_vs_swap_ratio = 1.0             # also fire if Committed_AS > (MemTotal+SwapTotal)*this

[checks.declared_overcommit]
enabled = true
headroom_reserve_pct = 15               # never let sum(limits) consume >85% of RAM
headroom_floor       = "512m"           # min headroom per UNBOUNDED container

[checks.unbounded_mem]  { enabled = true }
[checks.disk_fill]      { enabled = true, paths = ["/", "/var/lib/docker"], warn = 80, alert = 90, days_to_full_alert = 7 }
[checks.disk_inodes]    { enabled = true, alert = 90 }
[checks.swap_thrash]    { enabled = true, pswpin_pages_per_sec = 1000, flap_samples = 3 }
[checks.crashloop]      { enabled = true, restarts = 5, window = "10m" }
```

Secrets may alternatively come from `EnvironmentFile=-/etc/dokploy-sentinel/env`
(`0600`, root) with `${VAR}` expansion, so webhook URLs need not live in the TOML.

## 6.1 Config validation

`dokploy-sentinel check` runs a **semantic** pass (beyond TOML parse) and the installer
runs it **before** `enable --now` — it refuses to arm a broken config. Validates:

- `warn < alert < page` ordering per check;
- every routing target exists in `[targets.*]`;
- no unknown check ids or keys;
- non-negative / non-empty thresholds; non-empty target `url`;
- valid `min_tier`, durations, and byte sizes (`512m`);
- every `[[exceptions]]` rule is well-formed (see [§6.3](#63-per-container-exceptions):
  non-empty `match`, non-empty `reason`, ≥1 action, known + per-container check ids,
  valid `expires` date).

Errors are **named and specific**, not a generic parse failure. At runtime on a
previously-good box, a newly-broken config runs **last-known-good + a loud ALERT**
rather than exiting into silence (a fat-fingered threshold must not permanently kill the
monitor — there's a heartbeat, but degrade-loudly beats fail-silent).

## 6.2 Sensible defaults across box sizes

Thresholds are **percentages and ratios**, so `mem_pressure 80/90/95`, `committed_as
1.5/2.0`, `disk_fill 80/90` behave correctly on a 2 GB VPS and a 128 GB server without
per-host tuning. The two that *don't* auto-scale and may need per-host thought:

- `swap_thrash pswpin_pages_per_sec` (absolute page/s — means different things on an HDD
  VPS vs an NVMe server; documented as tunable, see open questions in
  [08-roadmap-and-decisions.md](08-roadmap-and-decisions.md)).
- `crashloop restarts/window` (a count, box-size-independent, but workload-dependent).

The shipped default config is tuned for a small/medium single-host box (the common
Dokploy case) and every value is one edit away.

## 6.3 Per-container exceptions

Some containers legitimately break a rule: a BuildKit agent is *supposed* to be
unbounded, a batch job is *supposed* to restart, a ClickHouse box is memory-hungry by
design. Without a scalpel, the first false page gets the whole check disabled globally —
so the tool needs to silence or re-tune a check **for specific containers** while leaving
it armed for everything else.

```toml
# ---- per-container exceptions ----
# Adjust or silence per-container checks for specific containers. Rules are evaluated
# top-to-bottom and ALL matching rules apply (a later rule wins on direct conflict).
# Host-level checks (mem_pressure, committed_as, disk_*, swap_thrash, load_high,
# boot_storm) have no container to match and are NOT exceptable here.

[[exceptions]]
reason = "ephemeral BuildKit agent — unbounded + restart churn are expected"
match  = { name = "buildkit-*" }            # glob on container name
mute   = ["unbounded_mem", "crashloop"]     # silence these checks for matches

[[exceptions]]
reason = "ClickHouse OLAP store is memory-hungry by design"
match  = { service = "clickhouse" }         # compose/swarm service-name shorthand
exclude_from_budget = true                  # drop from declared_overcommit's sum
  [exceptions.retier]
  container_health = "WARN"                 # downgrade this container's health alerts
  [exceptions.thresholds.crashloop]
  restarts = 20                             # override the global 5 for this container

[[exceptions]]
reason = "legacy worker not yet fixed — mute all, but time-box it"
match   = { label = "com.acme.tier=legacy" }  # key=value; value may glob
mute    = ["*"]                               # every exceptable check for this container
expires = "2026-09-01"                        # after this the rule is ignored + a WARN fires
```

**Match keys** (all provided keys must match — logical AND):

- `name` — glob on container name.
- `image` — glob on the image reference.
- `service` — shorthand for the compose/swarm service label
  (`com.docker.swarm.service.name`, else `com.docker.compose.service`). Preferred over
  `name` in Swarm, where task containers get fresh names on every reschedule.
- `label` — `"key=value"`; the value may glob.

**Actions** (at least one required per rule):

- `mute = ["check_id", …]` or `["*"]` — suppress those checks for matched containers.
- `[exceptions.retier]` — `check_id = "WARN"|"ALERT"|"PAGE"`, rewrite the tier for this
  container (usually a downgrade).
- `[exceptions.thresholds.<check_id>]` — override that check's thresholds for this
  container only (evaluated against the observation's measured value).
- `exclude_from_budget = true` — the special aggregate case: drop this container from
  `declared_overcommit`'s limit sum **and** from `unbounded_mem`, so a deliberately
  unbounded helper doesn't count against the host budget.

**Two invariants keep exceptions honest:**

1. **A mute relabels, it never hides.** A muted breach is still written to `report.json`
   under a `suppressed` list (with the matching rule's `reason`), and the daily digest
   (Phase 2) lists every active exception. Silencing a page must never make the host
   *look* green — that would recreate the false-all-clear failure this tool exists to
   prevent. Mute stops the Slack notification; it does not stop the observation.
2. **Exceptions can't rot silently.** `expires` (optional, `YYYY-MM-DD`) time-boxes a
   rule; past its date the rule is skipped and a one-line WARN (`exception "…" expired`)
   fires, so a "temporary" mute can't quietly become a permanent blind spot.

**Where it runs:** exceptions are applied **after checks emit observations, before the
state machine** ([02-architecture.md](02-architecture.md) §2.1 step 6b) — observations
already carry `measured` + `threshold`, so mute/retier/rethreshold are a uniform
post-check transform. `exclude_from_budget` is the one intent consulted *inside* the
aggregating checks (they already walk containers).

Validation ([§6.1](#61-config-validation)) rejects: an empty `match`, a missing/empty
`reason`, a rule with no action, an unknown check id, a **host-scoped** check named in
any action (nothing to match per-container), and a malformed `expires` date.
