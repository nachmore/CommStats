package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultAPIBase = "https://slack.com/api"

// Credentials authenticate as a Slack web-client session: a user session token
// (xoxc-) paired with the `d` cookie (xoxd-). This is the "direct" login — the
// same pair the browser uses — so stats reflect the real signed-in user.
type Credentials struct {
	Token        string // xoxc-...
	Cookie       string // full Cookie header value, must include d=xoxd-...
	WorkspaceURL string // e.g. https://acme.enterprise.slack.com (optional)
}

// client is a thin Slack Web API client for the endpoints we need. It mirrors
// the auth approach of the CliBridge reference: token in the form body, the
// session cookie + an app.slack.com Origin header on every request.
// minInterval paces API calls. search.messages is a restricted tier on Slack;
// ~1 call / 3s (≈20/min) keeps long backfills (a year ≈ 365+ calls) under the
// limit without tripping 429s.
const minInterval = 3 * time.Second

type client struct {
	http    *http.Client
	creds   Credentials
	apiBase string

	teamID       string
	enterpriseID string

	// nextAllowed is the earliest time the next request may go out; serialized
	// by mu so concurrent callers still pace correctly. mu also guards
	// userCache.
	mu          sync.Mutex
	nextAllowed time.Time
	userCache   map[string]string // user ID -> resolved display name
}

func newClient(creds Credentials) *client {
	apiBase := defaultAPIBase
	// Enterprise grids serve the API from their own origin; prefer it.
	if strings.Contains(creds.WorkspaceURL, ".enterprise.slack.com") {
		if origin := originOf(creds.WorkspaceURL); origin != "" {
			apiBase = origin + "/api"
		}
	}
	return &client{
		http:    &http.Client{Timeout: 30 * time.Second},
		creds:   creds,
		apiBase: apiBase,
	}
}

// throttle blocks until the configured min-interval has elapsed since the last
// request, or until ctx is cancelled.
func (c *client) throttle(ctx context.Context) error {
	c.mu.Lock()
	wait := time.Until(c.nextAllowed)
	// Reserve this slot, then schedule the following one a full interval out.
	if wait < 0 {
		wait = 0
	}
	c.nextAllowed = time.Now().Add(wait + minInterval)
	c.mu.Unlock()

	if wait == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// maxRetries bounds how many times call retries a single request on HTTP 429.
const maxRetries = 5

// call POSTs form values to a Slack method and decodes the JSON response into
// out. The token is injected automatically. Requests are paced by the rate
// limiter and retried on HTTP 429, honoring the server's Retry-After.
func (c *client) call(ctx context.Context, method string, form url.Values, out any) error {
	if form == nil {
		form = url.Values{}
	}
	form.Set("token", c.creds.Token)
	body := form.Encode()

	for attempt := 0; ; attempt++ {
		if err := c.throttle(ctx); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.apiBase+"/"+method, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// Enterprise Slack rejects API calls without a recognized Origin.
		req.Header.Set("Origin", "https://app.slack.com")
		if c.creds.Cookie != "" {
			req.Header.Set("Cookie", c.creds.Cookie)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			if attempt >= maxRetries {
				return fmt.Errorf("%s: rate limited (HTTP 429) after %d retries", method, maxRetries)
			}
			if err := c.waitRetryAfter(ctx, resp.Header.Get("Retry-After")); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
		}

		dec := json.NewDecoder(resp.Body)
		err = dec.Decode(out)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("%s: decode: %w", method, err)
		}
		return nil
	}
}

// waitRetryAfter sleeps for the Retry-After header's seconds (falling back to a
// sane default), or returns early if ctx is cancelled.
func (c *client) waitRetryAfter(ctx context.Context, header string) error {
	wait := 30 * time.Second // Slack's typical retry window when unspecified
	if header != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
			wait = time.Duration(secs) * time.Second
		}
	}
	// Push the limiter's next slot out past the cooldown so subsequent calls
	// don't immediately re-trip the limit.
	c.mu.Lock()
	c.nextAllowed = time.Now().Add(wait + minInterval)
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

