package slack

import (
	"github.com/nachmore/commstats/internal/report"
	"github.com/nachmore/commstats/internal/store"
)

func init() { report.RegisterReporter("slack", slackReporter{}) }

// slackReporter curates Slack's report tab from its per-channel "messages" and
// "messages_by_hour" metrics, restoring the rich views (messages over time,
// unique channels, by channel type, top channels vs DMs, by hour, by weekday)
// using the shared chart builders.
type slackReporter struct{}

func (slackReporter) AppName() string { return "Slack" }

// EstimatedMinutes estimates Slack time from message volume (~0.5 min each).
func (slackReporter) EstimatedMinutes(recs []store.Record) []report.DayPoint {
	return report.EstMinutesFromCount(report.WithMetric(recs, "messages"), report.MinPerMessage)
}

// Headline totals shown on the overview tab (windowed client-side).
func (slackReporter) Headline(recs []store.Record) []report.HeadlineStat {
	msgs := report.WithMetric(recs, "messages")
	return []report.HeadlineStat{
		report.SumStat("messages", msgs),
		report.DistinctStat("unique channels", "channel_id", msgs),
		report.AfterHoursStat("after-hours %", report.WithMetric(recs, "messages_by_hour")),
	}
}

func (slackReporter) Charts(recs []store.Record, topN int) []report.Chart {
	msgs := report.WithMetric(recs, "messages")
	hours := report.WithMetric(recs, "messages_by_hour")

	return []report.Chart{
		report.DualSeriesChart("messages & unique channels", "messages", msgs,
			"unique channels", "distinct", "channel_id", msgs),
		report.DoughnutChart("messages by channel type", "channel_type", msgs),
		report.TopNChart("top channels", "channel_id", "channel_name", msgs, topN,
			func(r store.Record) bool { return report.IsChannelType(r, true) }),
		report.TopNChart("top DMs", "channel_id", "channel_name", msgs, topN,
			func(r store.Record) bool { return report.IsChannelType(r, false) }),
		report.OrderedChart("messages by hour", "hour", hours),
		report.WeekdayChart("avg messages/day by weekday", msgs),
	}
}
