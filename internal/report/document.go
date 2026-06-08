package report

import (
	"sort"
	"strconv"
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
	Weekday StackedSeries    `json:"weekday"` // combined activity by weekday
}

// SourceHeadline is a compact per-source summary for the overview tab: the
// total of each scalar metric over the whole range.
type SourceHeadline struct {
	Source string         `json:"source"`
	Totals []LabeledValue `json:"totals"`
}

// LabeledValue is a name/number pair.
type LabeledValue struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// StackedSeries is labels (x-axis) plus one named dataset per series, used for
// stacked/grouped bar and multi-line charts.
type StackedSeries struct {
	Labels   []string      `json:"labels"`
	Datasets []NamedSeries `json:"datasets"`
}

type NamedSeries struct {
	Name string    `json:"name"`
	Data []float64 `json:"data"`
}

// SourceTab is one source's collection of charts.
type SourceTab struct {
	Source string  `json:"source"`
	Charts []Chart `json:"charts"`
}

// Chart is a single visualization, tagged by Kind so the client picks the right
// Chart.js type. Only the fields relevant to the kind are populated.
type Chart struct {
	Title string `json:"title"`
	Kind  string `json:"kind"` // "series" | "ordered" | "breakdown" | "topn"

	// Scalar time series (KindScalar): one PeriodSeries per granularity, with a
	// selectable period like the legacy report.
	Periods []NamedScalarSeries `json:"periods,omitempty"`

	// Ordered histogram (KindOrdered) and breakdown (KindBreakdown): a single
	// labeled bar set over the whole range.
	Bars []LabeledValue `json:"bars,omitempty"`

	// Top-N (KindTopN): ranked entities over the whole range.
	Top []LabeledValue `json:"top,omitempty"`
}

// NamedScalarSeries is a scalar metric's values across the buckets of one
// period granularity.
type NamedScalarSeries struct {
	Period string    `json:"period"`
	Labels []string  `json:"labels"`
	Data   []float64 `json:"data"`
}

// scalarPeriodPlan: granularities offered for scalar time-series charts.
var scalarPeriodPlan = []struct {
	period Period
	days   int
}{
	{Day, 30},
	{Week, 84},
	{Month, 365},
}

// BuildDocument assembles the full report payload from raw records. topN bounds
// any top-N chart.
func BuildDocument(recs []store.Record, generatedAt string, topN int) Document {
	doc := Document{GeneratedAt: generatedAt, Span: spanOf(recs)}

	groups := classify(recs)

	// Per-source tabs.
	bySource := map[string][]MetricGroup{}
	srcOrder := []string{}
	for _, g := range groups {
		if _, seen := bySource[g.Source]; !seen {
			srcOrder = append(srcOrder, g.Source)
		}
		bySource[g.Source] = append(bySource[g.Source], g)
	}
	sort.Strings(srcOrder)
	for _, src := range srcOrder {
		tab := SourceTab{Source: src}
		for _, g := range bySource[src] {
			tab.Charts = append(tab.Charts, chartFor(g, topN))
		}
		doc.Sources = append(doc.Sources, tab)
	}

	doc.Overview = buildOverview(recs, groups, srcOrder)
	return doc
}

// chartFor renders one classified metric group into a Chart.
func chartFor(g MetricGroup, topN int) Chart {
	switch g.Kind {
	case KindScalar:
		return Chart{Title: g.Metric, Kind: "series", Periods: scalarSeries(g.Records)}
	case KindOrdered:
		return Chart{Title: g.Metric + " by " + g.DimKey, Kind: "ordered", Bars: orderedBars(g)}
	case KindTopN:
		return Chart{Title: "top " + g.Metric, Kind: "topn", Top: topNBars(g, topN)}
	default: // KindBreakdown
		return Chart{Title: g.Metric + " by " + g.DimKey, Kind: "breakdown", Bars: breakdownBars(g)}
	}
}

// scalarSeries builds per-granularity time series for a scalar metric (summed
// per bucket).
func scalarSeries(rs []store.Record) []NamedScalarSeries {
	out := make([]NamedScalarSeries, 0, len(scalarPeriodPlan))
	for _, p := range scalarPeriodPlan {
		sums := map[string]float64{}
		labels := map[string]string{}
		for _, r := range rs {
			bk, bl := bucketOf(r.Day, p.period)
			sums[bk] += r.Value
			labels[bk] = bl
		}
		keys := sortedKeys(sums)
		s := NamedScalarSeries{Period: p.period.Title()}
		for _, k := range keys {
			s.Labels = append(s.Labels, labels[k])
			s.Data = append(s.Data, sums[k])
		}
		out = append(out, s)
	}
	return out
}

// orderedBars sums values per numeric dimension value, sorted numerically, with
// gaps filled so e.g. an hour histogram shows all 24 slots.
func orderedBars(g MetricGroup) []LabeledValue {
	sums := map[int]float64{}
	for _, r := range g.Records {
		v, err := strconv.Atoi(r.Dimensions[g.DimKey])
		if err != nil {
			continue
		}
		sums[v] += r.Value
	}
	if len(sums) == 0 {
		return nil
	}
	lo, hi := minMaxKey(sums)
	var out []LabeledValue
	for i := lo; i <= hi; i++ {
		out = append(out, LabeledValue{Label: padNum(i), Value: sums[i]})
	}
	return out
}

