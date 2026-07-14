# 4. Check catalog

← [Index](README.md) · Signals these consume: [03-signal-collection.md](03-signal-collection.md)

Every check is **independently enable/disable** and **threshold-configurable**
([06-config.md](06-config.md)). Each entry below documents: **what** it detects,
**how** (signal + fields), **tier**, **dedup scope** (the identity `check:scope` the
state machine keys on), and — most importantly — **why it adds value** (the concrete
failure it catches, tied to the 2026-07-09 incident where applicable).

The five latent incident contributors, for reference:
1. Disk silently filled → journald rotated away the evidence.
2. Memory 2.2× over-committed, no per-container limits, no swap.
3. A reboot caused a boot storm (37 stacks cold-start at once → load 34).
4. Single-replica Dokploy = SPOF → 502 during its own restart.
5. No early warning and no audit trail — discovery was manual and late.

**Phase 1 checks (below) cover contributors #1, #2, #3-partial, and #5.**

---

## Phase 1 — MVP (the checks that would have caught the incident earliest)

### `mem_pressure` — live RAM usage is dangerously high
- **What:** current real memory usage crossed a percentage of physical RAM.
- **How:** `(MemTotal − MemAvailable)/MemTotal` from `/proc/meminfo`. `warn 80 / alert
  90 / page 95` (percent, configurable).
- **Tier:** PAGE (at the top threshold). **Scope:** `host`.
- **Why it adds value:** this is the "OOM is imminent, act now" signal. In the incident
  the box thrashed to a standstill with **no warning**; a human only noticed when public
  apps went dark. Paging at 95% buys the minutes needed to restart or cap a runaway
  container *before* the kernel OOM-killer picks a victim at random. It uses
  `MemAvailable` (not `MemFree`), so it reflects true pressure, not cache.

### `committed_as` — the box is latently over-committed (the invisible 2.2×)
- **What:** the kernel has *promised* far more memory than physically exists.
- **How:** `Committed_AS / MemTotal` from `/proc/meminfo`. **Default `warn 1.5 /
  alert 2.0`** (configurable). Because Linux memory *overcommit* means a healthy host
  routinely sits above `1.0` (allocations that are reserved but never touched), the
  default is deliberately well above 1 to avoid noise — the incident was `2.2`. A
  second, swap-aware guard also fires when
  `Committed_AS > (MemTotal + SwapTotal) × commit_vs_swap_ratio` (default `1.0`), i.e.
  the promise exceeds *usable virtual* memory even counting swap — a stronger "this can
  actually OOM" signal on hosts that have added swap as a cushion (as the incident box
  did post-mortem). Host-only — **no container walk**, so it's nearly free and never
  jitters with live usage.
- **Tier:** WARN → ALERT. **Scope:** `host`.
- **Why it adds value:** the incident's **2.2× over-commit was visible in `Committed_AS`
  the entire time and nobody was looking.** This is the single cheapest early-warning of
  the exact latent condition that set up the outage — it fires while live usage still
  looks fine, which is precisely when you can still act (move a stack, add limits) with
  zero urgency. Making the default swap-aware keeps it quiet on normally-overcommitted
  healthy boxes while still catching the "promised more than RAM+swap" danger.

### `declared_overcommit` — the *config* promises more RAM than exists
- **What:** the sum of declared container memory limits, plus a realistic headroom for
  each unbounded container, exceeds usable RAM.
- **How:** `sum(HostConfig.Memory over bounded) + Σ headroom(unbounded)` vs
  `RAM × (1 − headroom_reserve_pct/100)` (default reserve 15%). **Headroom per unbounded
  container = `max(headroom_floor, current_working_set)`** (default floor 512 MB) — so an
  *idle* unbounded container still counts toward risk, while a hot one counts its real
  footprint. Observed once per run and flap-damped, so live-RSS jitter doesn't cause
  noise.
- **Tier:** ALERT. **Scope:** `host`.
- **Why it adds value:** this is UC-2 — "detect creeping over-subscription." As stacks
  accumulate, the declared budget crosses physical RAM long before usage does. Alerting
  here (e.g. "declared 31 GB vs 22 GB physical, 1.4× over") lets you right-size or move a
  stack off-box **before a reboot turns latent over-commit into a boot-storm outage.**
  Complements `committed_as`: that one reads the kernel's live promise; this one reads
  your *configuration's* promise, so it flags a bad deploy even before anything runs.

