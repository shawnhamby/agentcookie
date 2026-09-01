package chromepaths

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Store represents a discovered cookie store that may be readable.
type Store struct {
	// Root is the user-data-dir containing this profile (e.g.
	// ~/Library/Application Support/Google/Chrome).
	Root string

	// Profile is the profile directory name (e.g. "Default", "Profile 2").
	Profile string

	// CookiesPath is the resolved path to the Cookies SQLite file.
	// Either Root/Profile/Cookies or Root/Profile/Network/Cookies.
	CookiesPath string

	// IsDefault is true when this is the historical Default profile in
	// the OS-standard Chrome root.
	IsDefault bool

	// Browser identifies which enabled browser this store belongs to
	// ("chrome" or "edge").
	Browser string
}

// DiscoverOptions scopes discovery to enabled products.
type DiscoverOptions struct {
	// Browser limits automatic root scanning to one enabled product
	// ("chrome" or "edge"). Empty scans every enabled product.
	Browser string
	// ProfileDir is an explicit user-data-dir from CDP or source config.
	// Only user profiles of enabled products under it are returned.
	ProfileDir string
	// EnabledProducts is the ordered product list. Empty uses
	// DefaultEnabledProducts. Unlisted products are never scanned.
	EnabledProducts []string
}

// SkippedStore is a profile directory that was found but cannot be used,
// along with the reason it was skipped.
type SkippedStore struct {
	Root    string
	Profile string
	Reason  string
}

// DiscoverResult holds the outcome of a discovery pass.
type DiscoverResult struct {
	// Stores is the list of usable cookie stores found.
	Stores []Store

	// Skipped is the list of profile directories found but not usable.
	Skipped []SkippedStore
}

// profileDirName matches Chromium profile directory names so cache dirs are
// not mistaken for a user-data-dir. Matching is not enablement: only
// isEnabledStore decides which profiles become stores.
var profileDirName = regexp.MustCompile(`^(Default|Profile \d+|Guest Profile|System Profile)$`)

// userProfileName matches profiles that may become stores. Guest and System
// profiles are never stores even when their product is enabled.
var userProfileName = regexp.MustCompile(`^(Default|Profile \d+)$`)

// DefaultEnabledProducts mirrors config.DefaultEnabledProducts. Guarded by
// internal/cli registry consistency tests.
var DefaultEnabledProducts = []string{"chrome", "edge"}

func resolveEnabledProducts(products []string) []string {
	if len(products) == 0 {
		return append([]string(nil), DefaultEnabledProducts...)
	}
	out := make([]string, 0, len(products))
	seen := make(map[string]bool, len(products))
	for _, name := range products {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultEnabledProducts...)
	}
	return out
}

func productEnabled(browser string, enabled []string) bool {
	key := strings.ToLower(strings.TrimSpace(browser))
	for _, p := range enabled {
		if p == key {
			return true
		}
	}
	return false
}

// isEnabledStore reports whether browser+profile is an enabled source.
// A store requires an enabled product and a user profile directory
// (Default or Profile N). Guest, System, and unlisted products never qualify.
func isEnabledStore(browser, profile string, enabled []string) bool {
	if !productEnabled(browser, enabled) {
		return false
	}
	return userProfileName.MatchString(profile)
}

// skipDirs are directory names that should never be treated as profiles.
var skipDirs = map[string]bool{
	"Crashpad":              true,
	"ShaderCache":           true,
	"GrShaderCache":         true,
	"GPUCache":              true,
	"Cache":                 true,
	"Code Cache":            true,
	"component_crx_cache":   true,
	"Safe Browsing":         true,
	"Crowd Deny":            true,
	"MEIPreload":            true,
	"FileTypePolicies":      true,
	"hyphen-data":           true,
	"OptimizationHints":     true,
	"OriginTrials":          true,
	"SSLErrorAssistant":     true,
	"Subresource Filter":    true,
	"ZxcvbnData":            true,
	"BrowserMetrics":        true,
	"extensions_crx_cache":  true,
	"pnacl":                 true,
	"PnaclTranslationCache": true,
}

// enabledBrowserRoots returns OS-standard user-data-dir roots for enabled
// products only. Unlisted products (Brave, Arc, …) are never returned.
func enabledBrowserRoots(browser string, enabled []string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	key := strings.ToLower(strings.TrimSpace(browser))
	var roots []string

	want := func(name string) bool {
		if !productEnabled(name, enabled) {
			return false
		}
		return key == "" || key == name
	}

	switch runtime.GOOS {
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support")
		if want("chrome") {
			roots = append(roots, filepath.Join(appSupport, "Google", "Chrome"))
		}
		if want("edge") {
			roots = append(roots, filepath.Join(appSupport, "Microsoft Edge"))
		}
	case "linux":
		configDir := filepath.Join(home, ".config")
		if want("chrome") {
			roots = append(roots, filepath.Join(configDir, "google-chrome"))
		}
		if want("edge") {
			roots = append(roots, filepath.Join(configDir, "microsoft-edge"))
		}
	}

	return roots
}

// osDefaultChromeRoot returns the path to the OS-standard Chrome user-data-dir.
func osDefaultChromeRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config", "google-chrome")
	}
	return ""
}

// browserForRoot returns an enabled browser identifier for a user-data-dir
// root. Paths that are not Google Chrome or Microsoft Edge return "" so
// callers never touch them.
func browserForRoot(root string) string {
	lower := strings.ToLower(filepath.Clean(root))
	sep := string(filepath.Separator)
	if strings.Contains(lower, "microsoft edge") || strings.Contains(lower, "microsoft-edge") {
		return "edge"
	}
	if strings.Contains(lower, "google"+sep+"chrome") || strings.Contains(lower, "google-chrome") {
		return "chrome"
	}
	return ""
}

