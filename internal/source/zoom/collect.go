package zoom

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nachmore/commstats/internal/source"
)

// Month-keyed CSV cache: the Zoom export is slow (queue + poll + download) and
// capped at one month, so during a backfill we export each calendar month once
// and serve every day in it from the cached rows.
var (
	monthMu    sync.Mutex
	monthCache = map[string][]meeting{}
)

// exportForDay returns the meetings whose start falls on dayStart's calendar
// day, exporting (and caching) that day's whole month on first use.
func exportForDay(ctx context.Context, c *client, dayStart time.Time) ([]meeting, error) {
	key := dayStart.Format("2006-01")
	monthMu.Lock()
	rows, ok := monthCache[key]
	monthMu.Unlock()
	if !ok {
		mStart := time.Date(dayStart.Year(), dayStart.Month(), 1, 0, 0, 0, 0, dayStart.Location())
		mEnd := mStart.AddDate(0, 1, -1) // last day of month
		var err error
		rows, err = c.meetings(ctx, mStart, mEnd)
		if err != nil {
			return nil, fmt.Errorf("zoom export %s: %w", key, err)
		}
		monthMu.Lock()
		monthCache[key] = rows
		monthMu.Unlock()
	}

	dayEnd := dayStart.AddDate(0, 0, 1)
	var day []meeting
	for _, m := range rows {
		if !m.Start.Before(dayStart) && m.Start.Before(dayEnd) {
			day = append(day, m)
		}
	}
	return day, nil
}

// bucketMeetings turns a day's meetings into per-day histogram metrics:
// totals, size buckets (by actual participant count), duration buckets, and
// start-hour — mirroring the calendar source's shape.
func bucketMeetings(srcName string, w source.TimeWindow, rows []meeting, dayStart, dayEnd time.Time) []source.Metric {
	var (
		count        int
		totalMin     float64
		participantM float64
		bySize       = map[string]int{}
		byDuration   = map[string]int{}
		byHour       = map[int]int{}
	)
	for _, m := range rows {
		count++
		totalMin += m.DurationMin
		participantM += m.ParticipantM
		bySize[sizeBucket(m.Participants)]++
		byDuration[durationBucket(m.DurationMin)]++
		byHour[m.Start.Hour()]++
	}

	dim := func(name string, v int, k, val string) source.Metric {
		return source.Metric{Source: srcName, Name: name, Value: float64(v), Window: w,
			Dimensions: map[string]string{k: val}}
	}

	metrics := []source.Metric{
		{Source: srcName, Name: "zoom_meetings", Value: float64(count), Window: w},
		{Source: srcName, Name: "zoom_minutes", Value: totalMin, Window: w},
		{Source: srcName, Name: "participant_minutes", Value: participantM, Window: w},
	}
	for k, v := range bySize {
		metrics = append(metrics, dim("meetings", v, "size", k))
	}
	for k, v := range byDuration {
		metrics = append(metrics, dim("meetings", v, "duration", k))
	}
	for h, v := range byHour {
		metrics = append(metrics, dim("meetings_by_hour", v, "hour", fmt.Sprintf("%02d", h)))
	}
	return metrics
}

// sizeBucket classifies a meeting by actual participant count. solo means only
// the host showed (others no-showed) — a distinct, interesting signal.
func sizeBucket(participants int) string {
	switch {
	case participants <= 1:
		return "solo"
	case participants == 2:
		return "1:1"
	case participants <= 5:
		return "small"
	case participants <= 10:
		return "medium"
	default:
		return "large"
	}
}

// durationBucket classifies a meeting by length, matching the calendar source's
// buckets for cross-source comparability.
func durationBucket(min float64) string {
	switch {
	case min < 30:
		return "<30m"
	case min == 30:
		return "30m"
	case min < 60:
		return "30-60m"
	case min == 60:
		return "1h"
	case min < 120:
		return "1-2h"
	default:
		return "2h+"
	}
}
