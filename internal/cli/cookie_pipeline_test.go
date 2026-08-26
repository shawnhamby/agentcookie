package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
)

func TestReadFilteredCookies(t *testing.T) {
	key := []byte("0123456789abcdef")
	dbPath := filepath.Join(t.TempDir(), "Cookies")
	seedSourceCookiesDB(t, dbPath, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "b", Value: "1", Path: "/"},
		{HostKey: ".allowed.com", Name: "a", Value: "2", Path: "/"},
	}, key)

	t.Run("nil blocklist passes everything", func(t *testing.T) {
		cookies, st, err := readFilteredCookies(dbPath, nil, key, false, time.Now().UTC())
		if err != nil {
			t.Fatalf("readFilteredCookies: %v", err)
		}
		if st.totalRead != 2 || len(cookies) != 2 {
			t.Errorf("got totalRead=%d passing=%d, want 2/2", st.totalRead, len(cookies))
		}
		if st.totalDropped != 0 {
			t.Errorf("totalDropped=%d, want 0", st.totalDropped)
		}
	})

	t.Run("blocklist drops the matching host only", func(t *testing.T) {
		bl := &config.Blocklist{Version: 1, Domains: []config.BlocklistEntry{{Pattern: "%.blocked.com"}}}
		cookies, st, err := readFilteredCookies(dbPath, bl, key, false, time.Now().UTC())
		if err != nil {
			t.Fatalf("readFilteredCookies: %v", err)
		}
		if len(cookies) != 1 || cookies[0].HostKey != ".allowed.com" {
			t.Fatalf("expected only .allowed.com to pass, got %+v", cookies)
		}
		if st.totalDropped != 1 {
			t.Errorf("totalDropped=%d, want 1", st.totalDropped)
		}
	})

	t.Run("allowlist passes the matching host only", func(t *testing.T) {
		bl := &config.Blocklist{
			Version: 1,
			Policy:  config.CookiePolicyAllowlist,
			Domains: []config.BlocklistEntry{{Pattern: "%.allowed.com"}},
		}
		cookies, st, err := readFilteredCookies(dbPath, bl, key, false, time.Now().UTC())
		if err != nil {
			t.Fatalf("readFilteredCookies: %v", err)
		}
		if len(cookies) != 1 || cookies[0].HostKey != ".allowed.com" {
			t.Fatalf("expected only .allowed.com to pass, got %+v", cookies)
		}
		if st.totalDropped != 1 {
			t.Errorf("totalDropped=%d, want 1", st.totalDropped)
		}
	})
}

func TestExportPipelineAllowlist(t *testing.T) {
	key := []byte("0123456789abcdef")
	dbPath := filepath.Join(t.TempDir(), "Cookies")
	seedSourceCookiesDB(t, dbPath, []chrome.Cookie{
		{HostKey: ".allowed.com", Name: "allowed", Value: "1", Path: "/"},
		{HostKey: ".other.com", Name: "other", Value: "2", Path: "/"},
	}, key)

	tests := []struct {
		name    string
		domains []config.BlocklistEntry
		want    int
	}{
		{name: "empty allowlist fails closed", want: 0},
		{name: "only allowlisted host exports", domains: []config.BlocklistEntry{{Pattern: "%.allowed.com"}}, want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := &config.Blocklist{Version: 1, Policy: config.CookiePolicyAllowlist, Domains: test.domains}
			cookies, _, err := readFilteredCookies(dbPath, policy, key, false, time.Now().UTC())
			if err != nil {
				t.Fatalf("readFilteredCookies: %v", err)
			}
			if got := len(toExportCookies(cookies)); got != test.want {
				t.Fatalf("exported cookies = %d, want %d", got, test.want)
			}
		})
	}
}
