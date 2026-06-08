// Package report renders stored metrics as terminal, Markdown, or interactive
// HTML. It operates purely on store.Record and classifies each metric by its
// shape (see classify.go), so any source's metrics are visualized generically
// without source-specific rendering code.
//
// This file holds the shared primitives: output format, time-bucket
// granularity, and the Slack-specific display niceties (channel/DM name
// formatting and self-detection) that remain useful when labeling entities.
package report

import (
	"strings"
	"time"

	"github.com/nachmore/commstats/internal/store"
)

// Dimension keys used by Slack message/channel records, reused for entity
// labeling (e.g. prefixing channels with # and DMs with @).
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

// Period is the time-bucket granularity for scalar time series.
type Period int

const (
	Day Period = iota
	Week
	Month
	Year
)

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

var weekdayOrder = [7]time.Weekday{
	time.Monday, time.Tuesday, time.Wednesday, time.Thursday,
	time.Friday, time.Saturday, time.Sunday,
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
	for _, n := range names {
		body := strings.TrimSuffix(strings.TrimPrefix(n, "mpdm-"), "-1")
		seen := map[string]bool{}
		for _, m := range strings.Split(body, "--") {
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
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
//   - real channels    -> "#name"
//   - group DMs         -> "@a, @b, @c" from members encoded in the name
//   - direct messages   -> the stored "@name" as-is
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
	body := strings.TrimSuffix(strings.TrimPrefix(name, "mpdm-"), "-1")
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
