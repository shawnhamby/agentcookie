package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
)

var (
	exportDomains  []string
	exportSkipDBSC bool
	exportBrowser  string
)

// exportEpochOffsetSec converts Chrome's microseconds-since-1601 cookie expiry
// to the Unix seconds a Chromium consumer (orca's cookie importer) expects.
const exportEpochOffsetSec = 11644473600

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Emit this machine's current plaintext cookie set as JSON for a consumer to import",
	Long: `export runs the live read pipeline -- read this Mac's enabled browser
cookies, decrypt them, apply the blocklist, and drop device-bound (DBSC)
cookies -- and prints the surviving set to stdout as a JSON array in the shape
a Chromium consumer accepts (e.g. ` + "`orca cookie import`" + `):

  agentcookie export | orca cookie import

By default it merges every user profile of every enabled product
(source.yaml enabled_products, default chrome then edge) into one store.
Conflict precedence follows product list order (name+domain+path); earlier
products win. --browser chrome|edge pins to one product and still merges that
product's user profiles. Guest and System profiles are never stores.

It is a live read (the same pipeline source and agent-sync use), so it does
not depend on the sink or the sidecar and works on a purely-local machine.
Each object carries name, value, domain, path, secure, httpOnly, sameSite, and
(for persistent cookies) expirationDate in Unix seconds.

  agentcookie export                         emit the merged set as JSON
  agentcookie export --browser chrome        pin to one enabled product
  agentcookie export --domain %github.com    limit to matching hosts

stdout is a clean JSON document; the count of skipped device-bound cookies is
reported on stderr so it never corrupts the JSON. Device-bound (DBSC) sites --
Google/Workspace account cookies -- cannot transfer to another browser and are
excluded, not faked.`,
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringSliceVar(&exportDomains, "domain", nil, "limit to these host_key LIKE patterns (repeatable), e.g. --domain %github.com")
	exportCmd.Flags().BoolVar(&exportSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC); also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	exportCmd.Flags().StringVar(&exportBrowser, "browser", "", "pin to one enabled product (default: merge all enabled products)")
}

func runExport(cmd *cobra.Command, args []string) error {
	// LoadSourceLocal, not LoadSource: export has no push target, so it must
	// not require sink.url or a peer/secret. A missing source.yaml is fine
	// (defaults: enabled products chrome+edge, no blocklist).
	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return err
	}
	// Load the blocklist once, up front: fails fast on a broken file before any
	// keychain prompt, and is reused for the read below.
	blocklist, err := loadFreshBlocklist()
	if err != nil {
		return err
	}

	enabled, err := config.ResolveEnabledProducts(cfg)
	if err != nil {
		return err
	}

	pin := strings.ToLower(strings.TrimSpace(exportBrowser))
	if pin != "" {
		if !config.ProductEnabled(pin, enabled) {
			// Fail closed before any Keychain probe for unlisted products.
			if _, err := chrome.LookupBrowser(pin); err != nil {
				return err
			}
			return fmt.Errorf("browser %q is not in enabled_products (%s)", pin, strings.Join(enabled, ", "))
		}
		enabled = []string{pin}
	}

	skipDBSC := exportSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"
	cookies, st, err := readMergedExportCookies(enabled, blocklist, skipDBSC, time.Now().UTC())
	if err != nil {
		return err
	}
	cookies = sinkpush.FilterByHostPatterns(cookies, exportDomains)

	enc := json.NewEncoder(cmd.OutOrStdout())
	if err := enc.Encode(toExportCookies(cookies)); err != nil {
		return fmt.Errorf("export: encode cookies: %w", err)
	}

	// Report the DBSC-skipped count on stderr (never stdout) so a device-bound
	// cookie that was dropped is explainable rather than a mysterious
	// logged-out site, without corrupting the JSON document on stdout.
	if st.dbsc.skipped > 0 {
		fmt.Fprintf(os.Stderr, "agentcookie export: skipped %d device-bound (DBSC) cookies -- those sessions cannot transfer and will read as logged-out in the consumer\n", st.dbsc.skipped)
	}
	return nil
}

// readMergedExportCookies discovers stores for enabled products, decrypts each
// user profile, and merges with first-wins precedence following product order
// then Default-before-other profiles. Per-store decrypt failures are skipped
// so one locked profile does not fail the whole export.
func readMergedExportCookies(enabled []string, blocklist *config.Blocklist, skipDBSC bool, now time.Time) ([]chrome.Cookie, readStats, error) {
	discovery := chromepaths.DiscoverForSourceWithProducts("", "", enabled)
	if len(discovery.Stores) == 0 {
		return nil, readStats{}, fmt.Errorf("export: no cookie stores found for enabled products %s", strings.Join(enabled, ", "))
	}

	stores := sortStoresForMerge(discovery.Stores, enabled)
	var merged []chrome.Cookie
	var st readStats
	st.droppedHosts = map[string]int{}
	keys := map[string][]byte{}
	var readErrs []string

	for _, store := range stores {
		key, ok := keys[store.Browser]
		if !ok {
			derived, err := getChromeDecryptKey(store.Browser)
			if err != nil {
				readErrs = append(readErrs, store.Browser+"/"+store.Profile+": "+err.Error())
				continue
			}
			keys[store.Browser] = derived
			key = derived
		}
		cookies, storeStats, err := readFilteredCookies(store.CookiesPath, blocklist, key, skipDBSC, now)
		if err != nil {
			readErrs = append(readErrs, store.Browser+"/"+store.Profile+": "+err.Error())
			continue
		}
		st.totalRead += storeStats.totalRead
		st.totalDropped += storeStats.totalDropped
		for host, n := range storeStats.droppedHosts {
			st.droppedHosts[host] += n
		}
		st.dbsc.warned += storeStats.dbsc.warned
		st.dbsc.skipped += storeStats.dbsc.skipped
		merged = appendCookieMerge(merged, cookies)
	}

	if len(merged) == 0 && len(readErrs) > 0 {
		return nil, st, fmt.Errorf("export: failed to read cookie stores: %s", strings.Join(readErrs, "; "))
	}
	return merged, st, nil
}

