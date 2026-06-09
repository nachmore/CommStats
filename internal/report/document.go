package report

import (
	"sort"
	"time"

	"github.com/nachmore/commstats/internal/store"
)

// Document is the serializable payload backing the HTML report: a cross-source
// overview plus one tab per source, each a list of charts derived generically
// from the stored metrics' shapes.
type Document struct {
	GeneratedAt string      `json:"generated_at"`
	Span        string      `json:"span"`
	Overview    Overview    `json:"overview"`
	Sources     []SourceTab `json:"sources"`
}

// Overview is the cross-source summary tab.
type Overview struct {
	Sources []SourceHeadline `json:"sources"` // per-source headline totals
	// Activity holds, per source, the raw daily points the client windows and
	// normalizes for the combined weekday/hour charts.
	Activity map[string]SourceActivity `json:"activity"`
}

// SourceActivity carries a source's daily points for the combined overview
// charts: weekday uses the primary metric's daily values; hour uses the hour
// metric's points (keyed by hour).
type SourceActivity struct {
	Weekday []DayPoint `json:"weekday"`
	Hour    []DayPoint `json:"hour"`
}

// SourceHeadline is a compact per-source summary for the overview tab. Label is
// app-qualified; Stats carry raw daily points so the client recomputes them for
// the selected window.
type SourceHeadline struct {
	Source string         `json:"source"`
	Label  string         `json:"label"`
	Stats  []HeadlineStat `json:"stats"`
}

// HeadlineStat is one windowable summary figure. Points are the daily data; the
// client reduces them over the selected window per Reduce:
//   - "sum":       total of point values
//   - "distinct":  count of distinct point keys
//   - "afterhours": % of value outside business hours / on weekends (points are
//     hour-keyed, like an hour metric)
//
// Pct marks the result as a percentage for display.
type HeadlineStat struct {
	Label  string     `json:"label"`
	Reduce string     `json:"reduce"`
	Pct    bool       `json:"pct,omitempty"`
	Points []DayPoint `json:"points"`
}

// SourceTab is one source's collection of charts. Source is the stable key;
// Label is the display name, qualified with the app (e.g. "email (Outlook)").
type SourceTab struct {
	Source string  `json:"source"`
	Label  string  `json:"label"`
	Charts []Chart `json:"charts"`
}

// Chart is a single visualization. It ships raw daily data points; the client
// filters them by the global lookback window and aggregates by the global
// granularity, so one control drives every chart. Kind tells the client how to
// shape the aggregated data, and Agg how to combine days within a bucket.
type Chart struct {
	Title string `json:"title"`
	// Kind: "series" (time line), "breakdown" (categorical bars/doughnut),
	// "ordered" (numeric-keyed histogram, e.g. hour), "topn" (ranked entities),
	// "weekday" (avg per weekday), "dual" (two series, dual y-axis).
	Kind string `json:"kind"`
	// Agg: how to combine multiple days that fall in the same bucket/key —
	// "sum" or "distinct" (count distinct keys).
	Agg string `json:"agg"`
	// TopN bounds topn charts (0 = unlimited).
	TopN int `json:"topn,omitempty"`
	// Points are the raw daily data. For each kind, Key carries:
	//   series   : the series name (often "" for a single line)
	//   breakdown: the category
	//   ordered  : the numeric bucket (e.g. hour "09")
	//   topn     : the entity id (display name resolved via Labels)
	//   weekday  : "" (the date's weekday is derived client-side)
	Points []DayPoint `json:"points"`
	// Labels maps a Point Key to a display label (used by topn).
	Labels map[string]string `json:"labels,omitempty"`
	// Right is an optional second series rendered on a right-hand y-axis (dual).
	Right *DualSeries `json:"right,omitempty"`
}

// DayPoint is one day's contribution to a chart.
type DayPoint struct {
	Date  string  `json:"d"` // YYYY-MM-DD
	Key   string  `json:"k,omitempty"`
	Value float64 `json:"v"`
}

// DualSeries is the right-axis series of a dual-axis chart.
type DualSeries struct {
	Name   string     `json:"name"`
	Agg    string     `json:"agg"`
	Points []DayPoint `json:"points"`
}

// BuildDocument assembles the full report payload from raw records. topN bounds
// any top-N chart.
func BuildDocument(recs []store.Record, generatedAt string, topN int) Document {
	doc := Document{GeneratedAt: generatedAt, Span: spanOf(recs)}

	recsBySource, srcOrder := recordsBySource(recs)

	// Per-source tabs: a registered curated Reporter wins; otherwise fall back
	// to generic shape-based classification.
	for _, src := range srcOrder {
		tab := SourceTab{Source: src, Label: sourceLabel(src)}
		if r, ok := reporterFor(src); ok {
			tab.Charts = r.Charts(recsBySource[src], topN)
		} else {
			for _, g := range classify(recsBySource[src]) {
				tab.Charts = append(tab.Charts, chartFor(g, topN))
			}
		}
		doc.Sources = append(doc.Sources, tab)
	}

	doc.Overview = buildOverview(recsBySource, srcOrder)
	return doc
}

