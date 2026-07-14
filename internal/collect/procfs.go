package collect

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// ReadBootID reads /proc/sys/kernel/random/boot_id, used to detect reboots so
// monotonic-counter deltas aren't computed across a reset. Empty on failure.
func ReadBootID(procRoot string) string {
	data, err := os.ReadFile(filepath.Join(procRoot, "sys/kernel/random/boot_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readMeminfo parses <procRoot>/meminfo. All values are kB.
func readMeminfo(procRoot string) MemInfo {
	var m MemInfo
	data, err := os.ReadFile(filepath.Join(procRoot, "meminfo"))
	if err != nil {
		m.Health = health.UNKNOWN
		m.Err = err.Error()
		return m
	}
	fields := parseKVkB(data)
	// MemTotal is mandatory; without it we can measure nothing.
	total, ok := fields["MemTotal"]
	if !ok || total == 0 {
		m.Health = health.UNKNOWN
		m.Err = "meminfo missing MemTotal"
		return m
	}
	m.Health = health.OK
	m.MemTotalKB = total
	m.MemFreeKB = fields["MemFree"]
	m.MemAvailableKB = fields["MemAvailable"]
	m.CommittedASKB = fields["Committed_AS"]
	m.SwapTotalKB = fields["SwapTotal"]
	m.SwapFreeKB = fields["SwapFree"]
	// Fallback: pre-3.14 kernels lack MemAvailable. Approximate with MemFree so
	// we never divide against a zero available (which would read as 100% used).
	if _, has := fields["MemAvailable"]; !has {
		m.MemAvailableKB = m.MemFreeKB
	}
	return m
}

// parseKVkB parses "Key:   1234 kB" lines into a map of Key->value(kB).
func parseKVkB(data []byte) map[string]uint64 {
	out := make(map[string]uint64)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		rest := strings.Fields(line[colon+1:])
		if len(rest) == 0 {
			continue
		}
		v, err := strconv.ParseUint(rest[0], 10, 64)
		if err != nil {
			continue
		}
		out[key] = v
	}
	return out
}

// readVmstat parses the monotonic counters we care about from <procRoot>/vmstat.
func readVmstat(procRoot string) VmstatInfo {
	var v VmstatInfo
	data, err := os.ReadFile(filepath.Join(procRoot, "vmstat"))
	if err != nil {
		v.Health = health.UNKNOWN
		v.Err = err.Error()
		return v
	}
	v.Health = health.OK
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "pswpin":
			v.Pswpin = n
		case "pswpout":
			v.Pswpout = n
		case "pgmajfault":
			v.Pgmajfault = n
		}
	}
	return v
}

// readLoadavg parses <procRoot>/loadavg and records the core count.
func readLoadavg(procRoot string) LoadInfo {
	var l LoadInfo
	l.NumCPU = runtime.NumCPU()
	data, err := os.ReadFile(filepath.Join(procRoot, "loadavg"))
	if err != nil {
		l.Health = health.UNKNOWN
		l.Err = err.Error()
		return l
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		l.Health = health.UNKNOWN
		l.Err = "malformed loadavg"
		return l
	}
	l.Load1, _ = strconv.ParseFloat(f[0], 64)
	l.Load5, _ = strconv.ParseFloat(f[1], 64)
	l.Load15, _ = strconv.ParseFloat(f[2], 64)
	l.Health = health.OK
	return l
}

// swapPresent reports whether the host has any swap configured. SwapTotal from
// meminfo is authoritative; /proc/swaps is a corroborating fallback. A swap-less
// host OOMs abruptly, so callers raise memory sensitivity when this is false.
func swapPresent(procRoot string, mem MemInfo) bool {
	if mem.Health == health.OK && mem.SwapTotalKB > 0 {
		return true
	}
	data, err := os.ReadFile(filepath.Join(procRoot, "swaps"))
	if err != nil {
		return mem.SwapTotalKB > 0
	}
	// First line is a header; any further non-empty line means swap exists.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return len(lines) > 1
}
