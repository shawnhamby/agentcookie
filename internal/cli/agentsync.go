package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/watcher"
)

// defaultAgentSyncPort is the flag default and the port that keeps the legacy
// owned-profile dir name (agent-chrome) for existing consumers.
const defaultAgentSyncPort = 9400

const (
	agentSyncCapabilitiesSchemaVersion = 1
	canonicalSignIdentityEnv           = "AGENTCOOKIE_SIGN_IDENTITY"
	externalWrapperSignIdentityEnv     = "DEFAULT_SIGN_IDENTITY"
)

var (
	agentSyncPort              int
	agentSyncHeaded            bool
	agentSyncChromePath        string
	agentSyncUserDataDir       string
	agentSyncSkipDBSC          bool
	agentSyncDomains           []string
	agentSyncBrowser           string
	agentSyncProfile           string
	agentSyncVerbose           bool
	agentSyncUserAgent         string
	agentSyncWindowSize        string
	agentSyncScreenSize        string
	agentSyncScreenColorDepth  int
	agentSyncScreenWorkArea    string
	agentSyncDeviceScaleFactor float64
	agentSyncColorProfile      string
	agentSyncExtraScreens      []string
	agentSyncRequirePolicy     string
	agentSyncCapabilitiesJSON  bool
	agentSyncProxyServer       string
)

var agentSyncCmd = &cobra.Command{
	Use:   "agent-sync",
	Short: "Run an owned Chrome that Chromium agent browsers connect to, kept logged in from your real Chrome",
	Long: `agent-sync is the Chromium counterpart to cmux-sync. It launches a
dedicated Chrome on a loopback debug port, reads this Mac's Chrome cookies
(decrypt + cookie policy + DBSC filter, the same pipeline source uses), and
injects them -- as plaintext, over CDP -- into every browser context that
Chrome opens, including the context a connector like browser-use creates for
itself. browser-use / agent-browser connect to it via --cdp-url and wake up
logged into your sites.

This is live injection, not a cold profile or a storage_state file: cookies
go straight into the running browser's in-memory store, so Chrome 127+
App-Bound Encryption (which makes cold-profile cookies undecryptable on load)
never applies. The owned Chrome uses its own user-data-dir, so the debug port
is honored (Chrome 136+ only blocks it on the default profile) and your
everyday Chrome is never touched.

  agentcookie agent-sync                      launch + sync, hold until Ctrl-C
  agentcookie agent-sync --headed             show the owned browser window
  agentcookie agent-sync --domain %github.com limit to matching hosts

Device-bound (DBSC) cookies -- Google/Workspace account cookies -- cannot
transfer to another browser and are reported, not faked. Non-DBSC sites
(GitHub-class, the large majority) work.`,
	RunE: runAgentSync,
}

