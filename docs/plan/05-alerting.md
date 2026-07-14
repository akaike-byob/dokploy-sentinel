# 5. Alerting — Slack delivery + signal-not-spam state machine

← [Index](README.md)

**v1 supports Slack only.** The internal alert object is provider-neutral behind a
`Renderer` interface, so Discord / generic webhooks slot in later without touching the
state machine, routing, or checks — but they are **not** built in v1.

## 5.1 Tri-state observations (the false-all-clear invariant)

Each check emits `health ∈ {OK, BAD, UNKNOWN}`, never mere presence/absence:
- **OK** — collected successfully, condition not met.
- **BAD** — collected successfully, condition met (carries `tier`, `measured`, `fix`).
- **UNKNOWN** — could not collect (socket down, cgroup/proc read error, deadline hit).

**UNKNOWN freezes the key:** it neither fires, resolves, nor advances/resets
`consecutive_ok`. Auto-resolve requires `resolve_samples` consecutive **OK** (not
"absent") runs. This guarantees a transient Docker/cgroup failure can never turn a live
crash-loop / OOM / over-commit into a green RESOLVED. Sustained UNKNOWN on a
previously-firing key is itself surfaced (once) as "monitoring degraded for X."

> This is the most important rule in the whole design: **"couldn't measure" ≠
> "healthy."** A false recovery recreates the exact incident failure (silent, late
> discovery).

## 5.2 The neutral alert object → Slack renderer

```
Alert { tier, title, host, scope, measured, threshold, fix, key, check, ts, run_id }
```

**Slack payload** = Block Kit blocks **inside an attachment** (the attachment gives the
colored severity bar; blocks give the layout). The top-level `text` is the
self-contained phone-push line. Colors: WARN `#ECB22E`, ALERT `#E8912D`, PAGE
`#E01E5A`, RESOLVED `#2EB67D`. Example (PAGE):

```json
{
  "text": "🔴 PAGE — OOM imminent on dokploy-01: memory 96% (threshold 95%)",
  "attachments": [{
    "color": "#E01E5A",
    "blocks": [
      { "type": "header", "text": { "type": "plain_text", "text": "🔴 PAGE — Memory pressure" } },
      { "type": "section", "fields": [
        { "type": "mrkdwn", "text": "*Host:*\ndokploy-01" },
        { "type": "mrkdwn", "text": "*Scope:*\nhost" },
        { "type": "mrkdwn", "text": "*Measured:*\n96% (21.1/22 GiB)" },
        { "type": "mrkdwn", "text": "*Threshold:*\n95%" } ]},
      { "type": "section", "text": { "type": "mrkdwn",
        "text": "*Suggested fix:* restart or cap the largest unbounded container now; swap is thrashing (see swap_thrash)." } },
      { "type": "context", "elements": [
        { "type": "mrkdwn", "text": "check=`mem_pressure` · key=`mem_pressure:host` · 2026-07-14T09:42:03Z · run #1187" } ]}
    ]
  }]
}
```

An alert must be **actionable from a locked phone in 5 seconds**: tier + one-line
summary in the push text, the exact scope, measured-vs-threshold, and a one-line fix.
Everything else (check id, dedup key, timestamp, run id) goes in the de-emphasized
context line.

## 5.3 State machine (per key `check:scope`; tier is an attribute, not part of the key)

Persisted per key in `state.json`:
`status` (pending/firing), `tier`, `consecutive_bad`, `consecutive_ok`,
`first_detected`, `fired_at`, `last_notified`, `last_tier_notified`,
**`notified_targets`** (the union of channels that received sends — so RESOLVED fans
out to exactly those), `last_measured`.

- **Flap damping:** hold in `pending` until `consecutive_bad ≥ flap_samples` (per-tier
  default; **PAGE = 1** so it fires on first breach). A `pending` key that clears is
  silently forgotten (transient spike → no alert).
- **Fire** on first breach; append the routed channels to `notified_targets`.
- **Escalate** when tier worsens on the same key (WARN→ALERT→PAGE) — one escalation
  alert, not a false resolve + new fire. Tier is deliberately excluded from the key.
- **De-escalate** on improving-but-still-bad (PAGE→ALERT): update `last_tier_notified`,
  route future reminders to the lower tier — no false resolve.
- **Cooldown reminder:** while firing + unchanged, re-notify only after the per-tier
  cooldown (`last_notified` vs `now`); otherwise **silent**. This is the core anti-spam
  behavior — a persistent-but-known condition does not re-ping every interval.
- **Auto-resolve:** a firing key with `resolve_samples` consecutive **OK** runs → send a
  green RESOLVED to `notified_targets`, delete the key.
- **Guards:** boot-id / counter-reset detection on all delta checks; **clamp negative
  time deltas** (NTP step) so cooldown math can neither page early nor suppress forever.
- **Pruning:** drop `pending` keys for vanished containers each run; cap the disk-rate
  ring — `state.json` can't grow unbounded on churny hosts.

## 5.4 Routing (tier → Slack channels)

Config declares named Slack targets and a tier→targets table
([06-config.md](06-config.md)). Route by the **tier of the event** (an escalation to
PAGE routes to the PAGE set). **Fan-out for PAGE** (e.g. team channel + a
phone-notifying channel with a `mention`). Mentions are added **only on PAGE** to avoid
pinging humans for WARN/ALERT. The state machine runs **once per key**; only the final
send fans out to targets. RESOLVED follows `notified_targets`.

## 5.5 Dead-man's-switch heartbeat (Phase 1 — non-negotiable)

A crashed monitor is indistinguishable from "everything healthy" — both produce zero
alerts. The heartbeat inverts this: an **external** service expects a regular ping and
alerts when the pings *stop*.

- `POST <heartbeat_url>/start` at run begin.
- `POST <heartbeat_url>` on success — **even when problems were found** (a healthy
  monitor doing its job).
- `POST <heartbeat_url>/fail` only on a monitor-internal error (couldn't read state,
  socket unreachable *and* unhandled, config load crash).
- All with `-m 10 --retry 3` so a flaky network doesn't cause a false "monitor down."
- Plus systemd `OnFailure=` for non-zero exits (necessary but not sufficient — it can't
  catch a stopped timer or a fully-dead host; the external ping can).

Works with healthchecks.io, a self-hosted healthchecks instance, or an Uptime Kuma push
monitor. `heartbeat_url` empty → heartbeat disabled.

## 5.6 Rate-limit hygiene

Slack incoming webhooks allow ~1 msg/sec/channel. A 60-second timer emitting only
state-changes sends 0–3 messages/run — orders of magnitude under the limit. Still: on
`429`, honor `Retry-After`, retry once, then log and move on (never block the run);
space multiple sends by ~250 ms.
