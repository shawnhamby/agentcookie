package livecdp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// DefaultPollInterval is how often the Syncer scans for new browser
// contexts to inject. A connector like browser-use creates its own
// context on connect; the poll catches it. Kept short so a fresh context
// gets cookies before the agent's second navigation. Injection only fires
// for not-yet-seen contexts, so the steady-state cost is one cheap
// Target.getTargets call per tick.
const DefaultPollInterval = 600 * time.Millisecond

// DefaultPollTimeout bounds one new-context poll tick. Every CDP call must
// carry a deadline: chromedp's browser executor parks forever on its outgoing
// command queue once the websocket reader is gone, and an unbounded poll then
// leaks the whole tick's cookie set.
const DefaultPollTimeout = 60 * time.Second

// CookieProvider returns the current decrypted, filtered cookie set to
// inject. It is called fresh each sync so the loop always injects current
// values (the source pipeline owns reading/decrypt/blocklist/DBSC).
type CookieProvider func() ([]chrome.Cookie, error)

// ErrDisconnected is returned by Syncer calls made while the browser-level
// CDP connection is being rebuilt. Callers must fail fast on it instead of
// queueing work that a dead connection can never drain.
var ErrDisconnected = errors.New("livecdp: browser connection unavailable")

// Syncer keeps a live browser's contexts injected with the user's cookies.
// It solves the isolated-context problem: a connector (browser-use,
// agent-browser) opens its own browser context, so a one-time browser-level
// cookie write never reaches the agent's pages. The Syncer injects into
// every context as it appears (poll) and re-injects all contexts on demand
// (ReinjectAll, used by the source-change watch loop).
//
// Injection uses Storage.setCookies addressed by browserContextId via the
// BROWSER-LEVEL executor (browser) -- never a page-target-bound chromedp
// context. This is deliberate: a CDP connector attaching
// (agent-browser/browser-use) closes the pages a page-context is bound to,
// which cancels that context; a loop driven off it would then fail every
// Target.getTargets with "context canceled" forever and never inject the
// connector's own context. The browser-level connection is owned by the
// allocator and survives page-target churn, so injection keeps working
// through connect/disconnect cycles.
//
// Every entry point takes a caller context that must carry a deadline. The
// executor itself is swappable so the owner can rebuild a dead browser
// websocket without recreating the Syncer (and losing the seen-context set).
type Syncer struct {
	provider  CookieProvider
	pollEvery time.Duration
	log       func(format string, args ...any)

	agentSyncInject *AgentSyncInjectOpts

	mu      sync.Mutex
	browser cdp.Executor
	seen    map[cdp.BrowserContextID]bool
}

// NewSyncer builds a Syncer that injects via the browser-level executor
// (which survives page-target churn from a CDP connector). log may be nil.
func NewSyncer(browser cdp.Executor, provider CookieProvider, log func(string, ...any)) *Syncer {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Syncer{
		browser:   browser,
		provider:  provider,
		pollEvery: DefaultPollInterval,
		log:       log,
		seen:      map[cdp.BrowserContextID]bool{},
	}
}

// SetBrowser swaps the browser-level executor, or marks the Syncer
// disconnected when browser is nil. The owner calls it around a reconnect so
// in-flight callers fail fast with ErrDisconnected instead of handing work to
// a websocket that no longer has a reader.
func (s *Syncer) SetBrowser(browser cdp.Executor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.browser = browser
}

// executor returns the current browser executor. The lock is never held
// across a CDP call, so a reconnect can always make progress.
func (s *Syncer) executor() (cdp.Executor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.browser == nil {
		return nil, ErrDisconnected
	}
	return s.browser, nil
}

// EnableAgentSyncInject turns on clearance exclusion and downgrade protection
// for agent-sync's ReinjectAll and new-context poll paths.
func (s *Syncer) EnableAgentSyncInject() {
	s.agentSyncInject = &AgentSyncInjectOpts{
		ExcludeClearance: true,
		SkipDowngrade:    true,
	}
}