func init() {
	agentSyncCmd.Flags().IntVar(&agentSyncPort, "port", defaultAgentSyncPort, "loopback Chrome remote-debugging port for the owned browser")
	agentSyncCmd.Flags().BoolVar(&agentSyncHeaded, "headed", false, "show the owned browser window (default: headless)")
	agentSyncCmd.Flags().StringVar(&agentSyncChromePath, "chrome-path", "", "override the Chrome executable (default: auto-detect)")
	agentSyncCmd.Flags().StringVar(&agentSyncUserDataDir, "user-data-dir", "", "owned-browser profile dir (default: ~/.agentcookie/agent-chrome, or agent-chrome-<port> for non-default ports)")
	agentSyncCmd.Flags().BoolVar(&agentSyncSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC); also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	agentSyncCmd.Flags().StringSliceVar(&agentSyncDomains, "domain", nil, "limit to these host_key LIKE patterns (repeatable), e.g. --domain %github.com")
	agentSyncCmd.Flags().StringVar(&agentSyncBrowser, "browser", "", "pin to one source browser store (default: merge all enabled_products like export)")
	agentSyncCmd.Flags().StringVar(&agentSyncProfile, "profile", "", "pin to one source profile dir (requires single-store pin with --browser or alone)")
	agentSyncCmd.Flags().BoolVar(&agentSyncVerbose, "verbose", false, "log per-cycle counts to stderr")
	agentSyncCmd.Flags().StringVar(&agentSyncUserAgent, "user-agent", "", "override the owned browser User-Agent (pass a real Chrome UA to avoid a HeadlessChrome token; default: Chrome's own)")
	agentSyncCmd.Flags().StringVar(&agentSyncWindowSize, "window-size", "", "browser window size (viewport); does not affect the reported screen when --screen-size is set")
	agentSyncCmd.Flags().StringVar(&agentSyncScreenSize, "screen-size", "", "emulated screen for headless; the real display's logical size")
	agentSyncCmd.Flags().IntVar(&agentSyncScreenColorDepth, "screen-color-depth", 0, "emulated screen color depth for headless (e.g. 30 for wide-gamut; omitted when 0)")
	agentSyncCmd.Flags().StringVar(&agentSyncScreenWorkArea, "screen-work-area", "", "logical screen work-area insets top,bottom,left,right (e.g. 30,88,0,0 for menu bar and dock)")
	agentSyncCmd.Flags().Float64Var(&agentSyncDeviceScaleFactor, "device-scale-factor", 0, "owned browser device pixel ratio (e.g. 1.6 for this machine's Retina display; default: 1, unset when 0)")
	agentSyncCmd.Flags().StringVar(&agentSyncColorProfile, "force-color-profile", "", "Chrome color profile name, e.g. hdr10 or display-p3-d65, to match the real display's gamut/HDR answers")
	agentSyncCmd.Flags().StringArrayVar(&agentSyncExtraScreens, "extra-screen", nil, "additional logical screen size W,H (repeatable; scaled like the primary)")
	agentSyncCmd.Flags().StringVar(&agentSyncRequirePolicy, "require-policy", "", `refuse to start or sync unless this cookie policy is active (supported: "allowlist")`)
	agentSyncCmd.Flags().StringVar(&agentSyncProxyServer, "proxy-server", "", "HTTP/HTTPS/SOCKS proxy URL for the owned Chrome (explicit flag only; credentials are never logged)")
	agentSyncCmd.Flags().BoolVar(&agentSyncCapabilitiesJSON, "capabilities-json", false, "print the agent-sync capability contract as JSON and exit")
}

