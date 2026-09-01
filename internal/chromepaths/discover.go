package chromepaths

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Store represents a discovered Chrome cookie store that may be readable.
type Store struct {
	// Root is the user-data-dir containing this profile (e.g.
	// ~/Library/Application Support/Google/Chrome).
	Root string

	// Profile is the profile directory name (e.g. "Default", "Profile 1").
	Profile string

	// CookiesPath is the resolved path to the Cookies SQLite file.
	// Either Root/Profile/Cookies or Root/Profile/Network/Cookies.
	CookiesPath string

	// IsDefault is true when this is the historical Default profile in
	// the OS-standard Chrome root. Distinguishes "the" profile agentcookie
	// v0.7 read from extra profiles.
	IsDefault bool

	// Browser identifies which admitted browser this store belongs to
	// ("chrome" or "edge").
	Browser string
}

// DiscoverOptions scopes extra-profile discovery to admitted browsers only.
type DiscoverOptions struct {
	// Browser limits automatic root scanning to one admitted browser
	// ("chrome" or "edge"). Empty scans both admitted browsers.
	Browser string
	// ProfileDir is an explicit user-data-dir from CDP or source config.
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

// profileNameAllowlist matches profile directory names that are actual
// Chrome profiles, not cache/crash dirs. Patterns:
//   - Default
//   - Profile N (where N is a number)
//   - Guest Profile
//   - System Profile
var profileNameAllowlist = regexp.MustCompile(`^(Default|Profile \d+|Guest Profile|System Profile)$`)

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

// admittedBrowserRoots returns OS-standard user-data-dir roots for admitted
// browsers (Chrome and Edge only). Generic Chromium, Brave, and Arc paths are
// intentionally excluded.
func admittedBrowserRoots(browser string) []string {
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

	// Agent-specific Chrome user-data-dirs (never Edge/Brave/Arc).
	if key == "" || key == "chrome" {
		roots = append(roots,
			filepath.Join(home, "chrome-profile"),
			filepath.Join(home, ".agentcookie", "chrome-profile"),
		)
		if envDir := os.Getenv("CHROME_USER_DATA_DIR"); envDir != "" && !isDisallowedRoot(envDir) {
			roots = append(roots, envDir)
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

// isDisallowedRoot reports whether root belongs to a non-admitted browser
// (Brave, Arc, generic Chromium, etc.).
func isDisallowedRoot(root string) bool {
	lower := strings.ToLower(filepath.Clean(root))
	if strings.Contains(lower, "bravesoftware") || strings.Contains(lower, "brave-browser") {
		return true
	}
	if strings.Contains(lower, string(filepath.Separator)+"arc"+string(filepath.Separator)) ||
		strings.HasSuffix(lower, string(filepath.Separator)+"arc") {
		return true
	}
	if strings.Contains(lower, string(filepath.Separator)+"chromium") &&
		!strings.Contains(lower, "google") {
		return true
	}
	return false
}

// browserForRoot returns an admitted browser identifier based on the root path.
// Disallowed roots return "" so callers skip them without touching Keychain.
func browserForRoot(root string) string {
	if isDisallowedRoot(root) {
		return ""
	}
	lower := strings.ToLower(root)
	if strings.Contains(lower, "edge") || strings.Contains(lower, "microsoft-edge") {
		return "edge"
	}
	return "chrome"
}

// Discover scans known Chrome user-data-dirs and returns all usable
// cookie stores. A store is usable if a Cookies file exists at either
// <profile>/Cookies or <profile>/Network/Cookies. Local State is NOT
// required (matches real-world agent Chromes that lack it).
//
// The result includes both Stores (usable) and Skipped (found but not
// usable, with reasons), so doctor/status can report skip reasons.
func Discover() DiscoverResult {
	return DiscoverWithOptions(DiscoverOptions{})
}

// DiscoverWithOptions scans admitted browser user-data-dirs and optional
// explicit profile roots. Brave, Arc, and generic Chromium are never scanned.
func DiscoverWithOptions(opts DiscoverOptions) DiscoverResult {
	var result DiscoverResult

	defaultRoot := osDefaultChromeRoot()
	seen := make(map[string]bool)

	for _, root := range admittedBrowserRoots(opts.Browser) {
		if root == "" {
			continue
		}
		// Normalize to catch duplicates.
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
			// Root doesn't exist or not readable - not an error.
			continue
		}

		browser := browserForRoot(root)
		if browser == "" {
			continue
		}
		isDefaultRoot := (abs == defaultRoot || root == defaultRoot)

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()

			// Skip non-profile directories.
			if skipDirs[name] {
				continue
			}

			// Check if this looks like a profile directory.
			if !profileNameAllowlist.MatchString(name) {
				// For well-known agent roots (chrome-profile, agentcookie/chrome-profile),
				// the root itself is the profile (not a subdirectory).
				// We'll handle these specially below.
				continue
			}

			profileDir := filepath.Join(root, name)
			store, skipReason := probeProfileDir(root, name, profileDir, browser, isDefaultRoot && name == "Default")
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

		// For well-known agent roots that are themselves profiles (the root
		// IS the profile, no subdirectory), probe the root directly.
		if strings.HasSuffix(root, "chrome-profile") {
			store, skipReason := probeProfileDir(filepath.Dir(root), filepath.Base(root), root, browser, false)
			if skipReason != "" {
				result.Skipped = append(result.Skipped, SkippedStore{
					Root:    filepath.Dir(root),
					Profile: filepath.Base(root),
					Reason:  skipReason,
				})
			} else if store != nil {
				result.Stores = append(result.Stores, *store)
			}
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
	// Try Network/Cookies first (Chrome 96+), then Cookies.
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

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// expandTilde turns a leading "~/" into the user's home dir. Leaves all other
// paths alone. Matches config.ExpandTilde behavior.
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

// DiscoverForSource scopes discovery to admitted browsers. When browser is
// non-empty (from source.yaml or --browser), automatic root scanning is limited
// to that browser. profileDir adds an explicit user-data-dir when set.
func DiscoverForSource(profileDir, browser string) DiscoverResult {
	if profileDir == "" {
		return DiscoverWithOptions(DiscoverOptions{Browser: browser})
	}

	// A configured profile path is an explicit scope boundary. Do not union it
	// with every browser/profile discovered on the machine.
	var result DiscoverResult
	mergeExplicitProfileDir(profileDir, &result)
	return result
}

func mergeExplicitProfileDir(profileDir string, result *DiscoverResult) {
	expanded := expandTilde(profileDir)
	abs, err := filepath.Abs(expanded)
	if err != nil || isDisallowedRoot(abs) {
		return
	}
	for _, s := range result.Stores {
		if s.Root == abs || filepath.Join(s.Root, s.Profile) == abs {
			return
		}
	}
	browser := browserForRoot(abs)
	if browser == "" {
		return
	}
	if addedFromChildren := scanUserDataDir(abs, result); !addedFromChildren {
		store, skipReason := probeProfileDir(
			filepath.Dir(abs),
			filepath.Base(abs),
			abs,
			browser,
			false,
		)
		if skipReason != "" {
			result.Skipped = append(result.Skipped, SkippedStore{
				Root:    filepath.Dir(abs),
				Profile: filepath.Base(abs),
				Reason:  skipReason,
			})
		} else if store != nil {
			result.Stores = append(result.Stores, *store)
		}
	}
}

// scanUserDataDir scans root as a Chrome user-data-dir, looking for profile
// subdirectories (Default, Profile N, etc.). Returns true if any profiles
// were found (added or skipped), false if root doesn't look like a user-data-dir.
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

		if !profileNameAllowlist.MatchString(name) {
			continue
		}

		foundProfiles = true
		profileDir := filepath.Join(root, name)
		store, skipReason := probeProfileDir(root, name, profileDir, browser, name == "Default")
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

	return foundProfiles
}
