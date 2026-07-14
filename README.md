# dokploy-sentinel

A lightweight, host-level watchdog for **Dokploy / Docker** hosts that warns you
**before** memory over-subscription (and disk, swap, or crash-loop trouble) takes the
box down — not after.

It runs on a schedule, samples the host and every container, and raises a tiered alert
the moment something drifts toward danger:

1. **Is any container running unbounded?** (no `mem_limit` set — it can eat the whole box)
2. **Is the host over-provisioned?** (sum of limits, or live usage, exceeds RAM)
3. **Is the disk filling, is swap thrashing, is a container crash-looping?**

Status: **Phase 1 implemented** — Go, no runtime dependencies, unit + integration tested,
runs on any systemd + cgroup v2 + Docker host.

---

## Why this exists

Shared Docker / Dokploy hosts fail in a predictable, quiet way. Teams deploy stacks
without memory limits, so every container is entitled to the entire machine. The sum of
what's *promised* creeps past physical RAM long before live usage looks alarming — the
host is over-committed and **nothing says so**. Then a leak, a traffic spike, or a reboot
that cold-starts everything at once tips it over: the OOM-killer fires at random, swap
thrashes, latency collapses, and apps go dark. The disk quietly filling (image layers,
build cache, uncapped logs, fat volumes) is often what lights the fuse — and rotates away
the evidence.

The dangerous part is that the risk is **latent and invisible**: you have to manually sum
`docker inspect` limits and compare to RAM to even see it. `dokploy-sentinel` is the
missing smoke detector — it makes that latent over-commit **visible and loud while there's
still time to act**, and it keeps a local audit trail so the next incident isn't
investigated blind.

---

## What it does

- Enumerates every container and reads its memory limit (`HostConfig.Memory`; `0` = unbounded).
- Sums declared limits, counts unbounded containers, and samples live usage per container
  from cgroup v2, plus host memory / swap / disk / load.
- Compares against physical RAM and configurable thresholds using a **tiered policy**.
- Fires an off-box **webhook alert** (so it still lands when the host itself is degraded)
  and writes a local JSON report as an audit trail.
- Runs as a **host-level systemd timer** — deliberately *not* a container, so the very
  condition it watches for can't OOM-kill the watchdog itself. It's resource-capped and
  sandboxed by the unit so it can never become the problem it detects.

### Checks (Phase 1)

| Check | Detects | Tier |
|-------|---------|------|
| `mem_pressure` | live RAM usage crossing a % of physical RAM | WARN → PAGE |
| `committed_as` | the kernel has promised far more memory than exists (`Committed_AS`) | WARN → ALERT |
| `declared_overcommit` | sum of declared limits + headroom exceeds usable RAM | ALERT |
| `unbounded_mem` | a container has no `mem_limit` | WARN |
| `disk_fill` | a filesystem is past a threshold **or** its fill-rate projects full in N days | WARN → ALERT |
| `disk_inodes` | inode exhaustion (free bytes, but writes still fail) | ALERT |
| `swap_thrash` | sustained page-in from swap (latency collapse) | PAGE |
| `crashloop` | a container / service is restart-looping | ALERT |

Every check is independently enable/disable and threshold-configurable. More checks
(`oom_kill`, `boot_storm`, `cpu_throttle`, health, SPOF, hygiene, a daily digest) are on
the roadmap.

### Tiers

| Tier | Meaning | Routing intent |
|------|---------|----------------|
| **WARN** | Fix soon — latent risk | Low-noise channel |
| **ALERT** | Fix now — actively wrong | Team channel |
| **PAGE** | Human immediately — outage imminent | Phone-notifying channel (with mention) |

### Signal, not spam

Alerts are state-change driven, not fired every tick: tiered severity, flap damping,
per-tier cooldowns, escalation/de-escalation, and auto-resolve. A key invariant —
**"couldn't measure" is never treated as "healthy"** — so a transient Docker or cgroup
read failure can never turn a live problem into a false all-clear.