// Run injects into existing contexts immediately, then polls for new
// contexts until ctx is cancelled. Returns ctx.Err() on shutdown.
func (s *Syncer) Run(ctx context.Context) error {
	// Each tick gets its own bounded child context: a tick that inherited the
	// daemon lifetime could park on a dead CDP connection for days.
	tick := func() {
		tickCtx, cancel := context.WithTimeout(ctx, DefaultPollTimeout)
		defer cancel()
		if n, err := s.syncNewContexts(tickCtx); err != nil {
			s.log("livecdp: poll sync: %v", err)
		} else if n > 0 {
			s.log("livecdp: injected %d new context(s)", n)
		}
	}
	tick()
	t := time.NewTicker(s.pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			tick()
		}
	}
}

// ReinjectAll re-injects current cookies into ALL live contexts, including
// ones already seen. The source-change watch loop (U4) calls this so a
// cookie the user just refreshed propagates into running agent contexts.
// ctx bounds every CDP call it makes; ReinjectAll returns as soon as ctx
// expires rather than holding its cookie set on a stalled connection.
func (s *Syncer) ReinjectAll(ctx context.Context) (int, error) {
	if _, err := s.executor(); err != nil {
		return 0, err
	}
	cookies, err := s.provider()
	if err != nil {
		return 0, fmt.Errorf("livecdp: provider: %w", err)
	}
	return s.injectAllFiltered(ctx, cookies)
}

// syncNewContexts injects only into browser contexts not yet seen.
func (s *Syncer) syncNewContexts(ctx context.Context) (int, error) {
	browser, err := s.executor()
	if err != nil {
		return 0, err
	}
	ids, explicit, err := injectableContexts(ctx, browser)
	if err != nil {
		return 0, err
	}
	var cookies []chrome.Cookie
	loaded := false
	cache := newSinkCookieCache()
	var clearanceTotal, downgradeTotal int
	n := 0
	for _, id := range ids {
		s.mu.Lock()
		already := s.seen[id]
		s.mu.Unlock()
		if already {
			continue
		}
		if !loaded {
			cookies, err = s.provider()
			if err != nil {
				return n, fmt.Errorf("livecdp: provider: %w", err)
			}
			loaded = true
		}
		toInject, clearanceSkipped, downgradeSkipped, err := filterCookiesForContext(
			ctx, browser, id, explicit[id], cookies, s.agentSyncInject, cache,
		)
		if err != nil {
			s.log("livecdp: filter context %q: %v", id, err)
			continue
		}
		clearanceTotal += clearanceSkipped
		downgradeTotal += downgradeSkipped
		if err := injectIntoContext(ctx, browser, id, explicit[id], toInject); err != nil {
			s.log("livecdp: inject context %q: %v", id, err)
			continue
		}
		s.mu.Lock()
		s.seen[id] = true
		s.mu.Unlock()
		n++
	}
	if clearanceTotal > 0 || downgradeTotal > 0 {
		s.log("livecdp: inject skipped clearance=%d downgrade=%d", clearanceTotal, downgradeTotal)
	}
	return n, nil
}

// InjectAllContexts injects cookies into every injectable browser context in
// the connected browser, once per unique BrowserContextID. It accepts a
// chromedp browser context and drives the injection through that context's
// browser-level executor; used by one-shot callers and the live tests. The
// long-running Syncer uses the browser executor + a stable context directly
// (see injectAll) so it survives page-target churn.
func InjectAllContexts(browserCtx context.Context, cookies []chrome.Cookie) (int, error) {
	return injectAll(browserCtx, chromedp.FromContext(browserCtx).Browser, cookies)
}

