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
	const equal = int64(13350000000000000)

	source := []chrome.Cookie{
		{HostKey: ".example.com", Name: "a", Value: "src", Path: "/", ExpiresUTC: earlier},
		{HostKey: "example.com", Name: "b", Value: "src", Path: "/app", ExpiresUTC: equal},
		{HostKey: ".example.com", Name: "c", Value: "src", Path: "/", ExpiresUTC: later},
		{HostKey: ".example.com", Name: "csrf", Value: "src", Path: "/", ExpiresUTC: 0},
	}
	sink := []sinkCookie{
		{Name: "a", Domain: "example.com", Path: "/", ExpiresUTC: later},
		{Name: "b", Domain: ".example.com", Path: "/app", ExpiresUTC: equal},
		{Name: "c", Domain: ".example.com", Path: "/", ExpiresUTC: earlier},
		{Name: "csrf", Domain: ".example.com", Path: "/", ExpiresUTC: later},
	}

	got, skipped := filterDowngradeCookies(source, sink)
	if skipped != 1 {
		t.Fatalf("downgrade skipped = %d, want 1 (only cookie a)", skipped)
	}
	if len(got) != 3 {
		t.Fatalf("filtered len = %d, want 3", len(got))
	}
	names := map[string]bool{}
	for _, c := range got {
		names[c.Name] = true
	}
	for _, want := range []string{"b", "c", "csrf"} {
		if !names[want] {
			t.Errorf("expected cookie %q in filtered set", want)
		}
	}
	if names["a"] {
		t.Error("cookie a should be skipped (sink has later expiry)")
	}
}

func TestSinkHasLaterExpiry(t *testing.T) {
	const earlier = int64(13300000000000000)
	const later = int64(13400000000000000)

	if sinkHasLaterExpiry(
		chrome.Cookie{ExpiresUTC: earlier},
		sinkCookie{ExpiresUTC: later},
	) {
		// ok
	} else {
		t.Error("sink with later expiry should trigger skip")
	}
	if sinkHasLaterExpiry(
		chrome.Cookie{ExpiresUTC: later},
		sinkCookie{ExpiresUTC: earlier},
	) {
		t.Error("sink with earlier expiry should not trigger skip")
	}
	if sinkHasLaterExpiry(
		chrome.Cookie{ExpiresUTC: earlier},
		sinkCookie{ExpiresUTC: earlier},
	) {
		t.Error("equal expiry should not trigger skip")
	}
	if sinkHasLaterExpiry(
		chrome.Cookie{ExpiresUTC: 0},
		sinkCookie{ExpiresUTC: later},
	) {
		t.Error("session source should always inject")
	}
}

func TestCookieIdentityKeyDomainNormalization(t *testing.T) {
	k1 := cookieIdentityKey("n", ".Example.com", "/")
	k2 := cookieIdentityKey("N", "example.com", "/")
	if k1 != k2 {
		t.Fatalf("domain keys should match: %q vs %q", k1, k2)
	}
}
