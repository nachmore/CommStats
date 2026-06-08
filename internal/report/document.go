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
	Hour    StackedSeries    `json:"hour"`    // combined activity by hour-of-day
}

// SourceHeadline is a compact per-source summary for the overview tab: the
// total of each scalar metric over the whole range. Label is app-qualified.
type SourceHeadline struct {
	Source string         `json:"source"`
	Label  string         `json:"label"`
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

// SourceTab is one source's collection of charts. Source is the stable key;
// Label is the display name, qualified with the app (e.g. "email (Outlook)").
type SourceTab struct {
	Source string  `json:"source"`
	Label  string  `json:"label"`
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

// buildOverview produces per-source headline totals and a combined weekday-
// activity chart across sources. A source's registered Reporter supplies its
// own headline figures (it knows which metrics are canonical); otherwise we
// fall back to summing each dimensionless scalar metric.
func buildOverview(recsBySource map[string][]store.Record, srcOrder []string) Overview {
	var ov Overview
	for _, src := range srcOrder {
		h := SourceHeadline{Source: src, Label: sourceLabel(src)}
		if r, ok := reporterFor(src); ok {
			h.Totals = r.Headline(recsBySource[src])
		} else {
			h.Totals = genericHeadline(recsBySource[src])
		}
		ov.Sources = append(ov.Sources, h)
	}
	ov.Weekday = combinedWeekday(recsBySource, srcOrder)
	ov.Hour = combinedHour(recsBySource, srcOrder)
	return ov
}

// HourMetricer lets a curated source declare an hour-bucketed metric (with a
// numeric "hour" dimension) to contribute to the overview's combined busiest-
// hours chart.
type HourMetricer interface {
	HourMetric() string
}

// combinedHour builds a 0–23 activity chart with one dataset per source that
// declares an HourMetric, summing that metric's values per hour.
func combinedHour(recsBySource map[string][]store.Record, srcOrder []string) StackedSeries {
	ss := StackedSeries{}
	for h := 0; h < 24; h++ {
		ss.Labels = append(ss.Labels, padNum(h))
	}
	for _, src := range srcOrder {
		r, ok := reporterFor(src)
		if !ok {
			continue
		}
		hm, ok := r.(HourMetricer)
		if !ok {
			continue
		}
		metric := hm.HourMetric()
		hours := make([]float64, 24)
		found := false
		for _, rec := range recsBySource[src] {
			if rec.Name != metric {
				continue
			}
			h, err := strconv.Atoi(rec.Dimensions["hour"])
			if err != nil || h < 0 || h > 23 {
				continue
			}
			hours[h] += rec.Value
			found = true
		}
		if found {
			ss.Datasets = append(ss.Datasets, NamedSeries{Name: src, Data: hours})
		}
	}
	return ss
}

// genericHeadline sums each dimensionless scalar metric for sources without a
// curated reporter.
func genericHeadline(recs []store.Record) []LabeledValue {
	totals := map[string]float64{}
	for _, r := range recs {
		if len(r.Dimensions) == 0 {
			totals[r.Name] += r.Value
		}
	}
	var out []LabeledValue
	for _, m := range sortedKeys(totals) {
		out = append(out, LabeledValue{Label: m, Value: totals[m]})
	}
	return out
}

// combinedWeekday builds a Mon–Sun activity chart with one dataset per source.
// "Activity" is the source's primary volume metric (the largest scalar by
// total), so each source contributes a comparable single line.
func combinedWeekday(recsBySource map[string][]store.Record, srcOrder []string) StackedSeries {
	ss := StackedSeries{}
	for _, wd := range weekdayOrder {
		ss.Labels = append(ss.Labels, wd.String()[:3])
	}
	for _, src := range srcOrder {
		metric := primaryMetric(src, recsBySource[src])
		if metric == "" {
			continue
		}
		day := map[time.Weekday]float64{}
		for _, r := range recsBySource[src] {
			if r.Name == metric {
				day[r.Day.Weekday()] += r.Value
			}
		}
		ds := NamedSeries{Name: src}
		for _, wd := range weekdayOrder {
			ds.Data = append(ds.Data, day[wd])
		}
		ss.Datasets = append(ss.Datasets, ds)
	}
	return ss
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
