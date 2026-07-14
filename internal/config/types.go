package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Tier is the severity of an observation/alert. The alert state machine keys on
// check:scope; the tier is an attribute of the event, not part of the key, so a
// WARN can escalate to PAGE on the same key (see docs/plan/05-alerting.md §5.3).
type Tier int

const (
	// TierWARN — fix soon, latent risk. Low-noise channel.
	TierWARN Tier = iota
	// TierALERT — fix now, actively wrong. Team channel.
	TierALERT
	// TierPAGE — human immediately, outage imminent. Phone-notifying channel.
	TierPAGE
)

// String renders the tier as its uppercase name.
func (t Tier) String() string {
	switch t {
	case TierWARN:
		return "WARN"
	case TierALERT:
		return "ALERT"
	case TierPAGE:
		return "PAGE"
	default:
		return fmt.Sprintf("Tier(%d)", int(t))
	}
}

// ParseTier parses a tier name (case-insensitive). Used for config + exceptions.
func ParseTier(s string) (Tier, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "WARN":
		return TierWARN, nil
	case "ALERT":
		return TierALERT, nil
	case "PAGE":
		return TierPAGE, nil
	default:
		return 0, fmt.Errorf("unknown tier %q (want WARN, ALERT, or PAGE)", s)
	}
}

// AllTiers is the ordered set of alertable tiers.
var AllTiers = []Tier{TierWARN, TierALERT, TierPAGE}

// MarshalText serializes a tier as its name (so state.json/report.json read
// well). Implements encoding.TextMarshaler.
func (t Tier) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

// UnmarshalText parses a tier name. Implements encoding.TextUnmarshaler.
func (t *Tier) UnmarshalText(text []byte) error {
	v, err := ParseTier(string(text))
	if err != nil {
		return err
	}
	*t = v
	return nil
}

// Duration is a time.Duration that unmarshals from a TOML string like "20s"
// or "6h". Using a string form gives a loud, specific parse error on a bad
// hand-edit rather than a silent zero.
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler for TOML decoding.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(strings.TrimSpace(string(text)))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	*d = Duration(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler for round-tripping.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// ByteSize is a byte count that unmarshals from a human string like "512m",
// "2g", or a bare number of bytes. Suffixes: k, m, g, t (base 1024, case-insensitive).
type ByteSize int64

// UnmarshalText implements encoding.TextUnmarshaler for TOML decoding.
func (b *ByteSize) UnmarshalText(text []byte) error {
	v, err := ParseByteSize(string(text))
	if err != nil {
		return err
	}
	*b = ByteSize(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler for round-tripping.
func (b ByteSize) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(b), 10)), nil
}

// Bytes returns the raw byte count.
func (b ByteSize) Bytes() int64 { return int64(b) }

// ParseByteSize parses "512m" / "2g" / "1048576" into bytes (base 1024).
func ParseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty byte size")
	}
	mult := int64(1)
	last := s[len(s)-1]
	if last == 'b' || last == 'B' {
		// tolerate a trailing "b"/"B" (e.g. "512mb")
		s = s[:len(s)-1]
		if s == "" {
			return 0, fmt.Errorf("invalid byte size %q", s)
		}
		last = s[len(s)-1]
	}
	switch last {
	case 'k', 'K':
		mult = 1 << 10
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1 << 20
		s = s[:len(s)-1]
	case 'g', 'G':
		mult = 1 << 30
		s = s[:len(s)-1]
	case 't', 'T':
		mult = 1 << 40
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size %q: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("negative byte size %q", s)
	}
	return int64(n * float64(mult)), nil
}
