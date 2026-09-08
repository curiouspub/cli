package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/pkg/wire"
)

// TestNew_BaseURLResolution pins the base-URL precedence New documents:
// an explicit baseURL argument wins outright; an empty one falls back to
// CURIOUS_API_URL; and with neither set, the compiled-in default
// applies. This earns a test of its own rather than being left to the
// calls that happen to exercise New indirectly.
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

// TestNew_NonPositiveTimeoutRefused pins WithTimeout's own documented
// rule: a zero or negative timeout is refused at construction rather
// than accepted and left to fail every subsequent call with a
// deadline-exceeded error — context.WithTimeout given a non-positive
// duration expires before the call it wraps ever gets to run.
func TestNew_NonPositiveTimeoutRefused(t *testing.T) {
	if _, err := New("https://api.curious.pub", WithTimeout(0)); err == nil {
		t.Error("New with WithTimeout(0) succeeded, want a refusal at construction")
	}
	if _, err := New("https://api.curious.pub", WithTimeout(-1*time.Second)); err == nil {
		t.Error("New with a negative WithTimeout succeeded, want a refusal at construction")
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

// TestARefusedBaseURLDoesNotEchoTheSecretItRefuses is the userinfo rule's
// other half, and the one it was missing.
//
// The address guard refuses a base URL carrying "user:pass@" because
// net/http would turn it into an Authorization header on every request,
// silently — that is the ruling this row belongs to. Its refusal then
// printed the value back, so the password reached stderr, terminal
// scrollback, and whatever the reader pastes into a bug report. **The
// check whose entire subject is "do not put a password here" was the one
// echoing it.**
//
// Every refusal in the guard now renders through url.Redacted, not just
// the userinfo one: any of them can be reached by a URL that also carries
// a password, and a rule applied at one branch is a rule the other
// branches are exempt from.
//
// The parse-failure branch names no value at all, and that is stated
// where it lives: redaction needs a parsed URL, and that is the one
// branch without one.
//
// REQUIRED MUTATION, RUN: restore `raw` in place of u.Redacted() in any
// refusal below. Reds on the secret appearing in the message.
func TestARefusedBaseURLDoesNotEchoTheSecretItRefuses(t *testing.T) {
	const secret = "hunter2correcthorse"

	for _, row := range []struct {
		name string
		base string
	}{
		{"userinfo, the refusal this rule is named for", "https://user:" + secret + "@api.example.com"},
		{"userinfo on a bad scheme", "ftp://user:" + secret + "@api.example.com"},
		{"userinfo with a query string", "https://user:" + secret + "@api.example.com?x=1"},
		{"userinfo with a fragment", "https://user:" + secret + "@api.example.com#f"},
		{"userinfo over plaintext to a non-loopback host", "http://user:" + secret + "@api.example.com"},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, err := validateBaseURL(row.base)
			if err == nil {
				t.Fatal("the guard accepted a base URL carrying userinfo")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the refusal echoes the password it is refusing:\n%s", err)
			}
			// The positive control: the message is still about THIS value,
			// not a generic one — a refusal that named nothing would pass
			// the assertion above while telling the reader nothing.
			if !strings.Contains(err.Error(), "api.example.com") {
				t.Errorf("the refusal names no host, so it cannot be acted on:\n%s", err)
			}
		})
	}
}
