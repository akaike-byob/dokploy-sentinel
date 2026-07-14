package checks

import (
	"fmt"
	"math"
	"time"

	"github.com/akaike-byob/dokploy-sentinel/internal/collect"
	"github.com/akaike-byob/dokploy-sentinel/internal/config"
	"github.com/akaike-byob/dokploy-sentinel/internal/health"
	"github.com/akaike-byob/dokploy-sentinel/internal/state"
)

// diskFill — a filesystem is past a usage threshold, OR its fill rate projects it
// full within N days. Watching both level and slope is the difference between
// "you have days" and "you're already down".
type diskFill struct{ cfg config.DiskFillConfig }

func (diskFill) ID() string { return "disk_fill" }

func (c diskFill) Evaluate(ec *EvalContext) []Observation {
	var obs []Observation
	for _, d := range ec.Sample.Disks {
		obs = append(obs, c.evalOne(ec, d))
	}
	return obs
}

func (c diskFill) evalOne(ec *EvalContext, d collect.DiskInfo) Observation {
	o := Observation{Check: "disk_fill", Scope: d.Path, Title: "Disk filling"}
	if d.Health != health.OK {
		o.Health = health.UNKNOWN
		o.Measured = "could not statvfs " + d.Path
		return o
	}

	// Fill-rate trajectory from the persisted ring + the current reading.
	bytesPerSec, haveRate := fillRate(ec.Prev.Ring(d.Path), d, ec.Now)
	freeBytes := d.TotalBytes - d.UsedBytes
	daysToFull := math.Inf(1)
	if haveRate && bytesPerSec > 0 {
		daysToFull = float64(freeBytes) / (bytesPerSec * 86400)
	}

	measured := fmt.Sprintf("%s used (%s/%s)", pct(d.UsedPct), humanBytes(d.UsedBytes), humanBytes(d.TotalBytes))
	if haveRate && bytesPerSec > 0 {
		measured += fmt.Sprintf(", +%s/day → full in %.1fd", humanBytes(int64(bytesPerSec*86400)), daysToFull)
	} else if !haveRate {
		measured += ", rate warming up"
		o.Warming = true
	}
	o.MeasuredValue = d.UsedPct

	// Worst of level and trajectory.
	tier := config.TierWARN
	bad := false
	threshold := pct(c.cfg.Warn)
	if d.UsedPct >= c.cfg.Warn {
		bad = true
	}
	if d.UsedPct >= c.cfg.Alert {
		tier, threshold = config.TierALERT, pct(c.cfg.Alert)
	}
	if daysToFull <= c.cfg.DaysToFullAlert {
		bad = true
		tier = config.TierALERT
		threshold = fmt.Sprintf("full in ≤ %.0fd", c.cfg.DaysToFullAlert)
	}
	if !bad {
		o.Health = health.OK
		o.Measured = measured
		return o
	}
	o.Health = health.BAD
	o.Tier = tier
	o.Measured = measured
	o.Threshold = threshold
	o.Fix = fmt.Sprintf("free space on %s (docker system prune, rotate logs, grow the volume)", d.Path)
	return o
}

// fillRate computes an EWMA of the byte/sec slope across the ring history plus
// the current reading. haveRate is false with no baseline (first run / reboot).
func fillRate(ring *state.DiskRing, cur collect.DiskInfo, now time.Time) (float64, bool) {
	if ring == nil || len(ring.Points) == 0 {
		return 0, false
	}
	series := append([]state.DiskPoint(nil), ring.Points...)
	series = append(series, state.DiskPoint{Timestamp: now, UsedBytes: cur.UsedBytes})
	return ewmaSlope(series)
}

func ewmaSlope(pts []state.DiskPoint) (float64, bool) {
	const alpha = 0.5
	var ewma float64
	var have bool
	for i := 1; i < len(pts); i++ {
		dt := pts[i].Timestamp.Sub(pts[i-1].Timestamp).Seconds()
		if dt <= 0 {
			continue // clamp non-positive time deltas (NTP step / reordering)
		}
		slope := float64(pts[i].UsedBytes-pts[i-1].UsedBytes) / dt
		if !have {
			ewma = slope
			have = true
		} else {
			ewma = alpha*slope + (1-alpha)*ewma
		}
	}
	return ewma, have
}

// diskInodes — a filesystem has run out of inodes despite free bytes. ALERT.
type diskInodes struct{ cfg config.DiskInodesConfig }

func (diskInodes) ID() string { return "disk_inodes" }

func (c diskInodes) Evaluate(ec *EvalContext) []Observation {
	var obs []Observation
	for _, d := range ec.Sample.Disks {
		o := Observation{Check: "disk_inodes", Scope: d.Path, Title: "Inode exhaustion"}
		switch {
		case d.Health != health.OK:
			o.Health = health.UNKNOWN
			o.Measured = "could not statvfs " + d.Path
		case d.InodesTotal == 0:
			// Some filesystems report no inode accounting; nothing to measure.
			o.Health = health.OK
			o.Measured = "no inode accounting"
		case d.InodePct >= c.cfg.Alert:
			o.Health = health.BAD
			o.Tier = config.TierALERT
			o.Measured = fmt.Sprintf("%s inodes used (%d/%d)", pct(d.InodePct), d.InodesUsed, d.InodesTotal)
			o.Threshold = pct(c.cfg.Alert)
			o.Fix = fmt.Sprintf("remove many small files on %s (old overlay2 layers, temp files)", d.Path)
			o.MeasuredValue = d.InodePct
		default:
			o.Health = health.OK
			o.Measured = fmt.Sprintf("%s inodes used", pct(d.InodePct))
			o.MeasuredValue = d.InodePct
		}
		obs = append(obs, o)
	}
	return obs
}
