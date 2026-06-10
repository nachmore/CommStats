package outlook

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nachmore/commstats/internal/config"
	"github.com/nachmore/commstats/internal/source"
)

// interval is a busy time span, used for overbooking detection.
type interval struct{ start, end time.Time }

// collectCalendar turns a day's events into per-day histogram metrics. The
// window w is attached to every metric; homeOrg is the registrable org label of
// the signed-in user (e.g. "amazon") used for external-participant detection.
// dayStart/dayEnd bound the calendar day so multi-day or midnight-spanning
// events only contribute the portion that falls within the day (otherwise a
// single 3-day event would inflate one day past 24h).
func collectCalendar(srcName string, w source.TimeWindow, events []event, selfAddr, homeOrg string, dayStart, dayEnd time.Time, catFilter config.CategoryFilter, titleFilter config.TitleFilter) []source.Metric {
	var (
		byType     = map[string]int{} // entry kind: meeting/all-day/all-day-free/personal-block
		bySize     = map[string]int{} // participant-count buckets (real meetings only)
		byDuration = map[string]int{}
		byRole     = map[string]int{}
		byResponse = map[string]int{}
		byCategory = map[string]int{}
		byScope    = map[string]int{} // internal / external
		byHour     = map[int]int{}    // meeting start hour (local)
		totalMin   float64
		// Per-partition meeting minutes (real meetings only), so the overview can
		// chart hours — not just counts — by category/scope/size.
		minBySize     = map[string]float64{}
		minByScope    = map[string]float64{}
		minByCategory = map[string]float64{}
	)

	// Busy intervals for overbooking detection.
	var busy []interval
	// Real-meeting intervals, merged for actual (de-overlapped) time spent.
	var meetingIvls []interval

	oooCount := 0
	for _, e := range events {
		// Skip events whose category is configured not to count as a meeting
		// (e.g. Room Bookings, DND, Doc Writing, Family Block).
		if catFilter.Excludes(e.Categories) {
			continue
		}
		// Title-based exclusion: personal all-day holds (e.g. "CL block") that
		// carry no category but shouldn't count as meetings.
		if titleFilter.Excludes(e.Subject) {
			continue
		}
		// Out-of-office holds (OOO/OOTO/OOF) — frequently a colleague mistakenly
		// sends a *busy* OOO that lands on your calendar. They aren't meetings;
		// exclude from all meeting stats but tally as a fun extra metric.
		if isOOO(e.Subject) {
			oooCount++
			continue
		}
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

		// Type classifies every calendar entry by what kind it is — distinct
		// from participant size, which only applies to real meetings.
		entryType := ""
		switch {
		case e.IsAllDay && free:
			entryType = "all-day-free" // e.g. an informational all-day banner
		case e.IsAllDay:
			entryType = "all-day"
		case others == 0:
			entryType = "personal-block"
		default:
			entryType = "meeting"
		}
		byType[entryType]++

		// The remaining breakdowns describe actual meetings (with other people),
		// not all-day items or personal blocks.
		isMeeting := !e.IsAllDay && others > 0
		if isMeeting {
			mins := dur.Minutes()
			bySize[sizeBucket(others+1)]++
			minBySize[sizeBucket(others+1)] += mins
			byDuration[durationBucket(dur, e.IsAllDay)]++
			if e.IsOrganizer {
				byRole["organizer"]++
			} else {
				byRole["attendee"]++
			}
			byResponse[responseBucket(e.IsOrganizer, e.ResponseStatus.Response)]++
			if hasExternal {
				byScope["external"]++
				minByScope["external"] += mins
			} else {
				byScope["internal"]++
				minByScope["internal"] += mins
			}
			if okS {
				byHour[st.Local().Hour()]++
			}
			totalMin += mins
			for _, cat := range e.Categories {
				if cat != "" {
					minByCategory[cat] += mins
				}
			}
		}

		// Busy-time contribution (for de-overlapped time-spent): real meetings
		// contribute their in-day span; busy all-day events (offsites — not the
		// free informational banners) contribute a nominal capped block so a
		// 24h all-day event counts as a normal workday, not a literal 24h.
		if isMeeting && !free && okS && okE && en.After(st) {
			meetingIvls = append(meetingIvls, clampInterval(st, en, dayStart, dayEnd))
		} else if e.IsAllDay && !free {
			// 09:00–17:00 local nominal workday block, capped at allDayBusyCap.
			bs := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(), 9, 0, 0, 0, dayStart.Location())
			meetingIvls = append(meetingIvls, clampInterval(bs, bs.Add(allDayBusyCap), dayStart, dayEnd))
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
	add("meetings", byType, "type")
	add("meetings", bySize, "size")
	add("meetings", byDuration, "duration")
	add("meetings", byRole, "role")
	add("meetings", byResponse, "response")
	add("meetings", byScope, "scope")
	add("meetings", byCategory, "category")
	// Per-partition meeting minutes (real meetings only) for hours-based charts.
	addMin := func(name string, counts map[string]float64, dimKey string) {
		for k, v := range counts {
			metrics = append(metrics, source.Metric{Source: srcName, Name: name, Value: v,
				Window: w, Dimensions: map[string]string{dimKey: k}})
		}
	}
	addMin("meeting_minutes_by", minBySize, "size")
	addMin("meeting_minutes_by", minByScope, "scope")
	addMin("meeting_minutes_by", minByCategory, "category")
	// Meeting start hour (local), as its own ordered histogram metric.
	for h, v := range byHour {
		metrics = append(metrics, src("meetings_by_hour", v, map[string]string{"hour": fmt.Sprintf("%02d", h)}))
	}
	metrics = append(metrics,
		source.Metric{Source: srcName, Name: "meeting_minutes", Value: totalMin, Window: w},
		source.Metric{Source: srcName, Name: "meeting_busy_minutes", Value: mergedMinutes(meetingIvls), Window: w},
		source.Metric{Source: srcName, Name: "calendar_overbookings", Value: float64(overbooked), Window: w},
		source.Metric{Source: srcName, Name: "ooo_blocks", Value: float64(oooCount), Window: w},
	)

	// Focus time: the longest meeting-free block within the 09:00–17:00 work
	// window — the headline "deep work" capacity for the day. Only emitted on
	// weekdays; a weekend's wide-open calendar isn't protected focus time and
	// would otherwise inflate the average.
	if wd := dayStart.Weekday(); wd != time.Saturday && wd != time.Sunday {
		workStart := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(), 9, 0, 0, 0, dayStart.Location())
		workEnd := time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(), 17, 0, 0, 0, dayStart.Location())
		metrics = append(metrics, source.Metric{Source: srcName, Name: "focus_minutes",
			Value: longestFreeBlock(meetingIvls, workStart, workEnd), Window: w})
	}
	return metrics
}

