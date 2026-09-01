package cli

import (
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
)

func TestAppendCookieMerge_FirstWins(t *testing.T) {
	chromeCookie := chrome.Cookie{Name: "sid", HostKey: ".example.com", Path: "/", Value: "from-chrome"}
	edgeCookie := chrome.Cookie{Name: "sid", HostKey: ".example.com", Path: "/", Value: "from-edge"}
	edgeOnly := chrome.Cookie{Name: "edge_only", HostKey: ".example.com", Path: "/", Value: "e"}

	merged := appendCookieMerge(nil, []chrome.Cookie{chromeCookie})
	merged = appendCookieMerge(merged, []chrome.Cookie{edgeCookie, edgeOnly})
	if len(merged) != 2 {
		t.Fatalf("merged len = %d, want 2", len(merged))
	}
	if merged[0].Value != "from-chrome" {
		t.Fatalf("chrome should win conflict, got %q", merged[0].Value)
	}
	if merged[1].Name != "edge_only" {
		t.Fatalf("expected edge-only cookie retained, got %+v", merged[1])
	}
}

func TestSortStoresForMerge_ProductPrecedence(t *testing.T) {
	stores := []chromepaths.Store{
		{Browser: "edge", Profile: "Default", CookiesPath: "/e/Default"},
		{Browser: "chrome", Profile: "Profile 1", CookiesPath: "/c/P1"},
		{Browser: "chrome", Profile: "Default", CookiesPath: "/c/Default", IsDefault: true},
		{Browser: "edge", Profile: "Profile 2", CookiesPath: "/e/P2"},
	}
	sorted := sortStoresForMerge(stores, []string{"chrome", "edge"})
	want := []string{
		"chrome/Default",
		"chrome/Profile 1",
		"edge/Default",
		"edge/Profile 2",
	}
	if len(sorted) != len(want) {
		t.Fatalf("len = %d, want %d", len(sorted), len(want))
	}
	for i, w := range want {
		got := sorted[i].Browser + "/" + sorted[i].Profile
		if got != w {
			t.Fatalf("index %d: got %s, want %s", i, got, w)
		}
	}
}
