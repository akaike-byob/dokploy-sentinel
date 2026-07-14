# 3. Signal collection — how each source is read

← [Index](README.md) · Checks that consume these signals: [04-checks.md](04-checks.md)

## 3.1 Host memory — `/proc/meminfo` (values in kB)
- **Live used %** = `(MemTotal − MemAvailable)/MemTotal`. Use **`MemAvailable`**, not
  `MemFree` (Linux uses free RAM as cache; `MemFree` wildly overstates pressure).
- **Latent over-commit** = `Committed_AS/MemTotal`.
- **Swap present?** `SwapTotal == 0` / empty `/proc/swaps` → gate swap-thrash off and
  *raise* memory sensitivity (swap-less hosts OOM abruptly — the incident box had none).

## 3.2 Swap thrash — `/proc/vmstat` (delta required)
- Monotonic `pswpin`/`pswpout` (pages; × page-size for bytes), plus `pgmajfault`.
  Rate `= (now − prev)/dt`. Sustained `pswpin` (paging *back in*) is the real thrash
  signal. First run: no baseline → "warming up", never a false alert. Guard `now<prev`
  (reboot reset) via boot-id.

## 3.3 CPU / load — `/proc/loadavg` vs `nproc`
- `load_ratio = loadavg / nproc` (use the 5- or 15-min figure to avoid spikes). Load
  counts uninterruptible-sleep (I/O-wait) tasks — corroborate before "CPU overloaded".

## 3.4 Disk — `statvfs` syscall (not `df`)
- `used% = (f_blocks − f_bfree)/(f_blocks − f_bfree + f_bavail)` (matches `df`, which
  excludes root-reserved space). **Inodes:** `(f_files − f_ffree)/f_files`.
- **Fill rate:** persist a small ring of `(ts, used_bytes)` per path; EWMA →
  `days_to_full`. Enumerate real mounts from `/proc/self/mountinfo`; `statvfs` each
  distinct backing device once. `GET /system/df` (expensive) only when
  `/var/lib/docker` is already past a threshold, to attribute *what* is consuming space.

## 3.5 Docker — HTTP over the unix socket (configurable path)
- **Failure semantics:** socket missing → Docker checks = **UNKNOWN** (never resolve),
  surfaced **once** as INFO; host-only checks still run. `EACCES` → same, with a
  "run as root / add to docker group" hint. `/_ping` non-200 or connection refused
  (daemon restarting) → UNKNOWN freeze. Every call has a timeout; the run has an
  overall deadline.
- **Version negotiation:** `GET /_ping` → `API-Version` header; request
  `min(supported, host)`; baseline v1.41; API < 1.40 is rejected by modern daemons.
- **List:** `GET /containers/json?all=true` (include exited → catch crash-loop / OOM
  corpses). Group by **labels** (`com.docker.swarm.service.name`,
  `com.docker.compose.project/.service`), not names.
- **Inspect (cheap, bounded pool):** `HostConfig.Memory` (bytes; **0 = unbounded**),
  `State.Pid` (for cgroup resolution), `State.OOMKilled`, `State.ExitCode`,
  `State.StartedAt`, `RestartCount`, CPU limits, `RestartPolicy`, `Config.Healthcheck`.
- **Stats (expensive):** `GET /containers/{id}/stats?stream=false`, only for
  already-flagged containers.

## 3.6 cgroup v2 files — resolved, not constructed
- **Resolve the real path** from `/proc/<State.Pid>/cgroup` (handles Dokploy/compose
  `--cgroup-parent`, nested scopes, swarm) → read files under
  `/sys/fs/cgroup/<that-path>`. Construct-by-id
  (`system.slice/docker-<ID>.scope` | `docker/<ID>`) only as a fallback when Pid is 0.
- `memory.current`, `memory.max` (**string `max` = unlimited** — special-case),
  `memory.stat` (`inactive_file` to subtract for working set; `anon` = the
  non-reclaimable number that actually causes OOM). **Working set** =
  `memory.current − inactive_file`.
- **OOM-after-the-fact:** `memory.events.local` `oom_kill` monotonic counter →
  persist per container; `now > prev` = a kill since last sample.
- `cpu.stat` `nr_throttled` / `throttled_usec`.
- **cgroup v1:** detect (`/sys/fs/cgroup/cgroup.controllers` present ⇒ v2). v2 is the
  only fully-supported path; v1 → degrade to the stats API + a "reduced fidelity" notice.

## 3.7 Swarm nuance (Dokploy uses Swarm)
- Task containers are ordinary containers on the node — normal API + resolved cgroup
  paths; no `/services` needed. A node sees only its own containers (run on every host).
- **Crash-loop:** Swarm *reschedules* (new container ID) rather than restart-in-place,
  so `RestartCount` stays low → detect by **counting short-lived exited containers per
  `service.name` label over time**, and **key persisted per-container state on the
  service/task label, not the container ID** (else history is lost every reschedule).
