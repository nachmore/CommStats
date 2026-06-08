// Package zoom is the Zoom comm-source. It reuses the authenticated Zoom web
// session (via a CDP-driven browser with a persistent profile, like the Outlook
// source) to export the usage report CSV, which carries per-meeting actual
// participant counts and durations. The company Zoom host is detected at
// runtime from the post-login URL — nothing company-specific is hardcoded.
package zoom

import (
	"context"
	"fmt"
	"time"

	"github.com/nachmore/commstats/internal/source"
)

func init() { source.Register(New()) }

type Source struct{}

func New() *Source { return &Source{} }

func (*Source) Name() string { return "zoom" }

// Login opens Zoom web for interactive sign-in, then exports the last 30 days
// as a verification. The persistent profile lets later Collect runs export
// silently.
func (*Source) Login(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	fmt.Println("Opening Zoom web — sign in if prompted…")
	jar, host, err := captureSession(ctx, false)
	if err != nil {
		return err
	}
	to := time.Now()
	from := to.AddDate(0, 0, -29)
	rows, err := newClient(jar, host).meetings(ctx, from, to)
	if err != nil {
		return fmt.Errorf("zoom: session captured but export failed: %w", err)
	}
	fmt.Printf("zoom: authorized — %d meetings in the last 30 days\n", len(rows))
	return nil
}

// Collect reports Zoom meeting usage for the window's day, derived from the
// usage-report CSV (real participant counts + durations). Counts are absolute
// for the day so same-day re-runs upsert cleanly.
//
// Exporting hits a daily window; the per-process session+CSV cache means a
// multi-day backfill exports each needed month once rather than per day.
func (s *Source) Collect(ctx context.Context, w source.TimeWindow) ([]source.Metric, error) {
	interactive := source.IsInteractive(ctx)
	if !interactive && !profileExists() {
		return nil, nil
	}

	jar, host, err := getSession(ctx, !interactive)
	if err != nil {
		if !interactive {
			return nil, nil
		}
		return nil, err
	}

	day := w.End
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	dayEnd := dayStart.AddDate(0, 0, 1)

	rows, err := exportForDay(ctx, newClient(jar, host), dayStart)
	if err != nil {
		return nil, err
	}

	return bucketMeetings(s.Name(), w, rows, dayStart, dayEnd), nil
}
