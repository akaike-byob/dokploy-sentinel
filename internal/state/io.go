package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Load reads the snapshot at path. A missing, corrupt, or version-mismatched
// file is not an error: it returns a fresh snapshot with hadBaseline=false, so
// rate checks warm up and level checks still run (docs/plan/02-architecture.md
// §2.1 step 4). A genuine I/O error (e.g. unreadable dir) is returned.
func Load(path string) (snap *Snapshot, hadBaseline bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(), false, nil
		}
		return New(), false, fmt.Errorf("read state %s: %w", path, err)
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt state is treated as no baseline, not a hard failure.
		return New(), false, nil
	}
	if s.Version != Version {
		return New(), false, nil
	}
	s.ensureMaps()
	return &s, true, nil
}

// Save atomically writes the snapshot to path (tmp in the same dir → fsync →
// rename), so a crash mid-write can never leave a truncated state.json.
func Save(path string, s *Snapshot) error {
	s.Version = Version
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename state into place: %w", err)
	}
	return nil
}

// BootChanged reports whether the host rebooted since the snapshot was written
// (boot_id differs). Delta/rate checks treat this as "warming up" so a counter
// reset is never read as a huge negative or positive rate.
func (s *Snapshot) BootChanged(currentBootID string) bool {
	if s.BootID == "" || currentBootID == "" {
		return false // unknown → don't force warm-up on a missing boot_id
	}
	return s.BootID != currentBootID
}

// Service returns the per-service state, creating it if absent.
func (s *Snapshot) Service(key string) *ServiceState {
	if s.Services == nil {
		s.Services = map[string]*ServiceState{}
	}
	st := s.Services[key]
	if st == nil {
		st = &ServiceState{}
		s.Services[key] = st
	}
	return st
}

// Ring returns the disk fill-rate ring for a path, creating it if absent.
func (s *Snapshot) Ring(path string) *DiskRing {
	if s.Disks == nil {
		s.Disks = map[string]*DiskRing{}
	}
	r := s.Disks[path]
	if r == nil {
		r = &DiskRing{}
		s.Disks[path] = r
	}
	return r
}

// Alert returns the alert key state, creating it if absent.
func (s *Snapshot) Alert(key string) *KeyState {
	if s.Alerts == nil {
		s.Alerts = map[string]*KeyState{}
	}
	return s.Alerts[key]
}

// PrunePendingAlerts drops pending keys not seen in the current run (a transient
// condition on a vanished container). Firing keys are kept: an unmeasurable
// firing key freezes as UNKNOWN rather than being silently forgotten.
func (s *Snapshot) PrunePendingAlerts(currentRun int64) {
	for k, ks := range s.Alerts {
		if ks.Status == StatusPending && ks.LastSeenRun != currentRun {
			delete(s.Alerts, k)
		}
	}
}

// PruneServices drops service entries that weren't seen this run and carry no
// remaining exit events, bounding state growth on churny hosts.
func (s *Snapshot) PruneServices(currentRun int64) {
	for k, st := range s.Services {
		if st.LastSeenRun != currentRun && len(st.ExitEvents) == 0 {
			delete(s.Services, k)
		}
	}
}
