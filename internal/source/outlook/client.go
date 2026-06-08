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

// calendarCount returns how many calendar events fall in [start, end) using the
// CalendarView endpoint, which is the correct way to query a date range (it
// also expands recurring series into instances). Date range goes in the
// startDateTime/endDateTime query params, not a $filter.
func (c *client) calendarCount(ctx context.Context, start, end time.Time) (int, error) {
	q := url.Values{}
	q.Set("startDateTime", outlookTime(start))
	q.Set("endDateTime", outlookTime(end))
	return c.count(ctx, "/me/calendarview", q)
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
