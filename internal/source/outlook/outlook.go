package outlook

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/nachmore/commstats/internal/config"
	"github.com/nachmore/commstats/internal/source"
)

func init() { source.Register(New()) }

// The Outlook plugin collects two logically distinct comm mediums from one
// Microsoft 365 session. They're stored (and reported) under separate source
// names so email and calendar get their own report tabs, even though a single
// plugin handles login/collection.
const (
	sourceEmail    = "email"
	sourceCalendar = "calendar"
)

type Source struct{}

func New() *Source { return &Source{} }

// Name is the plugin/registration name; emitted metrics use the per-medium
// source names above.
func (*Source) Name() string { return "outlook" }

// Login opens Outlook web in a visible browser for the user to sign in, then
// captures the outlook.office.com bearer token and makes one verification call
// (today's inbox count) to confirm the token + endpoint work. The persistent
// browser profile means subsequent Collect runs can capture silently.
func (*Source) Login(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	fmt.Println("Opening Outlook web — sign in if prompted and let the mailbox load…")
	token, err := captureToken(ctx, false)
	if err != nil {
		return err
	}

	// Verify against yesterday, which (unlike early-in-the-day today) reliably
	// has a full day of mail to count.
	yesterday := time.Now().AddDate(0, 0, -1)
	n, err := newClient(token).count(ctx,
		"/me/mailfolders/inbox/messages", dayFilter("ReceivedDateTime", yesterday))
	if err != nil {
		return fmt.Errorf("outlook: token captured but verification call failed: %w", err)
	}
	fmt.Printf("outlook: authorized — %d message(s) received in the inbox on %s\n",
		n, yesterday.Format("2006-01-02"))
	return nil
}

// Collect reports email and calendar usage for the window's day. It captures a
// fresh outlook.office.com token from the persistent browser profile (silent
// once logged in), then issues per-metric count queries. Counts are absolute
// for the day so same-day re-runs upsert cleanly.
//
// If Outlook isn't authorized yet, a scheduled (non-interactive) run skips it
// cleanly; a foreground run opens a visible browser to sign in.
func (s *Source) Collect(ctx context.Context, w source.TimeWindow) ([]source.Metric, error) {
	interactive := source.IsInteractive(ctx)

	// Cold profile + scheduled run: skip rather than pop a browser unattended.
	if !interactive && !profileExists() {
		return nil, nil
	}

	token, err := getToken(ctx, !interactive)
	if err != nil {
		if !interactive {
			// Token likely needs re-login; don't break a multi-source run.
			return nil, nil
		}
		return nil, err
	}
	c := newClient(token)
	day := w.End

	email := func(name string, v int) source.Metric {
		return source.Metric{Source: sourceEmail, Name: name, Value: float64(v), Window: w}
	}

	received, err := c.count(ctx, "/me/mailfolders/inbox/messages", dayFilter("ReceivedDateTime", day))
	if err != nil {
		return nil, err
	}
	readFilter := dayFilter("ReceivedDateTime", day)
	readFilter.Set("$filter", readFilter.Get("$filter")+" and IsRead eq true")
	read, err := c.count(ctx, "/me/mailfolders/inbox/messages", readFilter)
	if err != nil {
		return nil, err
	}
	sent, err := c.count(ctx, "/me/mailfolders/sentitems/messages", dayFilter("SentDateTime", day))
	if err != nil {
		return nil, err
	}
	metrics := []source.Metric{
		email("emails_received", received),
		email("emails_read", read),
		email("emails_unread", received-read),
		email("emails_sent", sent),
	}

	// Email hour-of-day histograms (received + sent), so the overview can show
	// combined cross-source busiest hours.
	dStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	dEnd := dStart.AddDate(0, 0, 1)
	recvHours, err := c.messageHours(ctx, "inbox", "ReceivedDateTime", dStart, dEnd)
	if err != nil {
		return nil, err
	}
	sentHours, err := c.messageHours(ctx, "sentitems", "SentDateTime", dStart, dEnd)
	if err != nil {
		return nil, err
	}
	for h, n := range recvHours {
		metrics = append(metrics, source.Metric{Source: sourceEmail, Name: "emails_received_by_hour",
			Value: float64(n), Window: w, Dimensions: map[string]string{"hour": fmt.Sprintf("%02d", h)}})
	}
	for h, n := range sentHours {
		metrics = append(metrics, source.Metric{Source: sourceEmail, Name: "emails_sent_by_hour",
			Value: float64(n), Window: w, Dimensions: map[string]string{"hour": fmt.Sprintf("%02d", h)}})
	}

	// Rich calendar metrics: fetch the day's events and bucket them.
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	dayEnd := dayStart.AddDate(0, 0, 1)
	events, err := c.calendarEvents(ctx, dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	metrics = append(metrics, source.Metric{
		Source: sourceCalendar, Name: "calendar_events", Value: float64(len(events)), Window: w,
	})

	selfAddr, homeOrg, err := getIdentity(ctx, c)
	if err != nil {
		return nil, err
	}
	// Categories configured to not count as meetings (e.g. Room Bookings, DND).
	cfg, _ := config.Load()
	catFilter := config.NewCategoryFilter(cfg.IgnoreMeetingCategories)
	titleFilter := config.NewTitleFilter(cfg.IgnoreMeetingTitles)
	metrics = append(metrics, collectCalendar(sourceCalendar, w, events, selfAddr, homeOrg, dayStart, dayEnd, catFilter, titleFilter)...)

	return metrics, nil
}

// dayFilter builds a $filter selecting items whose field falls on the local
// calendar day of t.
func dayFilter(field string, t time.Time) url.Values {
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return filterRange(field, outlookTime(start), outlookTime(start.AddDate(0, 0, 1)))
}
