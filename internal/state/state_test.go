package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")

	s := New()
	s.RunCount = 42
	s.BootID = "boot-1"
	s.LastRun = time.Unix(1000, 0).UTC()
	s.Alerts["mem_pressure:host"] = &KeyState{Status: StatusFiring, ConsecutiveBad: 3, NotifiedTargets: []string{"team"}}
	s.Ring("/").Add(DiskPoint{Timestamp: time.Unix(1000, 0).UTC(), UsedBytes: 123})

	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, had, err := Load(path)
	if err != nil || !had {
		t.Fatalf("load failed: had=%v err=%v", had, err)
	}
	if got.RunCount != 42 || got.BootID != "boot-1" {
		t.Errorf("scalar fields lost: %+v", got)
	}
	if ks := got.Alerts["mem_pressure:host"]; ks == nil || ks.Status != StatusFiring || len(ks.NotifiedTargets) != 1 {
		t.Errorf("alert state lost: %+v", ks)
	}
	// File must be 0600 (state is written tight even without secrets).
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestLoadMissingIsNoBaseline(t *testing.T) {
	_, had, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || had {
		t.Fatalf("missing file should be no-baseline, no error; had=%v err=%v", had, err)
	}
}

func TestLoadCorruptIsNoBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	s, had, err := Load(path)
	if err != nil || had || s == nil {
		t.Fatalf("corrupt file should degrade to fresh no-baseline; had=%v err=%v", had, err)
	}
}

func TestLoadVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte(`{"version": 999, "run_count": 5}`), 0o600)
	_, had, _ := Load(path)
	if had {
		t.Fatal("version mismatch should be treated as no baseline")
	}
}

func TestBootChanged(t *testing.T) {
	s := &Snapshot{BootID: "a"}
	if !s.BootChanged("b") {
		t.Error("different boot id should report changed")
	}
	if s.BootChanged("a") {
		t.Error("same boot id should not report changed")
	}
	if s.BootChanged("") || (&Snapshot{}).BootChanged("b") {
		t.Error("missing boot id should not force warm-up")
	}
}

func TestPrunePendingAlerts(t *testing.T) {
	s := New()
	s.Alerts["a:host"] = &KeyState{Status: StatusPending, LastSeenRun: 5}
	s.Alerts["b:host"] = &KeyState{Status: StatusPending, LastSeenRun: 4} // stale
	s.Alerts["c:host"] = &KeyState{Status: StatusFiring, LastSeenRun: 4}  // firing: kept
	s.PrunePendingAlerts(5)
	if _, ok := s.Alerts["b:host"]; ok {
		t.Error("stale pending key should be pruned")
	}
	if _, ok := s.Alerts["a:host"]; !ok {
		t.Error("current pending key should be kept")
	}
	if _, ok := s.Alerts["c:host"]; !ok {
		t.Error("firing key must never be pruned (freezes as UNKNOWN instead)")
	}
}

func TestDiskRingCap(t *testing.T) {
	r := &DiskRing{}
	for i := 0; i < DiskRingMax*2; i++ {
		r.Add(DiskPoint{Timestamp: time.Unix(int64(i), 0), UsedBytes: int64(i)})
	}
	if len(r.Points) != DiskRingMax {
		t.Fatalf("ring should cap at %d, got %d", DiskRingMax, len(r.Points))
	}
	if r.Points[len(r.Points)-1].UsedBytes != DiskRingMax*2-1 {
		t.Error("ring should keep the newest points")
	}
}
