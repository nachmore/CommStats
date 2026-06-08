package slack

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nachmore/commstats/internal/platform"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const signinURL = "https://slack.com/signin"

// tokenSniffer hooks fetch() and XHR.send() on every page so that when the
// Slack web client makes its first authenticated API call, we can lift the
// xoxc- token out of the request body. Mirrors the CliBridge approach. The
// captured token is stashed on window for chromedp to poll.
const tokenSniffer = `
(function() {
  if (window.__commstatsInstalled) return;
  window.__commstatsInstalled = true;
  function extractXoxc(s) {
    if (!s || typeof s !== 'string') return null;
    var m = s.match(/xoxc-[A-Za-z0-9-]{20,}/);
    return m ? m[0] : null;
  }
  function bodyToString(body) {
    if (!body) return null;
    if (typeof body === 'string') return body;
    if (body instanceof URLSearchParams) return body.toString();
    if (body instanceof FormData) { var t = body.get && body.get('token'); if (t) return 'token=' + t; }
    return null;
  }
  function report(tok) { if (tok && !window.__commstatsToken) window.__commstatsToken = tok; }
  var of = window.fetch;
  if (of) window.fetch = function(i, init) {
    try { report(extractXoxc(init && init.body ? bodyToString(init.body) : null)); } catch(e){}
    return of.apply(this, arguments);
  };
  var os = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.send = function(b) {
    try { report(extractXoxc(bodyToString(b))); } catch(e){}
    return os.apply(this, arguments);
  };
})();
`

var xoxcRe = regexp.MustCompile(`xoxc-[A-Za-z0-9-]{20,}`)

// login opens a visible browser at the Slack sign-in page, waits for the user
// to authenticate, then captures the xoxc- session token (via an injected
// request sniffer) and the httpOnly `d` cookie (via CDP, which JS cannot read).
// It blocks until both are captured or ctx is cancelled.
func login(ctx context.Context) (Credentials, error) {
	browserPath, err := platform.Current().Browser().ExecutablePath()
	if err != nil {
		return Credentials{}, fmt.Errorf("slack login: %w", err)
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("headless", false),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()

	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	// Inject the sniffer into every new document before page scripts run.
	if err := chromedp.Run(taskCtx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(tokenSniffer).Do(ctx)
			return err
		}),
		chromedp.Navigate(signinURL),
	); err != nil {
		return Credentials{}, fmt.Errorf("slack login: open browser: %w", err)
	}

	fmt.Println("Opened browser for Slack sign-in. Complete login in the window…")

	token, err := waitForToken(taskCtx)
	if err != nil {
		return Credentials{}, err
	}

	cookie, workspaceURL, err := readSlackCookies(taskCtx)
	if err != nil {
		return Credentials{}, err
	}

	return Credentials{Token: token, Cookie: cookie, WorkspaceURL: workspaceURL}, nil
}

// waitForToken polls the injected sniffer's window var until a token appears or
// the context is cancelled.
func waitForToken(ctx context.Context) (string, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("slack login: cancelled before token captured")
		case <-ticker.C:
			var tok string
			// Errors here are expected during navigation; ignore and retry.
			_ = chromedp.Run(ctx, chromedp.Evaluate(`window.__commstatsToken || ""`, &tok))
			if xoxcRe.MatchString(tok) {
				return tok, nil
			}
		}
	}
}

// readSlackCookies reads cookies from the browser via CDP (covering the
// httpOnly `d` cookie that JS can't see) and returns a Cookie header value plus
// the best-guess workspace origin. Requires the `d` cookie to be present.
func readSlackCookies(ctx context.Context) (cookieHeader, workspaceURL string, err error) {
	var cookies []*network.Cookie
	if runErr := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		cookies, e = network.GetCookies().Do(ctx)
		return e
	})); runErr != nil {
		return "", "", fmt.Errorf("slack login: read cookies: %w", runErr)
	}

	var parts []string
	haveD := false
	for _, c := range cookies {
		if !strings.Contains(c.Domain, "slack.com") {
			continue
		}
		if c.Name == "d" && strings.HasPrefix(c.Value, "xoxd-") {
			haveD = true
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	if !haveD {
		return "", "", fmt.Errorf("slack login: 'd' session cookie not found")
	}

	// Best-effort workspace origin from the current page URL.
	var href string
	_ = chromedp.Run(ctx, chromedp.Location(&href))
	workspaceURL = originOf(href)

	return strings.Join(parts, "; "), workspaceURL, nil
}