// injectAll injects cookies into every injectable browser context reachable
// via the browser executor, once per unique BrowserContextID. Because
// Storage.setCookies is addressed by browserContextId, this reaches contexts
// a connector created for itself -- the fix for the isolated-context failure
// where a browser-level write never reached the agent's pages.
func injectAll(ctx context.Context, browser cdp.Executor, cookies []chrome.Cookie) (int, error) {
	ids, explicit, err := injectableContexts(ctx, browser)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, id := range ids {
		if err := injectIntoContext(ctx, browser, id, explicit[id], cookies); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

func (s *Syncer) injectAllFiltered(ctx context.Context, cookies []chrome.Cookie) (int, error) {
	browser, err := s.executor()
	if err != nil {
		return 0, err
	}
	ids, explicit, err := injectableContexts(ctx, browser)
	if err != nil {
		return 0, err
	}
	cache := newSinkCookieCache()
	var clearanceTotal, downgradeTotal int
	n := 0
	var firstErr error
	for _, id := range ids {
		toInject, clearanceSkipped, downgradeSkipped, err := filterCookiesForContext(
			ctx, browser, id, explicit[id], cookies, s.agentSyncInject, cache,
		)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.log("livecdp: filter context %q: %v", id, err)
			continue
		}
		clearanceTotal += clearanceSkipped
		downgradeTotal += downgradeSkipped
		if err := injectIntoContext(ctx, browser, id, explicit[id], toInject); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	if clearanceTotal > 0 || downgradeTotal > 0 {
		s.log("livecdp: inject skipped clearance=%d downgrade=%d", clearanceTotal, downgradeTotal)
	}
	return n, firstErr
}

// injectableContexts returns the unique BrowserContextIDs that own at least
// one injectable page target, plus the set of those that are EXPLICIT
// (created via Target.createBrowserContext -- e.g. browser-use's own
// context). Explicit contexts must be addressed by browserContextId in
// Storage.setCookies; the default context is NOT addressable by id (Chrome
// rejects its id with -32602) and must be set with the param omitted.
func injectableContexts(ctx context.Context, browser cdp.Executor) ([]cdp.BrowserContextID, map[cdp.BrowserContextID]bool, error) {
	infos, err := target.GetTargets().Do(cdp.WithExecutor(ctx, browser))
	if err != nil {
		return nil, nil, fmt.Errorf("livecdp: list targets: %w", err)
	}
	explicit, err := explicitContextSet(ctx, browser)
	if err != nil {
		// Degrade gracefully: treat all as default (omit the id). Worst
		// case an explicit context misses; logged by the caller.
		explicit = map[cdp.BrowserContextID]bool{}
	}
	seen := map[cdp.BrowserContextID]bool{}
	var ids []cdp.BrowserContextID
	for _, info := range infos {
		if !shouldInjectTarget(info) || seen[info.BrowserContextID] {
			continue
		}
		seen[info.BrowserContextID] = true
		ids = append(ids, info.BrowserContextID)
	}
	return ids, explicit, nil
}

// explicitContextSet returns the browser contexts created via
// Target.createBrowserContext (the default context is not included).
func explicitContextSet(ctx context.Context, browser cdp.Executor) (map[cdp.BrowserContextID]bool, error) {
	ids, _, err := target.GetBrowserContexts().Do(cdp.WithExecutor(ctx, browser))
	if err != nil {
		return nil, err
	}
	set := make(map[cdp.BrowserContextID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// injectIntoContext writes cookies into one browser context's cookie store
// via Storage.setCookies on the browser executor. useID controls whether the
// browserContextId param is sent: true for an explicit (createBrowserContext)
// context, false for the default context (which Chrome rejects when addressed
// by id). This never attaches to a page target, so it cannot close or disturb
// a tab the agent is driving.
func injectIntoContext(ctx context.Context, browser cdp.Executor, ctxID cdp.BrowserContextID, useID bool, cookies []chrome.Cookie) error {
	params := BuildCookieParams(cookies)
	if len(params) == 0 {
		return nil
	}
	sc := storage.SetCookies(params)
	if useID {
		sc = sc.WithBrowserContextID(ctxID)
	}
	if err := sc.Do(cdp.WithExecutor(ctx, browser)); err != nil {
		return fmt.Errorf("Storage.setCookies (%d cookies, ctx=%q useID=%v): %w", len(params), ctxID, useID, err)
	}
	return nil
}

// shouldInjectTarget reports whether a target should receive cookies: real
// page targets only, excluding Chrome-internal and extension surfaces and
// prerender subframes. about:blank pages qualify -- they belong to a real
// context whose cookie store the agent's pages will read.
func shouldInjectTarget(info *target.Info) bool {
	if info == nil || info.Type != "page" || info.Subtype == "prerender" {
		return false
	}
	for _, p := range []string{"chrome://", "devtools://", "chrome-extension://", "chrome-untrusted://"} {
		if strings.HasPrefix(info.URL, p) {
			return false
		}
	}
	return true
}
