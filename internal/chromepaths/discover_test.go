package chromepaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeProfile creates a minimal Chrome profile directory structure.
// If withCookies is true, creates an empty Cookies file.
// If networkLayout is true, puts Cookies in Network/Cookies.
func makeProfile(t *testing.T, root, profileName string, withCookies, networkLayout bool) string {
	t.Helper()
	profileDir := filepath.Join(root, profileName)
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile %s: %v", profileDir, err)
	}

	if withCookies {
		var cookiesPath string
		if networkLayout {
			networkDir := filepath.Join(profileDir, "Network")
			if err := os.MkdirAll(networkDir, 0o755); err != nil {
				t.Fatalf("mkdir network: %v", err)
			}
			cookiesPath = filepath.Join(networkDir, "Cookies")
		} else {
			cookiesPath = filepath.Join(profileDir, "Cookies")
		}
		if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
			t.Fatalf("write cookies: %v", err)
		}
	}

	return profileDir
}

func TestDiscover_DefaultWithCookies(t *testing.T) {
	// Create a fake Chrome root with a Default profile that has Cookies.
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProfile(t, chromeRoot, "Default", true, false)

	result := Discover()

	if len(result.Stores) == 0 {
		t.Fatal("expected at least one store, got none")
	}

	found := false
	for _, s := range result.Stores {
		if s.Profile == "Default" && s.IsDefault && s.Browser == "chrome" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile not found or not marked as default")
	}
}

func TestDiscover_NoCookiesFile_Skipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Default profile without Cookies file.
	makeProfile(t, chromeRoot, "Default", false, false)

	result := Discover()

	if len(result.Stores) != 0 {
		t.Errorf("expected no stores without Cookies file, got %d", len(result.Stores))
	}

	// Should be in Skipped list.
	found := false
	for _, s := range result.Skipped {
		if s.Profile == "Default" && s.Reason == "no Cookies file" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile should be in Skipped list with 'no Cookies file' reason")
	}
}

func TestDiscover_NetworkCookiesLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Default profile with Network/Cookies layout.
	makeProfile(t, chromeRoot, "Default", true, true)

	result := Discover()

	if len(result.Stores) == 0 {
		t.Fatal("expected at least one store with Network/Cookies, got none")
	}

	found := false
	for _, s := range result.Stores {
		if s.Profile == "Default" {
			if filepath.Base(filepath.Dir(s.CookiesPath)) != "Network" {
				t.Errorf("expected Network/Cookies path, got %s", s.CookiesPath)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile not found")
	}
}

func TestDiscover_MultipleProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create multiple profiles.
	makeProfile(t, chromeRoot, "Default", true, false)
	makeProfile(t, chromeRoot, "Profile 1", true, true) // Network/Cookies
	makeProfile(t, chromeRoot, "Profile 2", true, false)
	makeProfile(t, chromeRoot, "Guest Profile", true, false)

	result := Discover()

	if len(result.Stores) != 3 {
		t.Errorf("expected 3 user-profile stores, got %d", len(result.Stores))
	}

	profiles := make(map[string]bool)
	for _, s := range result.Stores {
		profiles[s.Profile] = true
		if s.Browser != "chrome" {
			t.Errorf("unexpected browser on store: %+v", s)
		}
		if s.Profile == "Guest Profile" || s.Profile == "System Profile" {
			t.Errorf("Guest/System must never be a store: %+v", s)
		}
	}
	for _, want := range []string{"Default", "Profile 1", "Profile 2"} {
		if !profiles[want] {
			t.Errorf("missing user profile store %s", want)
		}
	}
	if profiles["Guest Profile"] {
		t.Error("Guest Profile must not be a store")
	}
	for _, s := range result.Skipped {
		if !isEnabledStore("chrome", s.Profile, DefaultEnabledProducts) {
			t.Errorf("non-enabled profile %q must not appear in Skipped", s.Profile)
		}
	}
}