// breakdownBars sums values per categorical dimension value, sorted by value
// descending. DM-style group names are prettified.
func breakdownBars(g MetricGroup) []LabeledValue {
	sums := map[string]float64{}
	for _, r := range g.Records {
		sums[r.Dimensions[g.DimKey]] += r.Value
	}
	return sortedLabeled(sums)
}

// topNBars ranks entities (by id) by summed value, labeled with the prettified
// name, capped at n.
func topNBars(g MetricGroup, n int) []LabeledValue {
	type ent struct {
		name string
		typ  string
		sum  float64
	}
	byID := map[string]*ent{}
	for _, r := range g.Records {
		id := r.Dimensions[g.IDKey]
		if id == "" {
			continue
		}
		e := byID[id]
		if e == nil {
			e = &ent{name: r.Dimensions[g.NameKey], typ: r.Dimensions[dimChannelType]}
			byID[id] = e
		}
		e.sum += r.Value
	}
	out := make([]LabeledValue, 0, len(byID))
	for _, e := range byID {
		out = append(out, LabeledValue{Label: displayName(e.name, e.typ), Value: e.sum})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].Label < out[j].Label
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// buildOverview produces per-source headline totals (sum of each scalar metric)
// and a combined weekday-activity chart across sources.
func buildOverview(recs []store.Record, groups []MetricGroup, srcOrder []string) Overview {
	// Headlines: sum of each scalar metric per source.
	scalarTotals := map[string]map[string]float64{} // source -> metric -> total
	for _, g := range groups {
		if g.Kind != KindScalar {
			continue
		}
		if scalarTotals[g.Source] == nil {
			scalarTotals[g.Source] = map[string]float64{}
		}
		for _, r := range g.Records {
			scalarTotals[g.Source][g.Metric] += r.Value
		}
	}
	var ov Overview
	for _, src := range srcOrder {
		h := SourceHeadline{Source: src}
		metrics := scalarTotals[src]
		for _, m := range sortedKeys(metrics) {
			h.Totals = append(h.Totals, LabeledValue{Label: m, Value: metrics[m]})
		}
		ov.Sources = append(ov.Sources, h)
	}

	ov.Weekday = combinedWeekday(recs, srcOrder)
	return ov
}

// combinedWeekday builds a Mon–Sun activity chart with one dataset per source.
// "Activity" is the source's primary volume metric (the largest scalar by
// total), so each source contributes a comparable single line.
func combinedWeekday(recs []store.Record, srcOrder []string) StackedSeries {
	primary := primaryMetricBySource(recs)
	bySrcDay := map[string]map[time.Weekday]float64{}
	for _, r := range recs {
		if r.Name != primary[r.Source] {
			continue
		}
		if bySrcDay[r.Source] == nil {
			bySrcDay[r.Source] = map[time.Weekday]float64{}
		}
		bySrcDay[r.Source][r.Day.Weekday()] += r.Value
	}

	ss := StackedSeries{}
	for _, wd := range weekdayOrder {
		ss.Labels = append(ss.Labels, wd.String()[:3])
	}
	for _, src := range srcOrder {
		day := bySrcDay[src]
		if day == nil {
			continue
		}
		ds := NamedSeries{Name: src}
		for _, wd := range weekdayOrder {
			ds.Data = append(ds.Data, day[wd])
		}
		ss.Datasets = append(ss.Datasets, ds)
	}
	return ss
}

// primaryMetricBySource picks each source's highest-volume scalar metric, used
// as its representative "activity" line in cross-source charts.
func primaryMetricBySource(recs []store.Record) map[string]string {
	// Sum scalar (dimensionless) metrics per source+metric.
	totals := map[string]map[string]float64{}
	for _, r := range recs {
		if len(r.Dimensions) != 0 {
			continue
		}
		if totals[r.Source] == nil {
			totals[r.Source] = map[string]float64{}
		}
		totals[r.Source][r.Name] += r.Value
	}
	out := map[string]string{}
	for src, m := range totals {
		best, bestV := "", -1.0
		for _, name := range sortedKeys(m) {
			if m[name] > bestV {
				best, bestV = name, m[name]
			}
		}
		out[src] = best
	}
	return out
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

func sortedLabeled(m map[string]float64) []LabeledValue {
	out := make([]LabeledValue, 0, len(m))
	for k, v := range m {
		out = append(out, LabeledValue{Label: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func minMaxKey(m map[int]float64) (lo, hi int) {
	first := true
	for k := range m {
		if first {
			lo, hi, first = k, k, false
			continue
		}
		if k < lo {
			lo = k
		}
		if k > hi {
			hi = k
		}
	}
	return lo, hi
}

func padNum(i int) string {
	if i < 10 && i >= 0 {
		return "0" + strconv.Itoa(i)
	}
	return strconv.Itoa(i)
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