// chartFor renders one classified metric group into a Chart by delegating to
// the same builders the curated reporters use.
func chartFor(g MetricGroup, topN int) Chart {
	switch g.Kind {
	case KindScalar:
		return ScalarSeriesChart(g.Metric, g.Records)
	case KindOrdered:
		return OrderedChart(g.Metric+" by "+g.DimKey, g.DimKey, g.Records)
	case KindTopN:
		return TopNChart("top "+g.Metric, g.IDKey, g.NameKey, g.Records, topN, nil)
	default: // KindBreakdown
		return BreakdownChart(g.Metric+" by "+g.DimKey, g.DimKey, g.Records)
	}
}

// buildOverview produces per-source headline totals and a combined weekday-
// activity chart across sources. A source's registered Reporter supplies its
// own headline figures (it knows which metrics are canonical); otherwise we
// fall back to summing each dimensionless scalar metric.
func buildOverview(recsBySource map[string][]store.Record, srcOrder []string) Overview {
	ov := Overview{Activity: map[string]SourceActivity{}}
	for _, src := range srcOrder {
		h := SourceHeadline{Source: src, Label: sourceLabel(src)}
		if r, ok := reporterFor(src); ok {
			h.Stats = r.Headline(recsBySource[src])
		} else {
			h.Stats = genericHeadline(recsBySource[src])
		}
		ov.Sources = append(ov.Sources, h)
		ov.Activity[src] = sourceActivity(src, recsBySource[src])
	}
	return ov
}

// sourceActivity extracts a source's daily activity points for the combined
// overview charts: weekday from its primary metric (daily values), hour from
// its declared hour metric (keyed by hour). The client windows + normalizes.
func sourceActivity(src string, recs []store.Record) SourceActivity {
	var sa SourceActivity
	if metric := primaryMetric(src, recs); metric != "" {
		for _, r := range recs {
			if r.Name == metric {
				sa.Weekday = append(sa.Weekday, DayPoint{Date: r.Day.Format("2006-01-02"), Value: r.Value})
			}
		}
	}
	if r, ok := reporterFor(src); ok {
		if hm, ok := r.(HourMetricer); ok {
			hmetric := hm.HourMetric()
			for _, rec := range recs {
				if rec.Name == hmetric {
					sa.Hour = append(sa.Hour, DayPoint{
						Date: rec.Day.Format("2006-01-02"), Key: rec.Dimensions["hour"], Value: rec.Value})
				}
			}
		}
	}
	return sa
}

// HourMetricer lets a curated source declare an hour-bucketed metric (with a
// numeric "hour" dimension) to contribute to the overview's combined busiest-
// hours chart.
type HourMetricer interface {
	HourMetric() string
}

// genericHeadline emits a windowable sum stat for each dimensionless scalar
// metric, for sources without a curated reporter.
func genericHeadline(recs []store.Record) []HeadlineStat {
	byMetric := map[string][]store.Record{}
	for _, r := range recs {
		if len(r.Dimensions) == 0 {
			byMetric[r.Name] = append(byMetric[r.Name], r)
		}
	}
	var out []HeadlineStat
	for _, m := range sortedKeysS(byMetric) {
		out = append(out, SumStat(m, byMetric[m]))
	}
	return out
}

// PrimaryMetricer lets a curated source declare its representative volume
// metric for cross-source "activity" charts. It must be a single-partition
// metric (one whose records sum cleanly per day, e.g. Slack "messages").
type PrimaryMetricer interface {
	PrimaryMetric() string
}

// primaryMetric returns the metric name to use as a source's activity line:
// the reporter's declared PrimaryMetric if it implements PrimaryMetricer,
// otherwise the highest-volume dimensionless scalar metric.
func primaryMetric(src string, recs []store.Record) string {
	if r, ok := reporterFor(src); ok {
		if pm, ok := r.(PrimaryMetricer); ok {
			return pm.PrimaryMetric()
		}
	}
	totals := map[string]float64{}
	for _, r := range recs {
		if len(r.Dimensions) == 0 {
			totals[r.Name] += r.Value
		}
	}
	best, bestV := "", -1.0
	for _, name := range sortedKeys(totals) {
		if totals[name] > bestV {
			best, bestV = name, totals[name]
		}
	}
	return best
}

// --- small helpers ---

func sortedKeys(m map[string]float64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedKeysS(m map[string][]store.Record) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func spanOf(recs []store.Record) string {
	var lo, hi time.Time
	for _, r := range recs {
		if lo.IsZero() || r.Day.Before(lo) {
			lo = r.Day
		}
		if hi.IsZero() || r.Day.After(hi) {
			hi = r.Day
		}
	}
	if lo.IsZero() {
		return "no data"
	}
	return lo.Format("2006-01-02") + " to " + hi.Format("2006-01-02")
}
