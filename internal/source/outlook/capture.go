package outlook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nachmore/commstats/internal/platform"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const (
	outlookURL = "https://outlook.office.com/mail/"
	// tokenAudience is the audience whose bearer token grants mail/calendar
	// access (confirmed by the discovery probe).
	tokenAudience = "https://outlook.office.com"
	apiBase       = "https://outlook.office.com/api/v2.0"
)

// profileDir is the persistent browser profile so login survives between runs —
// after the first interactive sign-in, later captures are silent.
func profileDir() string {
	return filepath.Join(platform.Current().Paths().ConfigDir(), "outlook-profile")
}

// profileExists reports whether an Outlook browser profile has been created,
// used to decide whether a non-interactive run has any chance of a silent
// capture (vs. needing a first interactive sign-in).
func profileExists() bool {
	_, err := os.Stat(profileDir())
	return err == nil
}

// Process-lifetime token cache. Capturing a token launches a whole browser, so
// across a multi-day backfill (Collect called per day) we capture once and
// reuse — Outlook access tokens live ~1h, far longer than any single run.
var (
	tokenMu     sync.Mutex
	cachedTok   string
	cachedAt    time.Time
	tokenMaxAge = 45 * time.Minute
)

// identity caches the signed-in user's address + home org for the process, so
// a multi-day backfill makes at most one /me call.
var (
	identMu   sync.Mutex
	identAddr string
	identOrg  string
)

// getIdentity returns the signed-in user's email and home org label, fetching
// once and caching.
func getIdentity(ctx context.Context, c *client) (addr, org string, err error) {
	identMu.Lock()
	if identAddr != "" {
		a, o := identAddr, identOrg
		identMu.Unlock()
		return a, o, nil
	}
	identMu.Unlock()

	a, err := c.myAddress(ctx)
	if err != nil {
		return "", "", err
	}
	o := orgOf(a)
	identMu.Lock()
	identAddr, identOrg = a, o
	identMu.Unlock()
	return a, o, nil
}

// getToken returns a cached token if still fresh, otherwise captures a new one.
func getToken(ctx context.Context, headless bool) (string, error) {
	tokenMu.Lock()
	if cachedTok != "" && time.Since(cachedAt) < tokenMaxAge {
		t := cachedTok
		tokenMu.Unlock()
		return t, nil
	}
	tokenMu.Unlock()

	t, err := captureToken(ctx, headless)
	if err != nil {
		return "", err
	}
	tokenMu.Lock()
	cachedTok, cachedAt = t, time.Now()
	tokenMu.Unlock()
	return t, nil
}

// captureToken launches the (persistent-profile) browser, loads Outlook web,
// and returns the first bearer token it sees whose audience is
// outlook.office.com. With a warm profile this is silent; on a cold profile the
// user must sign in within the timeout. headless controls whether the window is
// shown — false on first login so the user can authenticate.
func captureToken(ctx context.Context, headless bool) (string, error) {
	browserPath, err := platform.Current().Browser().ExecutablePath()
	if err != nil {
		return "", fmt.Errorf("outlook: %w", err)
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

	var (
		mu    sync.Mutex
		token string
		done  = make(chan struct{})
	)

	chromedp.ListenTarget(taskCtx, func(ev interface{}) {
		e, ok := ev.(*network.EventRequestWillBeSentExtraInfo)
		if !ok {
			return
		}
		var auth string
		for k, v := range e.Headers {
			if strings.EqualFold(k, "authorization") {
				if s, ok := v.(string); ok {
					auth = s
				}
			}
		}
		if !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			return
		}
		tok := strings.TrimSpace(auth[len("bearer "):])
		if aud, _ := decodeJWTClaims(tok); aud != tokenAudience {
			return
		}
		mu.Lock()
		if token == "" {
			token = tok
			close(done)
		}
		mu.Unlock()
	})

	if err := chromedp.Run(taskCtx,
		network.Enable(),
		chromedp.Navigate(outlookURL),
	); err != nil {
		return "", fmt.Errorf("outlook: open browser: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-done:
		mu.Lock()
		t := token
		mu.Unlock()
		return t, nil
	case <-time.After(2 * time.Minute):
		return "", fmt.Errorf("outlook: timed out waiting for an %s token (sign-in needed?)", tokenAudience)
	}
}
