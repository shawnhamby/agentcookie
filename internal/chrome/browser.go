package chrome

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	defaultBrowserName    = "chrome"
	defaultBrowserProfile = "Default"
)

// Browser describes the Chromium-family source browser surfaces that vary by
// fork: the on-disk profile root and the Safe Storage keychain item.
type Browser struct {
	Name            string
	SupportDir      []string
	KeychainAccount string
	KeychainService string
}

var browserRegistry = map[string]Browser{
	defaultBrowserName: {
		Name:            defaultBrowserName,
		SupportDir:      []string{"Google", "Chrome"},
		KeychainAccount: keychainAccount,
		KeychainService: keychainService,
	},
	"edge": {
		Name:            "edge",
		SupportDir:      []string{"Microsoft Edge"},
		KeychainAccount: "Microsoft Edge",
		KeychainService: "Microsoft Edge Safe Storage",
	},
}

// LookupBrowser returns the browser descriptor for name. Empty name defaults
// to Chrome for backward compatibility. Only enabled sources (chrome, edge)
// resolve; everything else fails closed before any Keychain read.
func LookupBrowser(name string) (Browser, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = defaultBrowserName
	}
	b, ok := browserRegistry[key]
	if !ok {
		return Browser{}, fmt.Errorf("browser %q is not supported: enabled sources are chrome and edge only", name)
	}
	b.SupportDir = append([]string(nil), b.SupportDir...)
	return b, nil
}

// SupportedBrowserNames returns the configured source-browser adapter names.
func SupportedBrowserNames() []string {
	names := make([]string, 0, len(browserRegistry))
	for name := range browserRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ProfileDir returns this browser's profile directory. Empty profile defaults
// to "Default".
// On macOS: ~/Library/Application Support/<browser>/<profile>
// On Linux: ~/.config/<browser>/<profile>
func (b Browser) ProfileDir(profile string) string {
	if profile == "" {
		profile = defaultBrowserProfile
	}
	home, _ := os.UserHomeDir()
	var parts []string
	if isLinux() {
		parts = []string{home, ".config"}
		parts = append(parts, linuxSupportDir(b.SupportDir)...)
	} else {
		parts = []string{home, "Library", "Application Support"}
		parts = append(parts, b.SupportDir...)
	}
	parts = append(parts, profile)
	return filepath.Join(parts...)
}

// isLinux reports whether the current OS is Linux.
func isLinux() bool {
	return os.Getenv("GOOS") == "linux" || (os.Getenv("GOOS") == "" && runtime.GOOS == "linux")
}

// linuxSupportDir converts macOS Application Support subdirectory names to
// their Linux ~/.config equivalents.
func linuxSupportDir(macDirs []string) []string {
	if len(macDirs) == 0 {
		return macDirs
	}
	switch macDirs[0] {
	case "Google":
		if len(macDirs) >= 2 && macDirs[1] == "Chrome" {
			return []string{"google-chrome"}
		}
		return macDirs
	case "Chromium":
		return []string{"chromium"}
	case "Microsoft Edge":
		return []string{"microsoft-edge"}
	default:
		return macDirs
	}
}

// CookiesPath returns this browser's Cookies SQLite path for profile. Empty
// profile defaults to "Default".
func (b Browser) CookiesPath(profile string) string {
	return filepath.Join(b.ProfileDir(profile), "Cookies")
}

// LocalStorageLevelDB returns this browser's Local Storage LevelDB directory
// for profile. Empty profile defaults to "Default".
func (b Browser) LocalStorageLevelDB(profile string) string {
	return filepath.Join(b.ProfileDir(profile), "Local Storage", "leveldb")
}

// IndexedDBDir returns this browser's IndexedDB directory for profile. Empty
// profile defaults to "Default".
func (b Browser) IndexedDBDir(profile string) string {
	return filepath.Join(b.ProfileDir(profile), "IndexedDB")
}
