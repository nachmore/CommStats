package report

import (
	"sort"
	"strconv"

	"github.com/nachmore/commstats/internal/store"
)

// Kind is how a metric group should be visualized, inferred from its shape.
type Kind int

const (
	// KindScalar: records carry no dimensions — a single value per day. Charted
	// as a time series (e.g. emails_sent, meeting_minutes).
	KindScalar Kind = iota
	// KindOrdered: a single dimension whose values are numeric (e.g. hour) —
	// charted as a histogram in numeric order.
	KindOrdered
	// KindBreakdown: a single low-cardinality categorical dimension (e.g.
	// channel_type, size, duration, category) — charted as a share/grouped bar.
	KindBreakdown
	// KindTopN: an identity dimension pair (<x>_id + <x>_name) — a high-
	// cardinality entity ranked by value (e.g. channels, DMs).
	KindTopN
)

// breakdownCardinalityLimit separates a "breakdown" (few stable categories)
// from a high-cardinality identity dimension. Above this, a lone categorical
// dimension is treated as top-N-like rather than a breakdown.
const breakdownCardinalityLimit = 24

// MetricGroup is all records sharing a (source, metric) name, plus the
// classification of how to present them.
type MetricGroup struct {
	Source  string
	Metric  string
	Kind    Kind
	DimKey  string // the categorical/ordered dimension (KindOrdered/Breakdown)
	IDKey   string // identity id dimension (KindTopN), e.g. channel_id
	NameKey string // identity name dimension (KindTopN), e.g. channel_name
	Records []store.Record
}

// classify groups records by (source, metric) and infers each group's Kind.
func classify(recs []store.Record) []MetricGroup {
	type key struct{ src, metric string }
	groups := map[key][]store.Record{}
	for _, r := range recs {
		k := key{r.Source, r.Name}
		groups[k] = append(groups[k], r)
	}

	out := make([]MetricGroup, 0, len(groups))
	for k, rs := range groups {
		out = append(out, classifyGroup(k.src, k.metric, rs))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

func classifyGroup(src, metric string, rs []store.Record) MetricGroup {
	g := MetricGroup{Source: src, Metric: metric, Records: rs}

	// Collect the distinct dimension keys present across the group.
	dimKeys := map[string]struct{}{}
	for _, r := range rs {
		for k := range r.Dimensions {
			dimKeys[k] = struct{}{}
		}
	}

	// No dimensions → scalar time series.
	if len(dimKeys) == 0 {
		g.Kind = KindScalar
		return g
	}

	// Identity pair (<x>_id + <x>_name) → top-N entities.
	if idKey, nameKey, ok := identityPair(dimKeys); ok {
		g.Kind = KindTopN
		g.IDKey, g.NameKey = idKey, nameKey
		return g
	}

	// A single categorical dimension → ordered histogram (numeric values) or
	// breakdown (few categories). Multi-dim groups fall back to breakdown on
	// their first sorted key.
	dk := firstKey(dimKeys)
	g.DimKey = dk
	if allNumericValues(rs, dk) {
		g.Kind = KindOrdered
	} else {
		g.Kind = KindBreakdown
	}
	return g
}

// identityPair detects a "<x>_id" + "<x>_name" pairing among dimension keys.
func identityPair(keys map[string]struct{}) (idKey, nameKey string, ok bool) {
	for k := range keys {
		if base, found := trimSuffix(k, "_id"); found {
			nk := base + "_name"
			if _, has := keys[nk]; has {
				return k, nk, true
			}
		}
	}
	return "", "", false
}

func trimSuffix(s, suffix string) (string, bool) {
	if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)], true
	}
	return "", false
}

func firstKey(keys map[string]struct{}) string {
	ks := make([]string, 0, len(keys))
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return ""
	}
	return ks[0]
}

// allNumericValues reports whether every record's value for dim parses as an
// integer (so the dimension is an ordered axis like hour).
func allNumericValues(rs []store.Record, dim string) bool {
	seen := false
	for _, r := range rs {
		v, ok := r.Dimensions[dim]
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(v); err != nil {
			return false
		}
		seen = true
	}
	return seen
}
