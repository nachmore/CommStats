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
	// is case-insensitive and ignores a leading #/@ (so "#fiducia-leads" matches
	// the stored "fiducia-leads"), but is otherwise literal — a typo'd entry
	// matches nothing and is reported as a warning rather than silently fixed.
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

// IgnoreSet is a set of channel identifiers to exclude, tracking which entries
// have actually matched so unused (likely typo'd) entries can be reported.
type IgnoreSet struct {
	keys    map[string]struct{}
	matched map[string]struct{}
}

// NewIgnoreSet builds an IgnoreSet from raw config patterns.
func NewIgnoreSet(patterns []string) *IgnoreSet {
	keys := make(map[string]struct{}, len(patterns))
	for _, p := range patterns {
		if k := normalize(p); k != "" {
			keys[k] = struct{}{}
		}
	}
	return &IgnoreSet{keys: keys, matched: map[string]struct{}{}}
}

// Empty reports whether the set has no entries (so callers can skip filtering).
func (s *IgnoreSet) Empty() bool { return s == nil || len(s.keys) == 0 }

// MatchesRecord reports whether a record's channel (by name or id) is ignored,
// recording which entry matched.
func (s *IgnoreSet) MatchesRecord(r store.Record) bool {
	if s.Empty() {
		return false
	}
	for _, dim := range []string{"channel_id", "channel_name"} {
		v := normalize(r.Dimensions[dim])
		if v == "" {
			continue
		}
		if _, ok := s.keys[v]; ok {
			s.matched[v] = struct{}{}
			return true
		}
	}
	return false
}

// Filter returns the records that are not ignored.
func (s *IgnoreSet) Filter(recs []store.Record) []store.Record {
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

// Unmatched returns the configured ignore entries that never matched any
// record — typically typos. Order is not significant.
func (s *IgnoreSet) Unmatched() []string {
	if s.Empty() {
		return nil
	}
	var out []string
	for k := range s.keys {
		if _, ok := s.matched[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// normalize lower-cases and strips a leading #/@ so an ignore entry written the
// way it appears in a report ("#fiducia-leads", "@jbbaird") matches the stored
// bare name. Matching is otherwise literal — no fuzzy fixes — so a real typo
// fails to match and surfaces via Unmatched.
func normalize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "#")
	s = strings.TrimPrefix(s, "@")
	return s
}