// Discover scans enabled browser user-data-dirs and returns usable stores.
// A store is usable if Cookies or Network/Cookies exists. Local State is NOT
// required. All user profiles of enabled products are returned; Guest and
// System profiles never are.
func Discover() DiscoverResult {
	return DiscoverWithOptions(DiscoverOptions{})
}

// DiscoverWithOptions scans enabled browser roots and optional explicit dirs.
// Non-enabled products and Guest/System profiles are never scanned into Stores.
func DiscoverWithOptions(opts DiscoverOptions) DiscoverResult {
	var result DiscoverResult
	enabled := resolveEnabledProducts(opts.EnabledProducts)

	defaultRoot := osDefaultChromeRoot()
	seen := make(map[string]bool)

	for _, root := range enabledBrowserRoots(opts.Browser, enabled) {
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true

		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}

		browser := browserForRoot(root)
		if browser == "" || !productEnabled(browser, enabled) {
			continue
		}
		isDefaultRoot := abs == defaultRoot || root == defaultRoot

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if skipDirs[name] {
				continue
			}
			if !profileDirName.MatchString(name) {
				continue
			}
			profileDir := filepath.Join(root, name)
			addStoreIfEnabled(&result, root, name, profileDir, browser, isDefaultRoot && name == "Default", enabled)
		}
	}

	if opts.ProfileDir != "" {
		mergeExplicitProfileDir(opts.ProfileDir, &result, enabled)
	}

	return result
}

// probeProfileDir checks if profileDir has a usable Cookies file.
// Returns a Store if usable, or a skip reason string if not.
func probeProfileDir(root, profileName, profileDir, browser string, isDefault bool) (*Store, string) {
	networkCookies := filepath.Join(profileDir, "Network", "Cookies")
	legacyCookies := filepath.Join(profileDir, "Cookies")

	var cookiesPath string
	if fileExists(networkCookies) {
		cookiesPath = networkCookies
	} else if fileExists(legacyCookies) {
		cookiesPath = legacyCookies
	}

	if cookiesPath == "" {
		return nil, "no Cookies file"
	}

	return &Store{
		Root:        root,
		Profile:     profileName,
		CookiesPath: cookiesPath,
		IsDefault:   isDefault,
		Browser:     browser,
	}, ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func expandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// DiscoverForConfig returns stores scoped to an optional explicit profile dir.
// Deprecated: use DiscoverForSource which also honors source.yaml browser.
func DiscoverForConfig(profileDir string) DiscoverResult {
	return DiscoverForSource(profileDir, "")
}

// DiscoverForSource scopes discovery to enabled products. When browser is
// non-empty (from --browser), automatic root scanning is limited to that
// product. profileDir adds an explicit user-data-dir when set; only user
// profiles of enabled products under it become stores.
func DiscoverForSource(profileDir, browser string) DiscoverResult {
	return DiscoverForSourceWithProducts(profileDir, browser, nil)
}

// DiscoverForSourceWithProducts is DiscoverForSource with an explicit product list.
func DiscoverForSourceWithProducts(profileDir, browser string, enabledProducts []string) DiscoverResult {
	if profileDir == "" {
		return DiscoverWithOptions(DiscoverOptions{Browser: browser, EnabledProducts: enabledProducts})
	}

	var result DiscoverResult
	mergeExplicitProfileDir(profileDir, &result, resolveEnabledProducts(enabledProducts))
	return result
}

func mergeExplicitProfileDir(profileDir string, result *DiscoverResult, enabled []string) {
	expanded := expandTilde(profileDir)
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return
	}
	browser := browserForRoot(abs)
	if browser == "" {
		// Bare profile path: identify from parent user-data-dir.
		browser = browserForRoot(filepath.Dir(abs))
	}
	if browser == "" || !productEnabled(browser, enabled) {
		return
	}
	for _, s := range result.Stores {
		if s.Root == abs || filepath.Join(s.Root, s.Profile) == abs {
			return
		}
	}
	if addedFromChildren := scanUserDataDir(abs, result, enabled); !addedFromChildren {
		addStoreIfEnabled(result, filepath.Dir(abs), filepath.Base(abs), abs, browser, false, enabled)
	}
}

// scanUserDataDir scans root as a Chromium user-data-dir. Returns true if any
// profile-shaped children exist (enabled or not), so callers do not probe the
// root itself as a profile.
func scanUserDataDir(root string, result *DiscoverResult, enabled []string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}

	browser := browserForRoot(root)
	if browser == "" || !productEnabled(browser, enabled) {
		return false
	}
	foundProfiles := false

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if skipDirs[name] {
			continue
		}
		if !profileDirName.MatchString(name) {
			continue
		}
		foundProfiles = true
		profileDir := filepath.Join(root, name)
		addStoreIfEnabled(result, root, name, profileDir, browser, name == "Default", enabled)
	}

	return foundProfiles
}

// addStoreIfEnabled probes Cookies only for enabled product+user-profile pairs.
func addStoreIfEnabled(result *DiscoverResult, root, name, profileDir, browser string, isDefault bool, enabled []string) {
	if !isEnabledStore(browser, name, enabled) {
		return
	}
	store, skipReason := probeProfileDir(root, name, profileDir, browser, isDefault)
	if skipReason != "" {
		result.Skipped = append(result.Skipped, SkippedStore{
			Root:    root,
			Profile: name,
			Reason:  skipReason,
		})
	} else if store != nil {
		result.Stores = append(result.Stores, *store)
	}
}
