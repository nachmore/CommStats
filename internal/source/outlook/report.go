package outlook

import (
	"github.com/nachmore/commstats/internal/report"
	"github.com/nachmore/commstats/internal/store"
)

func init() { report.RegisterReporter("outlook", outlookReporter{}) }

// outlookReporter curates Outlook's report tab. The "meetings" metric is
// emitted as several independent partitions (by size, duration, role, response,
// scope, category) of the same events, so summing all meetings rows would
// multi-count — only the size partition is the canonical headcount.
type outlookReporter struct{}

// PrimaryMetric: emails sent is Outlook's activity line for the overview.
func (outlookReporter) PrimaryMetric() string { return "emails_sent" }

func (outlookReporter) Headline(recs []store.Record) []report.LabeledValue {
	meetings := report.WithMetric(recs, "meetings")
	// Canonical meeting count = total across the size partition only.
	meetingCount := report.SumValues(withDim(meetings, "size"))
	return []report.LabeledValue{
		{Label: "emails received", Value: report.SumValues(report.WithMetric(recs, "emails_received"))},
		{Label: "emails sent", Value: report.SumValues(report.WithMetric(recs, "emails_sent"))},
		{Label: "emails read", Value: report.SumValues(report.WithMetric(recs, "emails_read"))},
		{Label: "meetings", Value: meetingCount},
		{Label: "meeting minutes", Value: report.SumValues(report.WithMetric(recs, "meeting_minutes"))},
		{Label: "overbookings", Value: report.SumValues(report.WithMetric(recs, "calendar_overbookings"))},
	}
}

func (outlookReporter) Charts(recs []store.Record, topN int) []report.Chart {
	meetings := report.WithMetric(recs, "meetings")

	return []report.Chart{
		// Email volume over time.
		report.ScalarSeriesChart("emails received", report.WithMetric(recs, "emails_received")),
		report.ScalarSeriesChart("emails sent", report.WithMetric(recs, "emails_sent")),
		report.ScalarSeriesChart("emails read", report.WithMetric(recs, "emails_read")),

		// Meeting breakdowns (each its own partition of the events).
		report.BreakdownChart("meetings by size", "size", withDim(meetings, "size")),
		report.BreakdownChart("meetings by duration", "duration", withDim(meetings, "duration")),
		report.BreakdownChart("meetings by role", "role", withDim(meetings, "role")),
		report.BreakdownChart("meetings by response", "response", withDim(meetings, "response")),
		report.BreakdownChart("internal vs external", "scope", withDim(meetings, "scope")),
		report.BreakdownChart("meetings by category", "category", withDim(meetings, "category")),

		// Calendar load over time.
		report.ScalarSeriesChart("meeting minutes", report.WithMetric(recs, "meeting_minutes")),
		report.ScalarSeriesChart("overbookings", report.WithMetric(recs, "calendar_overbookings")),
		report.WeekdayChart("avg meeting minutes/day by weekday", report.WithMetric(recs, "meeting_minutes")),
	}
}

// withDim returns records carrying the given dimension key, so a breakdown only
// sees the partition it's meant to chart (the "meetings" metric is emitted with
// several disjoint dimension keys).
func withDim(recs []store.Record, dimKey string) []store.Record {
	out := recs[:0:0]
	for _, r := range recs {
		if _, ok := r.Dimensions[dimKey]; ok {
			out = append(out, r)
		}
	}
	return out
}