### `unbounded_mem` — a container has no memory limit
- **What:** a running container has `mem_limit` unset (entitled to the whole box).
- **How:** `HostConfig.Memory == 0` from inspect. Names the stack + container.
- **Tier:** WARN. **Scope:** `service-label` (fallback container id).
- **Why it adds value:** this is UC-1 and the **root config gap** of the incident —
  "`docker stats` showed nearly every container as `/ 22.91GiB`," i.e. every container
  could eat the entire machine, so they all competed for one unbounded pool. A single
  unbounded container with a leak or spike can starve the box. Catching it at deploy time
  ("add `mem_limit: 512m`") turns a latent landmine into a one-line fix. Because a box
  can have *many* already-unbounded containers, this is a low-noise WARN and is a prime
  candidate for the daily digest (Phase 2) rather than N immediate alerts.

### `disk_fill` — a filesystem is filling (level **and** trajectory)
- **What:** a monitored filesystem is past a usage threshold, **or** its fill rate
  projects it full within N days.
- **How:** `statvfs` per path (`/`, `/var/lib/docker`, …) for the level; a persisted
  ring of `(ts, used_bytes)` + EWMA for `days_to_full`. `warn 80 / alert 90 /
  days_to_full_alert 7`.
- **Tier:** WARN → ALERT. **Scope:** mount path.
- **Why it adds value:** **disk fill is what actually started the incident** — the
  containerd image store, build cache, and a 55 GB ClickHouse volume filled the disk,
  which then rotated away journald's evidence and led to the reboot. A point-in-time "90%
  full" is a *lagging* signal; **rate of fill ("+8 GB/day → full in 3 days") is the one
  that gives you time to act.** Watching both the level and the slope is the difference
  between "you have days" and "you're already down."

### `disk_inodes` — inode exhaustion (free bytes, but writes still fail)
- **What:** a filesystem has run out of inodes despite showing free space.
- **How:** `(f_files − f_ffree)/f_files` from `statvfs`. Alert at `> 90%`.
- **Tier:** ALERT. **Scope:** mount path.
- **Why it adds value:** Docker's overlay2 with many small image layers and short-lived
  containers can exhaust **inodes** while `df` shows plenty of bytes free — producing the
  baffling "no space left on device" where the disk *looks* empty. It's nearly free to
  check and catches a real, confusing Docker failure mode that a bytes-only disk check
  misses entirely.

### `swap_thrash` — the box is trading disk I/O for RAM (latency collapse)
- **What:** sustained paging *in* from swap — the real "grinding to a halt" signal.
- **How:** `pswpin` rate from `/proc/vmstat` (delta between runs), gated off when no
  swap exists. `pswpin_pages_per_sec > 1000` sustained (flap-damped).
- **Tier:** PAGE. **Scope:** `host`.
- **Why it adds value:** swap *usage* being high is benign (cold pages parked on disk);
  swap **thrash** — high page-in rate — means the working set no longer fits in RAM and
  every access is a disk round-trip, so latency collapses even though the box isn't
  technically OOM. The post-incident remediation added 16 GB of swap as a cushion; this
  check is what tells you the cushion has become a *crutch* and the box is thrashing on
  it. It's the correct alarm to page on, not raw swap %.

### `crashloop` — a container is restart-looping
- **What:** a container (or swarm service) is restarting repeatedly.
- **How:** plain Docker → `RestartCount` delta between runs. **Swarm → count short-lived
  exited containers per `service.name` over a window** (Swarm reschedules with new IDs,
  so `RestartCount` stays low). `restarts 5 / window 10m`.
- **Tier:** ALERT. **Scope:** `service-label`.
- **Why it adds value:** there was a **known dev-Postgres crash-loop during
  the incident that nobody was watching.** A climbing restart count is the cheapest
  possible detector of a broken deploy, an OOM-restart cycle, or a config error — and it
  distinguishes "restarting because broken" from "healthy." The swarm-aware counting is
  what makes it actually work on Dokploy, where naive `RestartCount` would silently miss
  it.

---

## Phase 2 — confirmation & audit trail

### `oom_kill` — an OOM kill actually happened (not just "might")
- **What:** the kernel OOM-killer has killed a process in a container's cgroup.
- **How:** `memory.events.local` `oom_kill` counter delta (per container), corroborated
  with `State.ExitCode == 137` and `State.OOMKilled`.
