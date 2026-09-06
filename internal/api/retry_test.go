package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// TestRetry_IdempotentSucceedsAfterTransientFailures pins the retry
// budget: Capacity is documented idempotent, so a handler that fails
// twice and then succeeds is retried through to a result, at exactly 3
// attempts, rather than surfacing the first failure.
func TestRetry_IdempotentSucceedsAfterTransientFailures(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if !resp.Open {
		t.Errorf("Open = false, want true")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("server saw %d attempts, want exactly 3 (2 failures + 1 success)", got)
	}
}

// TestRetry_NonIdempotentSingleAttempt is the budget-protection half:
// auth/start is never retried, because each call spends one of four
// sends allowed per identity per rolling hour. A handler that would
// eventually succeed on retry must never get the chance.
func TestRetry_NonIdempotentSingleAttempt(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.AuthStart(context.Background(), wire.AuthStartRequest{Email: "a@example.com"}); err == nil {
		t.Fatal("AuthStart succeeded against a handler that always fails")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("server saw %d attempts, want exactly 1 — auth/start must never "+
			"be retried, or a single keystroke could spend a third of the hourly "+
			"send budget", got)
	}
}
