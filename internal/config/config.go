// Package config loads the app-level configuration (as opposed to per-source
// credentials, which live beside it under the same ConfigDir).
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nachmore/commstats/internal/platform"
	"github.com/nachmore/commstats/internal/store"
)

// Config is the on-disk app config at <ConfigDir>/config.json.
type Config struct {
	// Ignore lists channels/DMs to exclude from reports, by name or id. Matching
	// is normalized (case-insensitive, leading #/@ stripped, "_" and "-"
	// treated as equivalent) so "nachmano_playground", "#nachmano-playground",
	// and the raw channel id all work.
	Ignore []string `json:"ignore"`
}

// Load reads <ConfigDir>/config.json. A missing file yields an empty config,
// not an error — the app is fully usable without one.
func Load() (Config, error) {
	path := filepath.Join(platform.Current().Paths().ConfigDir(), "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// IgnoreSet is a normalized set of channel identifiers to exclude.
type IgnoreSet struct {
	keys map[string]struct{}
}

// NewIgnoreSet builds an IgnoreSet from raw config patterns.
func NewIgnoreSet(patterns []string) IgnoreSet {
	keys := make(map[string]struct{}, len(patterns))
	for _, p := range patterns {
		if k := normalize(p); k != "" {
			keys[k] = struct{}{}
		}
	}
	return IgnoreSet{keys: keys}
}

// Empty reports whether the set has no entries (so callers can skip filtering).
func (s IgnoreSet) Empty() bool { return len(s.keys) == 0 }

// MatchesRecord reports whether a record's channel (by name or id) is ignored.
func (s IgnoreSet) MatchesRecord(r store.Record) bool {
	if len(s.keys) == 0 {
		return false
	}
	if id := r.Dimensions["channel_id"]; id != "" {
		if _, ok := s.keys[normalize(id)]; ok {
			return true
		}
	}
	if name := r.Dimensions["channel_name"]; name != "" {
		if _, ok := s.keys[normalize(name)]; ok {
			return true
		}
	}
	return false
}

// Filter returns the records that are not ignored.
func (s IgnoreSet) Filter(recs []store.Record) []store.Record {
	if s.Empty() {
		return recs
	}
	out := recs[:0:0]
	for _, r := range recs {
		if !s.MatchesRecord(r) {
			out = append(out, r)
		}
	}
	return out
}

// normalize lower-cases, strips a leading #/@, and maps "_" to "-" so the
// various ways a user might write a channel name all compare equal.
func normalize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "#")
	s = strings.TrimPrefix(s, "@")
	s = strings.ReplaceAll(s, "_", "-")
	return s
}
