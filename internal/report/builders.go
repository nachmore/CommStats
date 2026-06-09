package report

import (
	"strconv"
	"time"

	"github.com/nachmore/commstats/internal/store"
)

// This file holds the exported chart-builder helpers a source's Reporter
// composes to assemble its curated charts. Each emits raw daily DayPoints; the
// client windows + aggregates them per the global controls.

// dayStr formats a record's day as YYYY-MM-DD.
func dayStr(r store.Record) string { return r.Day.Format("2006-01-02") }

// ScalarSeriesChart is a single time series summing the records' values per day.
func ScalarSeriesChart(title string, recs []store.Record) Chart {
	pts := make([]DayPoint, 0, len(recs))
	for _, r := range recs {
		pts = append(pts, DayPoint{Date: dayStr(r), Value: r.Value})
	}
	return Chart{Title: title, Kind: "series", Agg: "sum", Points: pts}
}

// DistinctSeriesChart is a time series of the distinct count of a dimension's
// values per bucket (e.g. unique channels/day). The client counts distinct Keys
// within each bucket.
func DistinctSeriesChart(title, dimKey string, recs []store.Record) Chart {
	pts := make([]DayPoint, 0, len(recs))
	for _, r := range recs {
		id := r.Dimensions[dimKey]
		if id == "" {
			continue
		}
		pts = append(pts, DayPoint{Date: dayStr(r), Key: id, Value: 1})
	}
	return Chart{Title: title, Kind: "series", Agg: "distinct", Points: pts}
}

// DualSeriesChart combines two summed time series on left/right y-axes (e.g.
// zoom minutes + meetings, slack messages + unique channels). The right series
// may sum or count-distinct.
func DualSeriesChart(title, leftName string, left []store.Record, rightName, rightAgg, rightDimKey string, right []store.Record) Chart {
	lp := make([]DayPoint, 0, len(left))
	for _, r := range left {
		lp = append(lp, DayPoint{Date: dayStr(r), Value: r.Value})
	}
	rp := make([]DayPoint, 0, len(right))
	for _, r := range right {
		if rightAgg == "distinct" {
			id := r.Dimensions[rightDimKey]
			if id == "" {
				continue
			}
			rp = append(rp, DayPoint{Date: dayStr(r), Key: id, Value: 1})
		} else {
			rp = append(rp, DayPoint{Date: dayStr(r), Value: r.Value})
		}
	}
	return Chart{
		Title: title, Kind: "dual", Agg: "sum",
		Points: lp,
		Labels: map[string]string{"left": leftName, "right": rightName},
		Right:  &DualSeries{Name: rightName, Agg: rightAgg, Points: rp},
	}
}

// BreakdownChart sums values per categorical dimension value (rendered as bars
// or doughnut). doughnut controls the client rendering hint.
func BreakdownChart(title, dimKey string, recs []store.Record) Chart {
	return breakdown(title, dimKey, recs, "breakdown")
}

// DoughnutChart is a compositional breakdown rendered as a doughnut.
func DoughnutChart(title, dimKey string, recs []store.Record) Chart {
	return breakdown(title, dimKey, recs, "doughnut")
}

func breakdown(title, dimKey string, recs []store.Record, kind string) Chart {
	pts := make([]DayPoint, 0, len(recs))
	for _, r := range recs {
		v, ok := r.Dimensions[dimKey]
		if !ok {
			continue
		}
		pts = append(pts, DayPoint{Date: dayStr(r), Key: v, Value: r.Value})
	}
	return Chart{Title: title, Kind: kind, Agg: "sum", Points: pts}
}

// OrderedChart sums values per numeric dimension value (e.g. hour-of-day),
// rendered as a histogram ordered by the numeric key.
func OrderedChart(title, dimKey string, recs []store.Record) Chart {
	pts := make([]DayPoint, 0, len(recs))
	for _, r := range recs {
		v, ok := r.Dimensions[dimKey]
		if !ok {
			continue
		}
		pts = append(pts, DayPoint{Date: dayStr(r), Key: v, Value: r.Value})
	}
	return Chart{Title: title, Kind: "ordered", Agg: "sum", Points: pts}
}

// WeekdayChart shows average value per weekday over the windowed range. The
// client derives each point's weekday from its date and averages.
func WeekdayChart(title string, recs []store.Record) Chart {
	pts := make([]DayPoint, 0, len(recs))
	for _, r := range recs {
		pts = append(pts, DayPoint{Date: dayStr(r), Value: r.Value})
	}
	return Chart{Title: title, Kind: "weekday", Agg: "sum", Points: pts}
}

// TopNChart ranks entities (id via idKey, label via nameKey) by summed value,
// capped at n. keep filters participating records. Labels carry display names
// (#/@ prefixing, group-DM prettifying).
func TopNChart(title, idKey, nameKey string, recs []store.Record, n int, keep func(store.Record) bool) Chart {
	pts := make([]DayPoint, 0, len(recs))
	labels := map[string]string{}
	for _, r := range recs {
		if keep != nil && !keep(r) {
			continue
		}
		id := r.Dimensions[idKey]
		if id == "" {
			continue
		}
		pts = append(pts, DayPoint{Date: dayStr(r), Key: id, Value: r.Value})
		if _, ok := labels[id]; !ok {
			labels[id] = displayName(r.Dimensions[nameKey], r.Dimensions[dimChannelType])
		}
	}
	return Chart{Title: title, Kind: "topn", Agg: "sum", TopN: n, Points: pts, Labels: labels}
}

// Records filtering helpers a Reporter can use to slice its input before
// handing to a builder.

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

// DistinctDim counts distinct values of a dimension across records.
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

// businessStart/businessEnd bound "working hours" (local); activity outside is
// counted as after-hours.
const businessStart, businessEnd = 8, 18

// AfterHoursPct returns the percentage (0-100) of an hour-keyed metric's volume
// that falls outside business hours OR on a weekend — a boundary-erosion
// signal. recs must be the hour-bucketed metric (dimension "hour"); weekend is
// derived from each record's day.
func AfterHoursPct(recs []store.Record) float64 {
	var after, total float64
	for _, r := range recs {
		h, err := strconv.Atoi(r.Dimensions["hour"])
		if err != nil {
			continue
		}
		total += r.Value
		weekend := r.Day.Weekday() == time.Sunday || r.Day.Weekday() == time.Saturday
		if weekend || h < businessStart || h >= businessEnd {
			after += r.Value
		}
	}
	if total == 0 {
		return 0
	}
	return after / total * 100
}
