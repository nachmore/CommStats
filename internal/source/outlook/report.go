package outlook

import (
	"github.com/nachmore/commstats/internal/report"
	"github.com/nachmore/commstats/internal/store"
)

func init() {
	report.RegisterReporter(sourceEmail, emailReporter{})
	report.RegisterReporter(sourceCalendar, calendarReporter{})
}

// emailReporter curates the email report tab.
type emailReporter struct{}

func (emailReporter) PrimaryMetric() string { return "emails_sent" }

// HourMetric contributes received-email volume to the combined hours chart.
func (emailReporter) HourMetric() string { return "emails_received_by_hour" }

func (emailReporter) Headline(recs []store.Record) []report.LabeledValue {
	return []report.LabeledValue{
		{Label: "received", Value: report.SumValues(report.WithMetric(recs, "emails_received"))},
		{Label: "sent", Value: report.SumValues(report.WithMetric(recs, "emails_sent"))},
		{Label: "read", Value: report.SumValues(report.WithMetric(recs, "emails_read"))},
		{Label: "unread", Value: report.SumValues(report.WithMetric(recs, "emails_unread"))},
	}
}

func (emailReporter) Charts(recs []store.Record, topN int) []report.Chart {
	return []report.Chart{
		report.ScalarSeriesChart("emails received", report.WithMetric(recs, "emails_received")),
		report.ScalarSeriesChart("emails sent", report.WithMetric(recs, "emails_sent")),
		report.ScalarSeriesChart("emails read", report.WithMetric(recs, "emails_read")),
		report.ScalarSeriesChart("emails unread", report.WithMetric(recs, "emails_unread")),
		report.WeekdayChart("avg emails received/day by weekday", report.WithMetric(recs, "emails_received")),
		report.WeekdayChart("avg emails sent/day by weekday", report.WithMetric(recs, "emails_sent")),
		report.OrderedChart("emails received by hour", "hour", report.WithMetric(recs, "emails_received_by_hour")),
		report.OrderedChart("emails sent by hour", "hour", report.WithMetric(recs, "emails_sent_by_hour")),
	}
}

// calendarReporter curates the calendar report tab. The "meetings" metric is
// emitted as several independent partitions (size/duration/role/response/scope/
// category) of the same events, so summing all meetings rows would multi-count
// — only the size partition is the canonical headcount.
type calendarReporter struct{}

func (calendarReporter) PrimaryMetric() string { return "meeting_minutes" }

// HourMetric contributes meeting start-hour volume to the combined chart.
func (calendarReporter) HourMetric() string { return "meetings_by_hour" }

func (calendarReporter) Headline(recs []store.Record) []report.LabeledValue {
	meetingCount := report.SumValues(withDim(report.WithMetric(recs, "meetings"), "size"))
	return []report.LabeledValue{
		{Label: "events", Value: report.SumValues(report.WithMetric(recs, "calendar_events"))},
		{Label: "meetings", Value: meetingCount},
		{Label: "meeting minutes", Value: report.SumValues(report.WithMetric(recs, "meeting_minutes"))},
		{Label: "overbookings", Value: report.SumValues(report.WithMetric(recs, "calendar_overbookings"))},
	}
}

func (calendarReporter) Charts(recs []store.Record, topN int) []report.Chart {
	meetings := report.WithMetric(recs, "meetings")
	return []report.Chart{
		report.BreakdownChart("meetings by size", "size", withDim(meetings, "size")),
		report.BreakdownChart("meetings by duration", "duration", withDim(meetings, "duration")),
		report.BreakdownChart("meetings by role", "role", withDim(meetings, "role")),
		report.BreakdownChart("my response", "response", withDim(meetings, "response")),
		report.BreakdownChart("internal vs external", "scope", withDim(meetings, "scope")),
		report.BreakdownChart("meetings by category", "category", withDim(meetings, "category")),
		report.ScalarSeriesChart("meeting minutes", report.WithMetric(recs, "meeting_minutes")),
		report.ScalarSeriesChart("overbookings", report.WithMetric(recs, "calendar_overbookings")),
		report.WeekdayChart("avg meeting minutes/day by weekday", report.WithMetric(recs, "meeting_minutes")),
		report.OrderedChart("meetings by hour", "hour", report.WithMetric(recs, "meetings_by_hour")),
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
