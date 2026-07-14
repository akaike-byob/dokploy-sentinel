# dokploy-sentinel — Implementation Plan

**Status:** design, v0.4 (pre-code, reviewed). This directory is the buildable plan.
It turns [`../problem-statement.md`](../problem-statement.md) and
[`../best-practices.md`](../best-practices.md) into a concrete architecture, tech
stack, config model, and roadmap.

**Audience:** anyone running a Dokploy / Docker host — not just the host that
triggered the incident. Everything is host-agnostic and config-driven.

**v0.3 scope note:** delivery is **Slack-only** for now (single provider, one
renderer). The internal alert object is provider-neutral behind an interface, so
Discord / generic webhooks are a cheap later addition — but they are explicitly **not**
built in v1. See [05-alerting.md](05-alerting.md).

---

## Read in this order

| # | File | What it covers |
|---|------|----------------|
| — | **README.md** (this file) | Index, goals, design principles, supported matrix |
| 1 | [01-tech-stack.md](01-tech-stack.md) | Language, config format, distribution, runtime — with rationale |
| 2 | [02-architecture.md](02-architecture.md) | Run lifecycle, package layout, the check interface |
| 3 | [03-signal-collection.md](03-signal-collection.md) | How each source (`/proc`, cgroup, Docker socket) is read |
| 4 | [04-checks.md](04-checks.md) | **The check catalog — each check, how it works, and why it adds value** |
| 5 | [05-alerting.md](05-alerting.md) | Slack delivery, the signal-not-spam state machine, heartbeat |
| 6 | [06-config.md](06-config.md) | The TOML config model + validation |
| 7 | [07-deployment.md](07-deployment.md) | Installer + systemd units |
| 8 | [08-roadmap-and-decisions.md](08-roadmap-and-decisions.md) | Phased roadmap, decisions taken, open questions, review log |
| 9 | [09-testing.md](09-testing.md) | Test strategy — deterministic core, fixtures + live collectors, CI |

**Repo home:** `github.com/akaiketech/dokploy-sentinel` (module path, installer URL,
releases).

---

## 1. Goals & design principles

In priority order:

1. **Lightweight.** Short-lived process on a systemd timer: start → sample `/proc` +
   cgroup files + Docker socket → evaluate → maybe notify → write report → exit. Zero
   idle footprint. Working-set target **< 20 MB**, hard-capped 64 MB by the unit.
2. **Cannot become the problem it detects.** Not a container; host-level,
   resource-capped, bounded fan-out.
3. **Deployable by a stranger in one command**, any supported host, no runtime to
   install.
4. **Config-driven.** Every check enable/disable + every threshold, tier, cooldown,
   destination in one file. No recompile to tune.
5. **Signal, not spam.** Tiered severity, state-change alerting, flap damping,
   cooldowns, auto-resolve.
6. **Never a false all-clear.** "Couldn't measure" is **not** "healthy" — a first-class
   invariant (see [05-alerting.md](05-alerting.md)), because a false recovery recreates
   the exact incident failure.
7. **Trustworthy monitor.** A dead monitor must not look like a healthy host —
   dead-man's-switch heartbeat (Phase 1) + systemd `OnFailure`.
8. **Observe-only in v1.** Never restarts/kills. Remediation is a later, explicit
   decision.

**Design stance:** cheap signals first, escalate to expensive ones only on suspicion.
`/proc` + cgroup file reads (near-free) → Docker `inspect` (cheap, bounded fan-out) →
Docker `stats` / `system df` (expensive, targeted).

## 2. Tiers (used throughout)

| Tier | Meaning | Routing intent |
|------|---------|----------------|
| **WARN** | Fix soon — latent risk | Low-noise Slack channel |
| **ALERT** | Fix now — actively wrong | Team Slack channel |
| **PAGE** | Human immediately — outage imminent | Phone-notifying Slack channel (with mention) |

## 3. Supported matrix (the honest "any host" caveat)

Host-agnostic in **config** (no hardcoded RAM/thresholds/paths/webhook), but not
environment-agnostic. Requires **systemd + cgroup v2 + a reachable Docker socket**
(path configurable → rootless-Docker / Podman work). That covers essentially every
current Dokploy host (Ubuntu 22.04+ defaults). cgroup-v1-only hosts run
**reduced-fidelity**; non-systemd inits are out of scope for v1. Stated so the README's
"any Docker host" claim is precise, not aspirational.
