package outlook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type client struct {
	http  *http.Client
	token string
}

func newClient(token string) *client {
	return &client{http: &http.Client{Timeout: 30 * time.Second}, token: token}
}

// count returns how many items at the given collection path match the filter,
// using the $count=true query form (which returns @odata.count in a JSON body —
// cleaner than the text/plain /$count path endpoint).
func (c *client) count(ctx context.Context, path string, query url.Values) (int, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("$count", "true")
	query.Set("$top", "1") // we only want the count, not the items

	u := apiBase + path + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("outlook GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("outlook GET %s: HTTP %d", path, resp.StatusCode)
	}

	var body struct {
		Count int `json:"@odata.count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("outlook decode %s: %w", path, err)
	}
	return body.Count, nil
}

// myAddress returns the signed-in user's primary email address, used to
// identify self in attendee lists and to derive the home org for external-
// participant detection.
func (c *client) myAddress(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("outlook /me: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("outlook /me: HTTP %d", resp.StatusCode)
	}
	var m struct {
		EmailAddress string `json:"EmailAddress"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return "", err
	}
	return m.EmailAddress, nil
}

// event is the subset of a calendar event we bucket on.
type event struct {
	Subject     string   `json:"Subject"`
	IsAllDay    bool     `json:"IsAllDay"`
	IsOrganizer bool     `json:"IsOrganizer"`
	ShowAs      string   `json:"ShowAs"`
	Categories  []string `json:"Categories"`
	Start       struct {
		DateTime string `json:"DateTime"`
		TimeZone string `json:"TimeZone"`
	} `json:"Start"`
	End struct {
		DateTime string `json:"DateTime"`
		TimeZone string `json:"TimeZone"`
	} `json:"End"`
	ResponseStatus struct {
		Response string `json:"Response"`
	} `json:"ResponseStatus"`
	Attendees []struct {
		Type         string `json:"Type"`
		EmailAddress struct {
			Address string `json:"Address"`
		} `json:"EmailAddress"`
	} `json:"Attendees"`
}

// calendarEvents fetches all events in [start, end) via CalendarView, following
// @odata.nextLink pagination. Returns the selected fields needed for bucketing.
func (c *client) calendarEvents(ctx context.Context, start, end time.Time) ([]event, error) {
	q := url.Values{}
	q.Set("startDateTime", outlookTime(start))
	q.Set("endDateTime", outlookTime(end))
	q.Set("$select", "Subject,Start,End,IsAllDay,IsOrganizer,ShowAs,Categories,ResponseStatus,Attendees")
	q.Set("$top", "100")
	next := apiBase + "/me/calendarview?" + q.Encode()

	var out []event
	for next != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("outlook calendarview: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("outlook calendarview: HTTP %d", resp.StatusCode)
		}
		var page struct {
			Value    []event `json:"value"`
			NextLink string  `json:"@odata.nextLink"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("outlook calendarview decode: %w", err)
		}
		out = append(out, page.Value...)
		next = page.NextLink
	}
	return out, nil
}

// parseEventTime parses the Outlook event datetime ("2006-01-02T15:04:05.000…")
// which CalendarView returns in UTC, into a time.Time (UTC).
func parseEventTime(s string) (time.Time, bool) {
	// Trim fractional seconds beyond what Go's layout handles by trying a few.
	for _, layout := range []string{
		"2006-01-02T15:04:05.0000000",
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// filterRange builds a $filter selecting items where field is in [lo, hi).
func filterRange(field, lo, hi string) url.Values {
	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("%s ge %s and %s lt %s", field, lo, field, hi))
	return q
}

// outlookTime formats t as the UTC ISO-8601 the Outlook REST API expects.
func outlookTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
