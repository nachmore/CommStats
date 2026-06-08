// Package report renders stored metrics as a terminal summary or Markdown,
// operating purely on store.Record so it stays source-agnostic.
//
// Slack stores one "messages" record per channel per day, carrying channel_id,
// channel_name, and channel_type dimensions. From those raw rows the report
// derives, per time bucket:
//   - messages_sent  : sum of all per-channel counts
//   - unique_channels: distinct channel_id within the bucket (a true distinct
//     count, computed after bucketing — not a daily average)
//   - messages [channel_type=X]: per-type sums
//
// and, over the whole queried range, a top-channels ranking by message volume.
//
// Bucket granularity (day/week/month/year) is selected by Period, so the same
// machinery drives every rollup.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/nachmore/commstats/internal/store"
)

// Dimension keys used by message records.
const (
	dimChannelID   = "channel_id"
	dimChannelName = "channel_name"
	dimChannelType = "channel_type"
)

// Format selects the output rendering.
type Format string

const (
	Terminal Format = "terminal"
	Markdown Format = "markdown"
	HTML     Format = "html"
)

// Period is the time-bucket granularity for columns.
type Period int

const (
	Day Period = iota
	Week
	Month
	Year
)

func (p Period) avgHeader() string {
	switch p {
	case Week:
		return "Avg/wk"
	case Month:
		return "Avg/mo"
	case Year:
		return "Avg/yr"
	default:
		return "Avg/day"
	}
}

// Title is a human label for the period granularity.
func (p Period) Title() string {
	switch p {
	case Week:
		return "Weekly"
	case Month:
		return "Monthly"
	case Year:
		return "Yearly"
	default:
		return "Daily"
	}
}

// bucketOf returns the (sort key, display label) for the bucket t falls in.
func bucketOf(t time.Time, p Period) (key, label string) {
	switch p {
	case Week:
		// Anchor to the week's Monday so the key sorts chronologically.
		monday := t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
		return monday.Format("2006-01-02"), "wk " + monday.Format("01-02")
	case Month:
		return t.Format("2006-01"), t.Format("2006-01")
	case Year:
		return t.Format("2006"), t.Format("2006")
	default:
		return t.Format("2006-01-02"), t.Format("01-02")
	}
}

// RenderSummary writes the per-bucket summary matrix (one per source).
func RenderSummary(w io.Writer, recs []store.Record, format Format, period Period) error {
	mats := buildSummary(recs, period)
	switch format {
	case Markdown:
		return renderMatrixMarkdown(w, mats, period)
	default:
		return renderMatrixTerminal(w, mats, period)
	}
}

// matrix is one source's grid: metric rows by time-bucket columns.
type matrix struct {
	source string
	cols   []column
	rows   []row
}

type column struct {
	key   string
	label string
}

type row struct {
	label  string
	values map[string]float64 // bucket key -> value
	avg    float64
}

// bucketAgg accumulates the raw per-channel rows that fall into one source's
// time bucket, so summary series can be derived with correct semantics.
type bucketAgg struct {
	messagesSent float64
	channelIDs   map[string]struct{}
	byType       map[string]float64
}

func newBucketAgg() *bucketAgg {
	return &bucketAgg{channelIDs: map[string]struct{}{}, byType: map[string]float64{}}
}

func buildSummary(recs []store.Record, period Period) []matrix {
	// source -> bucket key -> aggregate
	bySource := map[string]map[string]*bucketAgg{}
	// source -> bucket key -> label
	colsBySource := map[string]map[string]string{}

	for _, r := range recs {
		if r.Name != "messages" {
			continue
		}
		bk, bl := bucketOf(r.Day, period)
		if bySource[r.Source] == nil {
			bySource[r.Source] = map[string]*bucketAgg{}
			colsBySource[r.Source] = map[string]string{}
		}
		agg := bySource[r.Source][bk]
		if agg == nil {
			agg = newBucketAgg()
			bySource[r.Source][bk] = agg
		}
		agg.messagesSent += r.Value
		if id := r.Dimensions[dimChannelID]; id != "" {
			agg.channelIDs[id] = struct{}{}
		}
		agg.byType[r.Dimensions[dimChannelType]] += r.Value
		colsBySource[r.Source][bk] = bl
	}

	mats := make([]matrix, 0, len(bySource))
	for src, buckets := range bySource {
		cols := sortedColumns(colsBySource[src])

		// Discover the set of channel types present, for stable row ordering.
		typeSet := map[string]struct{}{}
		for _, agg := range buckets {
			for t := range agg.byType {
				typeSet[t] = struct{}{}
			}
		}
		types := make([]string, 0, len(typeSet))
		for t := range typeSet {
			types = append(types, t)
		}
		sort.Strings(types)

		m := matrix{source: src, cols: cols}
		m.rows = append(m.rows,
			seriesRow("messages_sent", cols, func(bk string) float64 {
				if a := buckets[bk]; a != nil {
					return a.messagesSent
				}
				return 0
			}),
			seriesRow("unique_channels", cols, func(bk string) float64 {
				if a := buckets[bk]; a != nil {
					return float64(len(a.channelIDs))
				}
				return 0
			}),
		)
		for _, t := range types {
			t := t
			m.rows = append(m.rows, seriesRow(
				fmt.Sprintf("messages [channel_type=%s]", t),
				cols, func(bk string) float64 {
					if a := buckets[bk]; a != nil {
						return a.byType[t]
					}
					return 0
				}))
		}
		mats = append(mats, m)
	}
	sort.Slice(mats, func(i, j int) bool { return mats[i].source < mats[j].source })
	return mats
}

