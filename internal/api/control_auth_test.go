package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/controlwire"
)

func TestControlLoginOriginIsolation(t *testing.T) {
	for _, bad := range []string{"", "http://example.test", "https://user:secret@example.test", "https://example.test/path", "https://example.test/?x=1", "https://example.test/#x", "https://example.test:", "https://example.test:abc"} {
		if _, err := ControlOrigin(bad, false); err == nil {
			t.Errorf("accepted invalid origin %q", bad)
		}
	}
	if _, e := ControlOrigin("https://sensitive-user:sensitive-password@example.test", false); e == nil || strings.Contains(e.Error(), "sensitive-") {
		t.Fatal("origin error exposed credentials")
	}
	got, err := ControlOrigin("https://EXAMPLE.test:443/", false)
	if err != nil || got != "https://example.test" {
		t.Fatalf("canonical origin: %q %v", got, err)
	}
	canonical, err := ControlOrigin("https://[2001:0db8:0:0:0:0:0:1]:00444/", false)
	if err != nil || canonical != "https://[2001:db8::1]:444" {
		t.Fatalf("IPv6 origin canonicalization: %q %v", canonical, err)
	}
	if _, err := ControlOrigin("http://127.0.0.1:9000", false); err == nil {
		t.Fatal("loopback accepted outside tests")
	}
	if _, err := ControlOrigin("http://127.0.0.1:9000", true); err != nil {
		t.Fatal(err)
	}
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == controlwire.RevokePath {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: target.URL + controlwire.VerificationPath, VerificationURIComplete: target.URL + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
	}))
	defer origin.Close()
	client, e := NewControlClient(origin.URL, true)
	if e != nil {
		t.Fatal(e)
	}
	if e = client.Revoke(context.Background(), ui.Secret("synthetic-token")); e == nil {
		t.Fatal("accepted cross-origin redirect")
	}
	if reached.Load() {
		t.Fatal("credential followed cross-origin redirect")
	}
	if _, e = client.Create(context.Background(), "digest", "cli"); e == nil {
		t.Fatal("accepted cross-origin verification address")
	}
}
func TestControlLoginProtocol(t *testing.T) {
	t.Run("complete exchange and errors", testControlExchange)
	var redirected bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == controlwire.CapabilitiesPath {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		t.Errorf("unexpected path %s", r.URL.Path)
	}))
	defer srv.Close()
	c, err := NewControlClient(srv.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Capabilities(context.Background()); err == nil {
		t.Fatal("redirect was accepted")
	}
	if redirected {
		t.Fatal("cross-origin redirect followed")
	}
	for _, b := range []string{`{"error":"access_denied","error":"slow_down"}`, `{"error":"access_denied","extra":1}`, `{"error":"access_denied"} {}`} {
		var e controlwire.ErrorResponse
		if strictObject([]byte(b), &e) == nil {
			t.Fatalf("accepted %s", b)
		}
	}
	var invalid controlwire.ErrorResponse
	if strictObject([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, &invalid) == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	deep := json.NewDecoder(strings.NewReader(strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40)))
	if uniqueValue(deep, 0) == nil {
		t.Fatal("accepted excessive response nesting")
	}
	var cbody controlwire.Capabilities
	b, _ := json.Marshal(controlwire.Capabilities{Version: 1})
	if strictObject(b, &cbody) != nil {
		t.Fatal("valid JSON rejected")
	}
	if strings.Contains(err.Error(), srv.URL) {
		t.Fatal("transport error exposed origin")
	}
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	client, e := NewControlClient(deadURL, true)
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.Poll(context.Background(), "request", ui.Secret("sensitive-proof"))
	if e == nil || strings.Contains(e.Error(), "sensitive-proof") || strings.Contains(e.Error(), deadURL) {
		t.Fatal("transport error disclosed request data")
	}
}

