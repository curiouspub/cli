package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

const testToken = "sk-super-secret-token-value"

// TestToken_NeverSentOnUnauthenticatedCalls checks the first half of this
// client's token-handling rule: none of this epic's four calls take a
// bearer token — auth/verify RETURNS one, it does not spend one — so a
// token set on the Client must never appear in the headers any of the
// four calls actually sends.
//
// Two positive controls guard against this passing for the wrong reason.
// Checking c.Token itself proves WithToken took effect at all — without
// it, a WithToken that silently no-ops would make every assertion below
// pass vacuously. The second, larger one is below, right after the
// server is defined: a request that DOES carry the header must actually
// be seen by this handler's own capture, or every "never sent" assertion
// that follows is indistinguishable from a capture that does nothing.
//
// Read this test's result narrowly: until an authenticated call exists
// in this client, it proves only that these four calls don't send a
// token — not that one is sent correctly when it should be. That larger
// claim needs its own test once such a call exists.
func TestToken_NeverSentOnUnauthenticatedCalls(t *testing.T) {
	var sawAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			sawAuthHeader = h
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/capacity":
			_, _ = w.Write([]byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`))
		case "/v1/auth/verify":
			_, _ = w.Write([]byte(`{"token":"tok_x"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	// Positive control for the capture mechanism itself: a request that
	// DOES carry the header, sent straight at this handler with no
	// Client in between, must be seen. Without this, replacing the
	// capture assignment above with a discard (`_ = h`) leaves every
	// assertion below green for the wrong reason — none of this epic's
	// four calls send the header either way, so a capture that silently
	// does nothing looks identical to one that works.
	probe, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/capacity", nil)
	if err != nil {
		t.Fatalf("building the control request: %v", err)
	}
	probe.Header.Set("Authorization", "Bearer control-value")
	probeResp, err := http.DefaultClient.Do(probe)
	if err != nil {
		t.Fatalf("sending the control request: %v", err)
	}
	_ = probeResp.Body.Close()
	if sawAuthHeader != "Bearer control-value" {
		t.Fatalf("the capture did not see a header that was actually sent — got "+
			"%q, want %q; every assertion below would pass even with the capture "+
			"disabled", sawAuthHeader, "Bearer control-value")
	}
	sawAuthHeader = "" // reset before exercising the real calls below

	c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Token != ui.Secret(testToken) {
		t.Fatal("WithToken did not take effect — every assertion below would pass vacuously")
	}

	ctx := context.Background()

	if _, err := c.Capacity(ctx); err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if sawAuthHeader != "" {
		t.Errorf("Capacity sent Authorization %q, want none", sawAuthHeader)
	}

	if _, err := c.Waitlist(ctx, wire.WaitlistRequest{Email: "a@example.com"}); err != nil {
		t.Fatalf("Waitlist: %v", err)
	}
	if sawAuthHeader != "" {
		t.Errorf("Waitlist sent Authorization %q, want none", sawAuthHeader)
	}

	if _, err := c.AuthStart(ctx, wire.AuthStartRequest{Email: "a@example.com"}); err != nil {
		t.Fatalf("AuthStart: %v", err)
	}
	if sawAuthHeader != "" {
		t.Errorf("AuthStart sent Authorization %q, want none", sawAuthHeader)
	}

	if _, err := c.AuthVerify(ctx, wire.AuthVerifyRequest{Email: "a@example.com", Code: "000000"}); err != nil {
		t.Fatalf("AuthVerify: %v", err)
	}
	if sawAuthHeader != "" {
		t.Errorf("AuthVerify sent Authorization %q, want none", sawAuthHeader)
	}
}

// TestToken_NeverAppearsInLogOutput checks the other half of the rule: a
// Client formatted for a log line — the accident ui.Secret's
// own doc comment exists to survive — never shows the raw value, because
// Token is an EXPORTED field of type ui.Secret and every formatting
// path resolves through Secret's own methods rather than the underlying
// string.
func TestToken_NeverAppearsInLogOutput(t *testing.T) {
	c := &Client{baseURL: "https://api.curious.pub", Token: ui.Secret(testToken)}

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	logger.Printf("client = %v", c)
	logger.Printf("client = %+v", *c)
	logger.Printf("client = %#v", *c)
	if strings.Contains(buf.String(), testToken) {
		t.Fatalf("log output contains the raw token: %s", buf.String())
	}

	direct := fmt.Sprintf("%v %+v %#v %s %q", c, *c, *c, c.Token, c.Token)
	if strings.Contains(direct, testToken) {
		t.Fatalf("fmt output contains the raw token: %s", direct)
	}
}