func TestDiscover_SkipsCacheDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Crashpad and ShaderCache dirs (should be skipped).
	makeProfile(t, chromeRoot, "Crashpad", true, false)
	makeProfile(t, chromeRoot, "ShaderCache", true, false)
	makeProfile(t, chromeRoot, "GPUCache", true, false)
	// Also create a valid profile.
	makeProfile(t, chromeRoot, "Default", true, false)

	result := Discover()

	// Should only find Default.
	if len(result.Stores) != 1 {
		t.Errorf("expected 1 store (only Default), got %d", len(result.Stores))
	}
	if len(result.Stores) > 0 && result.Stores[0].Profile != "Default" {
		t.Errorf("expected Default profile, got %s", result.Stores[0].Profile)
	}

	// Cache dirs should not be in Skipped list either (they're just ignored).
	for _, s := range result.Skipped {
		if s.Profile == "Crashpad" || s.Profile == "ShaderCache" || s.Profile == "GPUCache" {
			t.Errorf("cache dir %s should not be in Skipped list", s.Profile)
		}
	}
}

func TestDiscover_AgentChromeProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create agent chrome-profile directory (the directory itself is the profile).
	chromeProfile := filepath.Join(home, "chrome-profile")
	if err := os.MkdirAll(chromeProfile, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(chromeProfile, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := Discover()

	found := false
	for _, s := range result.Stores {
		if s.Profile == "chrome-profile" {
			found = true
			break
		}
	}
	if found {
		t.Error("~/chrome-profile is not an enabled source")
	}
}

func TestDiscover_AgentCookieChromeProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create ~/.agentcookie/chrome-profile directory.
	chromeProfile := filepath.Join(home, ".agentcookie", "chrome-profile")
	if err := os.MkdirAll(chromeProfile, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(chromeProfile, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := Discover()

	found := false
	for _, s := range result.Stores {
		if s.Profile == "chrome-profile" {
			found = true
			break
		}
	}
	if found {
		t.Error("~/.agentcookie/chrome-profile is not an enabled source")
	}
}

func TestDiscover_ChromeUserDataDirEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a custom user-data-dir.
	customRoot := filepath.Join(t.TempDir(), "my-custom-chrome")
	makeProfile(t, customRoot, "Default", true, false)

	t.Setenv("CHROME_USER_DATA_DIR", customRoot)

	result := Discover()

	for _, s := range result.Stores {
		if s.Root == customRoot {
			t.Fatalf("CHROME_USER_DATA_DIR is not an enabled source: %+v", s)
		}
	}
}

func TestDiscover_NoLocalStateRequired(t *testing.T) {
	// AE1: Fixture root with Default/Cookies and no Local State is discovered.
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Default profile with Cookies but NO Local State.
	makeProfile(t, chromeRoot, "Default", true, false)

	// Verify no Local State exists.
	localStatePath := filepath.Join(chromeRoot, "Local State")
	if _, err := os.Stat(localStatePath); err == nil {
		t.Fatal("Local State should not exist for this test")
	}

	result := Discover()

	if len(result.Stores) == 0 {
		t.Fatal("expected store to be discovered without Local State")
	}

	found := false
	for _, s := range result.Stores {
		if s.Profile == "Default" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile should be discovered even without Local State")
	}
}

func TestDiscoverForConfig_RejectsBareNonEnabledProfileDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create an explicit profile dir not in the standard locations.
	explicitDir := filepath.Join(t.TempDir(), "explicit-profile")
	if err := os.MkdirAll(explicitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(explicitDir, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := DiscoverForConfig(explicitDir)

	found := false
	for _, s := range result.Stores {
		if s.CookiesPath == cookiesPath {
			found = true
			break
		}
	}
	if found {
		t.Error("bare non-enabled profile dir must not become a store")
	}
}

func TestIsEnabledStore(t *testing.T) {
	enabled := DefaultEnabledProducts
	cases := []struct {
		browser, profile string
		want             bool
	}{
		{"chrome", "Default", true},
		{"edge", "Profile 2", true},
		{"chrome", "Profile 2", true},
		{"edge", "Default", true},
		{"edge", "Guest Profile", false},
		{"chrome", "Guest Profile", false},
		{"chrome", "System Profile", false},
		{"edge", "System Profile", false},
		{"brave", "Default", false},
		{"chrome", "chrome-profile", false},
	}
	for _, tc := range cases {
		got := isEnabledStore(tc.browser, tc.profile, enabled)
		if got != tc.want {
			t.Errorf("isEnabledStore(%q, %q) = %v, want %v", tc.browser, tc.profile, got, tc.want)
		}
	}
	// Product list can exclude chrome entirely.
	if isEnabledStore("chrome", "Default", []string{"edge"}) {
		t.Error("chrome must not be enabled when products are edge-only")
	}
}

func TestDiscoverForConfig_ExplicitPathExcludesAutomaticProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	standardRoot := filepath.Join(home, ".config", "google-chrome")
	if runtime.GOOS == "darwin" {
		standardRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	}
	makeProfile(t, standardRoot, "Profile 2", true, false)

	explicitDir := filepath.Join(t.TempDir(), "explicit-profile")
	makeProfile(t, explicitDir, "Default", true, false)
	result := DiscoverForConfig(explicitDir)

	for _, store := range result.Stores {
		if store.Root == standardRoot {
			t.Fatalf("explicit discovery included automatic profile: %+v", store)
		}
	}
}

// TestDiscoverForConfig_ScansUserDataDir verifies that when cdp.profile_dir
// is a Chrome user-data-dir (containing Default/Profile N), DiscoverForConfig
// scans those children the same way Discover scans standard roots.
func TestDiscoverForConfig_ScansUserDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var userDataDir string
	if runtime.GOOS == "darwin" {
		userDataDir = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		userDataDir = filepath.Join(home, ".config", "google-chrome")
	}

	defaultDir := filepath.Join(userDataDir, "Default")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defaultCookies := filepath.Join(defaultDir, "Cookies")
	if err := os.WriteFile(defaultCookies, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	profile1Dir := filepath.Join(userDataDir, "Profile 1")
	networkDir := filepath.Join(profile1Dir, "Network")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(networkDir, "Cookies"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(userDataDir, "Cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result := DiscoverForConfig(userDataDir)

	foundDefault := false
	foundProfile1 := false
	for _, s := range result.Stores {
		if s.Root == userDataDir && s.Profile == "Default" && s.CookiesPath == defaultCookies {
			foundDefault = true
			if !s.IsDefault {
				t.Error("Default profile should have IsDefault=true")
			}
		}
		if s.Profile == "Profile 1" {
			foundProfile1 = true
		}
	}
	if !foundDefault {
		t.Error("Default profile should be discovered from enabled Chrome user-data-dir")
	}
	if !foundProfile1 {
		t.Error("Profile 1 user profile should be discovered from enabled Chrome user-data-dir")
	}
}

// TestDiscoverForConfig_ExpandsTilde verifies that DiscoverForConfig expands
// ~ to the home directory. filepath.Abs treats ~ as a literal path component,
// so we must expand it first.
func TestDiscoverForConfig_ExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot, tildePath string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
		tildePath = "~/Library/Application Support/Google/Chrome"
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
		tildePath = "~/.config/google-chrome"
	}
	defaultDir := filepath.Join(chromeRoot, "Default")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(defaultDir, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := DiscoverForConfig(tildePath)

	found := false
	for _, s := range result.Stores {
		if s.Root == chromeRoot && s.Profile == "Default" && s.CookiesPath == cookiesPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DiscoverForConfig(%q) should find enabled Chrome Default under home", tildePath)
	}
}

func TestProfileDirName(t *testing.T) {
	cases := []struct {
		name    string
		matches bool
	}{
		{"Default", true},
		{"Profile 1", true},
		{"Profile 2", true},
		{"Profile 10", true},
		{"Profile 123", true},
		{"Guest Profile", true},
		{"System Profile", true},
		{"Profile", false},        // Missing number
		{"Profile1", false},       // No space
		{"Profile X", false},      // Non-numeric
		{"Crashpad", false},       // Cache dir
		{"random-dir", false},     // Random
		{"Default ", false},       // Trailing space
		{" Default", false},       // Leading space
		{"Default123", false},     // Invalid
		{"MyProfile 1", false},    // Invalid prefix
		{"Guest", false},          // Incomplete
		{"System", false},         // Incomplete
		{"Guest Profile ", false}, // Trailing space
	}

	for _, tc := range cases {
		got := profileDirName.MatchString(tc.name)
		if got != tc.matches {
			t.Errorf("profileDirName.MatchString(%q) = %v, want %v", tc.name, got, tc.matches)
		}
	}
}

func TestBrowserForRoot(t *testing.T) {
	cases := []struct {
		root string
		want string
	}{
		{"/Users/me/Library/Application Support/Google/Chrome", "chrome"},
		{"/Users/me/Library/Application Support/Chromium", ""},
		{"/Users/me/Library/Application Support/BraveSoftware/Brave-Browser", ""},
		{"/Users/me/Library/Application Support/Microsoft Edge", "edge"},
		{"/Users/me/Library/Application Support/Arc/User Data", ""},
		{"/home/me/.config/google-chrome", "chrome"},
		{"/home/me/.config/chromium", ""},
		{"/home/me/.config/BraveSoftware/Brave-Browser", ""},
		{"/home/me/.config/microsoft-edge", "edge"},
		{"/home/me/chrome-profile", ""},
		{"/some/random/path", ""},
	}

	for _, tc := range cases {
		got := browserForRoot(tc.root)
		if got != tc.want {
			t.Errorf("browserForRoot(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}
}

func TestDiscover_DoesNotScanBraveOrChromiumRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var appSupport string
	if runtime.GOOS == "darwin" {
		appSupport = filepath.Join(home, "Library", "Application Support")
	} else {
		appSupport = filepath.Join(home, ".config")
	}

	braveRoot := filepath.Join(appSupport, "BraveSoftware", "Brave-Browser")
	if runtime.GOOS == "linux" {
		braveRoot = filepath.Join(appSupport, "BraveSoftware", "Brave-Browser")
	}
	chromiumRoot := filepath.Join(appSupport, "Chromium")
	if runtime.GOOS == "linux" {
		chromiumRoot = filepath.Join(appSupport, "chromium")
	}

	for _, root := range []string{braveRoot, chromiumRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		makeProfile(t, root, "Default", true, false)
	}

	result := Discover()
	for _, s := range result.Stores {
		if strings.Contains(strings.ToLower(s.Root), "brave") || strings.Contains(strings.ToLower(s.Root), "chromium") {
			t.Fatalf("Discover returned non-enabled root: %+v", s)
		}
	}
}

func TestDiscoverForSource_HonorsBrowserScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot, edgeRoot string
	if runtime.GOOS == "darwin" {
		appSupport := filepath.Join(home, "Library", "Application Support")
		chromeRoot = filepath.Join(appSupport, "Google", "Chrome")
		edgeRoot = filepath.Join(appSupport, "Microsoft Edge")
	} else {
		configDir := filepath.Join(home, ".config")
		chromeRoot = filepath.Join(configDir, "google-chrome")
		edgeRoot = filepath.Join(configDir, "microsoft-edge")
	}

	for _, root := range []string{chromeRoot, edgeRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	makeProfile(t, chromeRoot, "Default", true, false)
	makeProfile(t, edgeRoot, "Profile 2", true, false)
	makeProfile(t, edgeRoot, "Guest Profile", true, false)

	edgeOnly := DiscoverForSource("", "edge")
	for _, s := range edgeOnly.Stores {
		if s.Browser != "edge" {
			t.Fatalf("edge-scoped discovery included %q store: %+v", s.Browser, s)
		}
	}
	if len(edgeOnly.Stores) != 1 || edgeOnly.Stores[0].Profile != "Profile 2" {
		t.Fatalf("expected only edge/Profile 2 user profile, got %+v", edgeOnly.Stores)
	}

	chromeOnly := DiscoverForSource("", "chrome")
	for _, s := range chromeOnly.Stores {
		if s.Browser != "chrome" {
			t.Fatalf("chrome-scoped discovery included %q store: %+v", s.Browser, s)
		}
	}
	if len(chromeOnly.Stores) == 0 {
		t.Fatal("expected chrome store when scoped to chrome")
	}

	edgeOnlyProducts := DiscoverForSourceWithProducts("", "", []string{"edge"})
	for _, s := range edgeOnlyProducts.Stores {
		if s.Browser != "edge" {
			t.Fatalf("edge-only products included %q: %+v", s.Browser, s)
		}
	}
}
