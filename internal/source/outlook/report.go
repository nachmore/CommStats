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

func (emailReporter) AppName() string       { return "Outlook" }
func (emailReporter) PrimaryMetric() string { return "emails_sent" }

// HourMetric contributes received-email volume to the combined hours chart.
func (emailReporter) HourMetric() string { return "emails_received_by_hour" }

// EstimatedMinutes estimates email time from emails read + sent (the ones you
// actually engage with) at ~1.3 min each.
func (emailReporter) EstimatedMinutes(recs []store.Record) []report.DayPoint {
	engaged := append(report.WithMetric(recs, "emails_read"), report.WithMetric(recs, "emails_sent")...)
	return report.EstMinutesFromCount(engaged, report.MinPerEmail)
}

func (emailReporter) Headline(recs []store.Record) []report.HeadlineStat {
	return []report.HeadlineStat{
		report.SumStat("received", report.WithMetric(recs, "emails_received")),
		report.SumStat("sent", report.WithMetric(recs, "emails_sent")),
		report.SumStat("read", report.WithMetric(recs, "emails_read")),
		report.SumStat("unread", report.WithMetric(recs, "emails_unread")),
		report.AfterHoursStat("after-hours received %", report.WithMetric(recs, "emails_received_by_hour")),
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

func (calendarReporter) AppName() string       { return "Outlook" }
func (calendarReporter) PrimaryMetric() string { return "meeting_minutes" }

// HourMetric contributes meeting start-hour volume to the combined chart.
func (calendarReporter) HourMetric() string { return "meetings_by_hour" }

// EstimatedMinutes uses actual meeting time, de-overlapped (busy minutes), so
// double-booked meetings count their wall-clock time once.
func (calendarReporter) EstimatedMinutes(recs []store.Record) []report.DayPoint {
	return report.EstMinutesFromMinutes(report.WithMetric(recs, "meeting_busy_minutes"))
}

func (calendarReporter) Headline(recs []store.Record) []report.HeadlineStat {
	// Canonical meeting count = the size partition, which is emitted only for
	// real meetings (with other attendees) — unlike the type partition, which
	// also counts all-day banners and personal blocks.
	return []report.HeadlineStat{
		report.SumStat("events", report.WithMetric(recs, "calendar_events")),
		report.SumStat("meetings", withDim(report.WithMetric(recs, "meetings"), "size")),
		report.SumStat("meeting minutes", report.WithMetric(recs, "meeting_minutes")),
		report.SumStat("overbookings", report.WithMetric(recs, "calendar_overbookings")),
		report.SumStat("others' OOO holds", report.WithMetric(recs, "ooo_blocks")),
	}
}

func (calendarReporter) Charts(recs []store.Record, topN int) []report.Chart {
	meetings := report.WithMetric(recs, "meetings")
	return []report.Chart{
		report.DoughnutChart("calendar entries by type", "type", withDim(meetings, "type")),
		report.DoughnutChart("meetings by participant size", "size", withDim(meetings, "size")),
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
