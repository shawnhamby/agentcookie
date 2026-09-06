package livecdp

import (
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestIsClearanceCookie(t *testing.T) {
	for _, name := range ClearanceCookieNames {
		if !IsClearanceCookie(name) {
			t.Errorf("IsClearanceCookie(%q) = false, want true", name)
		}
		if !IsClearanceCookie(strings.ToUpper(name)) {
			t.Errorf("IsClearanceCookie(%q) case-insensitive = false, want true", strings.ToUpper(name))
		}
	}
	if IsClearanceCookie("user_session") {
		t.Error("ordinary auth cookie must not be clearance")
	}
	if IsClearanceCookie("") {
		t.Error("empty name must not be clearance")
	}
}

func TestFilterClearanceCookies(t *testing.T) {
	in := []chrome.Cookie{
		{HostKey: ".openevidence.com", Name: "datadome", Value: "stale", Path: "/"},
		{HostKey: ".openevidence.com", Name: "session", Value: "ok", Path: "/"},
		{HostKey: ".example.com", Name: "CF_CLEARANCE", Value: "x", Path: "/"},
	}
	got, skipped := filterClearanceCookies(in)
	if skipped != 2 {
		t.Fatalf("clearance skipped = %d, want 2", skipped)
	}
	if len(got) != 1 || got[0].Name != "session" {
		t.Fatalf("filtered = %+v, want only session", got)
	}
}

func TestFilterDowngradeCookies(t *testing.T) {
	const earlier = int64(13300000000000000)
	const later = int64(13400000000000000)

	tests := []struct {
		name        string
		source      chrome.Cookie
		sink        []sinkCookie
		wantSkipped int
	}{
		{
			name:        "session source versus sink session",
			source:      chrome.Cookie{HostKey: ".example.com", Name: "auth", Path: "/"},
			sink:        []sinkCookie{{Name: "auth", Domain: "example.com", Path: "/"}},
			wantSkipped: 1,
		},
		{
			name:        "session source versus sink persistent",
			source:      chrome.Cookie{HostKey: ".example.com", Name: "auth", Path: "/"},
			sink:        []sinkCookie{{Name: "auth", Domain: "example.com", Path: "/", ExpiresUTC: later}},
			wantSkipped: 1,
		},
		{
			name:        "persistent source versus older sink",
			source:      chrome.Cookie{HostKey: ".example.com", Name: "auth", Path: "/", ExpiresUTC: later},
			sink:        []sinkCookie{{Name: "auth", Domain: "example.com", Path: "/", ExpiresUTC: earlier}},
			wantSkipped: 0,
		},
		{
			name:        "persistent source versus newer sink",
			source:      chrome.Cookie{HostKey: ".example.com", Name: "auth", Path: "/", ExpiresUTC: earlier},
			sink:        []sinkCookie{{Name: "auth", Domain: "example.com", Path: "/", ExpiresUTC: later}},
			wantSkipped: 1,
		},
		{
			name:        "no sink match",
			source:      chrome.Cookie{HostKey: ".example.com", Name: "auth", Path: "/", ExpiresUTC: earlier},
			sink:        []sinkCookie{{Name: "other", Domain: "example.com", Path: "/", ExpiresUTC: later}},
			wantSkipped: 0,
		},
		{
			name:        "persistent source versus equal sink",
			source:      chrome.Cookie{HostKey: ".example.com", Name: "auth", Path: "/", ExpiresUTC: earlier},
			sink:        []sinkCookie{{Name: "auth", Domain: "example.com", Path: "/", ExpiresUTC: earlier}},
			wantSkipped: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, skipped := filterDowngradeCookies([]chrome.Cookie{tt.source}, tt.sink)
			if skipped != tt.wantSkipped {
				t.Fatalf("downgrade skipped = %d, want %d", skipped, tt.wantSkipped)
			}
			if len(got) != 1-tt.wantSkipped {
				t.Fatalf("filtered len = %d, want %d", len(got), 1-tt.wantSkipped)
			}
		})
	}
}

func TestCookieIdentityKeyDomainNormalization(t *testing.T) {
	k1 := cookieIdentityKey("n", ".Example.com", "/")
	k2 := cookieIdentityKey("N", "example.com", "/")
	if k1 != k2 {
		t.Fatalf("domain keys should match: %q vs %q", k1, k2)
	}
}