### Delivery

- **Slack incoming webhook** in v1 (one URL per channel; tier → channel routing).
- The internal alert object is provider-neutral behind a renderer interface, so Discord /
  generic webhooks are a small later addition.
- Off-box by design, so an alert still arrives when the host itself is thrashing.
- An optional **dead-man's-switch heartbeat** (healthchecks.io / Uptime Kuma style) means
  a crashed monitor doesn't look like a healthy host.

---

## Quickstart

Requirements on the target host: **systemd**, **cgroup v2**, and a reachable **Docker
socket** (path is configurable, so rootless Docker / Podman work). This covers current
Dokploy hosts (Ubuntu 22.04+ defaults).

```bash
# one-command install (installs a static binary + a systemd timer, every 60s)
curl -fsSL https://raw.githubusercontent.com/akaike-byob/dokploy-sentinel/main/packaging/install.sh | sh

# then edit the config, point a target at your Slack webhook, and verify:
sudo $EDITOR /etc/dokploy-sentinel/config.toml
dokploy-sentinel check    --config /etc/dokploy-sentinel/config.toml   # validate the config
dokploy-sentinel selftest --config /etc/dokploy-sentinel/config.toml   # docker reachable? targets 2xx?
```

Prefer to build it yourself:

```bash
CGO_ENABLED=0 go build -o dokploy-sentinel ./cmd/dokploy-sentinel

./dokploy-sentinel check   --config packaging/config.example.toml           # validate a config
./dokploy-sentinel run     --config /etc/dokploy-sentinel/config.toml --dry-run  # evaluate + print, send nothing
./dokploy-sentinel run     --config /etc/dokploy-sentinel/config.toml       # the real run (a systemd timer calls this)
```

## Configuration

Everything — every check's enable/disable and thresholds, the tiers, cooldowns, routing,
and per-container exceptions — lives in one TOML file. No recompile to tune. Thresholds
are percentages and ratios, so the defaults behave sensibly on a 2 GB VPS and a 128 GB
server alike.

See [`packaging/config.example.toml`](packaging/config.example.toml) for a fully-commented
reference. Webhook URLs can be kept out of the file and injected via `${VAR}` from a
systemd `EnvironmentFile`, so no secret needs to live in the config.

Per-container **exceptions** let you silence or re-tune a check for specific containers
(a BuildKit agent is *meant* to be unbounded; a batch job is *meant* to restart) without
disabling the check globally. A mute relabels, it never hides — a muted breach still
appears in the report — and rules can be time-boxed with an `expires` date so a
"temporary" mute can't quietly become a permanent blind spot.

## Non-goals

- **Not an uptime / HTTP monitor** — that's a separate concern (Better Stack, Uptime Kuma).
- **Not an auto-remediator** — v1 only *observes and warns*; it never restarts or kills
  containers. Remediation stays a human decision until the signal is trusted.
- **Not Dokploy-specific** — it reads the Docker API and host files directly, so it works
  on any Docker host, Dokploy or not.

## Development

```bash
go build ./...
go test ./...                              # unit + integration (fast, no I/O, no network)
go test -tags live ./internal/collect/     # opt-in: exercise the real /proc + cgroup + docker
gofmt -l . && go vet ./...
```

The design is documented under [`docs/plan/`](docs/plan/README.md) (tech stack,
architecture, check catalog, alerting state machine, config model, deployment, testing),
and [`CLAUDE.md`](CLAUDE.md) is a code map for contributors.

## Layout

```
dokploy-sentinel/
  cmd/dokploy-sentinel/      CLI: run | check | selftest | version
  internal/                  health · clock · config · collect · state · checks · alert · report · app
  packaging/                 install.sh · uninstall.sh · systemd units · config.example.toml
  docs/plan/                 the design (architecture, checks, alerting, config, deployment, testing)
  .github/workflows/         CI (build · vet · test · shellcheck · goreleaser) + release
```

## License

TBD.
