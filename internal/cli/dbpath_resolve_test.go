package cli

import (
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/config"
)

func mustDerive(t *testing.T, browser, profile string) string {
	t.Helper()
	p, err := config.SourceBrowserCookiesPath(browser, profile)
	if err != nil {
		t.Fatalf("derive %s/%s: %v", browser, profile, err)
	}
	return p
}

// resolveSourceDBPath must follow the effective source browser: an override
// that changes the browser re-derives the store path so the flag browser's
// key is never applied to the configured browser's cookie store.
func TestResolveSourceDBPath(t *testing.T) {
	edgeCfg := &config.SourceConfig{}
	edgeCfg.Browser.Name = "edge"
	edgeCfg.Browser.Profile = "Profile 2"
	edgeCfg.Chrome.DBPath = "/configured/edge/Cookies"
	chromeCfg := &config.SourceConfig{}
	chromeCfg.Browser.Name = "chrome"
	chromeCfg.Browser.Profile = "Default"
	chromeCfg.Chrome.DBPath = "/configured/chrome/Cookies"

	cases := []struct {
		name        string
		cfg         *config.SourceConfig
		flagBrowser string
		flagProfile string
		effective   string
		want        string
	}{
		{
			name: "no override keeps configured path",
			cfg:  edgeCfg, flagBrowser: "", flagProfile: "", effective: "edge",
			want: "/configured/edge/Cookies",
		},
		{
			name: "matching browser (case-insensitive) keeps configured path",
			cfg:  edgeCfg, flagBrowser: "Edge", flagProfile: "", effective: "edge",
			want: "/configured/edge/Cookies",
		},
		{
			name: "browser switch re-derives to enabled Chrome Default profile",
			cfg:  edgeCfg, flagBrowser: "chrome", flagProfile: "", effective: "chrome",
			want: mustDerive(t, "chrome", config.EnabledChromeProfile),
		},
		{
			name: "chrome to edge switch re-derives to enabled Edge profile",
			cfg:  chromeCfg, flagBrowser: "edge", flagProfile: "", effective: "edge",
			want: mustDerive(t, "edge", config.EnabledEdgeProfile),
		},
		{
			name: "explicit --profile re-derives even without a browser switch",
			cfg:  edgeCfg, flagBrowser: "", flagProfile: "Profile 5", effective: "edge",
			want: mustDerive(t, "edge", "Profile 5"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSourceDBPath(tc.cfg, tc.flagBrowser, tc.flagProfile, tc.effective)
			if err != nil {
				t.Fatalf("resolveSourceDBPath: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A browser switch with no config (empty Browser.Name) must still re-derive,
// not silently return the empty-config default as if it matched.
func TestResolveSourceDBPath_EmptyConfigBrowserSwitch(t *testing.T) {
	cfg := &config.SourceConfig{}
	cfg.Chrome.DBPath = config.DefaultChromeCookiesPath()
	got, err := resolveSourceDBPath(cfg, "edge", "", "edge")
	if err != nil {
		t.Fatalf("resolveSourceDBPath: %v", err)
	}
	if !strings.Contains(got, "Microsoft Edge") {
		t.Fatalf("expected Edge store path, got %q", got)
	}
}
