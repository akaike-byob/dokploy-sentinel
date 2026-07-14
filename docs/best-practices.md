# Operational Best Practices & Signal Catalog

This doc widens `dokploy-sentinel` beyond "is RAM over-committed" into the full set
of signals that would have caught the 2026-07-09 incident **earlier**, plus the
preventive guardrails that stop the class of problem from recurring.

The incident had **five** latent contributors, only one of which was memory:

1. Disk silently filled → journald rotated away the evidence.
2. Memory 2.2x over-committed, no per-container limits, no swap.
3. A reboot caused a **boot storm** (37 stacks cold-start at once).
4. A single-replica Dokploy = SPOF → 502 during its own restart.
5. **No early warning** and **no audit trail** — discovery was manual and late.

A memory-only watchdog catches #2. To catch the rest we need a broader signal
catalog and a set of guardrails. Everything below is organised as:

- **§1 Signal catalog** — what to measure, how to read it, when to alert.
- **§2 Preventive guardrails** — config policy that removes the failure mode.
- **§3 Monitor reliability** — making sure the watchdog itself is trustworthy.
- **§4 Alert hygiene** — signal, not spam.
- **§5 Phased roadmap** — what to build first.

---

## 1. Signal catalog

Each signal: **what it is**, **how to read it cheaply on the host**, and a
**suggested tier** (WARN = fix soon / ALERT = fix now / PAGE = human immediately).

### 1.1 Memory (core)

| Signal | How to detect | Tier |
|--------|---------------|------|
| Container has no `mem_limit` | `docker inspect -f '{{.HostConfig.Memory}}'` == 0 | WARN |
| Sum of limits + per-unbounded headroom > RAM | sum `HostConfig.Memory`, add live RSS for unbounded ones | ALERT |
| Live used memory > 85% of RAM | `/proc/meminfo` (MemTotal - MemAvailable) | PAGE |
| **OOM kill actually happened** | cgroup v2 `memory.events` `oom_kill` counter rising; `docker inspect` `State.OOMKilled=true`; exit code **137** | PAGE |
| Container near its **own** limit (>90% of its cgroup max) | `memory.current` / `memory.max` per cgroup | WARN |
| **Committed_AS** >> RAM (latent over-commit) | `/proc/meminfo` `Committed_AS` vs `MemTotal` | ALERT |

> The incident's 2.2x was visible in `Committed_AS` the whole time — nobody was
> looking. Make it a first-class signal.

### 1.2 Swap

| Signal | How to detect | Tier |
|--------|---------------|------|
| Swap absent on a memory-dense host | `/proc/swaps` empty | WARN |
| Swap usage sustained high | `/proc/meminfo` `SwapFree` vs `SwapTotal` | WARN |
| **Swap thrash** (paging storm, the real killer) | `vmstat` `si`/`so` sustained > 0; or `/proc/vmstat` `pswpin`/`pswpout` rate | PAGE |

> Swap *usage* being high is fine (cold pages). Swap *thrash* (high in+out rate)
> means the box is trading disk I/O for RAM and latency is collapsing — that is the
> PAGE-worthy signal, not raw swap %.

### 1.3 Disk & inodes (this is what actually started the incident)

| Signal | How to detect | Tier |
|--------|---------------|------|
| Filesystem > 80% / > 90% | `df -B1 /` (and `/var/lib/docker`, `/var/lib/containerd`) | WARN / ALERT |
| **Inode** exhaustion (disk shows free but writes fail) | `df -i` | ALERT |
| Docker image store / build cache bloat | `du -sh /var/lib/containerd`, `docker system df` | WARN |
| A single volume growing fast | per-dir `du` under `/var/lib/docker/volumes`, compare to last run | WARN |
| Container log file oversized (pre-cap containers) | `du` on `*-json.log` | WARN |
| **Growth rate** (will fill in < N days at current slope) | compare `df` to previous sample, extrapolate | ALERT |

