package cli

import (
	"context"
	"encoding/json"
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
	agentSyncPort             int
	agentSyncHeaded           bool
	agentSyncChromePath       string
	agentSyncUserDataDir      string
	agentSyncSkipDBSC         bool
	agentSyncDomains          []string
	agentSyncBrowser          string
	agentSyncProfile          string
	agentSyncVerbose          bool
	agentSyncUserAgent        string
	agentSyncWindowSize       string
	agentSyncRequirePolicy    string
	agentSyncCapabilitiesJSON bool
	agentSyncProxyServer      string
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
	agentSyncCmd.Flags().StringVar(&agentSyncWindowSize, "window-size", "", "owned browser window size WxH (e.g. 1728,1117 for this machine's real display; default: Chrome's 800x600 headless)")
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
		ChromePath:  agentSyncChromePath,
		UserDataDir: userDataDir,
		Port:        agentSyncPort,
		Headless:    !agentSyncHeaded,
		UserAgent:   agentSyncUserAgent,
		WindowSize:  agentSyncWindowSize,
		ProxyServer: agentSyncProxyServer,
		LeanProfile: true,
	})
	if err != nil {
		return err
	}
	defer oc.Close()

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, oc.Endpoint)
	defer allocCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	// Establish the browser-level CDP connection once, then drive all cookie
	// injection through the browser executor + the shutdown-scoped ctx (never
	// browserCtx). A CDP connector (agent-browser/browser-use) closes the pages
	// browserCtx is bound to, which cancels browserCtx; the browser-level
	// connection is owned by the allocator and survives, so injection keeps
	// working across connector churn. (Fixes: the poll loop used to die with
	// "list targets: context canceled" once a connector attached, so the
	// connector's own context never got cookies -- the agent woke up logged out.)
	if err := chromedp.Run(browserCtx); err != nil {
		return fmt.Errorf("agent-sync: establish browser connection: %w", err)
	}
	browser := chromedp.FromContext(browserCtx).Browser

	syncLog := func(format string, a ...any) {
		if agentSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie agent-sync: "+format+"\n", a...)
		}
	}
	if agentSyncVerbose && agentSyncProxyServer != "" {
		syncLog("proxy-server %s", livecdp.RedactProxyURL(agentSyncProxyServer))
	}
	syncer := livecdp.NewSyncer(ctx, browser, provider, syncLog)

	// Initial inject so the owned browser's default context is logged in
	// immediately; also surfaces connection/cookie errors at startup.
	n, err := syncer.ReinjectAll()
	if err != nil {
		return fmt.Errorf("agent-sync: initial inject: %w", err)
	}

	fmt.Fprintf(os.Stderr, "agentcookie agent-sync: owned Chrome on %s (profile %s); injected %d context(s)\n", oc.Endpoint, userDataDir, n)
	fmt.Fprintln(os.Stderr, "Connect an agent browser:")
	fmt.Fprintf(os.Stderr, "  browser-use --cdp-url %s open https://github.com\n", oc.Endpoint)
	fmt.Fprintf(os.Stderr, "  agent-browser --cdp %d\n", oc.Port)
	fmt.Fprintln(os.Stderr, "Watching Chrome cookies + new contexts. Ctrl-C to stop.")

	// Poll for new contexts (e.g. the one browser-use creates on connect) and
	// inject them. Runs concurrently with the cookie-change watcher below.
	go func() {
		if runErr := syncer.Run(ctx); runErr != nil && runErr != context.Canceled {
			syncLog("context poll: %v", runErr)
		}
	}()

	// Watch the source cookie DB(s); on each debounced change, re-inject
	// current cookies into every live context so a site the user just logged
	// into in their real Chrome becomes logged-in in the agent browser too.
	// A failed cycle is logged and the watcher keeps running.
	push := func(context.Context) (int, error) {
		return syncer.ReinjectAll()
	}
	onEvent := func(ev watcher.Event) {
		if agentSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie agent-sync: %s\n", ev.String())
		}
	}
	err = runAgentSyncWatchers(ctx, watchPaths, push, onEvent)
	if err != nil && err != context.Canceled {
		return err
	}
	fmt.Fprintln(os.Stderr, "agentcookie agent-sync: stopped")
	return nil
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
