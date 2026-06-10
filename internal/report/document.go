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
	// EstTime holds, per source, daily estimated-minutes points so the client
	// can window them into the "where does my time go" time-spent views.
	EstTime map[string][]DayPoint `json:"est_time"`
	// Charts are pre-built cross-source overview visualizations (meeting hours by
	// category/scope/size, calendar-vs-zoom, focus time). They reuse the same
	// Chart shape as source tabs, so the client windows + aggregates them with
	// the global controls; their values are already scaled to display units.
	Charts []Chart `json:"charts"`
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

// buildOverview produces per-source headline totals plus the cross-source
// overview charts. A source's registered Reporter supplies its own headline
// figures (it knows which metrics are canonical); otherwise we fall back to
// summing each dimensionless scalar metric.
func buildOverview(recsBySource map[string][]store.Record, srcOrder []string) Overview {
	ov := Overview{EstTime: map[string][]DayPoint{}}
	for _, src := range srcOrder {
		h := SourceHeadline{Source: src, Label: sourceLabel(src)}
		if r, ok := reporterFor(src); ok {
			h.Stats = r.Headline(recsBySource[src])
			if et, ok := r.(EstimatedTimer); ok {
				if pts := et.EstimatedMinutes(recsBySource[src]); len(pts) > 0 {
					ov.EstTime[src] = pts
				}
			}
		} else {
			h.Stats = genericHeadline(recsBySource[src])
		}
		ov.Sources = append(ov.Sources, h)
	}
	ov.Charts = overviewCharts(recsBySource)
	return ov
}

// overviewCharts builds the cross-source landing visualizations from the stored
// metrics. Minute-valued metrics are scaled to hours so the charts read in the
// same unit as the time-spent views.
func overviewCharts(recsBySource map[string][]store.Record) []Chart {
	cal := recsBySource["calendar"]
	zoom := recsBySource["zoom"]

	minByCat := withDimKey(WithMetric(cal, "meeting_minutes_by"), "category")
	minByScope := withDimKey(WithMetric(cal, "meeting_minutes_by"), "scope")
	minBySize := withDimKey(WithMetric(cal, "meeting_minutes_by"), "size")

	var charts []Chart
	if len(minByCat) > 0 {
		charts = append(charts, hoursDoughnut("Meeting hours by category", "category", minByCat))
	}
	// Calendar (de-overlapped busy) vs Zoom minutes over time, both in hours.
	calBusy := WithMetric(cal, "meeting_busy_minutes")
	zoomMin := WithMetric(zoom, "zoom_minutes")
	if len(calBusy) > 0 || len(zoomMin) > 0 {
		c := DualSeriesChart("Calendar vs Zoom hours", "calendar hours", scaleRecords(calBusy, 1.0/60),
			"zoom hours", "sum", "", scaleRecords(zoomMin, 1.0/60))
		charts = append(charts, c)
	}
	if focus := WithMetric(cal, "focus_minutes"); len(focus) > 0 {
		c := ScalarSeriesChart("Daily focus time (longest meeting-free block, hours)", scaleRecords(focus, 1.0/60))
		charts = append(charts, c)
	}
	if len(minByScope) > 0 {
		charts = append(charts, hoursBreakdown("Meeting hours: internal vs external", "scope", minByScope))
	}
	if len(minBySize) > 0 {
		charts = append(charts, hoursBreakdown("Meeting hours by size", "size", minBySize))
	}
	return charts
}

// scaleRecords returns copies of recs with values multiplied by factor — used
// to convert minute metrics to hours before charting.
func scaleRecords(recs []store.Record, factor float64) []store.Record {
	out := make([]store.Record, len(recs))
	for i, r := range recs {
		r.Value *= factor
		out[i] = r
	}
	return out
}

// hoursDoughnut / hoursBreakdown chart a minute-valued, dimensioned metric as
// hours, summed per dimension value.
func hoursDoughnut(title, dimKey string, recs []store.Record) Chart {
	return DoughnutChart(title, dimKey, scaleRecords(recs, 1.0/60))
}
func hoursBreakdown(title, dimKey string, recs []store.Record) Chart {
	return BreakdownChart(title, dimKey, scaleRecords(recs, 1.0/60))
}

// withDimKey returns records carrying the given dimension key (the
// "meeting_minutes_by" metric is emitted with several disjoint dimension keys).
func withDimKey(recs []store.Record, dimKey string) []store.Record {
	out := recs[:0:0]
	for _, r := range recs {
		if _, ok := r.Dimensions[dimKey]; ok {
			out = append(out, r)
		}
	}
	return out
}

// EstimatedTimer lets a source contribute daily estimated-minutes points to the
// overview's "where does my time go" views. Meetings report actual minutes;
// message/email sources apply a documented per-item heuristic.
type EstimatedTimer interface {
	EstimatedMinutes(recs []store.Record) []DayPoint
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

// --- small helpers ---

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