func runAgentSync(cmd *cobra.Command, args []string) error {
	// LoadSourceLocal: the agent-sync loop has no push target, so it must not
	// require sink.url or a peer/secret. Missing source.yaml is fine (default
	// Chrome path, no blocklist).
	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return err
	}
	blocklist, err := loadFreshBlocklist()
	if err != nil {
		return err
	}
	if agentSyncCapabilitiesJSON {
		return writeAgentSyncCapabilities(cmd.OutOrStdout(), cmd, cfg, blocklist)
	}
	requiredPolicy := agentSyncRequirePolicy
	if err := enforceAgentSyncPolicy(blocklist, requiredPolicy); err != nil {
		return err
	}

	browserName := agentSyncBrowser
	if browserName == "" {
		browserName = cfg.Browser.Name
	}

	var key []byte
	var watchPaths []string
	pinned := agentSyncSourcePinned(agentSyncBrowser, agentSyncProfile)
	if pinned {
		sourceBrowser, err := chrome.LookupBrowser(browserName)
		if err != nil {
			return err
		}
		password, err := chrome.SafeStoragePasswordFor(sourceBrowser)
		if err != nil {
			return err
		}
		key, err = chrome.DeriveAESKey(password)
		if err != nil {
			return err
		}
		dbPath, err := resolveSourceDBPath(cfg, agentSyncBrowser, agentSyncProfile, browserName)
		if err != nil {
			return err
		}
		watchPaths = []string{dbPath}
	} else {
		enabled, err := config.ResolveEnabledProducts(cfg)
		if err != nil {
			return err
		}
		watchPaths, err = discoverAgentSyncWatchPaths(enabled)
		if err != nil {
			return err
		}
	}

	skipDBSC := agentSyncSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"
	domainFilter := agentSyncDomains

	// Cookie provider: read+decrypt+filter fresh each call so the loop always
	// injects current values.
	provider := newAgentSyncCookieProvider(cfg, key, skipDBSC, domainFilter, requiredPolicy, agentSyncBrowser, agentSyncProfile)

	userDataDir := agentSyncUserDataDir
	if userDataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		// Per-port default so concurrent instances (e.g. 9400 research +
		// 9401 instruction-sync) never collide on one owned-profile dir.
		// The default port keeps the legacy dir name for compatibility.
		dirName := "agent-chrome"
		if agentSyncPort != defaultAgentSyncPort {
			dirName = fmt.Sprintf("agent-chrome-%d", agentSyncPort)
		}
		userDataDir = filepath.Join(home, ".agentcookie", dirName)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	oc, err := livecdp.LaunchOwnedChromeWithOptions(ctx, livecdp.LaunchOptions{
		ChromePath:        agentSyncChromePath,
		UserDataDir:       userDataDir,
		Port:              agentSyncPort,
		Headless:          !agentSyncHeaded,
		UserAgent:         agentSyncUserAgent,
		WindowSize:        agentSyncWindowSize,
		ScreenSize:        agentSyncScreenSize,
		ScreenColorDepth:  agentSyncScreenColorDepth,
		ScreenWorkArea:    agentSyncScreenWorkArea,
		DeviceScaleFactor: agentSyncDeviceScaleFactor,
		ColorProfile:      agentSyncColorProfile,
		ExtraScreens:      agentSyncExtraScreens,
		ProxyServer:       agentSyncProxyServer,
		LeanProfile:       true,
	})
	if err != nil {
		return err
	}
	defer oc.Close()

	syncLog := func(format string, a ...any) {
		if agentSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie agent-sync: "+format+"\n", a...)
		}
	}
	// Always logged, verbose or not: a lost browser websocket used to leave no
	// trace at all, which is why the original stall had no recorded cause.
	alwaysLog := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "agentcookie agent-sync: "+format+"\n", a...)
	}
	if agentSyncVerbose && agentSyncProxyServer != "" {
		syncLog("proxy-server %s", livecdp.RedactProxyURL(agentSyncProxyServer))
	}

	conn, err := connectAgentSyncBrowser(ctx, oc.Endpoint, alwaysLog, syncLog)
	if err != nil {
		return fmt.Errorf("agent-sync: establish browser connection: %w", err)
	}

	syncer := livecdp.NewSyncer(conn.browser, provider, syncLog)
	syncer.EnableAgentSyncInject()

	// Initial inject so the owned browser's default context is logged in
	// immediately; also surfaces connection/cookie errors at startup.
	initialCtx, initialCancel := context.WithTimeout(ctx, agentSyncPushTimeout)
	n, err := syncer.ReinjectAll(initialCtx)
	initialCancel()
	if err != nil {
		return fmt.Errorf("agent-sync: initial inject: %w", err)
	}

	fmt.Fprintf(os.Stderr, "agentcookie agent-sync: owned Chrome on %s (profile %s); injected %d context(s)\n", oc.Endpoint, userDataDir, n)
	fmt.Fprintln(os.Stderr, "Connect an agent browser:")
	fmt.Fprintf(os.Stderr, "  browser-use --cdp-url %s open https://github.com\n", oc.Endpoint)
	fmt.Fprintf(os.Stderr, "  agent-browser --cdp %d\n", oc.Port)
	fmt.Fprintln(os.Stderr, "Watching Chrome cookies + new contexts. Ctrl-C to stop.")

	// runCtx ends the sync loops without ending ctx's signal handling, so a
	// fatal connection failure can unwind through the normal deferred cleanup.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	sup := &agentSyncSupervisor{
		endpoint:   oc.Endpoint,
		chrome:     oc,
		syncer:     syncer,
		conn:       conn,
		logf:       alwaysLog,
		browserLog: syncLog,
		reconnect:  make(chan struct{}, 1),
		fatal:      make(chan error, 1),
		cancel:     runCancel,
	}
	// The deferred close must reach whichever connection is current when the
	// daemon stops, not the one captured at startup.
	defer sup.closeConn()
	sup.watchLostConnection(runCtx)
	go sup.run(runCtx)

	// Poll for new contexts (e.g. the one browser-use creates on connect) and
	// inject them. Runs concurrently with the cookie-change watcher below.
	go func() {
		if runErr := syncer.Run(runCtx); runErr != nil && runErr != context.Canceled {
			syncLog("context poll: %v", runErr)
		}
	}()

	// Watch the source cookie DB(s); on each debounced change, re-inject
	// current cookies into every live context so a site the user just logged
	// into in their real Chrome becomes logged-in in the agent browser too.
	// A failed cycle is logged and the watcher keeps running.
	push := func(pushCtx context.Context) (int, error) {
		n, err := syncer.ReinjectAll(pushCtx)
		if err != nil && errors.Is(err, context.DeadlineExceeded) {
			// A CDP call that never answered means the browser websocket is
			// unusable even though Chrome may still be serving the port.
			sup.requestReconnect("push deadline exceeded")
		}
		return n, err
	}
	onEvent := func(ev watcher.Event) {
		if agentSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie agent-sync: %s\n", ev.String())
		}
	}
	err = runAgentSyncWatchers(runCtx, watchPaths, push, onEvent)
	select {
	case fatalErr := <-sup.fatal:
		return fatalErr
	default:
	}
	if err != nil && err != context.Canceled {
		return err
	}
	fmt.Fprintln(os.Stderr, "agentcookie agent-sync: stopped")
	return nil
}