- **Tier:** PAGE. **Scope:** `service-label`.
- **Why it adds value:** turns "memory looks tight" into "an OOM just occurred here" —
  concrete, actionable, and forensically valuable. Critically, the cgroup counter
  **catches kills that Docker's `State.OOMKilled` flag misses**: when the *host* runs out
  of RAM and kills a process inside an *unbounded* container, Docker often reports
  `OOMKilled: false` even though the exit code is 137. Reading the counter directly means
  no OOM goes unrecorded — closing the "no audit trail" gap (#5).

### `boot_storm` — many containers started at once (post-reboot thundering herd)
- **What:** an unusual number of containers have `StartedAt` within a short window.
- **How:** cluster `StartedAt` timestamps from inspect; alert when N start inside a small
  window.
- **Tier:** ALERT. **Scope:** `host`.
- **Why it adds value:** the reboot **cold-started ~37 stacks simultaneously, driving
  load to 34 on 8 cores** — a boot storm that turned a recoverable reboot into an outage.
  Detecting the storm (and, later, informing staggered-start guardrails) means a future
  reboot doesn't repeat contributor #3. It also serves as a "something just restarted
  en masse — investigate" signal even outside a full reboot.

---

## Phase 3 — hygiene, saturation & SPOF

### `cpu_throttle` — a container's CPU limit is too tight
- **What:** a container is being CPU-throttled by its cgroup quota.
- **How:** `cpu.stat` `nr_throttled` / `throttled_usec` rising between runs.
- **Tier:** WARN. **Scope:** `service-label`.
- **Why it adds value:** an over-tight CPU limit silently starves an app — slow requests,
  timeouts, backed-up queues — with **no error and no OOM**, so it's invisible without
  reading cgroup stats. Surfaces a whole class of "it's slow but nothing's crashing"
  problems and points directly at the fix (raise the CPU share).

### `container_health` — a container reports `unhealthy`
- **What:** a container's Docker healthcheck is failing.
- **How:** `State.Health.Status == "unhealthy"` from inspect.
- **Tier:** WARN → ALERT. **Scope:** `service-label`.
- **Why it adds value:** app-level failure the resource checks can't see — a container
  can be well within its memory and CPU limits and still be serving errors. Catching
  `unhealthy` (especially for a backend Traefik is still routing to) prevents "the box is
  fine but the app is down" from going unnoticed.

### `load_high` — sustained CPU/run-queue saturation
- **What:** load average is a sustained multiple of core count.
- **How:** `loadavg / nproc` (5- or 15-min figure) from `/proc/loadavg`.
- **Tier:** WARN / ALERT. **Scope:** `host`.
- **Why it adds value:** load 34 on 8 cores was *the* visible symptom of the incident's
  boot storm. Sustained high load corroborates other pressure signals and catches
  saturation (a runaway process, a thundering herd) that isn't strictly memory or disk.
  Read with care — load counts I/O-wait too — so it's a supporting WARN, not a lone page.

### `single_replica` — a critical service is a single point of failure
- **What:** a stack tagged critical is running as a single replica.
- **How:** Swarm service `Replicas 1/1` for services matching a configured
  critical-label/pattern.
- **Tier:** WARN. **Scope:** `service-label`.
- **Why it adds value:** contributor #4 — **the single-replica Dokploy control plane
  returned 502s during its own restart.** Flagging SPOFs among services you've marked
  critical lets you add a replica *before* a routine restart becomes user-visible
  downtime.

### `no_restart_policy` — a long-running service won't come back after a crash
- **What:** a service has `RestartPolicy.Name == "no"`.
- **How:** from inspect `HostConfig.RestartPolicy`.
- **Tier:** WARN. **Scope:** `service-label`.
- **Why it adds value:** without a restart policy, a single crash becomes *permanent*
  downtime until a human notices — exactly the "discovery was manual and late" failure.
  Cheap to detect, and the fix is a one-line compose change.

### `no_healthcheck` — a service has no healthcheck defined
- **What:** a container defines no healthcheck.
- **How:** `Config.Healthcheck` empty from inspect.
- **Tier:** WARN. **Scope:** `service-label`.
- **Why it adds value:** without a healthcheck, Docker/Swarm/Traefik can't tell a wedged
  container from a healthy one, so a dead backend keeps receiving traffic and
  `container_health` has nothing to read. Flagging the gap improves the whole reliability
  stack, not just this tool.

### `hygiene` — config & security drift (privileged, `:latest`, exposed ports, no log cap)
- **What:** a bundle of low-severity config/security smells.
- **How:** inspect fields — `HostConfig.Privileged`, image ref ends `:latest`/untagged,
  host port bound to `0.0.0.0`, `HostConfig.LogConfig` unset with no daemon default.
- **Tier:** INFO / WARN. **Scope:** `service-label`.
- **Why it adds value:** each is a small latent risk that compounds. Notably, **oversized
  uncapped container logs were part of how the disk silently filled** in the incident
  (#1) — flagging a container with no log cap is direct prevention. `:latest` warns about
  non-reproducible drift; `privileged` and unexpected `0.0.0.0` binds are security
  hygiene. Rolled into the daily digest so they inform without paging.
