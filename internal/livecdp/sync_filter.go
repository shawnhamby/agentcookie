package livecdp

import (
	"context"
	"fmt"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// AgentSyncInjectOpts enables clearance exclusion and downgrade protection on
// the agent-sync injection path. One-shot inject (export, sink, tests) leaves
// this nil so all cookies pass through unchanged.
type AgentSyncInjectOpts struct {
	ExcludeClearance bool
	SkipDowngrade    bool
}

type sinkCookie struct {
	Name       string
	Domain     string
	Path       string
	ExpiresUTC int64
}

type sinkCookieCache struct {
	byContext map[cdp.BrowserContextID][]sinkCookie
}

func newSinkCookieCache() *sinkCookieCache {
	return &sinkCookieCache{byContext: map[cdp.BrowserContextID][]sinkCookie{}}
}

func (c *sinkCookieCache) get(ctx context.Context, browser cdp.Executor, ctxID cdp.BrowserContextID, useID bool) ([]sinkCookie, error) {
	if cached, ok := c.byContext[ctxID]; ok {
		return cached, nil
	}
	sink, err := readSinkCookies(ctx, browser, ctxID, useID)
	if err != nil {
		return nil, err
	}
	c.byContext[ctxID] = sink
	return sink, nil
}

func readSinkCookies(ctx context.Context, browser cdp.Executor, ctxID cdp.BrowserContextID, useID bool) ([]sinkCookie, error) {
	gc := storage.GetCookies()
	if useID {
		gc = gc.WithBrowserContextID(ctxID)
	}
	raw, err := gc.Do(cdp.WithExecutor(ctx, browser))
	if err != nil {
		return nil, fmt.Errorf("Storage.getCookies (ctx=%q useID=%v): %w", ctxID, useID, err)
	}
	out := make([]sinkCookie, 0, len(raw))
	for _, c := range raw {
		out = append(out, storageCookieToSink(c))
	}
	return out, nil
}

func storageCookieToSink(c *network.Cookie) sinkCookie {
	expires := int64(0)
	if !c.Session && c.Expires > 0 {
		expires = unixToExpiresUTC(c.Expires)
	}
	return sinkCookie{
		Name:       c.Name,
		Domain:     c.Domain,
		Path:       c.Path,
		ExpiresUTC: expires,
	}
}

func unixToExpiresUTC(unixSec float64) int64 {
	const chromeEpochOffsetSec = 11644473600
	return int64((unixSec + chromeEpochOffsetSec) * 1e6)
}

func cookieIdentityKey(name, hostKey, path string) string {
	return strings.ToLower(name) + "\x00" + normalizeDomain(hostKey) + "\x00" + path
}

func normalizeDomain(d string) string {
	return strings.ToLower(strings.TrimPrefix(d, "."))
}

func indexSinkCookies(sink []sinkCookie) map[string]sinkCookie {
	idx := make(map[string]sinkCookie, len(sink))
	for _, c := range sink {
		idx[cookieIdentityKey(c.Name, c.Domain, c.Path)] = c
	}
	return idx
}

// sinkHasLaterExpiry reports whether the sink cookie outlives the source row.
// Session source cookies always inject regardless of sink state.
func sinkHasLaterExpiry(source chrome.Cookie, sink sinkCookie) bool {
	if source.ExpiresUTC == 0 {
		return false
	}
	if sink.ExpiresUTC == 0 {
		return false
	}
	return sink.ExpiresUTC > source.ExpiresUTC
}

// filterClearanceCookies removes bot-clearance names from the inject set.
func filterClearanceCookies(source []chrome.Cookie) (filtered []chrome.Cookie, skipped int) {
	for _, c := range source {
		if IsClearanceCookie(c.Name) {
			skipped++
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered, skipped
}

// filterDowngradeCookies skips source rows the sink already holds with a later
// expiry for the same (name, domain, path).
func filterDowngradeCookies(source []chrome.Cookie, sink []sinkCookie) (filtered []chrome.Cookie, skipped int) {
	idx := indexSinkCookies(sink)
	for _, c := range source {
		if existing, ok := idx[cookieIdentityKey(c.Name, c.HostKey, c.Path)]; ok && sinkHasLaterExpiry(c, existing) {
			skipped++
			continue
		}
		filtered = append(filtered, c)
	}
	return filtered, skipped
}

func filterCookiesForContext(
	ctx context.Context,
	browser cdp.Executor,
	ctxID cdp.BrowserContextID,
	useID bool,
	source []chrome.Cookie,
	opts *AgentSyncInjectOpts,
	cache *sinkCookieCache,
) ([]chrome.Cookie, int, int, error) {
	if opts == nil {
		return source, 0, 0, nil
	}

	cookies := source
	clearanceSkipped := 0
	if opts.ExcludeClearance {
		cookies, clearanceSkipped = filterClearanceCookies(cookies)
	}

	downgradeSkipped := 0
	if opts.SkipDowngrade && len(cookies) > 0 {
		sink, err := cache.get(ctx, browser, ctxID, useID)
		if err != nil {
			return nil, clearanceSkipped, 0, err
		}
		cookies, downgradeSkipped = filterDowngradeCookies(cookies, sink)
	}

	return cookies, clearanceSkipped, downgradeSkipped, nil
}
