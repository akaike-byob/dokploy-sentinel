package checks

import (
	"fmt"
	"math"
)

// humanBytes formats a byte count as a human-readable size (base 1024).
func humanBytes(b int64) string {
	if b < 0 {
		return "unlimited"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// pct formats a percentage to one decimal.
func pct(v float64) string { return fmt.Sprintf("%.1f%%", v) }

// ratio formats a ratio (e.g. over-commit multiple) to two decimals.
func ratio(v float64) string { return fmt.Sprintf("%.2f×", v) }

// clampNonNeg returns v or 0 if v is negative or non-finite.
func clampNonNeg(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
