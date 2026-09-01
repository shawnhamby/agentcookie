package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
)

func TestAgentSyncSourcePinned(t *testing.T) {
	tests := []struct {
		name        string
		flagBrowser string
		flagProfile string
		want        bool
	}{
		{name: "default merged", flagBrowser: "", flagProfile: "", want: false},
		{name: "browser pin", flagBrowser: "edge", flagProfile: "", want: true},
		{name: "profile pin", flagBrowser: "", flagProfile: "Profile 2", want: true},
		{name: "both pin", flagBrowser: "chrome", flagProfile: "Default", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agentSyncSourcePinned(test.flagBrowser, test.flagProfile); got != test.want {
				t.Fatalf("agentSyncSourcePinned(%q, %q) = %v, want %v", test.flagBrowser, test.flagProfile, got, test.want)
			}
		})
	}
}

func TestReadAgentSyncCookies_MergedIncludesNonPrimaryProduct(t *testing.T) {
	key := []byte("0123456789abcdef")
	home := t.TempDir()
	t.Setenv("HOME", home)

	chromeRoot, edgeRoot := fakeBrowserRoots(t, home)
	chromePath := seedBrowserProfileCookies(t, chromeRoot, "Default", []chrome.Cookie{
		{Name: "sid", HostKey: ".example.com", Path: "/", Value: "from-chrome"},
	}, key)
	seedBrowserProfileCookies(t, edgeRoot, "Default", []chrome.Cookie{
		{Name: "edge_only", HostKey: ".example.com", Path: "/", Value: "edge-value"},
	}, key)

	paths, err := discoverAgentSyncWatchPaths([]string{"chrome", "edge"})
	if err != nil {
		t.Fatalf("discoverAgentSyncWatchPaths: %v", err)
	}
	if len(paths) < 2 {
		t.Fatalf("expected watch paths for chrome and edge, got %v", paths)
	}

	cfg := &config.SourceConfig{}
	cfg.Browser.Name = "chrome"
	cfg.Chrome.DBPath = chromePath
	pinned, _, err := readAgentSyncCookies(cfg, "chrome", "", nil, key, false, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("readAgentSyncCookies pinned: %v", err)
	}
	if slices.ContainsFunc(pinned, func(c chrome.Cookie) bool { return c.Name == "edge_only" }) {
		t.Fatal("single-store pin must not include cookies from other products")
	}
}

func TestReadMergedCookiesFromStores_FirstWins(t *testing.T) {
	key := []byte("0123456789abcdef")
	dir := t.TempDir()
	chromePath := filepath.Join(dir, "chrome", "Cookies")
	edgePath := filepath.Join(dir, "edge", "Cookies")
	if err := os.MkdirAll(filepath.Dir(chromePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(edgePath), 0o755); err != nil {
		t.Fatal(err)
	}
	seedSourceCookiesDB(t, chromePath, []chrome.Cookie{
		{Name: "sid", HostKey: ".example.com", Path: "/", Value: "from-chrome"},
	}, key)
	seedSourceCookiesDB(t, edgePath, []chrome.Cookie{
		{Name: "sid", HostKey: ".example.com", Path: "/", Value: "from-edge"},
		{Name: "edge_only", HostKey: ".example.com", Path: "/", Value: "edge-value"},
	}, key)

	stores := []chromepaths.Store{
		{Browser: "chrome", Profile: "Default", CookiesPath: chromePath},
		{Browser: "edge", Profile: "Default", CookiesPath: edgePath},
	}
	merged, _, err := readMergedCookiesFromStores(stores, nil, false, time.Now().UTC(), func(string) ([]byte, error) {
		return key, nil
	})
	if err != nil {
		t.Fatalf("readMergedCookiesFromStores: %v", err)
	}
	if !slices.ContainsFunc(merged, func(c chrome.Cookie) bool { return c.Name == "edge_only" }) {
		t.Fatalf("merged set missing edge-only cookie: %+v", merged)
	}
	if merged[0].Value != "from-chrome" {
		t.Fatalf("chrome should win conflict, got %q", merged[0].Value)
	}
}

func fakeBrowserRoots(t *testing.T, home string) (chromeRoot, edgeRoot string) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
		edgeRoot = filepath.Join(home, "Library", "Application Support", "Microsoft Edge")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
		edgeRoot = filepath.Join(home, ".config", "microsoft-edge")
	}
	for _, root := range []string{chromeRoot, edgeRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return chromeRoot, edgeRoot
}

func seedBrowserProfileCookies(t *testing.T, root, profile string, cookies []chrome.Cookie, key []byte) string {
	t.Helper()
	profileDir := filepath.Join(root, profile)
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profileDir, "Cookies")
	seedSourceCookiesDB(t, path, cookies, key)
	return path
}
