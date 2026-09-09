package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// TestDeployPublish_SendsTheTokenAndNamesTheDeploy. The last call of a
// deploy is authenticated per call, like the create and the start and
// unlike the four unauthenticated ones, and it names the deploy the
// create returned.
//
// It also decodes the body it is answered with — the subdomain LABEL and
// the expiry — because the response carries the one fact a client cannot
// derive, and a call that dropped it would leave the caller with nothing
// to show.
//
// REQUIRED MUTATION, run 2026-09-09: drop the per-call bearer option
// from DeployPublish. Reds on the Authorization assertion.
func TestDeployPublish_SendsTheTokenAndNamesTheDeploy(t *testing.T) {
	var (
		mu      sync.Mutex
		paths   []string
		methods []string
		bearers []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		methods = append(methods, r.Method)
		bearers = append(bearers, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w,
			`{"subdomain":"quick-koala-4f2a","expires_at":"2026-09-11T09:00:00Z"}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Token != ui.Secret(testToken) {
		t.Fatal("WithToken did not take effect — every assertion below would pass vacuously")
	}

	out, err := c.DeployPublish(context.Background(), "dpl-9f2a")
	if err != nil {
		t.Fatalf("DeployPublish: %v", err)
	}
	if out.Subdomain != "quick-koala-4f2a" {
		t.Errorf("Subdomain = %q, want %q", out.Subdomain, "quick-koala-4f2a")
	}
	if want := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC); !out.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", out.ExpiresAt, want)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("the publish made %d requests, want exactly 1", len(paths))
	}
	if want := "/v1/deploys/dpl-9f2a/publish"; paths[0] != want {
		t.Errorf("the publish called %q, want %q", paths[0], want)
	}
	if methods[0] != http.MethodPost {
		t.Errorf("the publish used %s, want POST", methods[0])
	}
	if want := "Bearer " + testToken; bearers[0] != want {
		t.Errorf("the publish sent Authorization %q, want %q", bearers[0], want)
	}
}

// TestDeployPublish_IsNeverRetried. Whether the call landed is the very
// question a retry would be asking, and asking again cannot answer it: a
// second request arriving after a first one succeeded is a second
// decision about a deploy this client has been told nothing about.
//
// The server here fails twice and would succeed on the third attempt,
// which is the shape that tells a call that gives up from one that does
// not — a handler that always failed cannot.
//
// REQUIRED MUTATION, run 2026-09-09: pass true for idempotent in
// DeployPublish. It reds EARLIER than predicted, on "the publish
// succeeded, so it went round again after a 5xx" rather than on the
// attempt count — the retried call reaches the handler's third answer,
// so the call returns no error at all and the count is never reached.
// The stricter assertion fires first, and it names the defect better.
func TestDeployPublish_IsNeverRetried(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"subdomain":"quick-koala-4f2a"}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.DeployPublish(context.Background(), "dpl-9f2a"); err == nil {
		t.Fatal("the publish succeeded, so it went round again after a 5xx")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("the server saw %d attempts, want exactly 1", got)
	}
}

// TestDeployPublish_CarriesTheServersCodeThrough. The two codes this
// endpoint answers with are what a client switches on, so an *APIError
// that lost one would send the caller back to reading prose — which is
// the thing these codes exist to stop.
func TestDeployPublish_CarriesTheServersCodeThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		code wire.ErrorCode
	}{
		{"the build is finished with", wire.CodeDeployFailed},
		{"the build has not finished", wire.CodeDeployNotReady},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w,
					`{"error":{"code":"`+string(tc.code)+`","message":"a sentence for a person"}}`)
			}))
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.DeployPublish(context.Background(), "dpl-9f2a")
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("the publish returned %T, want an *APIError", err)
			}
			if apiErr.Code != tc.code {
				t.Errorf("Code = %q, want %q", apiErr.Code, tc.code)
			}
			if apiErr.Status != http.StatusConflict {
				t.Errorf("Status = %d, want %d", apiErr.Status, http.StatusConflict)
			}
			if apiErr.Message != "a sentence for a person" {
				t.Errorf("Message = %q, want the server's own", apiErr.Message)
			}
		})
	}
}
