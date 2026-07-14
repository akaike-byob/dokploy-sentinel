//go:build live

// Package collect live tests exercise the real collectors against this host's
// actual /proc, cgroup v2 tree, and Docker socket. They assert the collectors
// parse without error and return sane ranges, not exact values. Run opt-in:
//
//	go test -tags live ./internal/collect/
package collect

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

func TestLiveMeminfo(t *testing.T) {
	m := readMeminfo("/proc")
	if m.Health != health.OK {
		t.Fatalf("live meminfo unreadable: %s", m.Err)
	}
	if m.MemTotalKB == 0 {
		t.Fatal("MemTotal is zero")
	}
	if p := m.UsedPct(); p < 0 || p > 100 {
		t.Fatalf("UsedPct out of range: %.2f", p)
	}
	if r := m.CommittedRatio(); r <= 0 {
		t.Fatalf("CommittedRatio non-positive: %.2f", r)
	}
}

func TestLiveVmstatLoad(t *testing.T) {
	if v := readVmstat("/proc"); v.Health != health.OK {
		t.Fatalf("live vmstat unreadable: %s", v.Err)
	}
	l := readLoadavg("/proc")
	if l.Health != health.OK || l.NumCPU < 1 {
		t.Fatalf("live loadavg bad: %+v", l)
	}
}

func TestLiveDisks(t *testing.T) {
	disks := collectDisks([]string{"/"})
	if len(disks) != 1 || disks[0].Health != health.OK {
		t.Fatalf("live disk stat failed: %+v", disks)
	}
	if disks[0].UsedPct < 0 || disks[0].UsedPct > 100 {
		t.Fatalf("disk used%% out of range: %.2f", disks[0].UsedPct)
	}
}

func TestLiveDocker(t *testing.T) {
	sock := "/var/run/docker.sock"
	if _, err := os.Stat(sock); err != nil {
		t.Skipf("no docker socket at %s", sock)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dc := NewDockerClient(sock, 5*time.Second)
	di := collectDocker(ctx, Options{Docker: dc, InspectConcurrency: 8, Deadline: 15 * time.Second})
	if di.Health != health.OK {
		t.Fatalf("live docker collection failed: %s (%s)", di.Err, di.Hint)
	}
	if di.APIVersion == "" {
		t.Error("expected a negotiated API version")
	}
	t.Logf("live docker: %d containers, API v%s", len(di.Containers), di.APIVersion)
}
