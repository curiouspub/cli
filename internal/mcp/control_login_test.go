package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/pkg/controlwire"
)

func controlCall(t *testing.T, s *Server, message string) string {
	t.Helper()
	out, logs, e := serve(t, s, message+"\n")
	if e != nil {
		t.Fatal(e)
	}
	if logs != "" {
		t.Fatal("unexpected MCP log output")
	}
	return out
}
func TestControlLoginMCPFraming(t *testing.T) {
	deep := json.NewDecoder(strings.NewReader(strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40)))
	if controlUniqueJSON(deep, 0) == nil {
		t.Fatal("accepted excessive argument nesting")
	}
	var legacyCalls atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { legacyCalls.Add(1) }))
	defer legacy.Close()
	t.Setenv("CURIOUS_API_URL", legacy.URL)
	s := New("curious", "test")
	RegisterTools(s)
	for _, name := range []string{toolControlLoginStart, toolControlLoginVerify, toolControlLogout} {
		raw := controlCall(t, s, `{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`)
		reply := onlyReply(t, raw)
		var result struct {
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(reply.Result, &result); err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Errorf("%s did not refuse missing origin", name)
		}
	}
	if legacyCalls.Load() != 0 {
		t.Fatal("missing origin contacted legacy service")
	}
	invalidExtra := controlCall(t, s, callMessage("12", toolControlLoginStart, `{"control_url":"https://example.test","local_only":false}`))
	if !resultOf(t, onlyReply(t, invalidExtra)).IsError {
		t.Fatal("accepted a field outside the schema")
	}
	nullHandle := controlCall(t, s, callMessage("13", toolControlLoginStart, `{"control_url":"https://example.test","handle":null}`))
	if !resultOf(t, onlyReply(t, nullHandle)).IsError {
		t.Fatal("accepted null handle")
	}

	httpCall := controlCall(t, s, callMessage("11", toolControlLoginStart, fmt.Sprintf(`{"control_url":%q}`, legacy.URL)))
	if !resultOf(t, onlyReply(t, httpCall)).IsError || legacyCalls.Load() != 0 {
		t.Fatal("production MCP accepted HTTP loopback")
	}

	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("CURIOUS_CONFIG", filepath.Join(dir, "config.json"))
	oldPrepare, oldResume, oldVerify, oldLogout := controlPrepare, controlResume, controlVerify, controlLogout
	defer func() {
		controlPrepare, controlResume, controlVerify, controlLogout = oldPrepare, oldResume, oldVerify, oldLogout
	}()
	controlPrepare = func(origin string) (flow.ControlRequest, error) { return flow.PrepareControlLogin(origin, "mcp", true) }
	controlResume = func(ctx context.Context, origin, handle string) (flow.ControlRequest, error) {
		return flow.ResumeControlLogin(ctx, origin, handle, true)
	}
	controlLogout = func(ctx context.Context, origin, id string, localOnly bool) error {
		return flow.LogoutControl(ctx, origin, id, localOnly, true)
	}
	controlVerify = func(ctx context.Context, origin, handle string) (flow.ControlAccount, error) {
		return flow.VerifyControlLogin(ctx, origin, handle, true)
	}
	var createCalls atomic.Int32
	var pollCalls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			_ = json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}, IntervalSeconds: 5})
		case controlwire.RequestsPath:
			createCalls.Add(1)
			_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
		case controlwire.PollPath:
			pollCalls.Add(1)
			_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1.clcred_QkJCQkJCQkJCQkJCQkJCQg.ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8", CredentialID: "clcred_QkJCQkJCQkJCQkJCQkJCQg", UserID: "user-one", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
		case controlwire.RevokePath:
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ccl1.") {
				t.Error("missing credential")
			}
			w.Header().Del("Content-Type")
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	args := fmt.Sprintf(`{"control_url":%q}`, srv.URL)
	wire := controlCall(t, s, callMessage("1", toolControlLoginStart, args))
	preparedWire := wire
	if strings.Contains(wire, "ICEiIyQlJicoKSor") {
		t.Fatal("secret in output")
	}
	res := resultOf(t, onlyReply(t, wire))
	if res.IsError {
		t.Fatalf("start failed: %s", text(t, res))
	}
	var req flow.ControlRequest
	if e = json.Unmarshal([]byte(text(t, res)), &req); e != nil || req.Handle == "" || !req.Prepared || req.ControlURL != srv.URL || req.NextTool != toolControlLoginStart {
		t.Fatalf("invalid prepared result: %v", e)
	}
	args = fmt.Sprintf(`{"control_url":%q,"handle":%q}`, srv.URL, req.Handle)
	wire = controlCall(t, s, callMessage("6", toolControlLoginVerify, args))
	beforeStart := resultOf(t, onlyReply(t, wire))
	if !beforeStart.IsError || !strings.Contains(text(t, beforeStart), toolControlLoginStart) || createCalls.Load() != 0 {
		t.Fatal("verify created a request or omitted the next step")
	}
	wire = controlCall(t, s, callMessage("4", toolControlLoginStart, args))
	res = resultOf(t, onlyReply(t, wire))
	if res.IsError {
		t.Fatalf("create failed: %s", text(t, res))
	}
	req = flow.ControlRequest{}
	if e = json.Unmarshal([]byte(text(t, res)), &req); e != nil || req.UserCode != "ABCD-EFGH" || req.Prepared {
		t.Fatalf("invalid start result: %v", e)
	}
	repeat := controlCall(t, s, callMessage("7", toolControlLoginStart, args))
	repeated := resultOf(t, onlyReply(t, repeat))
	var again flow.ControlRequest
	if repeated.IsError || json.Unmarshal([]byte(text(t, repeated)), &again) != nil || again.Handle != req.Handle || again.VerificationURI != req.VerificationURI || createCalls.Load() != 1 {
		t.Fatal("repeated start changed or recreated the request")
	}
	var storedProof string
	if e = config.ControlWithLock(srv.URL, func(st *config.ControlStore) error {
		state, e := st.Load()
		if e != nil {
			return e
		}
		p := state.Pending[req.Handle]
		storedProof = string(p.Proof)
		p.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		state.Pending[req.Handle] = p
		return st.Save(state)
	}); e != nil {
		t.Fatal(e)
	}
	if storedProof == "" || strings.Contains(wire, storedProof) || strings.Contains(preparedWire, storedProof) || strings.Contains(repeat, storedProof) {
		t.Fatal("proof appeared in output")
	}
	malformedProof := controlCall(t, s, callMessage("8", toolControlLoginStart, fmt.Sprintf(`{"%s":"value"}`, storedProof)))
	if strings.Contains(malformedProof, storedProof) {
		t.Fatal("proof echoed in argument error")
	}
	malformed := controlCall(t, s, callMessage("5", toolControlLoginStart, `{"sensitive-key":"value"}`))
	if strings.Contains(malformed, "sensitive-key") {
		t.Fatal("unknown key echoed")
	}
	args = fmt.Sprintf(`{"control_url":%q,"handle":%q}`, srv.URL, req.Handle)
	wire = controlCall(t, s, callMessage("2", toolControlLoginVerify, args))
	if strings.Contains(wire, "ICEiIyQlJicoKSor") {
		t.Fatal("credential in output")
	}
	res = resultOf(t, onlyReply(t, wire))
	if res.IsError || !strings.Contains(text(t, res), "user-one") {
		t.Fatalf("verify failed: %s", text(t, res))
	}
	replayWire := controlCall(t, s, callMessage("10", toolControlLoginVerify, args))
	replay := resultOf(t, onlyReply(t, replayWire))
	if replay.IsError || !strings.Contains(text(t, replay), `"recovered_from_storage": true`) || pollCalls.Load() != 1 {
		t.Fatal("stored recovery was not labeled or repolled")
	}
	args = fmt.Sprintf(`{"control_url":%q,"credential_id":"clcred_QkJCQkJCQkJCQkJCQkJCQg"}`, srv.URL)
	wire = controlCall(t, s, callMessage("3", toolControlLogout, args))
	if strings.Contains(wire, "ICEiIyQlJicoKSor") {
		t.Fatal("credential in logout output")
	}
	res = resultOf(t, onlyReply(t, wire))
	if res.IsError || !strings.Contains(text(t, res), "revoked") {
		t.Fatalf("logout failed: %s", text(t, res))
	}
	controlResume = func(_ context.Context, origin, handle string) (flow.ControlRequest, error) {
		return flow.ControlRequest{Handle: handle, ControlURL: origin}, &flow.ControlWait{After: 60 * time.Second}
	}
	wire = controlCall(t, s, callMessage("9", toolControlLoginStart, argsForControlHandle(srv.URL, req.Handle)))
	res = resultOf(t, onlyReply(t, wire))
	var waiting struct {
		Status     string `json:"status"`
		Handle     string `json:"handle"`
		ControlURL string `json:"control_url"`
		NextTool   string `json:"next_tool"`
		RetryAfter int    `json:"retry_after_seconds"`
	}
	if res.IsError || json.Unmarshal([]byte(text(t, res)), &waiting) != nil || waiting.Status != "waiting" || waiting.Handle != req.Handle || waiting.ControlURL != srv.URL || waiting.NextTool != toolControlLoginStart || waiting.RetryAfter != 60 {
		t.Fatal("throttle response was not actionable")
	}
}

