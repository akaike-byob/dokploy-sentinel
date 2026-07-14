# 7. Distribution & deployment

← [Index](README.md)

**Repo home:** `github.com/akaiketech/dokploy-sentinel` — the module path, the
installer URL, and the GitHub Releases location all point here.

## 7.1 Installer (`curl -fsSL https://raw.githubusercontent.com/akaiketech/dokploy-sentinel/main/install.sh | sh`)

- `set -e; set -o noglob`; wrap the body in `main() { … }; main "$@"` (truncated-download
  safety); detect arch via `uname -m` (`x86_64→amd64`, `aarch64→arm64`) and OS via
  `/etc/os-release`.
- Download the binary **to a temp file**, **verify SHA256 vs the published `SHA256SUMS`**
  (and, when present, **`minisign -V`** against the published pubkey) **before** install —
  never pipe the binary through a shell. Install to `/usr/local/bin/dokploy-sentinel`.
- Write default config to `/etc/dokploy-sentinel/config.toml` **only if absent** (`0600`,
  root; else write `config.toml.new` for reference and say so). Create
  `/var/lib/dokploy-sentinel`.
- **Run `dokploy-sentinel check` before arming**; refuse `enable --now` on invalid config.
- Install `.service` + `.timer`; interval via `INTERVAL=60s … | sh` baked into a timer
  **drop-in** (`…timer.d/interval.conf`) so upgrades don't clobber it; `daemon-reload`;
  `enable --now`; optional immediate `systemctl start` so the user sees a report + a test
  alert and validates their webhook without waiting for the first tick (report marks
  "no baseline yet" for rate checks).
- **Idempotent** re-run = clean upgrade (replace binary + units, keep config).
- `--uninstall` disables the timer, removes units + binary, and **asks** before deleting
  `/etc/dokploy-sentinel` + `/var/lib/dokploy-sentinel`.
- **First-class manual path** (documented, not a footnote): download the tarball from
  Releases, `minisign -V` / `sha256sum -c`, copy the binary, install the units — for
  security-conscious / air-gapped users who won't `curl | sh`.

## 7.2 systemd units

**`dokploy-sentinel.service`** (`Type=oneshot`):

```ini
[Unit]
Description=dokploy-sentinel host watchdog
After=docker.service
Wants=docker.service
OnFailure=dokploy-sentinel-failure.service   # self-report non-zero exits

[Service]
Type=oneshot
ExecStart=/usr/local/bin/dokploy-sentinel run --config /etc/dokploy-sentinel/config.toml
Nice=10
IOSchedulingClass=idle
# Resource caps — the watchdog must never become the problem it watches for
MemoryHigh=48M
MemoryMax=64M
CPUQuota=25%
TasksMax=32
# Sandboxing — but it NEEDS host visibility, so do not over-sandbox
NoNewPrivileges=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/dokploy-sentinel
PrivateTmp=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
# Do NOT set: ProtectControlGroups=true, ProcSubset=pid, PrivateNetwork
#   -> it reads /proc + cgroup files and talks to docker.sock + Slack.
```

**`dokploy-sentinel.timer`**:

```ini
[Unit]
Description=Run dokploy-sentinel periodically

[Timer]
OnBootSec=1min           # don't fire during boot churn
OnUnitActiveSec=60s      # default interval; overridden by the install-time drop-in
AccuracySec=10s
Persistent=false

[Install]
WantedBy=timers.target
```

- **Default interval 60 s** — with PAGE checks at `flap_samples=1`, time-to-first-page ≈
  one interval (~60 s), the best a timer-based tool can do without becoming a daemon.
- Validate hardening with `systemd-analyze security dokploy-sentinel.service` in CI.
- **User:** root by default (simplest correct choice for `/proc` + cgroup + `docker.sock`
  reads). A non-root system user (`useradd --system --no-create-home
  --shell /usr/sbin/nologin`) added to the `docker` group is offered as a **documented
  opt-in**, with `User=`/`Group=` in the unit.
