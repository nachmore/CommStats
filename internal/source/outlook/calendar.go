package outlook

import (
	"sort"
	"strings"
	"time"

	"github.com/nachmore/commstats/internal/source"
)

// interval is a busy time span, used for overbooking detection.
type interval struct{ start, end time.Time }

// collectCalendar turns a day's events into per-day histogram metrics. The
// window w is attached to every metric; homeOrg is the registrable org label of
// the signed-in user (e.g. "amazon") used for external-participant detection.
func collectCalendar(srcName string, w source.TimeWindow, events []event, selfAddr, homeOrg string) []source.Metric {
	var (
		byShape    = map[string]int{} // size/shape bucket (incl. personal-block)
		byDuration = map[string]int{}
		byRole     = map[string]int{}
		byResponse = map[string]int{}
		byCategory = map[string]int{}
		byScope    = map[string]int{} // internal / external
		totalMin   float64
	)

	// Busy intervals for overbooking detection.
	var busy []interval

	for _, e := range events {
		// Skip free/canceled holds entirely from "meeting" stats: ShowAs Free
		// means it doesn't occupy the calendar as busy time.
		free := strings.EqualFold(e.ShowAs, "Free")

		st, okS := parseEventTime(e.Start.DateTime)
		en, okE := parseEventTime(e.End.DateTime)
		dur := time.Duration(0)
		if okS && okE && en.After(st) {
			dur = en.Sub(st)
		}

		// External attendee count (excluding self).
		others := 0
		hasExternal := false
		for _, a := range e.Attendees {
			addr := strings.ToLower(a.EmailAddress.Address)
			if addr == "" || addr == strings.ToLower(selfAddr) {
				continue
			}
			others++
			if orgOf(addr) != homeOrg {
				hasExternal = true
			}
		}

		// Shape: all-day (split by whether it holds busy time), personal block
		// (no other attendees), or a sized meeting by headcount (self + others).
		shape := ""
		switch {
		case e.IsAllDay && free:
			shape = "all-day-free" // e.g. an informational all-day banner
		case e.IsAllDay:
			shape = "all-day"
		case others == 0:
			shape = "personal-block"
		default:
			shape = sizeBucket(others + 1)
		}
		byShape[shape]++

		// The remaining breakdowns describe actual meetings (with other people),
		// not all-day items or personal blocks.
		isMeeting := !e.IsAllDay && others > 0
		if isMeeting {
			byDuration[durationBucket(dur, e.IsAllDay)]++
			if e.IsOrganizer {
				byRole["organizer"]++
			} else {
				byRole["attendee"]++
			}
			byResponse[responseBucket(e.IsOrganizer, e.ResponseStatus.Response)]++
			if hasExternal {
				byScope["external"]++
			} else {
				byScope["internal"]++
			}
			totalMin += dur.Minutes()
		}

		for _, cat := range e.Categories {
			if cat != "" {
				byCategory[cat]++
			}
		}

		// Overbooking: only events that hold busy time and have a real span.
		if !free && okS && okE && en.After(st) {
			busy = append(busy, interval{st, en})
		}
	}

	overbooked := countOverlapping(busy)

	src := func(name string, v int, dims map[string]string) source.Metric {
		return source.Metric{Source: srcName, Name: name, Value: float64(v), Window: w, Dimensions: dims}
	}

	var metrics []source.Metric
	add := func(name string, counts map[string]int, dimKey string) {
		for k, v := range counts {
			metrics = append(metrics, src(name, v, map[string]string{dimKey: k}))
		}
	}
	add("meetings", byShape, "size")
	add("meetings", byDuration, "duration")
	add("meetings", byRole, "role")
	add("meetings", byResponse, "response")
	add("meetings", byScope, "scope")
	add("meetings", byCategory, "category")
	metrics = append(metrics,
		source.Metric{Source: srcName, Name: "meeting_minutes", Value: totalMin, Window: w},
		source.Metric{Source: srcName, Name: "calendar_overbookings", Value: float64(overbooked), Window: w},
	)
	return metrics
}

// sizeBucket classifies a meeting by total headcount (including self).
func sizeBucket(total int) string {
	switch {
	case total <= 2:
		return "1:1"
	case total <= 5:
		return "small"
	case total <= 10:
		return "medium"
	default:
		return "large"
	}
}

// durationBucket classifies a meeting by length. Exact 30m and 1h get their own
// buckets since they're the common defaults.
func durationBucket(d time.Duration, allDay bool) string {
	if allDay {
		return "all-day"
	}
	switch m := d.Minutes(); {
	case m < 30:
		return "<30m"
	case m == 30:
		return "30m"
	case m < 60:
		return "30-60m"
	case m == 60:
		return "1h"
	case m < 120:
		return "1-2h"
	default:
		return "2h+"
	}
}

// responseBucket normalizes the user's RSVP. Organizer events report as such.
func responseBucket(isOrganizer bool, resp string) string {
	if isOrganizer {
		return "organizer"
	}
	switch strings.ToLower(resp) {
	case "accepted":
		return "accepted"
	case "tentativelyaccepted", "tentative":
		return "tentative"
	case "declined":
		return "declined"
	default:
		return "noresponse"
	}
}

// orgOf returns the registrable org label of an email address — the label
// before the public suffix, so amazon.com and amazon.co.uk both yield "amazon".
// Heuristic (no full public-suffix list): for a 2-label TLD where the
// second-to-last label is a known short ccTLD-second-level (co, com, etc.), use
// the third-to-last label; otherwise the second-to-last.
func orgOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	host := strings.ToLower(email[at+1:])
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	secondLevel := map[string]bool{"co": true, "com": true, "org": true, "net": true, "gov": true, "ac": true, "edu": true}
	if len(labels) >= 3 && secondLevel[labels[len(labels)-2]] {
		return labels[len(labels)-3]
	}
	return labels[len(labels)-2]
}

// countOverlapping returns how many intervals overlap at least one other
// interval (a measure of double-booking).
func countOverlapping(iv []interval) int {
	if len(iv) < 2 {
		return 0
	}
	sort.Slice(iv, func(i, j int) bool { return iv[i].start.Before(iv[j].start) })
	overlaps := make([]bool, len(iv))
	for i := 0; i < len(iv); i++ {
		for j := i + 1; j < len(iv); j++ {
			if !iv[j].start.Before(iv[i].end) { // sorted: no later start can overlap once one clears
				break
			}
			overlaps[i] = true
			overlaps[j] = true
		}
	}
	n := 0
	for _, o := range overlaps {
		if o {
			n++
		}
	}
	return n
}