// seriesRow builds a row by evaluating valueAt for each column, with the
// trailing average taken over every displayed bucket (empty buckets count 0).
func seriesRow(label string, cols []column, valueAt func(bucketKey string) float64) row {
	values := make(map[string]float64, len(cols))
	var sum float64
	for _, c := range cols {
		v := valueAt(c.key)
		values[c.key] = v
		sum += v
	}
	avg := 0.0
	if len(cols) > 0 {
		avg = sum / float64(len(cols))
	}
	return row{label: label, values: values, avg: avg}
}

func sortedColumns(buckets map[string]string) []column {
	cols := make([]column, 0, len(buckets))
	for k, l := range buckets {
		cols = append(cols, column{key: k, label: l})
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].key < cols[j].key })
	return cols
}

func renderMatrixTerminal(w io.Writer, mats []matrix, period Period) error {
	if len(mats) == 0 {
		_, err := fmt.Fprintln(w, "No metrics for the selected period.")
		return err
	}
	avgH := period.avgHeader()
	for _, m := range mats {
		if _, err := fmt.Fprintf(w, "\n%s\n%s\n", strings.ToUpper(m.source), strings.Repeat("=", len(m.source))); err != nil {
			return err
		}

		labelW := len("metric")
		for _, r := range m.rows {
			labelW = max(labelW, len(r.label))
		}
		colW := make([]int, len(m.cols))
		for i, c := range m.cols {
			colW[i] = len(c.label)
			for _, r := range m.rows {
				colW[i] = max(colW[i], len(fmtVal(r.values[c.key], false)))
			}
		}
		avgW := len(avgH)
		for _, r := range m.rows {
			avgW = max(avgW, len(fmtVal(r.avg, true)))
		}

		fmt.Fprintf(w, "%-*s", labelW, "metric")
		for i, c := range m.cols {
			fmt.Fprintf(w, "  %*s", colW[i], c.label)
		}
		fmt.Fprintf(w, "  %*s\n", avgW, avgH)

		for _, r := range m.rows {
			fmt.Fprintf(w, "%-*s", labelW, r.label)
			for i, c := range m.cols {
				fmt.Fprintf(w, "  %*s", colW[i], fmtVal(r.values[c.key], false))
			}
			fmt.Fprintf(w, "  %*s\n", avgW, fmtVal(r.avg, true))
		}
	}
	return nil
}

func renderMatrixMarkdown(w io.Writer, mats []matrix, period Period) error {
	if len(mats) == 0 {
		_, err := fmt.Fprintln(w, "\nNo metrics for the selected period.")
		return err
	}
	avgH := period.avgHeader()
	for _, m := range mats {
		fmt.Fprintf(w, "\n### %s\n\n", m.source)
		fmt.Fprint(w, "| Metric |")
		for _, c := range m.cols {
			fmt.Fprintf(w, " %s |", c.label)
		}
		fmt.Fprintf(w, " %s |\n", avgH)

		fmt.Fprint(w, "| --- |")
		for range m.cols {
			fmt.Fprint(w, " ---: |")
		}
		fmt.Fprint(w, " ---: |\n")

		for _, r := range m.rows {
			fmt.Fprintf(w, "| %s |", r.label)
			for _, c := range m.cols {
				fmt.Fprintf(w, " %s |", fmtVal(r.values[c.key], false))
			}
			fmt.Fprintf(w, " %s |\n", fmtVal(r.avg, true))
		}
	}
	return nil
}

// WeekdayStat is messaging volume for one weekday, aggregated over the range.
// Avg divides Total by how many times that weekday occurred in the data's date
// span — so quiet days correctly pull the average down.
type WeekdayStat struct {
	Weekday string  `json:"weekday"`
	Total   float64 `json:"total"`
	Avg     float64 `json:"avg"`
}

var weekdayOrder = [7]time.Weekday{
	time.Monday, time.Tuesday, time.Wednesday, time.Thursday,
	time.Friday, time.Saturday, time.Sunday,
}

