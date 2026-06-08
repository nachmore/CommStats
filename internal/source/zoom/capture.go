package zoom

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nachmore/commstats/internal/platform"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

const signinProbeURL = "https://zoom.us/account/my/report"

func profileDir() string {
	return filepath.Join(platform.Current().Paths().ConfigDir(), "zoom-profile")
}

func profileExists() bool {
	_, err := os.Stat(profileDir())
	return err == nil
}

// Process-lifetime session cache: capturing cookies launches a browser, so a
// multi-day backfill captures once and reuses. Zoom session cookies last well
// beyond a single run.
var (
	sessMu   sync.Mutex
	sessJar  http.CookieJar
	sessHost string
	sessAt   time.Time
	sessTTL  = 30 * time.Minute
)

func getSession(ctx context.Context, headless bool) (http.CookieJar, string, error) {
	sessMu.Lock()
	if sessJar != nil && time.Since(sessAt) < sessTTL {
		j, h := sessJar, sessHost
		sessMu.Unlock()
		return j, h, nil
	}
	sessMu.Unlock()

	jar, host, err := captureSession(ctx, headless)
	if err != nil {
		return nil, "", err
	}
	sessMu.Lock()
	sessJar, sessHost, sessAt = jar, host, time.Now()
	sessMu.Unlock()
	return jar, host, nil
}

// captureSession drives the (persistent-profile) browser to the Zoom report
// page, waits for sign-in, then extracts the session cookies into a Go cookie
// jar and returns the company Zoom host detected from the final URL. With a warm
// profile this is silent; cold profiles need an interactive sign-in (headless
// must be false).
func captureSession(ctx context.Context, headless bool) (http.CookieJar, string, error) {
	browserPath, err := platform.Current().Browser().ExecutablePath()
	if err != nil {
		return nil, "", fmt.Errorf("zoom: %w", err)
	}
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("headless", headless),
		chromedp.UserDataDir(profileDir()),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	if err := chromedp.Run(taskCtx,
		network.Enable(),
		chromedp.Navigate(signinProbeURL),
	); err != nil {
		return nil, "", fmt.Errorf("zoom: open browser: %w", err)
	}

	// Wait (up to 4 min) for sign-in to settle on a real Zoom page.
	deadline := time.Now().Add(4 * time.Minute)
	var finalURL string
	for time.Now().Before(deadline) {
		var loc string
		_ = chromedp.Run(taskCtx, chromedp.Location(&loc))
		l := strings.ToLower(loc)
		if loc != "" && strings.Contains(l, "zoom.us") && !strings.Contains(l, "signin") &&
			!strings.Contains(l, "/login") && !strings.Contains(l, "oauth") && !strings.Contains(l, "saml") {
			finalURL = loc
			break
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if finalURL == "" {
		return nil, "", fmt.Errorf("zoom: sign-in did not complete in time")
	}

	host := "zoom.us"
	if u, err := url.Parse(finalURL); err == nil && u.Host != "" {
		host = u.Host
	}

	var cookies []*network.Cookie
	if err := chromedp.Run(taskCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		cs, err := storage.GetCookies().Do(ctx)
		cookies = cs
		return err
	})); err != nil {
		return nil, "", fmt.Errorf("zoom: read cookies: %w", err)
	}

	jar, _ := cookiejar.New(nil)
	loadJar(jar, cookies)
	return jar, host, nil
}

// loadJar copies the browser's zoom.us cookies into a Go cookie jar, scoped to
// the zoom.us registrable domain so they're sent to both zoom.us and any
// company subdomain.
func loadJar(jar http.CookieJar, cookies []*network.Cookie) {
	base, _ := url.Parse("https://zoom.us")
	var hcs []*http.Cookie
	for _, c := range cookies {
		if !strings.Contains(strings.ToLower(c.Domain), "zoom.us") {
			continue
		}
		hcs = append(hcs, &http.Cookie{Name: c.Name, Value: c.Value, Path: "/", Domain: "zoom.us"})
	}
	jar.SetCookies(base, hcs)
}
