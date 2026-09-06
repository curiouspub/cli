package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/curiouspub/cli/pkg/wire"
)

// TestNew_BaseURLResolution is the "Construction" scope's own claim: an
// explicit baseURL argument wins outright; an empty one falls back to
// CURIOUS_API_URL; and with neither set, the compiled-in default
// applies. Not in the acceptance table's own bullet list, but stated
// directly in the task's Construction section, so it earns a test of
// its own rather than being left to the calls that happen to exercise
// New indirectly.
func TestNew_BaseURLResolution(t *testing.T) {
	t.Run("explicit argument wins over the environment", func(t *testing.T) {
		t.Setenv("CURIOUS_API_URL", "http://localhost:1")
		c, err := New("http://127.0.0.1:2")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.baseURL != "http://127.0.0.1:2" {
			t.Errorf("baseURL = %q, want the explicit argument", c.baseURL)
		}
	})

	t.Run("empty argument falls back to CURIOUS_API_URL", func(t *testing.T) {
		t.Setenv("CURIOUS_API_URL", "http://127.0.0.1:3")
		c, err := New("")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.baseURL != "http://127.0.0.1:3" {
			t.Errorf("baseURL = %q, want the environment value", c.baseURL)
		}
	})

	t.Run("neither set falls back to the compiled-in default", func(t *testing.T) {
		t.Setenv("CURIOUS_API_URL", "")
		c, err := New("")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.baseURL != defaultAPIBase {
			t.Errorf("baseURL = %q, want the compiled-in default %q", c.baseURL, defaultAPIBase)
		}
	})
}

// TestNew_TrailingSlashNormalised is the other unstated-but-scoped
// claim: "Trailing slashes normalised."
func TestNew_TrailingSlashNormalised(t *testing.T) {
	c, err := New("https://api.curious.pub/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != "https://api.curious.pub" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", c.baseURL)
	}
}

// TestNew_DefaultTimeout pins the documented default so a change to it
// is a deliberate, reviewable edit rather than an accident nothing
// notices.
func TestNew_DefaultTimeout(t *testing.T) {
	c, err := New("https://api.curious.pub")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != defaultTimeout {
		t.Errorf("timeout = %v, want the default %v", c.timeout, defaultTimeout)
	}
}

// TestWithTimeout_ActuallyCancels is the behavioural half: WithTimeout
// is not just a stored field, it is what context.WithTimeout in attempt
// actually uses to bound connect-through-body. A handler slower than
// the configured timeout must make the call fail, not hang until some
// other, larger deadline.
func TestWithTimeout_ActuallyCancels(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock // never answers within the test's own lifetime
	}))
	// Close() blocks until every in-flight request completes, so the
	// handler must be released FIRST — close(unblock) is registered
	// second and therefore, LIFO, runs before srv.Close() above it.
	defer srv.Close()
	defer close(unblock)

	c, err := New(srv.URL, WithTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != 20*time.Millisecond {
		t.Fatalf("WithTimeout did not take effect: timeout = %v", c.timeout)
	}

	start := time.Now()
	_, err = c.AuthStart(context.Background(), wire.AuthStartRequest{Email: "a@example.com"})
	if err == nil {
		t.Fatal("AuthStart against a server that never answers succeeded, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v to fail — the configured timeout was not honoured", elapsed)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && !netErr.Timeout() {
		t.Errorf("error %v does not report itself as a timeout", err)
	}
}
