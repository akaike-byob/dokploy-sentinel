package collect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadMeminfoOvercommittedSwapless(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "meminfo"), `MemTotal:       22000000 kB
MemFree:         1000000 kB
MemAvailable:    5000000 kB
Buffers:          200000 kB
Cached:          3000000 kB
Committed_AS:   48500000 kB
SwapTotal:             0 kB
SwapFree:              0 kB
`)
	m := readMeminfo(dir)
	if m.Health != health.OK {
		t.Fatalf("health = %s", m.Health)
	}
	if m.MemTotalKB != 22000000 || m.MemAvailableKB != 5000000 || m.CommittedASKB != 48500000 {
		t.Fatalf("parsed wrong: %+v", m)
	}
	if used := m.UsedPct(); used < 77.2 || used > 77.3 {
		t.Errorf("UsedPct = %.2f, want ~77.27", used)
	}
	if r := m.CommittedRatio(); r < 2.2 || r > 2.21 {
		t.Errorf("CommittedRatio = %.3f, want ~2.204 (the incident's 2.2x)", r)
	}
	if swapPresent(dir, m) {
		t.Error("swap-less host should report no swap")
	}
}

func TestReadVmstatAndLoad(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "vmstat"), "nr_free_pages 100\npswpin 1234\npswpout 5678\npgmajfault 90\n")
	v := readVmstat(dir)
	if v.Health != health.OK || v.Pswpin != 1234 || v.Pswpout != 5678 || v.Pgmajfault != 90 {
		t.Fatalf("vmstat parse: %+v", v)
	}

	writeFile(t, filepath.Join(dir, "loadavg"), "0.50 1.20 2.00 1/234 5678\n")
	l := readLoadavg(dir)
	if l.Health != health.OK || l.Load1 != 0.5 || l.Load5 != 1.2 || l.Load15 != 2.0 {
		t.Fatalf("loadavg parse: %+v", l)
	}
}

func TestReadMeminfoMissingIsUnknown(t *testing.T) {
	m := readMeminfo(t.TempDir())
	if m.Health != health.UNKNOWN {
		t.Fatalf("missing meminfo should be UNKNOWN, got %s", m.Health)
	}
}

func TestReadCgroupV2(t *testing.T) {
	proc := t.TempDir()
	cg := t.TempDir()
	pid := 4242
	scope := "system.slice/docker-abc123.scope"

	writeFile(t, filepath.Join(proc, "4242", "cgroup"), "0::/"+scope+"\n")
	base := filepath.Join(cg, scope)
	writeFile(t, filepath.Join(base, "memory.current"), "104857600\n") // 100 MiB
	writeFile(t, filepath.Join(base, "memory.max"), "max\n")           // unlimited
	writeFile(t, filepath.Join(base, "memory.stat"), "anon 83886080\ninactive_file 20971520\nfile 20971520\n")
	writeFile(t, filepath.Join(base, "memory.events.local"), "low 0\noom 1\noom_kill 2\n")
	writeFile(t, filepath.Join(base, "cpu.stat"), "usage_usec 999\nnr_throttled 5\nthrottled_usec 12345\n")
	// cgroup v2 marker
	writeFile(t, filepath.Join(cg, "cgroup.controllers"), "memory cpu\n")

	if !cgroupV2Available(cg) {
		t.Fatal("should detect cgroup v2")
	}
	cs := readCgroup(proc, cg, "abc123", pid)
	if cs.Health != health.OK {
		t.Fatalf("cgroup health = %s (%s)", cs.Health, cs.Err)
	}
	if cs.Current != 104857600 || cs.Max != -1 {
		t.Errorf("current/max wrong: %+v", cs)
	}
	if cs.InactiveFile != 20971520 || cs.WorkingSet != 104857600-20971520 {
		t.Errorf("working set wrong: %+v", cs)
	}
	if cs.OOMKillCount != 2 || cs.NrThrottled != 5 || cs.ThrottledUsec != 12345 {
		t.Errorf("counters wrong: %+v", cs)
	}
}