// agentSyncPushTimeout bounds one cookie push, matching the watcher's own
// per-push deadline.
const agentSyncPushTimeout = 60 * time.Second

const (
	agentSyncReconnectMinBackoff = 1 * time.Second
	agentSyncReconnectMaxBackoff = 30 * time.Second
	// agentSyncReconnectMaxFailures is when a dead websocket stops looking
	// like a transient peer close and starts looking like a gone browser.
	agentSyncReconnectMaxFailures = 5
)

// agentSyncConn is one live browser-level CDP connection to the owned Chrome.
type agentSyncConn struct {
	browser *chromedp.Browser
	lost    <-chan struct{}
	close   func()
}

// connectAgentSyncBrowser establishes the browser-level CDP connection and
// returns it with its own teardown. Injection is driven through the browser
// executor, never a page-target-bound chromedp context: a CDP connector
// (agent-browser/browser-use) closes the pages such a context is bound to,
// which cancels it, and the loop would then fail every Target.getTargets with
// "context canceled" forever and never inject the connector's own context.
func connectAgentSyncBrowser(ctx context.Context, endpoint string, errLog, log func(string, ...any)) (*agentSyncConn, error) {
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, endpoint)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithBrowserOption(
		// chromedp's reader errors are the only record of why a connection
		// died; without these the original stall left no cause in the log.
		chromedp.WithBrowserErrorf(func(format string, a ...any) { errLog("chromedp: "+format, a...) }),
		chromedp.WithBrowserLogf(func(format string, a ...any) { log("chromedp: "+format, a...) }),
	))
	if err := chromedp.Run(browserCtx); err != nil {
		browserCancel()
		allocCancel()
		return nil, err
	}
	browser := chromedp.FromContext(browserCtx).Browser
	return &agentSyncConn{
		browser: browser,
		lost:    browser.LostConnection,
		close: func() {
			browserCancel()
			allocCancel()
		},
	}, nil
}

