package chrome

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// browserTestBase returns the base config directory for browser tests.
// On macOS: ~/Library/Application Support
// On Linux: ~/.config
func browserTestBase(home string) string {
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config")
	}
	return filepath.Join(home, "Library", "Application Support")
}

// browserTestCookiesDir returns the expected cookies directory path for a browser.
// Handles the OS-specific path translation.
func browserTestCookiesDir(home string, macDirs []string, profile string) string {
	base := browserTestBase(home)
	if runtime.GOOS == "linux" {
		dirs := linuxSupportDir(macDirs)
		return filepath.Join(append(append([]string{base}, dirs...), profile, "Cookies")...)
	}
	return filepath.Join(append(append([]string{base}, macDirs...), profile, "Cookies")...)
}

func TestLookupBrowserDefaultsToChrome(t *testing.T) {
	b, err := LookupBrowser("")
	if err != nil {
		t.Fatalf("LookupBrowser(\"\"): %v", err)
	}
	if b.Name != "chrome" {
		t.Errorf("Name: got %q, want chrome", b.Name)
	}
	if b.KeychainAccount != "Chrome" || b.KeychainService != "Chrome Safe Storage" {
		t.Errorf("keychain: got account=%q service=%q", b.KeychainAccount, b.KeychainService)
	}
}

func TestLookupBrowserRejectsDisallowedForks(t *testing.T) {
	for _, name := range []string{"brave", "arc", "atlas", "chromium"} {
		_, err := LookupBrowser(name)
		if err == nil {
			t.Fatalf("LookupBrowser(%q): expected error", name)
		}
		if !strings.Contains(err.Error(), "admitted sources are chrome and edge only") {
			t.Errorf("LookupBrowser(%q) error = %v, want admitted-sources message", name, err)
		}
	}
}

func TestLookupBrowserUnknownListsSupportedNames(t *testing.T) {
	_, err := LookupBrowser("dia")
	if err == nil {
		t.Fatal("expected unsupported browser error")
	}
	if !strings.Contains(err.Error(), "supported:") || !strings.Contains(err.Error(), "chrome") {
		t.Errorf("error should list supported names, got %v", err)
	}
}

func TestLookupBrowserStandardForks(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		name       string
		cookiesDir []string
		account    string
		service    string
	}{
		{"edge", []string{"Microsoft Edge"}, "Microsoft Edge", "Microsoft Edge Safe Storage"},
	}
	for _, tc := range cases {
		b, err := LookupBrowser(tc.name)
		if err != nil {
			t.Fatalf("LookupBrowser(%s): %v", tc.name, err)
		}
		if b.KeychainAccount != tc.account || b.KeychainService != tc.service {
			t.Errorf("%s keychain: got account=%q service=%q", tc.name, b.KeychainAccount, b.KeychainService)
		}
		wantCookies := browserTestCookiesDir(home, tc.cookiesDir, "Default")
		if got := b.CookiesPath(""); got != wantCookies {
			t.Errorf("%s cookies path: got %q, want %q", tc.name, got, wantCookies)
		}
	}
}

func TestBrowserCookiesPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	base := browserTestBase(home)

	chromeBrowser, err := LookupBrowser("chrome")
	if err != nil {
		t.Fatal(err)
	}

	var chromePath, chromeProfileDir, chromeLocalStorage, chromeIndexedDB string
	if runtime.GOOS == "linux" {
		chromePath = filepath.Join(base, "google-chrome", "Default", "Cookies")
		chromeProfileDir = filepath.Join(base, "google-chrome", "Default")
		chromeLocalStorage = filepath.Join(base, "google-chrome", "Default", "Local Storage", "leveldb")
		chromeIndexedDB = filepath.Join(base, "google-chrome", "Default", "IndexedDB")
	} else {
		chromePath = filepath.Join(base, "Google", "Chrome", "Default", "Cookies")
		chromeProfileDir = filepath.Join(base, "Google", "Chrome", "Default")
		chromeLocalStorage = filepath.Join(base, "Google", "Chrome", "Default", "Local Storage", "leveldb")
		chromeIndexedDB = filepath.Join(base, "Google", "Chrome", "Default", "IndexedDB")
	}

	if got := chromeBrowser.CookiesPath(""); got != chromePath {
		t.Errorf("chrome default path: got %q, want %q", got, chromePath)
	}
	if got := chromeBrowser.ProfileDir(""); got != chromeProfileDir {
		t.Errorf("chrome default profile dir: got %q, want %q", got, chromeProfileDir)
	}
	if got := chromeBrowser.LocalStorageLevelDB(""); got != chromeLocalStorage {
		t.Errorf("chrome default local storage path: got %q, want %q", got, chromeLocalStorage)
	}
	if got := chromeBrowser.IndexedDBDir(""); got != chromeIndexedDB {
		t.Errorf("chrome default indexeddb path: got %q, want %q", got, chromeIndexedDB)
	}
}
