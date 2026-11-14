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
	b, _ := json.Marshal(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}})
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
			w.Header().Del("Content-Type")
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

func TestControlLoginProtocolExactSchema(t *testing.T) {
	cases := []struct {
		name, body string
		dst        func() any
	}{
		{"capabilities", `{"version":1,"client_kinds":["cli","mcp"],"endpoints":{"capabilities":"a","requests":"b","verification":"c","decision":"d","poll":"e","session":"f","revoke":"g"},"interval_seconds":5}`, func() any { return &controlwire.Capabilities{} }},
		{"create", `{"request_id":"id","user_code":"code","verification_uri":"uri","verification_uri_complete":"full","expires_at":"expiry","interval_seconds":5}`, func() any { return &controlwire.CreateResponse{} }},
		{"issuance", `{"credential":"secret","credential_id":"id","user_id":"user","expires_at":"expiry"}`, func() any { return &controlwire.IssuanceResponse{} }},
		{"session", `{"credential_id":"id","user_id":"user","expires_at":"expiry"}`, func() any { return &controlwire.SessionResponse{} }},
		{"error", `{"error":"slow_down","retry_after_seconds":5}`, func() any { return &controlwire.ErrorResponse{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if e := strictObject([]byte(tc.body), tc.dst()); e != nil {
				t.Fatalf("valid schema rejected: %v", e)
			}
			var root map[string]any
			if e := json.Unmarshal([]byte(tc.body), &root); e != nil {
				t.Fatal(e)
			}
			var visit func(map[string]any, []string)
			visit = func(fields map[string]any, path []string) {
				for key, value := range fields {
					current := append(append([]string{}, path...), key)
					for _, mutation := range []string{"alias", "duplicate_alias", "null"} {
						var copyRoot map[string]any
						_ = json.Unmarshal([]byte(tc.body), &copyRoot)
						parent := copyRoot
						for _, component := range current[:len(current)-1] {
							parent = parent[component].(map[string]any)
						}
						old := parent[key]
						switch mutation {
						case "alias":
							delete(parent, key)
							parent[strings.ToUpper(key)] = old
						case "duplicate_alias":
							parent[strings.ToUpper(key)] = old
						case "null":
							parent[key] = nil
						}
						data, _ := json.Marshal(copyRoot)
						if strictObject(data, tc.dst()) == nil {
							t.Errorf("accepted %s %s", strings.Join(current, "."), mutation)
						}
					}
					if nested, ok := value.(map[string]any); ok {
						visit(nested, current)
					}
				}
			}
			visit(root, nil)
		})
	}
	if strictObject([]byte(`{"error":"access_denied","retry_after_seconds":null}`), &controlwire.ErrorResponse{}) == nil {
		t.Fatal("accepted null optional field")
	}
}

func TestControlLoginProtocolEndpointErrors(t *testing.T) {
	cases := []struct {
		path, code string
		status     int
		allowed    bool
	}{
		{controlwire.RequestsPath, "restart_required", 409, false},
		{controlwire.PollPath, "restart_required", 409, true},
		{controlwire.CapabilitiesPath, "slow_down", 429, false},
		{controlwire.RequestsPath, "request_conflict", 409, true},
		{controlwire.RevokePath, "access_denied", 403, false},
		{controlwire.RevokePath, "invalid_credential", 401, true},
	}
	for _, tc := range cases {
		t.Run(tc.path+"/"+tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				if tc.code == "slow_down" {
					w.Header().Set("Retry-After", "5")
				}
				w.WriteHeader(tc.status)
				if tc.code == "slow_down" {
					_, _ = w.Write([]byte(`{"error":"slow_down","retry_after_seconds":5}`))
				} else {
					_ = json.NewEncoder(w).Encode(map[string]string{"error": tc.code})
				}
			}))
			defer srv.Close()
			client, err := NewControlClient(srv.URL, true)
			if err != nil {
				t.Fatal(err)
			}
			err = client.call(context.Background(), http.MethodPost, tc.path, nil, "", nil, 200)
			var problem *ControlError
			trusted := errors.As(err, &problem) && problem.Code == tc.code
			if trusted != tc.allowed {
				t.Fatalf("endpoint error trusted=%t error=%v", trusted, err)
			}
		})
	}
	for _, tc := range []struct {
		status             int
		contentType, allow string
		valid              bool
	}{{204, "", "", true}, {204, "application/json; charset=utf-8", "", false}, {405, "application/json; charset=utf-8", "POST", true}, {405, "application/json; charset=utf-8", "GET", false}, {405, "application/json; charset=utf-8", "", false}} {
		t.Run(fmt.Sprintf("%d-%s-%s", tc.status, tc.contentType, tc.allow), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				if tc.allow != "" {
					w.Header().Set("Allow", tc.allow)
				}
				w.WriteHeader(tc.status)
				if tc.status == 405 {
					_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
				}
			}))
			defer srv.Close()
			client, err := NewControlClient(srv.URL, true)
			if err != nil {
				t.Fatal(err)
			}
			err = client.Revoke(context.Background(), "credential")
			valid := err == nil
			var problem *ControlError
			if errors.As(err, &problem) && problem.Code == "invalid_request" {
				valid = true
			}
			if valid != tc.valid {
				t.Fatalf("header acceptance=%t error=%v", valid, err)
			}
		})
	}
}
