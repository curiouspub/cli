package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

type capturedRequest struct {
	Method      string
	Path        string
	ContentType string
	Body        []byte
}

// captureServer records the shape of exactly one request and answers it
// with status/respBody.
func captureServer(status int, respBody string) (*httptest.Server, *capturedRequest) {
	captured := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.ContentType = r.Header.Get("Content-Type")
		captured.Body = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	return srv, captured
}

// TestCalls_RequestShapes is the acceptance table's first bullet: each
// of the four calls sends the right method, path and Content-Type, and
// — for the three that carry one — a request body that round-trips
// through the pkg/wire request type. Capacity carries no request body
// (GET /v1/capacity takes none), so only the other three assert a
// round trip.
func TestCalls_RequestShapes(t *testing.T) {
	t.Run("Capacity", func(t *testing.T) {
		srv, got := captureServer(http.StatusOK,
			`{"open":true,"accounts_left":5,"resets_at":"2026-01-01T00:00:00Z"}`)
		defer srv.Close()

		c, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		resp, err := c.Capacity(context.Background())
		if err != nil {
			t.Fatalf("Capacity: %v", err)
		}
		if got.Method != http.MethodGet {
			t.Errorf("method = %q, want %q", got.Method, http.MethodGet)
		}
		if got.Path != "/v1/capacity" {
			t.Errorf("path = %q, want %q", got.Path, "/v1/capacity")
		}
		if got.ContentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got.ContentType)
		}
		if !resp.Open || resp.AccountsLeft != 5 {
			t.Errorf("response = %+v, want Open=true AccountsLeft=5", resp)
		}
	})

	t.Run("Waitlist", func(t *testing.T) {
		srv, got := captureServer(http.StatusAccepted, `{}`)
		defer srv.Close()

		c, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		req := wire.WaitlistRequest{Email: "person@example.com"}
		if _, err := c.Waitlist(context.Background(), req); err != nil {
			t.Fatalf("Waitlist: %v", err)
		}
		if got.Method != http.MethodPost {
			t.Errorf("method = %q, want %q", got.Method, http.MethodPost)
		}
		if got.Path != "/v1/waitlist" {
			t.Errorf("path = %q, want %q", got.Path, "/v1/waitlist")
		}
		if got.ContentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got.ContentType)
		}
		var roundTripped wire.WaitlistRequest
		if err := json.Unmarshal(got.Body, &roundTripped); err != nil {
			t.Fatalf("request body %s does not decode as wire.WaitlistRequest: %v", got.Body, err)
		}
		if roundTripped != req {
			t.Errorf("request body round-tripped to %+v, want %+v", roundTripped, req)
		}
	})

	t.Run("AuthStart", func(t *testing.T) {
		srv, got := captureServer(http.StatusAccepted, `{}`)
		defer srv.Close()

		c, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		req := wire.AuthStartRequest{Email: "person@example.com"}
		if _, err := c.AuthStart(context.Background(), req); err != nil {
			t.Fatalf("AuthStart: %v", err)
		}
		if got.Method != http.MethodPost {
			t.Errorf("method = %q, want %q", got.Method, http.MethodPost)
		}
		if got.Path != "/v1/auth/start" {
			t.Errorf("path = %q, want %q", got.Path, "/v1/auth/start")
		}
		if got.ContentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got.ContentType)
		}
		var roundTripped wire.AuthStartRequest
		if err := json.Unmarshal(got.Body, &roundTripped); err != nil {
			t.Fatalf("request body %s does not decode as wire.AuthStartRequest: %v", got.Body, err)
		}
		if roundTripped != req {
			t.Errorf("request body round-tripped to %+v, want %+v", roundTripped, req)
		}
	})

	t.Run("AuthVerify", func(t *testing.T) {
		srv, got := captureServer(http.StatusOK, `{"token":"tok_abc123"}`)
		defer srv.Close()

		c, err := New(srv.URL)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		req := wire.AuthVerifyRequest{Email: "person@example.com", Code: "123456", MarketingOptIn: true}
		resp, err := c.AuthVerify(context.Background(), req)
		if err != nil {
			t.Fatalf("AuthVerify: %v", err)
		}
		if got.Method != http.MethodPost {
			t.Errorf("method = %q, want %q", got.Method, http.MethodPost)
		}
		if got.Path != "/v1/auth/verify" {
			t.Errorf("path = %q, want %q", got.Path, "/v1/auth/verify")
		}
		if got.ContentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got.ContentType)
		}
		var roundTripped wire.AuthVerifyRequest
		if err := json.Unmarshal(got.Body, &roundTripped); err != nil {
			t.Fatalf("request body %s does not decode as wire.AuthVerifyRequest: %v", got.Body, err)
		}
		if roundTripped != req {
			t.Errorf("request body round-tripped to %+v, want %+v", roundTripped, req)
		}
		if resp.Token != "tok_abc123" {
			t.Errorf("Token = %q, want %q", resp.Token, "tok_abc123")
		}
	})
}

// userAgentPattern is the contract: curious/<version> (<goos>/<goarch>),
// asserted structurally so a hostname, username or random id slipped in
// fails on sight rather than on a human noticing an odd string in a bug
// report.
var userAgentPattern = regexp.MustCompile(`^curious/\S+ \([a-z0-9]+/[a-z0-9]+\)$`)

func TestUserAgent_Format(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Capacity(context.Background()); err != nil {
		t.Fatalf("Capacity: %v", err)
	}

	if !userAgentPattern.MatchString(ua) {
		t.Fatalf("User-Agent %q does not match %s", ua, userAgentPattern)
	}
	// A hostname, username or random install id is the one thing this
	// public, no-phone-home client must never smuggle in the one header
	// every request always carries.
	for _, marker := range []string{"@", "\\", "/home/", "/Users/", "C:\\"} {
		if strings.Contains(ua, marker) {
			t.Errorf("User-Agent %q contains %q, which looks like a smuggled identifier", ua, marker)
		}
	}
}
