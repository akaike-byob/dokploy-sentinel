# Problem Statement — dokploy-sentinel

## 1. Context

This project was prompted by a real incident on a **shared VPS** used as a
general-purpose Dokploy host for multiple teams. At the time of the incident the host
carried:

- **8 vCPU, 22 GiB RAM, 193 GB disk**, cgroup v2, Ubuntu.
- **~37 Dokploy stacks** (Docker Swarm + Compose), including roughly **13 database
  engines** (Postgres / ClickHouse / others) across prod, dev, and demo tiers.
- **Traefik** reverse proxy; a **single** Dokploy replica (a SPOF).
- **No swap.** Default `vm.swappiness=60`.
- **Almost no per-container memory limits** — `docker stats` showed nearly every
  container as `/ 22.91GiB`, i.e. entitled to the *entire* machine.

Memory was over-committed: `Committed_AS` was ~48.5 GB against 22 GB physical —
a **2.2x** over-subscription that no one could see at a glance.

## 2. What actually happened (the incident)

Timeline of the 2026-07-09 → 2026-07-14 incident:

1. **Disk filled to ~90%.** Culprits: ~79 GB in the containerd image store,
   ~23 GB of build cache, and a ~55 GB analytics (ClickHouse) volume. When the disk
   filled, **journald lost its evidence** — so the earliest signal was already gone.
2. **The box rebooted** (~06:31 UTC). Every one of the ~37 stacks **cold-started
   simultaneously** — a boot storm that drove **load to 34 on 8 cores**.
3. **Public apps went down.** Several public apps — a marketing site, a scraping
   API, and the Dokploy dashboard itself — became unreachable with bad-gateway
   errors. Dokploy's single replica meant its own restart returned 502s.
4. **No alert fired.** We only learned about it when a human noticed the apps were
   down. There was **no early warning** for the latent over-commit that set it all up.

### Why we were flying blind

- No per-container limits → no signal of "who is allowed to eat the box."
- No swap → no cushion; pressure went straight to OOM / thrash.
- Over-commit was invisible: you had to manually sum `docker inspect` limits and
  compare to RAM to even notice the 2.2x.
- journald filled and rotated away the disk-fill evidence.
- No external notifier — discovery was manual and late.

## 3. Remediation already applied (post-incident)

These reduced the blast radius but do **not** provide early warning:

- Pruned images + build cache (disk 90% → 49%).
- Added a **16 GB swapfile** (persistent via fstab), swappiness left at 60.
- Capped Docker container logs (`max-size 50m` x `max-file 3`) and journald
  (`SystemMaxUse=2G`, `SystemKeepFree=1G`) so disk can't be silently filled again.

**Gap that remains:** nothing tells us when the box drifts back into
over-subscription, or when a new stack is deployed with no memory limit. That is
exactly the gap `dokploy-sentinel` closes.

## 4. Use cases

### UC-1 — Catch an unbounded container at deploy time
A team ships a new stack (or edits an existing one) without a `mem_limit`. Within
one check interval, sentinel emits a **WARN** naming the container and its stack,
so it can be fixed before it ever misbehaves.

### UC-2 — Detect creeping over-subscription
As stacks accumulate, the sum of declared limits (plus a headroom estimate for
each unbounded container) crosses physical RAM. Sentinel emits an **ALERT** with
the current ratio (e.g. "declared 31 GB vs 22 GB physical, 1.4x over") so we can
right-size or move a stack off-box **before** a reboot turns it into an outage.

### UC-3 — Page on live memory pressure
Real usage crosses 85% of RAM (a leak, a load spike, a runaway query). Sentinel
emits a **PAGE** so a human can intervene before the OOM killer picks a victim at
random.

### UC-4 — Off-box notification that survives a degraded VM
Alerts go to a **Slack/Discord webhook**, not an on-box mailbox, so we still get
the message when the machine itself is thrashing or a container-based monitor
would already be dead.

### UC-5 — Leave an audit trail
Every check writes a local JSON/text report (last state, ratios, offenders) that a
future uptime monitor or a human doing forensics can read — so the next incident
isn't investigated blind the way this one was.

## 5. Situations the tool must handle

- A container with **no** `mem_limit` (`HostConfig.Memory == 0`).
- A mix of bounded and unbounded containers on the same host.
- **Sum of limits exceeds RAM** even though live usage is currently fine (latent
  over-commit — the state that caused this incident).
- **Live usage high** even though declared limits look conservative (a leak or
  spike inside a bounded container, or many small unbounded ones adding up).
- The **VM is under pressure** while the check runs — the tool must be cheap,
  host-level, and not itself a candidate for the OOM killer.
- **Alert fatigue:** on a box where almost every container is currently unbounded,
  a naive "alert on any unbounded" is pure noise. Policy must be tiered and
  (later) support de-duplication / cooldown so we get signal, not spam.

## 6. Design decisions (locked)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Deployment** | Host-level **systemd timer** (not a Dokploy stack / container) | A container monitor can be OOM-killed by the exact condition it should detect; it also needs host RAM + all-container visibility. |
| **Alert channel** | **Slack/Discord incoming webhook** | One URL to configure; off-box, so alerts survive a degraded VM. |
| **Threshold policy** | **Tiered:** WARN (any unbounded) / ALERT (sum-limits > RAM) / PAGE (live > 85%) | Matches the three distinct failure shapes above without collapsing them into one noisy signal. |
| **Scope (v1)** | Observe + warn only; **no** auto-restart/kill | Safety first; remediation is a human decision until the signal is trusted. |
| **Data source** | Docker API directly (`inspect` / `stats`) | Works on any Docker host, not just Dokploy; no dependency on Dokploy internals. |

## 7. Open questions (for the build phase)

- Headroom estimate per unbounded container: fixed (e.g. assume 1 GB each) or
  derived from current RSS? (Leaning: current RSS, so the estimate reflects reality.)
- Alert de-duplication / cooldown window to avoid re-paging every interval while a
  condition persists.
- Check interval (leaning: every 2–5 min for PAGE-class, hourly summary for WARN).
- Whether to also watch **disk** and **swap** usage in the same pass, since both
  were part of this incident.
