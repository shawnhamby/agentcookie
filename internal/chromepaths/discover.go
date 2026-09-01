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

// DiscoverOptions scopes discovery to enabled sources.
type DiscoverOptions struct {
	// Browser limits automatic root scanning to one enabled browser
	// ("chrome" or "edge"). Empty scans both enabled browsers.
	Browser string
	// ProfileDir is an explicit user-data-dir from CDP or source config.
	// Only enabled browser+profile pairs under it are returned.
	ProfileDir string
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

const (
	enabledChromeProfile = "Default"   // Personal
	enabledEdgeProfile   = "Profile 2" // School / WSU research
)

// isEnabledStore reports whether browser+profile is an enabled source.
// Enabled sources are Chrome Default and Edge Profile 2 only.
func isEnabledStore(browser, profile string) bool {
	switch browser {
	case "chrome":
		return profile == enabledChromeProfile
	case "edge":
		return profile == enabledEdgeProfile
	default:
		return false
	}
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
// browsers only (Google Chrome and Microsoft Edge).
func enabledBrowserRoots(browser string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	key := strings.ToLower(strings.TrimSpace(browser))
	var roots []string

	switch runtime.GOOS {
	case "darwin":
		appSupport := filepath.Join(home, "Library", "Application Support")
		if key == "" || key == "chrome" {
			roots = append(roots, filepath.Join(appSupport, "Google", "Chrome"))
		}
		if key == "" || key == "edge" {
			roots = append(roots, filepath.Join(appSupport, "Microsoft Edge"))
		}
	case "linux":
		configDir := filepath.Join(home, ".config")
		if key == "" || key == "chrome" {
			roots = append(roots, filepath.Join(configDir, "google-chrome"))
		}
		if key == "" || key == "edge" {
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
// required. Only enabled sources (Chrome Default, Edge Profile 2) are returned.
func Discover() DiscoverResult {
	return DiscoverWithOptions(DiscoverOptions{})
}

// DiscoverWithOptions scans enabled browser roots and optional explicit dirs.
// Non-enabled browsers and profiles are never scanned into Stores.
func DiscoverWithOptions(opts DiscoverOptions) DiscoverResult {
	var result DiscoverResult

	defaultRoot := osDefaultChromeRoot()
	seen := make(map[string]bool)

	for _, root := range enabledBrowserRoots(opts.Browser) {
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
		if browser == "" {
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
			addStoreIfEnabled(&result, root, name, profileDir, browser, isDefaultRoot && name == "Default")
		}
	}

	if opts.ProfileDir != "" {
		mergeExplicitProfileDir(opts.ProfileDir, &result)
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

// DiscoverForSource scopes discovery to enabled browsers. When browser is
// non-empty (from source.yaml or --browser), automatic root scanning is limited
// to that browser. profileDir adds an explicit user-data-dir when set; only
// enabled profiles under it become stores.
func DiscoverForSource(profileDir, browser string) DiscoverResult {
	if profileDir == "" {
		return DiscoverWithOptions(DiscoverOptions{Browser: browser})
	}

	var result DiscoverResult
	mergeExplicitProfileDir(profileDir, &result)
	return result
}

func mergeExplicitProfileDir(profileDir string, result *DiscoverResult) {
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
	if browser == "" {
		return
	}
	for _, s := range result.Stores {
		if s.Root == abs || filepath.Join(s.Root, s.Profile) == abs {
			return
		}
	}
	if addedFromChildren := scanUserDataDir(abs, result); !addedFromChildren {
		addStoreIfEnabled(result, filepath.Dir(abs), filepath.Base(abs), abs, browser, false)
	}
}

// scanUserDataDir scans root as a Chromium user-data-dir. Returns true if any
// profile-shaped children exist (enabled or not), so callers do not probe the
// root itself as a profile.
func scanUserDataDir(root string, result *DiscoverResult) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}

	browser := browserForRoot(root)
	if browser == "" {
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
		addStoreIfEnabled(result, root, name, profileDir, browser, name == "Default")
	}

	return foundProfiles
}

// addStoreIfEnabled probes Cookies only for enabled browser+profile pairs.
func addStoreIfEnabled(result *DiscoverResult, root, name, profileDir, browser string, isDefault bool) {
	if !isEnabledStore(browser, name) {
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