type authTestResponse struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error"`
	UserID       string `json:"user_id"`
	TeamID       string `json:"team_id"`
	EnterpriseID string `json:"enterprise_id"`
}

// authTest verifies the credentials and captures team/enterprise IDs needed by
// later calls on grid workspaces. Returns the authenticated user ID.
func (c *client) authTest(ctx context.Context) (userID string, err error) {
	var r authTestResponse
	if err := c.call(ctx, "auth.test", nil, &r); err != nil {
		return "", err
	}
	if !r.OK {
		return "", fmt.Errorf("auth.test failed: %s", r.Error)
	}
	c.teamID = r.TeamID
	c.enterpriseID = r.EnterpriseID
	return r.UserID, nil
}

// searchMessages runs a single page of search.messages and returns the total
// match count plus the matches on the requested page. The web client uses this
// endpoint with xoxc tokens; query syntax supports `from:@me on:YYYY-MM-DD`.
func (c *client) searchMessages(ctx context.Context, query string, page int) (*searchPage, error) {
	form := url.Values{}
	form.Set("query", query)
	form.Set("count", "100")
	form.Set("page", fmt.Sprint(page))
	if c.teamID != "" {
		form.Set("team_id", c.teamID)
	}
	var r searchMessagesResponse
	if err := c.call(ctx, "search.messages", form, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, fmt.Errorf("search.messages failed: %s", r.Error)
	}
	return &r.Messages, nil
}

type searchMessagesResponse struct {
	OK       bool       `json:"ok"`
	Error    string     `json:"error"`
	Messages searchPage `json:"messages"`
}

type searchPage struct {
	Total   int           `json:"total"`
	Paging  searchPaging  `json:"paging"`
	Matches []searchMatch `json:"matches"`
}

type searchPaging struct {
	Count int `json:"count"`
	Total int `json:"total"`
	Page  int `json:"page"`
	Pages int `json:"pages"`
}

type searchMatch struct {
	Type    string `json:"type"`
	Channel struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		User      string `json:"user"` // for IMs, the other participant's user ID
		IsChannel bool   `json:"is_channel"`
		IsGroup   bool   `json:"is_group"`
		IsIM      bool   `json:"is_im"`
		IsMPIM    bool   `json:"is_mpim"`
		IsPrivate bool   `json:"is_private"`
	} `json:"channel"`
	TS string `json:"ts"`
}

type userInfoResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	User  struct {
		Name    string `json:"name"`
		Profile struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
		} `json:"profile"`
	} `json:"user"`
}

// userName resolves a user ID to a human display name via users.info, caching
// results for the lifetime of the client so a backfill never looks up the same
// user twice. On failure it falls back to the raw ID so collection never aborts
// over a single unresolvable user.
func (c *client) userName(ctx context.Context, userID string) string {
	if userID == "" {
		return ""
	}
	c.mu.Lock()
	if c.userCache == nil {
		c.userCache = map[string]string{}
	}
	if name, ok := c.userCache[userID]; ok {
		c.mu.Unlock()
		return name
	}
	c.mu.Unlock()

	form := url.Values{}
	form.Set("user", userID)
	var r userInfoResponse
	name := userID
	if err := c.call(ctx, "users.info", form, &r); err == nil && r.OK {
		switch {
		case r.User.Profile.DisplayName != "":
			name = r.User.Profile.DisplayName
		case r.User.Profile.RealName != "":
			name = r.User.Profile.RealName
		case r.User.Name != "":
			name = r.User.Name
		}
	}

	c.mu.Lock()
	c.userCache[userID] = name
	c.mu.Unlock()
	return name
}

// originOf returns scheme://host for a URL, or "" if it can't be parsed.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
