package livecdp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// hangingExecutor answers no command and, like chromedp's browser executor,
// releases a caller only when its context ends.
type hangingExecutor struct{}

func (hangingExecutor) Execute(ctx context.Context, method string, params, res any) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestReinjectAllHonorsContextDeadline pins the property whose absence let a
// dead CDP connection accumulate one parked goroutine, and its cookie set, per
// source write: a push must end with its own deadline.
func TestReinjectAllHonorsContextDeadline(t *testing.T) {
	syncer := NewSyncer(hangingExecutor{}, func() ([]chrome.Cookie, error) {
		return []chrome.Cookie{{HostKey: ".example.com", Name: "sid", Value: "v", Path: "/"}}, nil
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := syncer.ReinjectAll(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ReinjectAll error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReinjectAll did not return after its context expired")
	}
}
