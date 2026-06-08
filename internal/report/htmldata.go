package report

import (
	"sort"
	"time"

	"github.com/nachmore/commstats/internal/store"
)

// Document is the serializable payload backing the HTML report. It is built
// once from the raw per-channel records and consumed both by the Go-rendered
// fallback tables and the client-side charts.
type Document struct {
	GeneratedAt string           `json:"generated_at"`
	Sources     []SourceDocument `json:"sources"`
}

// SourceDocument holds everything chartable for one source.
type SourceDocument struct {
	Source    string         `json:"source"`
	Periods   []PeriodSeries `json:"periods"`    // time-series per granularity
	TypeShare []TypeSlice    `json:"type_share"` // overall channel_type split
	Weekdays  []WeekdayStat  `json:"weekdays"`   // by day-of-week
	TopRanges []TopRange     `json:"top_ranges"` // top channels/DMs per lookback window
}

// TopRange is the ranked top channels and DMs over one lookback window.
type TopRange struct {
	Label    string         `json:"label"`    // e.g. "7d", "30d", "All"
	Channels []ChannelTotal `json:"channels"` // real channels
	DMs      []ChannelTotal `json:"dms"`      // direct + group DMs
}

// topRangePlan defines the selectable lookback windows for the top lists. Days
// of 0 means all available data.
var topRangePlan = []struct {
	label string
	days  int
}{
	{"7d", 7},
	{"30d", 30},
	{"90d", 90},
	{"All", 0},
}

// PeriodSeries is a time-series at one granularity (daily/weekly/...).
type PeriodSeries struct {
	Period         string               `json:"period"`          // "Daily", "Weekly", ...
	Labels         []string             `json:"labels"`          // bucket labels (x-axis)
	MessagesSent   []float64            `json:"messages_sent"`   // total per bucket
	UniqueChannels []float64            `json:"unique_channels"` // distinct channels per bucket
	ByType         map[string][]float64 `json:"by_type"`         // channel_type -> per-bucket totals
	Types          []string             `json:"types"`           // stable type ordering for stacking
}

// TypeSlice is one slice of the channel-type doughnut.
type TypeSlice struct {
	Type  string  `json:"type"`
	Total float64 `json:"total"`
}

// ChannelTotal is one channel's aggregate volume (JSON-tagged for the charts).
type ChannelTotal struct {
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Total float64 `json:"total"`
}

// periodPlan defines which granularities the HTML report includes and how far
// back each looks. Kept here so the HTML view and overview stay consistent.
var periodPlan = []struct {
	period Period
	days   int
}{
	{Day, 14},
	{Week, 56},
	{Month, 365},
	{Year, 365 * 3},
}

// BuildDocument assembles the full chartable payload from raw records. topN
// bounds the top-channels list (<=0 means all).
func BuildDocument(recs []store.Record, generatedAt string, topN int) Document {
	bySource := map[string][]store.Record{}
	for _, r := range recs {
		bySource[r.Source] = append(bySource[r.Source], r)
	}
	srcs := make([]string, 0, len(bySource))
	for s := range bySource {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)

	doc := Document{GeneratedAt: generatedAt}
	for _, src := range srcs {
		sr := bySource[src]
		sd := SourceDocument{
			Source:    src,
			Periods:   buildPeriodSeries(sr),
			TypeShare: buildTypeShare(sr),
			Weekdays:  buildWeekdayStats(sr),
			TopRanges: buildTopRanges(sr, topN),
		}
		doc.Sources = append(doc.Sources, sd)
	}
	return doc
}

func buildPeriodSeries(recs []store.Record) []PeriodSeries {
	out := make([]PeriodSeries, 0, len(periodPlan))
	for _, p := range periodPlan {
		// buildSummary already aggregates per bucket with correct semantics
		// (sum for additive, distinct count for unique_channels). Reuse it,
		// then reshape its single matrix into chart series.
		mats := buildSummary(recs, p.period)
		ps := PeriodSeries{Period: p.period.Title(), ByType: map[string][]float64{}}
		if len(mats) == 0 {
			out = append(out, ps)
			continue
		}
		m := mats[0]
		for _, c := range m.cols {
			ps.Labels = append(ps.Labels, c.label)
		}
		for _, row := range m.rows {
			vals := make([]float64, len(m.cols))
			for i, c := range m.cols {
				vals[i] = row.values[c.key]
			}
			switch {
			case row.label == "messages_sent":
				ps.MessagesSent = vals
			case row.label == "unique_channels":
				ps.UniqueChannels = vals
			default:
				if t, ok := typeFromLabel(row.label); ok {
					ps.ByType[t] = vals
					ps.Types = append(ps.Types, t)
				}
			}
		}
		sort.Strings(ps.Types)
		out = append(out, ps)
	}
	return out
}

func buildTypeShare(recs []store.Record) []TypeSlice {
	byType := map[string]float64{}
	for _, r := range recs {
		if r.Name != "messages" {
			continue
		}
		byType[r.Dimensions[dimChannelType]] += r.Value
	}
	out := make([]TypeSlice, 0, len(byType))
	for t, v := range byType {
		out = append(out, TypeSlice{Type: t, Total: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	return out
}

// buildTopRanges computes top channels/DMs for each configured lookback window.
// A window of N days includes records on or after (latestDay - N + 1); a window
// of 0 includes everything.
func buildTopRanges(recs []store.Record, topN int) []TopRange {
	var latest time.Time
	for _, r := range recs {
		if r.Day.After(latest) {
			latest = r.Day
		}
	}

	out := make([]TopRange, 0, len(topRangePlan))
	for _, p := range topRangePlan {
		scoped := recs
		if p.days > 0 && !latest.IsZero() {
			cutoff := latest.AddDate(0, 0, -(p.days - 1))
			scoped = scoped[:0:0]
			for _, r := range recs {
				if !r.Day.Before(cutoff) {
					scoped = append(scoped, r)
				}
			}
		}
		channels, dms := buildTopConversations(scoped, topN)
		out = append(out, TopRange{Label: p.label, Channels: channels, DMs: dms})
	}
	return out
}

// buildTopConversations ranks a source's conversations split into real channels
// and DMs, each capped at topN, with display-prefixed names.
func buildTopConversations(recs []store.Record, topN int) (channels, dms []ChannelTotal) {
	byID := map[string]*channelTotal{}
	for _, r := range recs {
		if r.Name != "messages" {
			continue
		}
		id := r.Dimensions[dimChannelID]
		if id == "" {
			continue
		}
		ct := byID[id]
		if ct == nil {
			ct = &channelTotal{name: r.Dimensions[dimChannelName], typ: r.Dimensions[dimChannelType]}
			byID[id] = ct
		}
		ct.total += r.Value
	}
	rc, rd := splitChannels(byID, topN)
	return toChannelTotals(rc), toChannelTotals(rd)
}

func toChannelTotals(ranked []channelTotal) []ChannelTotal {
	out := make([]ChannelTotal, len(ranked))
	for i, c := range ranked {
		out[i] = ChannelTotal{Name: displayName(c.name, c.typ), Type: c.typ, Total: c.total}
	}
	return out
}