func testControlExchange(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			_, _ = fmt.Fprint(w, `{"version":1,"client_kinds":["cli","mcp"],"endpoints":{"capabilities":"/v1/auth/client/capabilities","requests":"/v1/auth/client/requests","verification":"/client-login","decision":"/v1/auth/client/decision","poll":"/v1/auth/client/poll","session":"/v1/auth/client/session","revoke":"/v1/auth/client/revoke"},"interval_seconds":5}`)
		case controlwire.RequestsPath:
			if r.Header.Get("Authorization") != "" {
				t.Error("credential sent during create")
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
		case controlwire.PollPath:
			var q controlwire.PollRequest
			_ = json.NewDecoder(r.Body).Decode(&q)
			switch q.Proof {
			case "pending":
				w.WriteHeader(202)
				_, _ = fmt.Fprint(w, `{"error":"authorization_pending"}`)
			case "slow":
				w.Header().Set("Retry-After", "6")
				w.WriteHeader(429)
				_, _ = fmt.Fprint(w, `{"error":"slow_down","retry_after_seconds":6}`)
			case "wrong":
				w.WriteHeader(401)
				_, _ = fmt.Fprint(w, `{"error":"invalid_credential"}`)
			case "denied":
				w.WriteHeader(403)
				_, _ = fmt.Fprint(w, `{"error":"access_denied"}`)
			case "expired":
				w.WriteHeader(410)
				_, _ = fmt.Fprint(w, `{"error":"request_expired"}`)
			case "restart":
				w.WriteHeader(409)
				_, _ = fmt.Fprint(w, `{"error":"restart_required"}`)
			case "duplicate-header":
				w.Header().Add("Cache-Control", "no-store")
				_, _ = fmt.Fprint(w, `{"error":"invalid_credential"}`)
			case "missing-header":
				w.Header().Del("Cache-Control")
				_, _ = fmt.Fprint(w, `{"error":"invalid_credential"}`)
			case "oversized":
				_, _ = fmt.Fprint(w, strings.Repeat("x", controlwire.MaxResponseBytes+1))
			case "bad-retry":
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(429)
				_, _ = fmt.Fprint(w, `{"error":"slow_down","retry_after_seconds":6}`)
			case "malformed":
				_, _ = fmt.Fprint(w, `{"credential":"bad","credential_id":"bad","user_id":"u","expires_at":"invalid"}`)
			default:
				_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1.clcred_QkJCQkJCQkJCQkJCQkJCQg.ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8", CredentialID: "clcred_QkJCQkJCQkJCQkJCQkJCQg", UserID: "user-one", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
			}
		case controlwire.RevokePath:
			if r.Header.Get("Authorization") != "Bearer synthetic-token" {
				t.Error("missing bearer credential")
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, err := NewControlClient(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = c.Capabilities(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := c.Create(ctx, "digest", "cli")
	if err != nil || created.UserCode != "ABCD-EFGH" {
		t.Fatalf("create: %v", err)
	}
	for _, tc := range []struct{ proof, code string }{{"pending", "authorization_pending"}, {"slow", "slow_down"}, {"wrong", "invalid_credential"}, {"denied", "access_denied"}, {"expired", "request_expired"}, {"restart", "restart_required"}} {
		_, err = c.Poll(ctx, created.RequestID, ui.Secret(tc.proof))
		var problem *ControlError
		if !errors.As(err, &problem) || problem.Code != tc.code {
			t.Errorf("%s: %v", tc.proof, err)
		}
	}
	if _, err = c.Poll(ctx, created.RequestID, ui.Secret("malformed")); err == nil {
		t.Fatal("accepted malformed issuance")
	}
	for _, proof := range []string{"oversized", "bad-retry", "duplicate-header", "missing-header"} {
		if _, err = c.Poll(ctx, created.RequestID, ui.Secret(proof)); err == nil {
			t.Errorf("accepted %s response", proof)
		}
	}
	account, err := c.Poll(ctx, created.RequestID, ui.Secret("correct"))
	if err != nil || account.UserID != "user-one" {
		t.Fatalf("poll: %v", err)
	}
	encoded, _ := json.Marshal(account)
	if strings.Contains(fmt.Sprintf("%+v", account)+string(encoded), "ICEiIyQlJicoKSor") {
		t.Fatal("credential did not redact")
	}
	if err = c.Revoke(ctx, ui.Secret("synthetic-token")); err != nil {
		t.Fatal(err)
	}
}
