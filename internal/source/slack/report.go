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

// PrimaryMetric is Slack's activity line for the cross-source overview.
func (slackReporter) PrimaryMetric() string { return "messages" }

// HourMetric contributes Slack's hourly message volume to the combined chart.
func (slackReporter) HourMetric() string { return "messages_by_hour" }

// Headline totals shown on the overview tab.
func (slackReporter) Headline(recs []store.Record) []report.LabeledValue {
	msgs := report.WithMetric(recs, "messages")
	return []report.LabeledValue{
		{Label: "messages", Value: report.SumValues(msgs)},
		{Label: "unique channels", Value: float64(report.DistinctDim(msgs, "channel_id"))},
	}
}

func (slackReporter) Charts(recs []store.Record, topN int) []report.Chart {
	msgs := report.WithMetric(recs, "messages")
	hours := report.WithMetric(recs, "messages_by_hour")

	return []report.Chart{
		report.ScalarSeriesChart("messages sent", msgs),
		report.DistinctSeriesChart("unique channels", "channel_id", msgs),
		report.BreakdownChart("messages by channel type", "channel_type", msgs),
		report.TopNChart("top channels", "channel_id", "channel_name", msgs, topN,
			func(r store.Record) bool { return report.IsChannelType(r, true) }),
		report.TopNChart("top DMs", "channel_id", "channel_name", msgs, topN,
			func(r store.Record) bool { return report.IsChannelType(r, false) }),
		report.OrderedChart("messages by hour", "hour", hours),
		report.WeekdayChart("avg messages/day by weekday", msgs),
	}
}