// sortStoresForMerge orders stores by enabled-product precedence, then Default
// first, then profile name. First-wins merge therefore prefers earlier products.
func sortStoresForMerge(stores []chromepaths.Store, enabled []string) []chromepaths.Store {
	rank := make(map[string]int, len(enabled))
	for i, name := range enabled {
		rank[name] = i
	}
	out := append([]chromepaths.Store(nil), stores...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank[out[i].Browser], rank[out[j].Browser]
		if ri != rj {
			return ri < rj
		}
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].CookiesPath < out[j].CookiesPath
	})
	return out
}

func cookieMergeKey(c chrome.Cookie) string {
	return c.Name + "\x00" + c.HostKey + "\x00" + c.Path
}

// appendCookieMerge appends cookies from next that are not already present
// under the name+domain+path identity key (first-wins).
func appendCookieMerge(dst, next []chrome.Cookie) []chrome.Cookie {
	seen := make(map[string]struct{}, len(dst))
	for _, c := range dst {
		seen[cookieMergeKey(c)] = struct{}{}
	}
	for _, c := range next {
		k := cookieMergeKey(c)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		dst = append(dst, c)
	}
	return dst
}

// exportCookie is the per-cookie JSON shape a Chromium consumer's importer
// accepts (orca's RawCookieEntry): domain, name, value, path, secure, httpOnly,
// sameSite, and an optional expirationDate in Unix seconds. Field names and
// JSON tags match so the output imports with no mapping step on the consumer.
type exportCookie struct {
	Domain         string              `json:"domain"`
	Name           string              `json:"name"`
	Value          string              `json:"value"`
	Path           string              `json:"path"`
	Secure         bool                `json:"secure"`
	HTTPOnly       bool                `json:"httpOnly"`
	SameSite       string              `json:"sameSite"`
	ExpirationDate *int64              `json:"expirationDate,omitempty"`
	PartitionKey   *exportPartitionKey `json:"partitionKey,omitempty"`
}

// exportPartitionKey mirrors CDP's network.CookiePartitionKey field names so
// a CHIPS-partitioned cookie (e.g. Cloudflare's cf_clearance) round-trips
// through export without a mapping step. Omitted entirely for ordinary,
// unpartitioned cookies (the common case).
type exportPartitionKey struct {
	TopLevelSite         string `json:"topLevelSite"`
	HasCrossSiteAncestor bool   `json:"hasCrossSiteAncestor"`
}

// toExportCookies maps decrypted chrome.Cookie rows into the consumer import
// shape. Pure (no I/O) so it is unit-testable without a Chrome DB. Domain and
// value pass through verbatim: the value is already App-Bound-stripped by the
// source read pipeline.
func toExportCookies(cookies []chrome.Cookie) []exportCookie {
	out := make([]exportCookie, 0, len(cookies))
	for _, c := range cookies {
		ec := exportCookie{
			Domain:   c.HostKey,
			Name:     c.Name,
			Value:    c.Value,
			Path:     c.Path,
			Secure:   c.IsSecure == 1,
			HTTPOnly: c.IsHTTPOnly == 1,
			SameSite: exportSameSite(c.SameSite),
		}
		// Persistent cookies carry an expiry; session cookies (HasExpires == 0)
		// omit it so the consumer treats them as session cookies rather than
		// expiring them against a bogus 1601-epoch value.
		if c.HasExpires != 0 && c.ExpiresUTC != 0 {
			exp := c.ExpiresUTC/1_000_000 - exportEpochOffsetSec
			ec.ExpirationDate = &exp
		}
		if c.TopFrameSiteKey != "" {
			ec.PartitionKey = &exportPartitionKey{
				TopLevelSite:         c.TopFrameSiteKey,
				HasCrossSiteAncestor: c.HasCrossSiteAncestor != 0,
			}
		}
		out = append(out, ec)
	}
	return out
}

// exportSameSite maps Chrome's numeric SameSite (-1 unspecified, 0 None, 1 Lax,
// 2 Strict) to the string a Chromium importer normalizes.
func exportSameSite(s int) string {
	switch s {
	case 0:
		return "no_restriction"
	case 1:
		return "lax"
	case 2:
		return "strict"
	default:
		return "unspecified"
	}
}
