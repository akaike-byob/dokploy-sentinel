package collect

import (
	"syscall"

	"github.com/akaike-byob/dokploy-sentinel/internal/health"
)

// collectDisks statvfs's each configured path, de-duplicating by backing device
// so two paths on the same filesystem are only measured once. Order of the
// returned slice follows the first path that mapped to each device.
func collectDisks(paths []string) []DiskInfo {
	var out []DiskInfo
	seen := make(map[uint64]bool)
	for _, p := range paths {
		di := statPath(p)
		// De-dup only when we actually resolved a device id.
		if di.Health == health.OK {
			if seen[di.Device] {
				continue
			}
			seen[di.Device] = true
		}
		out = append(out, di)
	}
	return out
}

// statPath runs statvfs (via Statfs) + stat (for st_dev) on a single path.
func statPath(path string) DiskInfo {
	di := DiskInfo{Path: path}

	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err == nil {
		di.Device = uint64(st.Dev)
	}

	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		di.Health = health.UNKNOWN
		di.Err = err.Error()
		return di
	}
	di.Health = health.OK

	// Block accounting uses f_frsize (fragment size) for byte math, matching df.
	bsize := int64(fs.Frsize)
	if bsize == 0 {
		bsize = int64(fs.Bsize)
	}
	blocks := int64(fs.Blocks)
	bfree := int64(fs.Bfree)
	bavail := int64(fs.Bavail)

	usedBlocks := blocks - bfree
	di.TotalBytes = blocks * bsize
	di.UsedBytes = usedBlocks * bsize
	di.AvailBytes = bavail * bsize

	// used% excludes root-reserved space (matches df): used/(used+avail).
	denom := usedBlocks + bavail
	if denom > 0 {
		di.UsedPct = float64(usedBlocks) / float64(denom) * 100
	}

	// Inodes: (f_files − f_ffree)/f_files.
	di.InodesTotal = fs.Files
	if fs.Files >= fs.Ffree {
		di.InodesUsed = fs.Files - fs.Ffree
	}
	if fs.Files > 0 {
		di.InodePct = float64(di.InodesUsed) / float64(fs.Files) * 100
	}
	return di
}