// buildWeekdayStats aggregates messages_sent per weekday for one source's
// records. The average denominator is the count of each weekday across the
// inclusive span [minDay, maxDay], not just days with activity.
func buildWeekdayStats(recs []store.Record) []WeekdayStat {
	total := map[time.Weekday]float64{}
	var minDay, maxDay time.Time
	for _, r := range recs {
		if r.Name != "messages" {
			continue
		}
		total[r.Day.Weekday()] += r.Value
		if minDay.IsZero() || r.Day.Before(minDay) {
			minDay = r.Day
		}
		if maxDay.IsZero() || r.Day.After(maxDay) {
			maxDay = r.Day
		}
	}

	occur := map[time.Weekday]int{}
	if !minDay.IsZero() {
		for d := minDay; !d.After(maxDay); d = d.AddDate(0, 0, 1) {
			occur[d.Weekday()]++
		}
	}

	out := make([]WeekdayStat, 0, 7)
	for _, wd := range weekdayOrder {
		avg := 0.0
		if n := occur[wd]; n > 0 {
			avg = total[wd] / float64(n)
		}
		out = append(out, WeekdayStat{Weekday: wd.String(), Total: total[wd], Avg: avg})
	}
	return out
}

// RenderWeekday writes a by-weekday section (one per source) to w.
func RenderWeekday(w io.Writer, recs []store.Record, format Format) error {
	bySource := map[string][]store.Record{}
	for _, r := range recs {
		bySource[r.Source] = append(bySource[r.Source], r)
	}
	srcs := make([]string, 0, len(bySource))
	for s := range bySource {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)

	for _, src := range srcs {
		stats := buildWeekdayStats(bySource[src])
		if format == Markdown {
			fmt.Fprintf(w, "\n### %s\n\n| Weekday | Total | Avg/day |\n| --- | ---: | ---: |\n", src)
			for _, s := range stats {
				fmt.Fprintf(w, "| %s | %s | %s |\n", s.Weekday, fmtVal(s.Total, false), fmtVal(s.Avg, true))
			}
			continue
		}
		fmt.Fprintf(w, "\n%s\n%s\n", strings.ToUpper(src), strings.Repeat("=", len(src)))
		fmt.Fprintf(w, "  %-10s  %8s  %8s\n", "weekday", "total", "avg/day")
		for _, s := range stats {
			fmt.Fprintf(w, "  %-10s  %8s  %8s\n", s.Weekday, fmtVal(s.Total, false), fmtVal(s.Avg, true))
		}
	}
	return nil
}

// channelTotal is one channel's aggregate over the whole queried range.
type channelTotal struct {
	name  string
	typ   string
	total float64
}

// isRealChannel reports whether a channel_type is an actual channel (public or
// private), as opposed to a direct/group message.
func isRealChannel(typ string) bool {
	return typ == "public_channel" || typ == "private_channel"
}

// SelfUsername is excluded from prettified group-DM member lists. It's the
// authenticated user, who is in every one of their own group DMs. Set it via
// DetectSelf before rendering.
var SelfUsername string

// DetectSelf infers the authenticated user's username from group-DM names and
// sets SelfUsername. The self user is in every group DM, so the member that
// appears in all group-dm conversations is self. With a single group DM this is
// ambiguous, so it only sets self when there are at least two to compare.
func DetectSelf(recs []store.Record) {
	var names []string
	for _, r := range recs {
		if r.Dimensions[dimChannelType] == "group-dm" {
			n := r.Dimensions[dimChannelName]
			if strings.HasPrefix(n, "mpdm-") {
				names = append(names, n)
			}
		}
	}
	if len(names) < 2 {
		return
	}

	count := map[string]int{}
	seen := map[string]map[string]bool{} // name -> member set, to avoid double-count within one name
	for _, n := range names {
		seen[n] = map[string]bool{}
		body := strings.TrimSuffix(strings.TrimPrefix(n, "mpdm-"), "-1")
		for _, m := range strings.Split(body, "--") {
			if m == "" || seen[n][m] {
				continue
			}
			seen[n][m] = true
			count[m]++
		}
	}
	for m, c := range count {
		if c == len(names) {
			SelfUsername = m
			return
		}
	}
}

// displayName produces a human label for a conversation:
//   - real channels  -> "#name"
//   - group DMs (group-dm) -> "@a, @b, @c" from the members encoded in the name
//   - direct messages -> the stored "@name" as-is
func displayName(name, typ string) string {
	switch {
	case isRealChannel(typ):
		if strings.HasPrefix(name, "#") {
			return name
		}
		return "#" + name
	case typ == "group-dm":
		if pretty := prettyGroupDM(name); pretty != "" {
			return pretty
		}
		return name
	default:
		return name
	}
}