// agentSyncSupervisor rebuilds the browser-level CDP connection when it dies,
// instead of leaving the daemon to push cookies into a socket with no reader.
// It never signals the owned Chrome: the browser holds live agent sessions, so
// losing the websocket must not cost the login state it is protecting.
type agentSyncSupervisor struct {
	endpoint   string
	chrome     *livecdp.OwnedChrome
	syncer     *livecdp.Syncer
	logf       func(string, ...any)
	browserLog func(string, ...any)
	reconnect  chan struct{}
	fatal      chan error
	cancel     context.CancelFunc

	mu   sync.Mutex
	conn *agentSyncConn
}

// requestReconnect coalesces reconnect requests; the queue is one deep because
// one rebuild answers every caller that noticed the same dead connection.
func (s *agentSyncSupervisor) requestReconnect(reason string) {
	select {
	case s.reconnect <- struct{}{}:
		s.logf("reconnect requested: %s", reason)
	default:
	}
}

func (s *agentSyncSupervisor) current() *agentSyncConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

func (s *agentSyncSupervisor) setConn(conn *agentSyncConn) {
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
}

func (s *agentSyncSupervisor) closeConn() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	if conn != nil {
		conn.close()
	}
}

// watchLostConnection reports the current connection's death. chromedp closes
// LostConnection when its websocket reader returns, which is the transition
// the stalled daemons never observed.
func (s *agentSyncSupervisor) watchLostConnection(ctx context.Context) {
	conn := s.current()
	if conn == nil {
		return
	}
	go func() {
		select {
		case <-conn.lost:
			s.requestReconnect("browser websocket closed")
		case <-ctx.Done():
		}
	}()
}

func (s *agentSyncSupervisor) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.reconnect:
		}
		// Fail pushes fast while the connection is down: queueing them is what
		// turned a dead socket into gigabytes of parked goroutines.
		s.syncer.SetBrowser(nil)
		s.closeConn()
		if err := s.reconnectLoop(ctx); err != nil {
			select {
			case s.fatal <- err:
			default:
			}
			s.cancel()
			return
		}
	}
}

