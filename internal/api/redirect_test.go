package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestRedirect_NeverFollowed proves this client refuses every redirect
// rather than following it: an origin that only ever answers with a 302
// must never cause the redirect target to be dialled at all. Asserted by
// the target's own request count, not by the call merely failing — a
// call that failed for some other reason would also leave that counter
// at 0, but only "the target was never reached" is the actual claim.
func TestRedirect_NeverFollowed(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/capacity", http.StatusFound)
	}))
	defer origin.Close()

	c, err := New(origin.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Capacity(context.Background())
	if err == nil {
		t.Fatal("Capacity against a server that only ever redirects succeeded, want an error")
	}
	if got := atomic.LoadInt32(&targetHits); got != 0 {
		t.Fatalf("the redirect target saw %d requests, want 0 — this client must "+
			"never follow a redirect", got)
	}

	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err is %T, want *APIError carrying the origin's own redirect status", err)
	}
	if apiErr.Status != http.StatusFound {
		t.Errorf("Status = %d, want %d — the unfollowed redirect response itself",
			apiErr.Status, http.StatusFound)
	}
}
