// Package slack is the Slack comm-source. It self-registers on import so the
// CLI only needs a blank import to wire it in.
//
// Auth uses the "direct" web-client session: a user token (xoxc-) plus the `d`
// cookie, read from <ConfigDir>/slack.json. Stats are derived from
// search.messages, the same endpoint the Slack web client uses, which accepts
// xoxc tokens and supports `from:<@U…> on:YYYY-MM-DD` filtering.
package slack

import (
	"context"
	"fmt"

	"github.com/nachmore/commstats/internal/source"
)

func init() { source.Register(New()) }

type Source struct{}

func New() *Source { return &Source{} }

func (*Source) Name() string { return "slack" }

// Login runs the interactive browser login and persists the credentials. Used
// by the explicit `slack login` command.
func (*Source) Login(ctx context.Context) error {
	creds, err := login(ctx)
	if err != nil {
		return err
	}
	return saveCredentials(creds)
}

// Collect reports, for the calendar day of the window's end, the messages the
// user sent broken down per channel: one "messages" metric per channel carrying
// channel_id (stable key), channel_name (display), and channel_type dimensions.
// Aggregate views (total sent, unique channels, by-type, top channels) are
// derived from these rows at report time. Counts are absolute for the day, so
// same-day re-runs upsert cleanly.
//
// If Slack isn't configured, Collect returns no metrics and no error so it
// doesn't break a multi-source run.
func (s *Source) Collect(ctx context.Context, w source.TimeWindow) ([]source.Metric, error) {
	creds, err := loadCredentials()
	if err == errNoConfig {
		// Foreground runs trigger interactive login; scheduled runs skip
		// gracefully so a missing config never pops a browser unattended.
		if !source.IsInteractive(ctx) {
			return nil, nil
		}
		if creds, err = loadOrLogin(ctx); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	c := newClient(creds)
	userID, err := c.authTest(ctx)
	if err != nil {
		return nil, err
	}

	// Use the explicit user ID rather than the `from:@me` shorthand: on
	// enterprise grids `@me` silently matches nothing, whereas `from:<@U…>`
	// works everywhere.
	day := w.End.Format("2006-01-02")
	query := fmt.Sprintf("from:<@%s> on:%s", userID, day)

	type channelStat struct {
		name  string
		typ   string
		count int
	}
	byChannel := map[string]*channelStat{}

	for page := 1; ; page++ {
		res, err := c.searchMessages(ctx, query, page)
		if err != nil {
			return nil, err
		}
		for _, m := range res.Matches {
			id := m.Channel.ID
			cs := byChannel[id]
			if cs == nil {
				cs = &channelStat{name: channelName(ctx, c, m), typ: channelType(m)}
				byChannel[id] = cs
			}
			cs.count++
		}
		if page >= res.Paging.Pages || res.Paging.Pages == 0 {
			break
		}
	}

	metrics := make([]source.Metric, 0, len(byChannel))
	for id, cs := range byChannel {
		metrics = append(metrics, source.Metric{
			Source: s.Name(),
			Name:   "messages",
			Value:  float64(cs.count),
			Window: w,
			Dimensions: map[string]string{
				"channel_id":   id,
				"channel_name": cs.name,
				"channel_type": cs.typ,
			},
		})
	}
	return metrics, nil
}

// channelName returns a display name for the match's channel. For IMs the
// channel "name" is just the other user's ID, so resolve it to a real name via
// users.info (cached). Named channels use their name directly.
func channelName(ctx context.Context, c *client, m searchMatch) string {
	if m.Channel.IsIM {
		uid := m.Channel.User
		if uid == "" {
			uid = m.Channel.Name // fallback: IM channel name holds the user ID
		}
		if name := c.userName(ctx, uid); name != "" {
			return "@" + name
		}
		return "im:" + m.Channel.ID
	}
	if m.Channel.Name != "" {
		return m.Channel.Name
	}
	return m.Channel.ID
}

// channelType maps a search match's channel flags to a stable label used as a
// metric dimension.
func channelType(m searchMatch) string {
	switch {
	case m.Channel.IsIM:
		return "dm"
	case m.Channel.IsMPIM:
		return "group-dm"
	case m.Channel.IsGroup || m.Channel.IsPrivate:
		return "private_channel"
	case m.Channel.IsChannel:
		return "public_channel"
	default:
		return "unknown"
	}
}
