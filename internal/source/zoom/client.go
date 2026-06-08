package zoom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// reportType is the Zoom usage report we export: active host by user/meeting,
// which yields per-meeting rows with actual participant counts.
const reportType = "download.report.type.activehostbyuserormeeting"

type client struct {
	http *http.Client
	host string // company Zoom host (e.g. zoom.us or <co>.zoom.us)

	// CSRF token (OWASP CSRFGuard): name+value fetched from /csrf_js and sent
	// as a request header on state-changing POSTs. The name is dynamic.
	csrfName  string
	csrfValue string
}

func newClient(jar http.CookieJar, host string) *client {
	if host == "" {
		host = "zoom.us"
	}
	return &client{
		http: &http.Client{Jar: jar, Timeout: 60 * time.Second},
		host: host,
	}
}

func (c *client) url(path string) string { return "https://" + c.host + path }

// meeting is one parsed CSV row we care about.
type meeting struct {
	Topic        string
	Start        time.Time
	DurationMin  float64
	Participants int
	ParticipantM float64
}

// meetings exports and parses the usage report for [from, to] (inclusive),
// honoring Zoom's 1-month-per-query cap by chunking. Dates are by meeting Start
// Time.
func (c *client) meetings(ctx context.Context, from, to time.Time) ([]meeting, error) {
	if err := c.fetchCSRF(ctx); err != nil {
		return nil, err
	}
	var all []meeting
	for start := from; !start.After(to); start = start.AddDate(0, 0, 30) {
		end := start.AddDate(0, 0, 29)
		if end.After(to) {
			end = to
		}
		rows, err := c.exportChunk(ctx, start, end)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	return all, nil
}

// fetchCSRF gets a CSRFGuard token: POST /csrf_js with FETCH-CSRF-TOKEN:1
// returns a body of the form "TOKEN_NAME:TOKEN_VALUE".
func (c *client) fetchCSRF(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/csrf_js"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("FETCH-CSRF-TOKEN", "1")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("zoom csrf: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	parts := strings.SplitN(strings.TrimSpace(string(body)), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("zoom csrf: unexpected token response %q", string(body))
	}
	c.csrfName, c.csrfValue = parts[0], parts[1]
	return nil
}

func zoomDate(t time.Time) string { return t.Format("01/02/2006") }

// exportChunk runs export→poll→download for one <=1-month window and parses CSV.
func (c *client) exportChunk(ctx context.Context, from, to time.Time) ([]meeting, error) {
	genTime, hashkey, err := c.requestExport(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if err := c.pollExport(ctx); err != nil {
		return nil, err
	}
	return c.download(ctx, genTime, hashkey)
}

type exportResp struct {
	Status bool `json:"status"`
	Result []struct {
		Status       string `json:"status"`
		GenerateTime string `json:"generateTime"`
		FileName     string `json:"fileName"`
		UserID       string `json:"userId"`
	} `json:"result"`
}

// requestExport POSTs meeting/export and returns the generateTime (rangekey)
// and userId (hashkey) the download endpoint needs to locate the file.
func (c *client) requestExport(ctx context.Context, from, to time.Time) (genTime, hashkey string, err error) {
	form := url.Values{}
	form.Set("from", zoomDate(from))
	form.Set("to", zoomDate(to))
	form.Set("reportType", reportType)
	form.Set("id", "")
	form.Set("groupId", "")
	form.Set("scheduleForUserId", "")
	form.Set("meetingNumber", "")

	var r exportResp
	if err := c.postForm(ctx, "/account/my/report/meeting/export", form, &r); err != nil {
		return "", "", err
	}
	if len(r.Result) == 0 {
		return "", "", fmt.Errorf("zoom export: empty queue response")
	}
	return r.Result[0].GenerateTime, r.Result[0].UserID, nil
}

// pollExport calls export_ask until the latest export reports complete.
func (c *client) pollExport(ctx context.Context) error {
	for attempt := 0; attempt < 30; attempt++ {
		form := url.Values{}
		form.Set("reportType", reportType)
		form.Set("askCount", strconv.Itoa(attempt+1))
		var r exportResp
		if err := c.postForm(ctx, "/account/my/report/export_ask", form, &r); err != nil {
			return err
		}
		for _, res := range r.Result {
			if strings.Contains(res.Status, "complete") {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("zoom export: timed out waiting for report generation")
}

// download fetches the generated CSV and parses it.
func (c *client) download(ctx context.Context, genTime, hashkey string) ([]meeting, error) {
	form := url.Values{}
	form.Set("hashkey", hashkey)
	form.Set("rangekey", genTime)
	form.Set("reportType", reportType)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.url("/account/my/export/download"), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zoom download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zoom download: HTTP %d", resp.StatusCode)
	}
	return parseCSV(resp.Body)
}

// postForm POSTs a urlencoded form (with CSRF + XHR headers) and decodes JSON.
func (c *client) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(path), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("zoom %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zoom %s: HTTP %d", path, resp.StatusCode)
	}
	return decodeJSON(resp.Body, out)
}

// setHeaders applies the form content type, Origin, the XHR marker CSRFGuard
// checks, and the dynamic CSRF token header.
func (c *client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://"+c.host)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if c.csrfName != "" {
		req.Header.Set(c.csrfName, c.csrfValue)
	}
}
