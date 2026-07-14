package collect

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// cgroupV2Available reports whether the host uses cgroup v2 (the only fully
// supported path). Presence of cgroup.controllers at the root ⇒ v2.
func cgroupV2Available(cgroupRoot string) bool {
	_, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers"))
	return err == nil
}

// readCgroup resolves a container's real cgroup path from
// <procRoot>/<pid>/cgroup (handling compose/swarm --cgroup-parent, nested scopes)
// and reads its memory + cpu files under cgroupRoot. Construct-by-id is only a
// fallback when pid is 0 (container not running).
func readCgroup(procRoot, cgroupRoot, containerID string, pid int) CgroupStats {
	var cs CgroupStats

	rel := ""
	if pid > 0 {
		rel = resolveCgroupPath(procRoot, pid)
	}
	if rel == "" {
		// Fallback: the two common construct-by-id layouts.
		for _, cand := range []string{
			"system.slice/docker-" + containerID + ".scope",
			"docker/" + containerID,
		} {
			if _, err := os.Stat(filepath.Join(cgroupRoot, cand, "memory.current")); err == nil {
				rel = cand
				break
			}
		}
	}
	if rel == "" {
		cs.Health = health.UNKNOWN
		cs.Err = "could not resolve cgroup path"
		return cs
	}

	base := filepath.Join(cgroupRoot, rel)
	if _, err := os.Stat(base); err != nil {
		cs.Health = health.UNKNOWN
		cs.Err = "cgroup path not found: " + rel
		return cs
	}
	cs.Path = rel
	cs.Resolved = pid > 0

	current, curOK := readIntFile(filepath.Join(base, "memory.current"))
	if !curOK {
		cs.Health = health.UNKNOWN
		cs.Err = "memory.current unreadable"
		return cs
	}
	cs.Health = health.OK
	cs.Current = current

	// memory.max: literal "max" = unlimited (-1).
	if raw, ok := readStringFile(filepath.Join(base, "memory.max")); ok {
		if raw == "max" {
			cs.Max = -1
		} else if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			cs.Max = v
		}
	}

	// memory.stat: inactive_file (subtract for working set), anon (non-reclaimable).
	stat := readKeyedFile(filepath.Join(base, "memory.stat"))
	cs.InactiveFile = int64(stat["inactive_file"])
	cs.Anon = int64(stat["anon"])
	cs.WorkingSet = cs.Current - cs.InactiveFile
	if cs.WorkingSet < 0 {
		cs.WorkingSet = 0
	}

	// OOM kills: prefer memory.events.local, fall back to memory.events.
	events := readKeyedFile(filepath.Join(base, "memory.events.local"))
	if _, ok := events["oom_kill"]; !ok {
		events = readKeyedFile(filepath.Join(base, "memory.events"))
	}
	cs.OOMKillCount = events["oom_kill"]

	// cpu.stat throttling counters.
	cpu := readKeyedFile(filepath.Join(base, "cpu.stat"))
	cs.NrThrottled = cpu["nr_throttled"]
	cs.ThrottledUsec = cpu["throttled_usec"]

	return cs
}

// resolveCgroupPath reads <procRoot>/<pid>/cgroup and returns the v2 relative
// path (the part after "0::"). Empty on failure.
func resolveCgroupPath(procRoot string, pid int) string {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		// cgroup v2 unified line: "0::/system.slice/docker-<id>.scope"
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(strings.TrimPrefix(line, "0::"), "/")
		}
	}
	return ""
}

func readIntFile(path string) (int64, bool) {
	raw, ok := readStringFile(path)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func readStringFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// readKeyedFile parses "key value\n" files (memory.stat, memory.events, cpu.stat).
func readKeyedFile(path string) map[string]uint64 {
	out := make(map[string]uint64)
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		out[f[0]] = v
	}
	return out
}