func TestControlLoginMCPResumeAfterLostCreate(t *testing.T) {
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("CURIOUS_CONFIG", filepath.Join(dir, "config.json"))
	oldPrepare, oldResume := controlPrepare, controlResume
	defer func() { controlPrepare, controlResume = oldPrepare, oldResume }()
	controlPrepare = func(origin string) (flow.ControlRequest, error) { return flow.PrepareControlLogin(origin, "mcp", true) }
	controlResume = func(ctx context.Context, origin, handle string) (flow.ControlRequest, error) {
		return flow.ResumeControlLogin(ctx, origin, handle, true)
	}
	digests := make(chan string, 2)
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			_ = json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}, IntervalSeconds: 5})
		case controlwire.RequestsPath:
			var q controlwire.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&q)
			digests <- q.ProofSHA256
			if count.Add(1) == 1 {
				h, ok := w.(http.Hijacker)
				if !ok {
					t.Error("no hijacker")
					return
				}
				conn, _, err := h.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := New("curious", "test")
	RegisterTools(s)
	result := resultOf(t, onlyReply(t, controlCall(t, s, callMessage("1", toolControlLoginStart, fmt.Sprintf(`{"control_url":%q}`, srv.URL)))))
	if result.IsError {
		t.Fatal("prepare failed")
	}
	var prepared flow.ControlRequest
	if e = json.Unmarshal([]byte(text(t, result)), &prepared); e != nil || prepared.Handle == "" {
		t.Fatal("missing prepared handle")
	}
	args := fmt.Sprintf(`{"control_url":%q,"handle":%q}`, srv.URL, prepared.Handle)
	result = resultOf(t, onlyReply(t, controlCall(t, s, callMessage("2", toolControlLoginStart, args))))
	if !result.IsError || !strings.Contains(text(t, result), prepared.Handle) {
		t.Fatal("lost creation was not recoverable")
	}
	result = resultOf(t, onlyReply(t, controlCall(t, s, callMessage("4", toolControlLoginStart, args))))
	var waiting struct {
		Status     string `json:"status"`
		Handle     string `json:"handle"`
		RetryAfter int    `json:"retry_after_seconds"`
	}
	if result.IsError || json.Unmarshal([]byte(text(t, result)), &waiting) != nil || waiting.Status != "waiting" || waiting.Handle != prepared.Handle || waiting.RetryAfter < 1 {
		t.Fatalf("lost create did not preserve its normal retry floor: %s", text(t, result))
	}
	if e = config.ControlWithLock(srv.URL, func(store *config.ControlStore) error {
		st, err := store.Load()
		if err != nil {
			return err
		}
		p := st.Pending[prepared.Handle]
		if p.Unresolved {
			return fmt.Errorf("healthy transport loss left an unresolved marker")
		}
		p.NextCreateAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		st.Pending[prepared.Handle] = p
		return store.Save(st)
	}); e != nil {
		t.Fatal(e)
	}
	result = resultOf(t, onlyReply(t, controlCall(t, s, callMessage("3", toolControlLoginStart, args))))
	if result.IsError {
		t.Fatalf("resume failed: %s", text(t, result))
	}
	var resumed flow.ControlRequest
	if e = json.Unmarshal([]byte(text(t, result)), &resumed); e != nil || resumed.Handle != prepared.Handle || resumed.UserCode != "ABCD-EFGH" {
		t.Fatal("resume changed request")
	}
	first, second := <-digests, <-digests
	if first != second {
		t.Fatal("resume changed proof")
	}
}

func argsForControlHandle(origin, handle string) string {
	return fmt.Sprintf(`{"control_url":%q,"handle":%q}`, origin, handle)
}

func TestControlLoginMCPRejectsFieldAliases(t *testing.T) {
	for _, raw := range []string{
		`{"control_url":"https://first.example.test","CONTROL_URL":"https://second.example.test","local_only":true}`,
		`{"control_url":"https://first.example.test","local_only":true,"LOCAL_ONLY":false}`,
		`{"control_url":"https://first.example.test","local_only":null}`,
	} {
		var args controlLogoutArgs
		if decodeControlArgs(json.RawMessage(raw), &args) == nil {
			t.Fatal("accepted ambiguous or null arguments")
		}
	}
}
