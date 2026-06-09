package zoom

import (
	"github.com/nachmore/commstats/internal/report"
	"github.com/nachmore/commstats/internal/store"
)

func init() { report.RegisterReporter("zoom", zoomReporter{}) }

// zoomReporter curates Zoom's report tab from the usage-report-derived metrics:
// meeting/minute volume over time, size (by actual attendance) and duration
// breakdowns, and by-hour/weekday.
type zoomReporter struct{}

func (zoomReporter) AppName() string       { return "Zoom" }
func (zoomReporter) PrimaryMetric() string { return "zoom_minutes" }
func (zoomReporter) HourMetric() string    { return "meetings_by_hour" }

func (zoomReporter) Headline(recs []store.Record) []report.LabeledValue {
	return []report.LabeledValue{
		{Label: "meetings", Value: report.SumValues(report.WithMetric(recs, "zoom_meetings"))},
		{Label: "minutes", Value: report.SumValues(report.WithMetric(recs, "zoom_minutes"))},
		{Label: "participant minutes", Value: report.SumValues(report.WithMetric(recs, "participant_minutes"))},
		{Label: "after-hours %", Value: report.AfterHoursPct(report.WithMetric(recs, "meetings_by_hour"))},
	}
}

func (zoomReporter) Charts(recs []store.Record, topN int) []report.Chart {
	meetings := report.WithMetric(recs, "meetings")
	return []report.Chart{
		report.DualSeriesChart("minutes & meetings", "minutes", report.WithMetric(recs, "zoom_minutes"),
			"meetings", "sum", "", report.WithMetric(recs, "zoom_meetings")),
		report.DoughnutChart("meetings by attendance", "size", withDim(meetings, "size")),
		report.BreakdownChart("meetings by duration", "duration", withDim(meetings, "duration")),
		report.OrderedChart("meetings by hour", "hour", report.WithMetric(recs, "meetings_by_hour")),
		report.WeekdayChart("avg meeting minutes/day by weekday", report.WithMetric(recs, "zoom_minutes")),
	}
}

// withDim returns records carrying the given dimension key, so a breakdown only
// sees its own partition of the multi-partition "meetings" metric.
func withDim(recs []store.Record, dimKey string) []store.Record {
	out := recs[:0:0]
	for _, r := range recs {
		if _, ok := r.Dimensions[dimKey]; ok {
			out = append(out, r)
		}
	}
	return out
}
