package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

// resolveSourceDBPath returns the cookie DB path for the effective source
// browser. cfg.Chrome.DBPath is derived from source.yaml's browser+profile at
// config load, so a flag override that changes the browser (or an explicit
// profile) must re-derive the path; otherwise the flag browser's Keychain key
// would be applied to the configured browser's store and decryption would
// fail wholesale. Profile names are browser-local, so a browser switch with
// no explicit profile lands on the target browser's default profile.
func resolveSourceDBPath(cfg *config.SourceConfig, flagBrowser, flagProfile, effectiveBrowser string) (string, error) {
	if (flagBrowser == "" || strings.EqualFold(flagBrowser, cfg.Browser.Name)) && flagProfile == "" {
		return cfg.Chrome.DBPath, nil
	}
	profile := flagProfile
	if profile == "" {
		profile = config.AdmittedProfileForBrowser(effectiveBrowser, cfg)
	}
	dbPath, err := config.SourceBrowserCookiesPath(effectiveBrowser, profile)
	if err != nil {
		return "", fmt.Errorf("resolve cookie store for --browser %s: %w", effectiveBrowser, err)
	}
	return dbPath, nil
}

// readStats summarizes one read+filter pass for logging by the caller.
type readStats struct {
	totalRead    int
	totalDropped int
	droppedHosts map[string]int
	dbsc         dbscSummary
}

// readFilteredCookies reads every cookie from the browser's Cookies DB,
// applies the cookie policy, and runs the DBSC classifier -- the shared read
// pipeline behind both `source` (push to a peer) and `cmux-sync` (local
// loop into cmux). Keeping it in one place is what guarantees the two
// paths filter identically.
//
// It returns the cookies that survive both filters (DBSC "shipped"),
// plus stats for the caller to log however fits its surface. Logging and
// result-map shaping stay with the caller so each command keeps its own
// output voice.
func readFilteredCookies(dbPath string, blocklist *config.Blocklist, key []byte, skipDBSC bool, now time.Time) ([]chrome.Cookie, readStats, error) {
	all, err := chrome.ReadCookiesForHost(dbPath, "%", key)
	if err != nil {
		return nil, readStats{}, fmt.Errorf("read cookies: %w", err)
	}
	st := readStats{totalRead: len(all)}

	all, st.droppedHosts = protocol.NewBlocklistMatcher(blocklist).Filter(all)
	for _, n := range st.droppedHosts {
		st.totalDropped += n
	}

	dbscRes := chrome.ClassifyCookies(all, now, skipDBSC)
	all = dbscRes.Shipped
	st.dbsc = dbscSummary{
		warned:  len(dbscRes.Warned),
		skipped: len(dbscRes.Skipped),
		sample:  dbscSampleReasons(dbscRes),
	}
	return all, st, nil
}