> Point-in-time "90% full" is a lagging signal. **Rate of fill** ("+8 GB/day, full
> in 3 days") is the one that gives you time to act. Track deltas between runs.

### 1.4 CPU & load

| Signal | How to detect | Tier |
|--------|---------------|------|
| Load average > N x cores, sustained | `/proc/loadavg` vs `nproc` | WARN/ALERT |
| **CPU throttling** (cgroup cpu limit too tight) | cgroup `cpu.stat` `nr_throttled` / `throttled_usec` rising | WARN |
| No CPU limit on any container | `docker inspect` `NanoCpus`/`CpuQuota` == 0 | WARN |
| **Boot/restart storm** | many containers with `StartedAt` within the same short window | ALERT |

### 1.5 Container lifecycle & health

| Signal | How to detect | Tier |
|--------|---------------|------|
| **Crash loop** (restart count climbing) | `docker inspect` `RestartCount` delta between runs | ALERT |
| Container stuck `restarting` | `docker ps` status | ALERT |
| Healthcheck `unhealthy` | `docker inspect` `State.Health.Status` | WARN/ALERT |
| Exit 137 (OOM) vs exit 1 (app error) — distinguish them | `State.ExitCode` | PAGE / WARN |
| No `restart:` policy on a long-running service | `HostConfig.RestartPolicy.Name` == "no" | WARN |
| No healthcheck defined | `Config.Healthcheck` empty | WARN |

> We had a known dev-Postgres **crash-loop** during the incident.
> `RestartCount` climbing is the cheapest possible detector for that and it was
> never watched.

### 1.6 Reliability / SPOF

| Signal | How to detect | Tier |
|--------|---------------|------|
| Critical service running as a **single replica** | Swarm service `Replicas 1/1` for tagged-critical stacks | WARN |
| Docker daemon **`live-restore` disabled** (restart bounces everything) | `docker info` `Live Restore Enabled: false` | WARN |
| Reverse proxy (Traefik) / Dokploy control-plane down | health endpoint / container state | PAGE |

### 1.7 Config & security hygiene

| Signal | How to detect | Tier |
|--------|---------------|------|
| `privileged: true` container | `HostConfig.Privileged` | WARN |
| Running as root where it needn't | `Config.User` empty | INFO |
| Image pinned to `:latest` (drift, non-reproducible) | image ref ends `:latest` / untagged | INFO |
| Host port bound to `0.0.0.0` unexpectedly | `docker ps` port map | WARN |
| No log cap on a container (pre-daemon-default) | `HostConfig.LogConfig` unset & daemon default absent | WARN |

---

## 2. Preventive guardrails (remove the failure mode, don't just alert on it)

Alerting is detection. These are **prevention** — cheaper than any alert.

1. **Per-stack resource limits by default.** Every stack declares `mem_limit` and a
   CPU share. Our `qcs` stack already does this
   (`mem_limit: ${..._MEM_LIMIT:-Ng}` per service) — it is the template other
   stacks should copy. A deploy with no limit should be treated as a bug.
2. **A reserved "system headroom" budget.** Never let the *sum* of limits consume
   100% of RAM — leave ~15% for the kernel, Docker, and burst. Sentinel's ALERT
   tier encodes this.
3. **Swap as a cushion, not a crutch.** Keep swap (done: 16 GB) so pressure
   degrades gracefully instead of OOM-killing at random; but treat sustained swap
   *thrash* as the real alarm (§1.2).
4. **Staggered starts to defuse boot storms.** On a box with many DB engines, add
   small `restart` back-off / start delays so a reboot doesn't cold-start 37 stacks
   at once (load 34). Swarm `update_config`/`restart_policy` delays, or dependency
   ordering.
5. **Log & journal caps** (done): Docker `max-size 50m x 3`, journald
   `SystemMaxUse=2G` + `SystemKeepFree=1G`. `SystemKeepFree` is the guarantee that
   logs can never again fill the disk and blind forensics.
6. **Persistent, size-capped journald + a Docker events ledger.** Persist journald
   (done) and append `docker events` for `oom`/`die`/`start` to a small rotating
   log so the *next* incident has a timeline even if the box rebooted.
7. **Separate tiers.** Prod stacks should not share an OOM domain with dev/demo.
   Even without new hardware, per-container limits give prod a protected floor.
8. **Enable `live-restore`** so `systemctl reload/restart docker` doesn't bounce
   every container (mitigates the SPOF-during-maintenance case).

---

## 3. Monitor reliability (a silent watchdog is worse than none)

1. **Dead-man's switch / heartbeat.** Sentinel should emit a positive "I ran and
   everything is OK" heartbeat to an external check (e.g. a healthchecks.io-style
   ping or a periodic OK to the webhook's quiet channel). If the *heartbeat* stops,
   that itself is the alert — otherwise a crashed monitor looks identical to "all
   healthy."
2. **Off-box delivery.** Alerts go to Slack/Discord, never an on-box mailbox, so
   they land even when the VM is thrashing (locked decision).
3. **Cheap and host-level.** Read `/proc`, cgroup files, and `docker inspect`;
   avoid anything that itself allocates a lot of memory or forks heavily under
   pressure. Run as a systemd timer, not a container.
4. **Bounded resource use for the monitor itself.** If it ever runs as a unit with
   a slice, cap it (`MemoryMax=`, `CPUQuota=`) so the watchdog can't become a
   contributor.
5. **Idempotent, stateless-ish.** Persist only a tiny previous-sample file (for
   deltas/rates); tolerate it being missing.

---

## 4. Alert hygiene (signal, not spam)

On a host where **most** containers are currently unbounded, naive alerting is pure
noise and will be muted within a day. Rules:

1. **Tiered severity → routing.** WARN to a low-noise channel / daily digest;
   ALERT to the team channel; PAGE to a channel that actually notifies phones.
2. **De-duplication + cooldown.** Don't re-send the same condition every interval.
   Alert on **state change** (OK→bad, bad→worse) and re-notify only after a
   cooldown (e.g. 6 h) while a condition persists.
3. **Auto-resolve.** Send a "recovered" message when a condition clears, so the
   channel reflects reality.
4. **Digest for chronic-but-known issues.** The pile of already-unbounded
   containers becomes a single daily "N unbounded (list)" summary, not N alerts.
5. **Actionable payloads.** Every alert names the **stack**, the **container**, the
   **measured value vs threshold**, and a **one-line suggested fix** (e.g. "add
   `mem_limit: 512m`"). An alert you can't act on from your phone is noise.
6. **Thresholds are config, not code.** Per-host tunables so a big box and a small
   box use different lines.

---

## 5. Phased roadmap

Build in order of "would have caught the incident earliest, cheapest first":

- **Phase 1 (MVP):** Memory (§1.1) + disk incl. growth rate (§1.3) + swap thrash
  (§1.2) + crash-loop `RestartCount` (§1.5). Tiered Slack/Discord alerts with
  state-change + cooldown. JSON report on disk. **These four alone cover 4 of the 5
  incident contributors.**
- **Phase 2:** OOM-kill / exit-137 detection from cgroup `memory.events` + a
  `docker events` ledger (audit trail). Boot-storm detection. Dead-man heartbeat.
- **Phase 3:** CPU throttling, health/`unhealthy`, SPOF/single-replica, config
  hygiene (privileged, `:latest`, missing healthcheck), daily digest.
- **Phase 4 (optional):** trend history (small on-disk time series), a tiny local
  status page/JSON endpoint a separate uptime monitor can scrape.

> Deliberately **out of scope for v1:** auto-remediation (restart/kill). Observe and
> warn until the signal is trusted; automated action is a later, separate decision.