// oooRe matches out-of-office hold titles: the acronyms OOO/OOTO/OOF (as whole
// words, case-insensitive, so they don't false-match inside other words) or the
// phrase "out of office".
var oooRe = regexp.MustCompile(`(?i)(\boo+(t?o|f)\b|out of office)`)

// isOOO reports whether an event title denotes an out-of-office hold.
func isOOO(subject string) bool { return oooRe.MatchString(subject) }

// sizeBucket classifies a meeting by total headcount (including self). Labels
// carry the participant range so "medium" etc. is self-explanatory in reports.
func sizeBucket(total int) string {
	switch {
	case total <= 2:
		return "1:1 (2)"
	case total <= 5:
		return "small (3-5)"
	case total <= 10:
		return "medium (6-10)"
	default:
		return "large (11+)"
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

// allDayBusyCap is the time a busy all-day event (e.g. an offsite) contributes
// to time-spent — a nominal workday rather than a literal 24h.
const allDayBusyCap = 8 * time.Hour

// clampInterval restricts [s,e] to the [dayStart,dayEnd] window.
func clampInterval(s, e, dayStart, dayEnd time.Time) interval {
	if s.Before(dayStart) {
		s = dayStart
	}
	if e.After(dayEnd) {
		e = dayEnd
	}
	return interval{s, e}
}

// mergedMinutes returns the total minutes covered by the union of the given
// intervals, so overlapping (double-booked) meetings count their wall-clock
// time once — the real "time spent in meetings".
func mergedMinutes(iv []interval) float64 {
	sorted := make([]interval, 0, len(iv))
	for _, v := range iv {
		if v.end.After(v.start) { // drop empty/inverted intervals
			sorted = append(sorted, v)
		}
	}
	if len(sorted) == 0 {
		return 0
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start.Before(sorted[j].start) })
	var total float64
	curStart, curEnd := sorted[0].start, sorted[0].end
	for _, v := range sorted[1:] {
		if v.start.After(curEnd) {
			total += curEnd.Sub(curStart).Minutes()
			curStart, curEnd = v.start, v.end
		} else if v.end.After(curEnd) {
			curEnd = v.end
		}
	}
	total += curEnd.Sub(curStart).Minutes()
	return total
}

// longestFreeBlock returns the longest stretch of minutes inside [workStart,
// workEnd] not covered by any meeting interval. Meetings are clamped to the
// work window first, then merged; the answer is the largest gap between merged
// busy spans (including the leading gap before the first meeting and the
// trailing gap after the last). A fully free work window returns its full
// length; a fully booked one returns 0.
func longestFreeBlock(iv []interval, workStart, workEnd time.Time) float64 {
	if !workEnd.After(workStart) {
		return 0
	}
	// Clamp meeting intervals to the work window, dropping those outside it.
	var busy []interval
	for _, v := range iv {
		c := clampInterval(v.start, v.end, workStart, workEnd)
		if c.end.After(c.start) {
			busy = append(busy, c)
		}
	}
	if len(busy) == 0 {
		return workEnd.Sub(workStart).Minutes()
	}
	sort.Slice(busy, func(i, j int) bool { return busy[i].start.Before(busy[j].start) })
	var best float64
	cursor := workStart // end of the busy coverage seen so far
	for _, b := range busy {
		if gap := b.start.Sub(cursor).Minutes(); gap > best {
			best = gap
		}
		if b.end.After(cursor) {
			cursor = b.end
		}
	}
	if gap := workEnd.Sub(cursor).Minutes(); gap > best {
		best = gap
	}
	return best
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
