package chrome

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// wrongKey differs from testKey; cookies written under testKey must not
// decrypt cleanly under it.
var wrongKey = []byte("fedcba9876543210")

func seedCookies(t *testing.T, n int, key []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)
	cookies := make([]Cookie, n)
	for i := range cookies {
		cookies[i] = Cookie{
			HostKey: ".example.com", Name: "c" + string(rune('a'+i)),
			Value: "value-for-cookie-number-" + string(rune('a'+i)), Path: "/", IsSecure: 1,
		}
	}
	if _, err := WriteCookies(path, cookies, key); err != nil {
		t.Fatalf("WriteCookies: %v", err)
	}
	return path
}

// A read with the correct key returns every cookie.
func TestReadCookies_CorrectKey(t *testing.T) {
	path := seedCookies(t, 12, testKey)
	got, err := ReadCookiesForHost(path, "%example.com", testKey)
	if err != nil {
		t.Fatalf("ReadCookiesForHost: %v", err)
	}
	if len(got) != 12 {
		t.Fatalf("got %d cookies, want 12", len(got))
	}
}

// A wrong key must fail the whole read (key/store mismatch), not silently
// return the small fraction of cookies that survive strict PKCS#7 by chance.
func TestReadCookies_WrongKeyFailsLoudly(t *testing.T) {
	path := seedCookies(t, 40, testKey)
	_, err := ReadCookiesForHost(path, "%example.com", wrongKey)
	if err == nil {
		t.Fatal("expected wrong-key read to error, got nil")
	}
	if !strings.Contains(err.Error(), "failed decrypt") {
		t.Fatalf("expected key-mismatch error, got: %v", err)
	}
}

// The failure-ratio guard trips at exactly the 50% boundary (ties fail
// closed): 5 undecryptable of 10 must error rather than return the 5 good ones.
func TestReadCookies_HalfFailedTripsGuard(t *testing.T) {
	path := seedCookies(t, 10, testKey)
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer db.Close()
	bad := append([]byte("v10"), make([]byte, 5)...) // not a block multiple → always fails
	for _, name := range []string{"ca", "cb", "cc", "cd", "ce"} {
		if _, err := db.Exec(`UPDATE cookies SET encrypted_value = ? WHERE name = ?`, bad, name); err != nil {
			t.Fatalf("corrupt %s: %v", name, err)
		}
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	_, err = ReadCookiesForHost(path, "%example.com", testKey)
	if err == nil {
		t.Fatal("expected 5/10 failed to trip the guard, got nil")
	}
	if !strings.Contains(err.Error(), "failed decrypt") {
		t.Fatalf("expected failure-ratio error, got: %v", err)
	}
}

// One corrupt entry among many good ones is skipped, and the rest survive.
func TestReadCookies_SingleCorruptSkipped(t *testing.T) {
	path := seedCookies(t, 10, testKey)
	// Overwrite one row's encrypted_value with a v10 blob whose ciphertext
	// length is not a block multiple: decryptValue rejects it deterministically,
	// so exactly one cookie is skipped while the other 9 decrypt cleanly.
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer db.Close()
	bad := append([]byte("v10"), make([]byte, 5)...) // 5 bytes: not a 16-byte multiple
	if _, err := db.Exec(`UPDATE cookies SET encrypted_value = ? WHERE name = 'ca'`, bad); err != nil {
		t.Fatalf("corrupt one row: %v", err)
	}
	// Fold the WAL into the main db: ReadCookiesForHost opens with
	// immutable=1, which ignores the -wal file, so an uncheckpointed UPDATE
	// would not be visible to the read under test.
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	got, err := ReadCookiesForHost(path, "%example.com", testKey)
	if err != nil {
		t.Fatalf("single corrupt entry should not fail the read: %v", err)
	}
	if len(got) != 9 {
		t.Fatalf("got %d cookies, want 9 (one skipped)", len(got))
	}
}
