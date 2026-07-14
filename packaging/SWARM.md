# Deploying across a Docker Swarm (multiple nodes)

`dokploy-sentinel` is a **per-node** watchdog: in Swarm each node sees only its own
containers, so it must run on **every node** — managers and workers alike. The config is
host-agnostic (thresholds are percentages/ratios), so the **same config works on every
node**; leave `host_label` empty and each node reports its own hostname.

There are two ways to roll it out. Pick based on one trade-off.

| | **A. SSH fan-out** (recommended) | **B. Swarm global service** (zero-SSH) |
|---|---|---|
| Runs as | host-level **systemd timer** on each node | a **container** on each node |
| OOM-kill safety | ✅ can't be killed by the condition it detects | ⚠️ softened (capped + restart-policied, but it's a container) |
| Setup needs | SSH to every node | just a Swarm manager |
| New nodes | re-run the script | **auto-covered** |
| Command | `deploy-swarm.sh …` | `docker stack deploy …` |

The core reliability promise of this tool is that it *can't be OOM-killed by the very
memory pressure it exists to detect* — which only holds for **A**. Prefer A unless the
zero-SSH convenience of B is worth softening that guarantee.

---

## A. SSH fan-out (recommended)

`deploy-swarm.sh` runs the normal, verify-before-install `install.sh` on each node over
SSH, and can push one shared config to all of them. From a manager (or any box with SSH to
the nodes):

```sh
# list the nodes explicitly...
SUDO=sudo ./packaging/deploy-swarm.sh --config ./my-config.toml web1 web2 db1

# ...or from a file (one SSH target per line: host, user@host, or ~/.ssh/config alias)
./packaging/deploy-swarm.sh --nodes-file nodes.txt --config ./my-config.toml

# ...or let it discover nodes from `docker node ls` (hostnames must be SSH-reachable)
SUDO=sudo ./packaging/deploy-swarm.sh --config ./my-config.toml
```

Useful env vars: `SSH_USER`, `SSH_OPTS` (e.g. `-i key -o StrictHostKeyChecking=no`),
`SUDO=sudo` (if you don't SSH in as root), `VERSION`, `INTERVAL`, `REPO`.

Keep the webhook out of the shared config file by leaving `url = "${SLACK_WEBHOOK_URL}"`
and providing the value via a systemd `EnvironmentFile=-/etc/dokploy-sentinel/env` on each
node (a line `SLACK_WEBHOOK_URL=https://hooks.slack.com/...`).

**Upgrade** the whole fleet by re-running the script — `install.sh` is idempotent, so it
replaces the binary + units on each node and keeps config + state. Omit `--config` to leave
each node's config untouched:

```sh
# to the latest release on every node
SUDO=sudo ./packaging/deploy-swarm.sh web1 web2 db1

# or pin a specific release
VERSION=v0.2.0 SUDO=sudo ./packaging/deploy-swarm.sh web1 web2 db1
```

Uninstall everywhere:

```sh
SUDO=sudo ./packaging/deploy-swarm.sh --uninstall web1 web2 db1
```

---

## B. Swarm global service (zero-SSH)

A `mode: global` service runs one task per node — including nodes you add later — with the
host's `/proc`, cgroup tree, disk, and docker socket bind-mounted **read-only**. The Swarm
config object distributes the config to every node; the webhook URL is passed via the
environment so it never lives in the file.

```sh
# from a Swarm manager, in the packaging/ directory:
SLACK_WEBHOOK_URL='https://hooks.slack.com/services/...' \
  docker stack deploy -c docker-stack.yml sentinel

docker service ps sentinel_sentinel      # watch it run on every node
docker service logs sentinel_sentinel    # see reports / any errors
```

How it works (see [`docker-stack.yml`](docker-stack.yml) and
[`config.swarm.example.toml`](config.swarm.example.toml)):

- **Cadence:** the container runs the one-shot `run` and Swarm's
  `restart_policy: { condition: any, delay: 60s }` re-runs it every ~60s — so it stays a
  short-lived process, not a daemon.
- **Host visibility:** `/proc → /host/proc`, `/sys/fs/cgroup → /host/cgroup`, `/ → /host`,
  and `docker.sock` are mounted read-only; the config points `proc_root` / `cgroup_root` /
  `disk_fill.paths` at those mounts.
- **State:** `/var/lib/dokploy-sentinel` is bind-mounted from each host so rate deltas and
  alert memory survive the 60s restarts.
- **Identity:** `hostname: "{{.Node.Hostname}}"` makes each task report its node's name.
- **Caps:** memory is limited to 64M — the watchdog can't become the problem it watches.

**Upgrade** by re-running the deploy — `docker stack deploy` defaults to
`--resolve-image always`, so it re-pulls `:latest` and rolls the service across all nodes:

```sh
SLACK_WEBHOOK_URL='...' docker stack deploy -c docker-stack.yml sentinel
```

To pin (or roll back to) a specific image, either edit `image:` in `docker-stack.yml` to a
tag, or update in place without redeploying:

```sh
docker service update --image ghcr.io/akaike-byob/dokploy-sentinel:v0.2.0 sentinel_sentinel
```

Change the config later (Swarm configs are immutable): bump the config name in
`docker-stack.yml` (e.g. `sentinel-config-v2`) and redeploy. Remove everything with
`docker stack rm sentinel`.

> The image is published to `ghcr.io/akaike-byob/dokploy-sentinel` by the release workflow.
