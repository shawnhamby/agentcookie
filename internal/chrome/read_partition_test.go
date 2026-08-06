package chrome

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestReadCookiesForHost_PartitionKey is the CHIPS regression case: Chrome's
// top_frame_site_key and has_cross_site_ancestor columns must surface on the
// returned Cookie so downstream injection can reproduce the partition key.
// Before this fix, ReadCookiesForHost's SELECT omitted both columns and
// every partitioned cookie (e.g. Cloudflare's cf_clearance) silently lost
// its partition on read.
func TestReadCookiesForHost_PartitionKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	enc, err := encryptValueBytes([]byte("chips-value"), testKey)
	if err != nil {
		t.Fatalf("encryptValueBytes: %v", err)
	}

	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO cookies (creation_utc, host_key, top_frame_site_key, name, value, encrypted_value, path, expires_utc, is_secure, is_httponly, last_access_utc, has_expires, is_persistent, priority, samesite, source_scheme, source_port, last_update_utc, source_type, has_cross_site_ancestor) VALUES (0, ?, ?, 'cf_clearance', '', ?, '/', 0, 1, 0, 0, 0, 0, 1, 0, 2, 443, 0, 0, ?)`,
		".chatgpt.com", "https://chatgpt.com", enc, 1); err != nil {
		t.Fatalf("insert partitioned cookie: %v", err)
	}

	got, err := ReadCookiesForHost(path, "%chatgpt.com", testKey)
	if err != nil {
		t.Fatalf("ReadCookiesForHost: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d cookies, want 1", len(got))
	}
	c := got[0]
	if c.TopFrameSiteKey != "https://chatgpt.com" {
		t.Errorf("TopFrameSiteKey: got %q, want %q", c.TopFrameSiteKey, "https://chatgpt.com")
	}
	if c.HasCrossSiteAncestor != 1 {
		t.Errorf("HasCrossSiteAncestor: got %d, want 1", c.HasCrossSiteAncestor)
	}
}

// TestReadCookiesForHost_UnpartitionedLeavesKeyEmpty confirms an ordinary
// cookie (empty top_frame_site_key, the common case) reads back with an
// empty TopFrameSiteKey so downstream injection treats it as unpartitioned.
func TestReadCookiesForHost_UnpartitionedLeavesKeyEmpty(t *testing.T) {
	path := seedCookies(t, 1, testKey)
	got, err := ReadCookiesForHost(path, "%example.com", testKey)
	if err != nil {
		t.Fatalf("ReadCookiesForHost: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d cookies, want 1", len(got))
	}
	if got[0].TopFrameSiteKey != "" {
		t.Errorf("TopFrameSiteKey: got %q, want empty", got[0].TopFrameSiteKey)
	}
}

func TestReadCookiesForHost_LegacySchemaWithoutCrossSiteAncestor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE cookies(
	host_key TEXT NOT NULL,
	top_frame_site_key TEXT NOT NULL,
	name TEXT NOT NULL,
	encrypted_value BLOB NOT NULL,
	path TEXT NOT NULL,
	expires_utc INTEGER NOT NULL,
	is_secure INTEGER NOT NULL,
	is_httponly INTEGER NOT NULL,
	last_access_utc INTEGER NOT NULL,
	has_expires INTEGER NOT NULL,
	is_persistent INTEGER NOT NULL,
	priority INTEGER NOT NULL,
	samesite INTEGER NOT NULL,
	source_scheme INTEGER NOT NULL,
	source_port INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	enc, err := encryptValueBytes([]byte("legacy-value"), testKey)
	if err != nil {
		t.Fatalf("encryptValueBytes: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cookies VALUES (?, ?, ?, ?, '/', 0, 1, 0, 0, 0, 0, 1, 0, 2, 443)`,
		".example.com", "https://top-level.example", "legacy", enc); err != nil {
		t.Fatalf("insert legacy cookie: %v", err)
	}

	got, err := ReadCookiesForHost(path, "%example.com", testKey)
	if err != nil {
		t.Fatalf("ReadCookiesForHost: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d cookies, want 1", len(got))
	}
	if got[0].TopFrameSiteKey != "https://top-level.example" {
		t.Errorf("TopFrameSiteKey: got %q, want %q", got[0].TopFrameSiteKey, "https://top-level.example")
	}
	if got[0].HasCrossSiteAncestor != 0 {
		t.Errorf("HasCrossSiteAncestor: got %d, want zero-value fallback", got[0].HasCrossSiteAncestor)
	}
}
