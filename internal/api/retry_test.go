package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// retryEndpointCase names one of the four calls and what its retry
// behaviour must be. The call functions all return only an error: what
// each subtest below checks is attempt counts and success/failure, not
// response payload correctness, which TestCalls_RequestShapes already
// covers.
type retryEndpointCase struct {
	name       string
	idempotent bool
	call       func(c *Client) error
	// okBody is what the handler answers with on the attempt that is
	// meant to succeed — shaped to decode cleanly as this endpoint's own
	// response type.
	okBody []byte
}

var retryEndpointCases = []retryEndpointCase{
	{
		name:       "Capacity",
		idempotent: true,
		okBody:     []byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`),
		call: func(c *Client) error {
			_, err := c.Capacity(context.Background())
			return err
		},
	},
	{
		name:       "Waitlist",
		idempotent: true,
		okBody:     []byte(`{}`),
		call: func(c *Client) error {
			_, err := c.Waitlist(context.Background(), wire.WaitlistRequest{Email: "a@example.com"})
			return err
		},
	},
	{
		name:       "AuthStart",
		idempotent: false,
		okBody:     []byte(`{}`),
		call: func(c *Client) error {
			_, err := c.AuthStart(context.Background(), wire.AuthStartRequest{Email: "a@example.com"})
			return err
		},
	},
	{
		name:       "AuthVerify",
		idempotent: false,
		okBody:     []byte(`{"token":"tok_x"}`),
		call: func(c *Client) error {
			_, err := c.AuthVerify(context.Background(), wire.AuthVerifyRequest{Email: "a@example.com", Code: "000000"})
			return err
		},
	},
}

// TestRetry_PerEndpoint pins EACH of the four calls' idempotency
// individually, by name, rather than only Capacity and AuthStart as
// round 1 did. That gap mattered: with only those two rows, inverting
// Waitlist's or AuthVerify's own idempotent argument in calls.go left
// the whole suite green, because nothing named either of them
// specifically. Flip any one call's argument now and its OWN subtest —
// not some other one — reds.
//
// Each server fails the first two requests, then succeeds. An idempotent
// call must retry through to that success at exactly 3 attempts; a
// non-idempotent call must surface the first failure at exactly 1 —
// never getting the chance the server would otherwise give it.
func TestRetry_PerEndpoint(t *testing.T) {
	for _, tc := range retryEndpointCases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := atomic.AddInt32(&attempts, 1)
				if n <= 2 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(tc.okBody)
			}))
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			callErr := tc.call(c)
			got := atomic.LoadInt32(&attempts)

			if tc.idempotent {
				if callErr != nil {
					t.Fatalf("%s: call failed after the retry budget: %v", tc.name, callErr)
				}
				if got != 3 {
					t.Fatalf("%s: server saw %d attempts, want exactly 3 (2 failures + "+
						"1 success) — this call is documented idempotent and must retry "+
						"through to the eventual success", tc.name, got)
				}
			} else {
				if callErr == nil {
					t.Fatalf("%s: call succeeded against a handler that fails its first "+
						"two requests — this call must never be retried, so it should "+
						"have surfaced the first failure", tc.name)
				}
				if got != 1 {
					t.Fatalf("%s: server saw %d attempts, want exactly 1 — this call must "+
						"never be retried", tc.name, got)
				}
			}
		})
	}
}

// TestRetry_CeilingIsExactlyThree pins the OTHER half of the retry
// budget: not just that an idempotent call retries at least until the
// third attempt, but that it gives up AT the third and never tries a
// fourth. A handler that fails every single time is what makes this
// observable — the succeeds-on-the-third-attempt handler used above
// cannot, because the retry loop stops the moment it sees a success
// regardless of how high the ceiling actually is, so raising it from 3
// to 10 would leave that test just as green.
func TestRetry_CeilingIsExactlyThree(t *testing.T) {
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
	if _, err := c.Capacity(context.Background()); err == nil {
		t.Fatal("Capacity against a handler that always fails succeeded, want an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("server saw %d attempts, want exactly 3 — the retry ceiling must "+
			"stop at 3, not merely reach it", got)
	}
}

// TestRetry_TruncatedBodyIsRetried is F9's row: a 2xx response whose body
// is cut short by a dropped connection decodes as io.ErrUnexpectedEOF,
// which is a connection-level failure exactly like any other dropped
// connection — not a decode problem this client caused — and must be
// retried the same way. The handler declares a Content-Length larger
// than what it actually writes on the first request; net/http's server
// notices the shortfall and closes the connection instead of padding it,
// which is what produces io.ErrUnexpectedEOF client-side rather than a
// clean io.EOF.
func TestRetry_TruncatedBodyIsRetried(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"open":true`)) // far short of the declared 100 bytes
			return
		}
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
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("server saw %d attempts, want exactly 2 — a body truncated by a "+
			"dropped connection must be retried like any other connection-level "+
			"failure", got)
	}
}