// prettyGroupDM turns Slack's "mpdm-a--b--c-1" group-DM name into "@a, @b, @c",
// dropping the authenticated user. Returns "" if the name isn't in that form.
func prettyGroupDM(name string) string {
	if !strings.HasPrefix(name, "mpdm-") {
		return ""
	}
	body := strings.TrimPrefix(name, "mpdm-")
	body = strings.TrimSuffix(body, "-1") // trailing conversation index
	members := strings.Split(body, "--")
	out := make([]string, 0, len(members))
	for _, m := range members {
		if m == "" || m == SelfUsername {
			continue
		}
		out = append(out, "@"+m)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, ", ")
}

// RenderTopChannels writes the top-n channels by message volume over the whole
// record set, per source.
func RenderTopChannels(w io.Writer, recs []store.Record, format Format, n int) error {
	bySource := map[string]map[string]*channelTotal{} // source -> channel_id -> total
	for _, r := range recs {
		if r.Name != "messages" {
			continue
		}
		id := r.Dimensions[dimChannelID]
		if id == "" {
			continue
		}
		if bySource[r.Source] == nil {
			bySource[r.Source] = map[string]*channelTotal{}
		}
		ct := bySource[r.Source][id]
		if ct == nil {
			ct = &channelTotal{name: r.Dimensions[dimChannelName], typ: r.Dimensions[dimChannelType]}
			bySource[r.Source][id] = ct
		}
		ct.total += r.Value
	}

	srcs := make([]string, 0, len(bySource))
	for s := range bySource {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)

	for _, src := range srcs {
		channels, dms := splitChannels(bySource[src], n)
		if format == Markdown {
			renderTopMarkdown(w, src, "Top Channels", channels)
			renderTopMarkdown(w, src, "Top DMs", dms)
		} else {
			renderTopTerminal(w, src, "channels", channels)
			renderTopTerminal(w, src, "DMs", dms)
		}
	}
	return nil
}

// splitChannels ranks a source's conversations and partitions them into real
// channels and DMs (direct + group), each capped at n.
func splitChannels(byID map[string]*channelTotal, n int) (channels, dms []channelTotal) {
	for _, ct := range byID {
		if isRealChannel(ct.typ) {
			channels = append(channels, *ct)
		} else {
			dms = append(dms, *ct)
		}
	}
	return capRanked(channels, n), capRanked(dms, n)
}

func capRanked(all []channelTotal, n int) []channelTotal {
	sort.Slice(all, func(i, j int) bool {
		if all[i].total != all[j].total {
			return all[i].total > all[j].total
		}
		return all[i].name < all[j].name
	})
	if n > 0 && len(all) > n {
		all = all[:n]
	}
	return all
}

func renderTopTerminal(w io.Writer, src, kind string, ranked []channelTotal) {
	heading := strings.ToUpper(src) + " — " + kind
	fmt.Fprintf(w, "\n%s\n%s\n", heading, strings.Repeat("-", len(heading)))
	if len(ranked) == 0 {
		fmt.Fprintf(w, "  (no %s activity)\n", kind)
		return
	}
	nameW := len("name")
	for _, c := range ranked {
		nameW = max(nameW, len(displayName(c.name, c.typ)))
	}
	fmt.Fprintf(w, "  %-3s  %-*s  %-15s  %s\n", "#", nameW, "name", "type", "messages")
	for i, c := range ranked {
		fmt.Fprintf(w, "  %-3d  %-*s  %-15s  %s\n", i+1, nameW, displayName(c.name, c.typ), c.typ, fmtVal(c.total, false))
	}
}

func renderTopMarkdown(w io.Writer, src, kind string, ranked []channelTotal) {
	fmt.Fprintf(w, "\n### %s — %s\n\n", src, kind)
	if len(ranked) == 0 {
		fmt.Fprintf(w, "_(no %s activity)_\n", kind)
		return
	}
	fmt.Fprint(w, "| # | Name | Type | Messages |\n| ---: | --- | --- | ---: |\n")
	for i, c := range ranked {
		fmt.Fprintf(w, "| %d | %s | %s | %s |\n", i+1, displayName(c.name, c.typ), c.typ, fmtVal(c.total, false))
	}
}

// typeFromLabel extracts the channel type from a derived row label of the form
// "messages [channel_type=X]".
func typeFromLabel(label string) (string, bool) {
	const prefix = "messages [channel_type="
	if !strings.HasPrefix(label, prefix) || !strings.HasSuffix(label, "]") {
		return "", false
	}
	return label[len(prefix) : len(label)-1], true
}

// fmtVal formats counts as whole numbers; averages keep one decimal.
func fmtVal(v float64, isAvg bool) string {
	if isAvg {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}