// reconnectLoop dials the same endpoint with backoff. It gives up only when
// the owned Chrome is gone or the endpoint refuses repeatedly, and the caller
// then exits non-zero so the launcher relaunches a clean pair on next ensure.
func (s *agentSyncSupervisor) reconnectLoop(ctx context.Context) error {
	backoff := agentSyncReconnectMinBackoff
	for attempt := 1; ; attempt++ {
		if s.chrome.Exited() {
			return fmt.Errorf("agent-sync: owned Chrome exited; cannot reconnect to %s", s.endpoint)
		}
		s.logf("reconnect attempt %d to %s", attempt, s.endpoint)
		conn, err := connectAgentSyncBrowser(ctx, s.endpoint, s.logf, s.browserLog)
		if err == nil {
			s.setConn(conn)
			s.watchLostConnection(ctx)
			s.syncer.SetBrowser(conn.browser)
			s.logf("reconnected to %s after %d attempt(s)", s.endpoint, attempt)
			return nil
		}
		s.logf("reconnect attempt %d failed: %v", attempt, err)
		if attempt >= agentSyncReconnectMaxFailures {
			return fmt.Errorf("agent-sync: reconnect to %s failed %d consecutive times: %w", s.endpoint, attempt, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > agentSyncReconnectMaxBackoff {
			backoff = agentSyncReconnectMaxBackoff
		}
	}
}

func newAgentSyncCookieProvider(cfg *config.SourceConfig, key []byte, skipDBSC bool, domainFilter []string, requiredPolicy, flagBrowser, flagProfile string) livecdp.CookieProvider {
	return func() ([]chrome.Cookie, error) {
		blocklist, err := loadRequiredAgentSyncPolicy(requiredPolicy)
		if err != nil {
			return nil, err
		}
		cookies, st, err := readAgentSyncCookies(cfg, flagBrowser, flagProfile, blocklist, key, skipDBSC, domainFilter, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if agentSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie agent-sync: read %d, filtered %d, dbsc(warn=%d skip=%d), injecting %d\n",
				st.totalRead, st.totalDropped, st.dbsc.warned, st.dbsc.skipped, len(cookies))
		}
		return cookies, nil
	}
}

func runAgentSyncWatchers(ctx context.Context, paths []string, push func(context.Context) (int, error), onEvent func(watcher.Event)) error {
	if len(paths) == 0 {
		return fmt.Errorf("agent-sync: no cookie stores to watch")
	}
	if len(paths) == 1 {
		w, err := watcher.New(watcher.Config{
			CookiesPath: paths[0],
			LogLabel:    "agentcookie agent-sync",
			Push:        push,
			OnEvent:     onEvent,
		})
		if err != nil {
			return fmt.Errorf("init watcher: %w", err)
		}
		return w.Run(ctx)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, len(paths))
	for _, path := range paths {
		wg.Add(1)
		go func(cookiesPath string) {
			defer wg.Done()
			w, err := watcher.New(watcher.Config{
				CookiesPath: cookiesPath,
				LogLabel:    "agentcookie agent-sync",
				Push:        push,
				OnEvent:     onEvent,
			})
			if err != nil {
				errCh <- fmt.Errorf("init watcher for %s: %w", cookiesPath, err)
				cancel()
				return
			}
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				errCh <- err
				cancel()
			}
		}(path)
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

type agentSyncSigningSummary struct {
	CanonicalIdentityEnv   string                         `json:"canonical_identity_env"`
	ExternalWrapperMapping agentSyncSigningWrapperMapping `json:"external_wrapper_mapping"`
}

type agentSyncSigningWrapperMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type agentSyncCapabilities struct {
	SchemaVersion           int                     `json:"schema_version"`
	SupportedFlags          []string                `json:"supported_flags"`
	EffectiveBrowserDefault string                  `json:"effective_browser_default"`
	PolicyMode              string                  `json:"policy_mode"`
	BuildVersion            string                  `json:"build_version"`
	SigningSummary          agentSyncSigningSummary `json:"signing_summary"`
}

func enforceAgentSyncPolicy(blocklist *config.Blocklist, required string) error {
	if required == "" {
		return nil
	}
	if required != string(config.CookiePolicyAllowlist) {
		return fmt.Errorf("unsupported --require-policy value %q (supported: %q)", required, config.CookiePolicyAllowlist)
	}
	if blocklist.PolicyMode() != config.CookiePolicyAllowlist {
		return fmt.Errorf("agent-sync: required cookie policy %q is not active (effective policy: %s)", required, blocklist.CookiePolicySummary())
	}
	return nil
}

func loadRequiredAgentSyncPolicy(required string) (*config.Blocklist, error) {
	blocklist, err := loadFreshBlocklist()
	if err != nil {
		return nil, err
	}
	if err := enforceAgentSyncPolicy(blocklist, required); err != nil {
		return nil, err
	}
	return blocklist, nil
}

func writeAgentSyncCapabilities(w io.Writer, cmd *cobra.Command, cfg *config.SourceConfig, blocklist *config.Blocklist) error {
	browser, err := chrome.LookupBrowser(cfg.Browser.Name)
	if err != nil {
		return err
	}
	flags := make([]string, 0, cmd.Flags().NFlag())
	cmd.Flags().VisitAll(func(flag *pflag.Flag) {
		flags = append(flags, "--"+flag.Name)
	})
	sort.Strings(flags)

	report := agentSyncCapabilities{
		SchemaVersion:           agentSyncCapabilitiesSchemaVersion,
		SupportedFlags:          flags,
		EffectiveBrowserDefault: browser.Name,
		PolicyMode:              blocklist.CookiePolicySummary(),
		BuildVersion:            Version,
		SigningSummary: agentSyncSigningSummary{
			CanonicalIdentityEnv: canonicalSignIdentityEnv,
			ExternalWrapperMapping: agentSyncSigningWrapperMapping{
				From: externalWrapperSignIdentityEnv,
				To:   canonicalSignIdentityEnv,
			},
		},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
