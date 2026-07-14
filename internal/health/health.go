// Package health defines the tri-state used everywhere a measurement can either
// succeed-good, succeed-bad, or fail to be taken. It is the concrete form of the
// design's most important invariant: "couldn't measure" is NOT "healthy"
// (docs/plan/05-alerting.md §5.1).
package health

// Health is the tri-state of a collection source or an observation.
type Health int

const (
	// OK — collected successfully, condition not met (source healthy).
	OK Health = iota
	// BAD — collected successfully, condition met. Only observations are BAD;
	// collectors emit OK or UNKNOWN.
	BAD
	// UNKNOWN — could not collect (socket down, read error, deadline hit).
	UNKNOWN
)

// String renders the health as its name.
func (h Health) String() string {
	switch h {
	case OK:
		return "OK"
	case BAD:
		return "BAD"
	case UNKNOWN:
		return "UNKNOWN"
	default:
		return "INVALID"
	}
}

// MarshalText serializes health as its name so report.json/state.json read well.
func (h Health) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

// UnmarshalText parses a health name.
func (h *Health) UnmarshalText(text []byte) error {
	switch string(text) {
	case "OK":
		*h = OK
	case "BAD":
		*h = BAD
	case "UNKNOWN":
		*h = UNKNOWN
	default:
		*h = UNKNOWN
	}
	return nil
}
