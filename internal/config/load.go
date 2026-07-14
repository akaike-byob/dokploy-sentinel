package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Decode parses TOML on top of Default() and reports any keys it did not
// recognize (typos, unknown checks). It does NOT run semantic validation — call
// Validate for that. Env references (${VAR}) in secret-bearing fields are
// expanded so webhook URLs need not live in the TOML.
func Decode(data []byte) (cfg *Config, undecoded []string, err error) {
	cfg = Default()
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("config parse error: %w", err)
	}
	for _, k := range md.Undecoded() {
		undecoded = append(undecoded, k.String())
	}
	expandEnv(cfg)
	return cfg, undecoded, nil
}

// Load reads, decodes, and fully validates the config at path. On any error the
// returned error is named and specific (docs/plan/06-config.md §6.1).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config %s: %w", path, err)
	}
	cfg, undecoded, err := Decode(data)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(undecoded); err != nil {
		return nil, err
	}
	return cfg, nil
}

// expandEnv expands ${VAR} / $VAR in fields that may legitimately hold secrets
// injected via systemd EnvironmentFile.
func expandEnv(cfg *Config) {
	cfg.HeartbeatURL = os.ExpandEnv(cfg.HeartbeatURL)
	for name, t := range cfg.Targets {
		t.URL = os.ExpandEnv(t.URL)
		t.Mention = os.ExpandEnv(t.Mention)
		cfg.Targets[name] = t
	}
}

// MinTierParsed returns the target's minimum tier and whether one was set.
// Callers should only rely on this after Validate has confirmed it parses.
func (t TargetConfig) MinTierParsed() (Tier, bool) {
	if strings.TrimSpace(t.MinTier) == "" {
		return TierWARN, false
	}
	tier, err := ParseTier(t.MinTier)
	if err != nil {
		return TierWARN, false
	}
	return tier, true
}
