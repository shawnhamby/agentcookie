package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestPushesAreSingleFlightWithOnePendingRerun pins the bound that keeps a
// stalled sink from accumulating pushes: one running, one pending, the rest
// coalesced into that pending one.
func TestPushesAreSingleFlightWithOnePendingRerun(t *testing.T) {
	cookies := filepath.Join(t.TempDir(), "Cookies")
	if err := os.WriteFile(cookies, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	entered := make(chan string, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once

	w, err := New(Config{
		CookiesPath: cookies,
		Push: func(ctx context.Context) (int, error) {
			entered <- "push"
			// Hold the first push open long enough for the other triggers to
			// arrive while it is still in flight.
			<-release
			return 1, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseOnce.Do(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.runPushes(ctx)

	w.enqueue("first")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first push never started")
	}

	// Three more triggers while the first push is in flight: one becomes the
	// pending rerun, the other two are dropped.
	w.enqueue("second")
	w.enqueue("third")
	w.enqueue("fourth")

	select {
	case <-entered:
		t.Fatal("a second push ran while the first was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("pending push never ran after the first finished")
	}
	select {
	case <-entered:
		t.Fatal("more than one pending push ran")
	case <-time.After(200 * time.Millisecond):
	}

	if got := w.Stats().PushCount; got != 2 {
		t.Fatalf("PushCount = %d, want 2 (one running plus one coalesced rerun)", got)
	}
}
