package report

import (
	"sort"
	"time"

	"github.com/nachmore/commstats/internal/store"
)

// This file holds the exported chart-builder helpers a source's Reporter
// composes to assemble its curated charts. Each takes already-filtered records
// (typically a single metric name) and returns a Chart.

// ScalarSeriesChart builds a multi-granularity time-series chart that sums the
// records' values per time bucket. Use for dimensionless metrics like
// emails_sent, or to total a dimensioned metric's volume over time.
func ScalarSeriesChart(title string, recs []store.Record) Chart {
	return Chart{Title: title, Kind: "series", Periods: scalarSeries(recs)}
}

// DistinctSeriesChart builds a time-series of the distinct count of a dimension
// value per bucket (e.g. unique channels/day). Unlike ScalarSeriesChart it
// counts distinct dim values rather than summing record values.
func DistinctSeriesChart(title, dimKey string, recs []store.Record) Chart {
	out := make([]NamedScalarSeries, 0, len(scalarPeriodPlan))
	for _, p := range scalarPeriodPlan {
		sets := map[string]map[string]struct{}{}
		labels := map[string]string{}
		for _, r := range recs {
			id := r.Dimensions[dimKey]
			if id == "" {
				continue
			}
			bk, bl := bucketOf(r.Day, p.period)
			if sets[bk] == nil {
				sets[bk] = map[string]struct{}{}
			}
			sets[bk][id] = struct{}{}
			labels[bk] = bl
		}
		keys := make([]string, 0, len(sets))
		for k := range sets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		s := NamedScalarSeries{Period: p.period.Title()}
		for _, k := range keys {
			s.Labels = append(s.Labels, labels[k])
			s.Data = append(s.Data, float64(len(sets[k])))
		}
		out = append(out, s)
	}
	return Chart{Title: title, Kind: "series", Periods: out}
}

// BreakdownChart sums values per categorical dimension value, sorted by value
// descending. Use for channel_type, meeting size/duration/category, etc.
func BreakdownChart(title, dimKey string, recs []store.Record) Chart {
	sums := map[string]float64{}
	for _, r := range recs {
		v, ok := r.Dimensions[dimKey]
		if !ok {
			continue
		}
		sums[v] += r.Value
	}
	return Chart{Title: title, Kind: "breakdown", Bars: sortedLabeled(sums)}
}

// OrderedChart sums values per numeric dimension value, in numeric order with
// gaps filled (e.g. an hour-of-day histogram showing all 24 slots).
func OrderedChart(title, dimKey string, recs []store.Record) Chart {
	g := MetricGroup{DimKey: dimKey, Records: recs}
	return Chart{Title: title, Kind: "ordered", Bars: orderedBars(g)}
}

// TopNChart ranks entities (identified by idKey, labeled via nameKey) by summed
// value, capped at n. keep, if non-nil, filters which records participate —
// e.g. to split top channels from top DMs by channel_type. Labels are passed
// through displayName for #/@ prefixing and group-DM prettifying.
func TopNChart(title, idKey, nameKey string, recs []store.Record, n int, keep func(store.Record) bool) Chart {
	g := MetricGroup{IDKey: idKey, NameKey: nameKey}
	for _, r := range recs {
		if keep == nil || keep(r) {
			g.Records = append(g.Records, r)
		}
	}
	return Chart{Title: title, Kind: "topn", Top: topNBars(g, n)}
}

// WeekdayChart builds an average-per-weekday bar chart from a metric, averaging
// each weekday's total over how many of that weekday appear in the data span.
func WeekdayChart(title string, recs []store.Record) Chart {
	total := map[time.Weekday]float64{}
	var lo, hi time.Time
	for _, r := range recs {
		total[r.Day.Weekday()] += r.Value
		if lo.IsZero() || r.Day.Before(lo) {
			lo = r.Day
		}
		if hi.IsZero() || r.Day.After(hi) {
			hi = r.Day
		}
	}
	occur := map[time.Weekday]int{}
	if !lo.IsZero() {
		for d := lo; !d.After(hi); d = d.AddDate(0, 0, 1) {
			occur[d.Weekday()]++
		}
	}
	var bars []LabeledValue
	for _, wd := range weekdayOrder {
		avg := 0.0
		if n := occur[wd]; n > 0 {
			avg = total[wd] / float64(n)
		}
		bars = append(bars, LabeledValue{Label: wd.String()[:3], Value: avg})
	}
	return Chart{Title: title, Kind: "ordered", Bars: bars}
}

// Records filtering helpers a Reporter can use to slice its input by metric or
// dimension presence before handing to a builder.

// WithMetric returns records whose metric Name matches.
func WithMetric(recs []store.Record, name string) []store.Record {
	out := recs[:0:0]
	for _, r := range recs {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

// SumValues totals the values of the given records (for headline figures).
func SumValues(recs []store.Record) float64 {
	var s float64
	for _, r := range recs {
		s += r.Value
	}
	return s
}

// DistinctDim counts distinct values of a dimension across records (for
// headline figures like total unique channels).
func DistinctDim(recs []store.Record, dimKey string) int {
	set := map[string]struct{}{}
	for _, r := range recs {
		if v := r.Dimensions[dimKey]; v != "" {
			set[v] = struct{}{}
		}
	}
	return len(set)
}

// IsChannelType reports whether a record's channel_type is a real channel, for
// use as a TopNChart keep filter. (DMs are everything else.)
func IsChannelType(r store.Record, realChannel bool) bool {
	return isRealChannel(r.Dimensions[dimChannelType]) == realChannel
}
