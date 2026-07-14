# 1. Tech stack (decisions + rationale)

← [Index](README.md)

| Concern | Decision | Why |
|---|---|---|
| **Language** | **Go**, stdlib-first, `CGO_ENABLED=0` static binary | 5–10 MB binary, zero runtime deps, ~10–15 MB working set, native `/proc`+cgroup reads, HTTP-over-unix-socket in stdlib, trivial amd64/arm64 cross-compile, AOT (starts reliably under memory pressure where a Python interpreter may not). The alert / collection-health state machine is real logic that wants unit tests — untenable in Bash. |
| **Config** | **TOML** at `/etc/dokploy-sentinel/config.toml` (mode `0600`, root) | Explicit types (no YAML footguns), clean nested `[checks.*]` tables + arrays-of-tables for targets, **loud parse error on a bad hand-edit** — critical for a safety tool edited under stress. Lib: `github.com/BurntSushi/toml`. |
| **Docker access** | Raw HTTP over a **configurable** unix socket (default `/var/run/docker.sock`); stdlib `http.Client` + unix `DialContext`, per-call + per-run timeouts | No SDK bloat. Explicit failure semantics ([03-signal-collection.md](03-signal-collection.md)). |
| **Delivery** | **Slack incoming webhook only** (v1); one renderer behind a provider interface | One thing to build and test well. Discord/generic are a documented later extension, not built now. See [05-alerting.md](05-alerting.md). |
| **State** | JSON at `/var/lib/dokploy-sentinel/state.json`, atomic write (`tmp`+`fsync`+`rename`) | Deltas/rates + alert + collection-health memory across runs (the process itself has none). |
| **Report** | JSON at `/var/lib/dokploy-sentinel/report.json` (last full evaluation) — **never contains webhook URLs or tokens** | Audit trail; scrapeable by a future uptime monitor. |
| **Distribution** | GitHub Releases (per-arch static binaries + `SHA256SUMS` + **minisign** signature) via GoReleaser; `curl -fsSL https://raw.githubusercontent.com/akaiketech/dokploy-sentinel/main/install.sh \| sh` **and** a first-class manual verify-and-install path | Standard idiom (node_exporter / tailscale / k3s). |
| **Runtime** | systemd `Type=oneshot` service + timer, resource-capped & sandboxed, **root** by default | Root reads `/proc`, cgroup, `docker.sock` unconditionally; a `docker`-group user is effectively root anyway. Non-root+docker-group as documented opt-in. |

**Module path:** `github.com/akaiketech/dokploy-sentinel` (drives every import, the
installer URL above, and the GitHub Releases location).

## Rejected alternatives

- **Python** — PEP 668 (externally-managed-environment) breaks a one-command `pip`
  install on Ubuntu 23.04+/24.04; interpreter cold-start + memory on every tick; the
  least reliable thing to invoke on a degraded, memory-pressured box.
- **Rust** — marginal footprint win over Go, slower iteration for a tool whose check
  set will keep growing.
- **Bash as the core** — the state machine + JSON + float math + webhook logic rot into
  ~500 lines of brittle, untestable shell, and `jq` becomes an unguaranteed runtime
  dependency. **Bash is used only for the installer.**

## Why Slack-only for v1

The provider-neutral `Alert` object (title, host, scope, measured, threshold, fix,
tier, key) is rendered by a single `SlackRenderer` behind a `Renderer` interface.
Supporting only Slack now means:

- one payload format to get right (attachment-wrapped Block Kit with a colored
  severity bar),
- no provider auto-detection, no Discord decimal-color quirks, no generic-envelope
  contract to maintain,
- adding Discord/generic later is a new `Renderer` implementation + URL detection —
  no change to checks, state machine, or routing.

The config still supports **multiple Slack targets** (one webhook URL per channel) so
tier→channel routing works from day one.
