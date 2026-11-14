package flow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/controlwire"
)

// Tests retain the handle even when creation returns an error.
func StartControlLogin(ctx context.Context, origin, kind string, allowLoopback bool) (ControlRequest, error) {
	prepared, e := PrepareControlLogin(origin, kind, allowLoopback)
	if e != nil {
		return prepared, e
	}
	out, e := ResumeControlLogin(ctx, origin, prepared.Handle, allowLoopback)
	if e != nil && out.Handle == "" {
		out = prepared
	}
	return out, e
}

func controlFixture(t *testing.T, firstLost, firstPollLost bool, onPoll func()) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	digests := []string{}
	polls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			_ = json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}, IntervalSeconds: 5})
		case controlwire.RequestsPath:
			var q controlwire.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&q)
			// Independently observe the proof on disk before the server
			// accepts creation, without acquiring the caller's lock.
			paths, _ := filepath.Glob(filepath.Join(filepath.Dir(os.Getenv("CURIOUS_CONFIG")), "control", "*.json"))
			present := false
			for _, path := range paths {
				b, e := os.ReadFile(path)
				if e != nil {
					continue
				}
				var state struct {
					Pending map[string]struct {
						Proof string `json:"proof"`
					} `json:"pending"`
				}
				if json.Unmarshal(b, &state) != nil {
					continue
				}
				for _, pending := range state.Pending {
					raw, e := base64.RawURLEncoding.DecodeString(pending.Proof)
					if e == nil {
						digest := sha256.Sum256(raw)
						if base64.RawURLEncoding.EncodeToString(digest[:]) == q.ProofSHA256 {
							present = true
						}
					}
				}
			}
			if !present {
				t.Error("proof was not durable before create")
			}
			mu.Lock()
			digests = append(digests, q.ProofSHA256)
			count := len(digests)
			requestID := "clreq_QUFBQUFBQUFBQUFBQUFBQQ"
			code := "ABCD-EFGH"
			if q.ProofSHA256 != digests[0] {
				requestID = "clreq_TU1NTU1NTU1NTU1NTU1NTQ"
				code = "JKLM-NPQR"
			}
			mu.Unlock()
			if firstLost && count == 1 {
				h, ok := w.(http.Hijacker)
				if !ok {
					t.Error("no hijacker")
					return
				}
				conn, _, e := h.Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				_ = conn.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: requestID, UserCode: code, VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=" + code, ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
		case controlwire.PollPath:
			var q controlwire.PollRequest
			_ = json.NewDecoder(r.Body).Decode(&q)
			mu.Lock()
			polls = append(polls, q.Proof)
			pollCount := len(polls)
			mu.Unlock()
			if firstPollLost && pollCount == 1 {
				h, ok := w.(http.Hijacker)
				if !ok {
					t.Error("no hijacker")
					return
				}
				conn, _, e := h.Hijack()
				if e != nil {
					t.Error(e)
					return
				}
				_ = conn.Close()
				return
			}
			if onPoll != nil {
				onPoll()
			}
			_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1.clcred_QkJCQkJCQkJCQkJCQkJCQg.ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8", CredentialID: "clcred_QkJCQkJCQkJCQkJCQkJCQg", UserID: "user-one", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
		case controlwire.RevokePath:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"authority_unavailable"}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	return srv, &digests, &polls
}
func controlFlowTestConfig(t *testing.T) {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("CURIOUS_CONFIG", filepath.Join(dir, "config.json"))
}
func TestControlLoginRestartAndLostResponse(t *testing.T) {
	controlFlowTestConfig(t)
	srv, digests, polls := controlFixture(t, true, false, nil)
	defer srv.Close()
	ctx := context.Background()
	first, err := StartControlLogin(ctx, srv.URL, "cli", true)
	if err == nil || first.Handle == "" {
		t.Fatalf("lost response not retained: %+v %v", first, err)
	}
	if err = config.ControlWithLock(srv.URL, func(store *config.ControlStore) error {
		st, e := store.Load()
		if e != nil {
			return e
		}
		p := st.Pending[first.Handle]
		p.NextCreateAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		st.Pending[first.Handle] = p
		return store.Save(st)
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeControlLogin(ctx, srv.URL, first.Handle, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(*digests) != 2 || (*digests)[0] != (*digests)[1] {
		t.Fatal("retry changed proof")
	}
	if resumed.UserCode != "ABCD-EFGH" {
		t.Fatal("missing recovered request")
	}
	if err = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		p := st.Pending[first.Handle]
		p.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		st.Pending[first.Handle] = p
		return s.Save(st)
	}); err != nil {
		t.Fatal(err)
	}
	account, err := VerifyControlLogin(ctx, srv.URL, first.Handle, true)
	if err != nil || account.UserID != "user-one" {
		t.Fatalf("verify: %+v %v", account, err)
	}
	recovered, e := ResumeControlLogin(ctx, srv.URL, first.Handle, true)
	if e != nil || !recovered.Completed || recovered.UserID != "user-one" {
		t.Fatalf("saved result not recoverable: %v", e)
	}
	replay, e := VerifyControlLogin(ctx, srv.URL, first.Handle, true)
	if e != nil || replay.UserID != "user-one" || !replay.RecoveredFromStorage || len(*polls) != 1 {
		t.Fatalf("saved result changed or repolled: %v", e)
	}
	if err = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if len(st.Pending) != 0 || len(st.Credentials) != 1 {
			return fmt.Errorf("incorrect state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestControlLoginLostPollResponse(t *testing.T) {
	controlFlowTestConfig(t)
	srv, _, proofs := controlFixture(t, false, true, nil)
	defer srv.Close()
	ctx := context.Background()
	request, e := StartControlLogin(ctx, srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	ready := func() {
		t.Helper()
		if e := config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			p := st.Pending[request.Handle]
			p.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			st.Pending[request.Handle] = p
			return s.Save(st)
		}); e != nil {
			t.Fatal(e)
		}
	}
	ready()
	if _, e = VerifyControlLogin(ctx, srv.URL, request.Handle, true); e == nil {
		t.Fatal("lost poll response reported success")
	}
	ready()
	account, e := VerifyControlLogin(ctx, srv.URL, request.Handle, true)
	if e != nil || account.UserID != "user-one" {
		t.Fatalf("recovery failed: %v", e)
	}
	if len(*proofs) != 2 || (*proofs)[0] != (*proofs)[1] {
		t.Fatal("poll retry changed proof")
	}
}
func TestControlLoginSaveFailureRecovery(t *testing.T) {
	controlFlowTestConfig(t)
	root := filepath.Join(filepath.Dir(os.Getenv("CURIOUS_CONFIG")), "control")
	var once sync.Once
	var statePath string
	srv, _, polls := controlFixture(t, false, false, func() {
		once.Do(func() {
			if e := os.Rename(statePath, statePath+".backup"); e != nil {
				t.Error(e)
				return
			}
			if e := os.Mkdir(statePath, 0700); e != nil {
				t.Error(e)
			}
		})
	})
	defer srv.Close()
	ctx := context.Background()
	request, e := StartControlLogin(ctx, srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	paths, e := filepath.Glob(filepath.Join(root, "*.json"))
	if e != nil || len(paths) != 1 {
		t.Fatal("missing state file")
	}
	statePath = paths[0]
	ready := func() {
		t.Helper()
		if e := config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			p := st.Pending[request.Handle]
			p.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			st.Pending[request.Handle] = p
			return s.Save(st)
		}); e != nil {
			t.Fatal(e)
		}
	}
	ready()
	if _, e = VerifyControlLogin(ctx, srv.URL, request.Handle, true); e == nil {
		t.Fatal("reported success without saving credential")
	}
	if e = os.Remove(statePath); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(statePath+".backup", statePath); e != nil {
		t.Fatal(e)
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if st.Pending[request.Handle].Proof == "" {
			return fmt.Errorf("recovery proof lost")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = VerifyControlLogin(ctx, srv.URL, request.Handle, true); !errors.Is(e, errControlUncertain) {
		t.Fatalf("ambiguous outcome did not fail safely: %v", e)
	}
	if len(*polls) != 1 {
		t.Fatal("ambiguous outcome sent another poll")
	}
}

func TestControlLogoutTruthfulness(t *testing.T) {
	t.Run("remote revocation preserves unrelated state", func(t *testing.T) {
		controlFlowTestConfig(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != controlwire.RevokePath || r.Header.Get("Authorization") != "Bearer synthetic-one" {
				t.Error("wrong credential or path")
			}
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(204)
		}))
		defer srv.Close()
		if e := config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			st.Credentials["one"] = config.ControlCredential{CredentialID: "one", UserID: "first", Token: "synthetic-one"}
			st.Credentials["two"] = config.ControlCredential{CredentialID: "two", UserID: "second", Token: "synthetic-two"}
			st.Pending["other"] = config.ControlPending{Handle: "other", Kind: "cli", Proof: "synthetic"}
			return s.Save(st)
		}); e != nil {
			t.Fatal(e)
		}
		if e := LogoutControl(context.Background(), srv.URL, "one", false, true); e != nil {
			t.Fatal(e)
		}
		if e := config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			if len(st.Credentials) != 1 || st.Credentials["two"].UserID != "second" || len(st.Pending) != 1 {
				return fmt.Errorf("unrelated state changed")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	})
	controlFlowTestConfig(t)
	srv, _, _ := controlFixture(t, false, false, nil)
	defer srv.Close()
	ctx := context.Background()
	request, err := StartControlLogin(ctx, srv.URL, "cli", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		p := st.Pending[request.Handle]
		p.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		st.Pending[request.Handle] = p
		return s.Save(st)
	}); err != nil {
		t.Fatal(err)
	}
	account, err := VerifyControlLogin(ctx, srv.URL, request.Handle, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = LogoutControl(ctx, srv.URL, account.CredentialID, false, true); err == nil {
		t.Fatal("reported remote revocation during outage")
	}
	if err = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if len(st.Credentials) != 1 {
			return fmt.Errorf("credential lost after outage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = LogoutControl(ctx, srv.URL, account.CredentialID, true, true); err != nil {
		t.Fatal(err)
	}
}

// Separate processes saving and logging out must preserve unrelated state.
func TestControlLoginConcurrentProcesses(t *testing.T) {
	mode := os.Getenv("CURIOUS_FLOW_CONTROL_CHILD")
	if mode != "" {
		if mode == "logout" {
			if e := LogoutControl(context.Background(), "https://example.test", "old", true, false); e != nil {
				t.Fatal(e)
			}
			return
		}
		e := config.ControlWithLock("https://example.test", func(s *config.ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			st.Pending["new"] = config.ControlPending{Handle: "new", Kind: "cli", Proof: "synthetic"}
			st.Credentials["new"] = config.ControlCredential{CredentialID: "new", UserID: "user-new", Token: "synthetic-token"}
			return s.Save(st)
		})
		if e != nil {
			t.Fatal(e)
		}
		return
	}
	controlFlowTestConfig(t)
	e := config.ControlWithLock("https://example.test", func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		st.Credentials["old"] = config.ControlCredential{CredentialID: "old", UserID: "user-old", Token: "synthetic-token"}
		st.Credentials["keep"] = config.ControlCredential{CredentialID: "keep", UserID: "user-keep", Token: "synthetic-token"}
		st.Pending["keep"] = config.ControlPending{Handle: "keep", Kind: "cli", Proof: "synthetic"}
		return s.Save(st)
	})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, mode := range []string{"logout", "save"} {
		wg.Add(1)
		go func(mode string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlLoginConcurrentProcesses$", "-test.count=1")
			cmd.Env = append(os.Environ(), "CURIOUS_FLOW_CONTROL_CHILD="+mode)
			out, e := cmd.CombinedOutput()
			if e != nil {
				errs <- fmt.Errorf("child failed: %v: %s", e, out)
			}
		}(mode)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if t.Failed() {
		return
	}
	if e = config.ControlWithLock("https://example.test", func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if len(st.Credentials) != 2 || st.Credentials["keep"].UserID != "user-keep" || st.Credentials["new"].UserID != "user-new" || len(st.Pending) != 2 {
			return fmt.Errorf("unrelated state changed")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestControlLoginPollingInterval(t *testing.T) {
	controlFlowTestConfig(t)
	srv, _, polls := controlFixture(t, false, false, nil)
	defer srv.Close()
	ctx := context.Background()
	request, e := StartControlLogin(ctx, srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = VerifyControlLogin(ctx, srv.URL, request.Handle, true); e == nil {
		t.Fatal("polled before the interval")
	} else {
		var wait *ControlWait
		if !errors.As(e, &wait) || wait.After <= 0 {
			t.Fatalf("missing wait: %v", e)
		}
	}
	if len(*polls) != 0 {
		t.Fatal("early poll reached server")
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		p := st.Pending[request.Handle]
		p.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
		st.Pending[request.Handle] = p
		return s.Save(st)
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = ResumeControlLogin(ctx, srv.URL, request.Handle, true); e == nil {
		t.Fatal("resumed expired request")
	}
	if _, e = VerifyControlLogin(ctx, srv.URL, request.Handle, true); e == nil {
		t.Fatal("polled expired request")
	}
	if len(*polls) != 0 {
		t.Fatal("expired request reached server")
	}
}

func TestControlLoginCredentialSubstitutionRefused(t *testing.T) {
	controlFlowTestConfig(t)
	srv, _, _ := controlFixture(t, false, false, nil)
	defer srv.Close()
	ctx := context.Background()
	first, e := StartControlLogin(ctx, srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	second, e := StartControlLogin(ctx, srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	ready := func(handle string) {
		t.Helper()
		if e := config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			p := st.Pending[handle]
			p.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			st.Pending[handle] = p
			return s.Save(st)
		}); e != nil {
			t.Fatal(e)
		}
	}
	ready(first.Handle)
	if _, e = VerifyControlLogin(ctx, srv.URL, first.Handle, true); e != nil {
		t.Fatal(e)
	}
	ready(second.Handle)
	if _, e = VerifyControlLogin(ctx, srv.URL, second.Handle, true); e == nil {
		t.Fatal("accepted another request's credential")
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if len(st.Credentials) != 1 || st.Pending[second.Handle].Proof == "" {
			return fmt.Errorf("account or recoverable request changed")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestControlLoginThrottlePersistence(t *testing.T) {
	controlFlowTestConfig(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"slow_down","retry_after_seconds":60}`))
	}))
	defer srv.Close()
	handle := "QUFBQUFBQUFBQUFBQUFBQQ"
	proof := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	if e := config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		st.Pending[handle] = config.ControlPending{Handle: handle, Kind: "cli", Proof: ui.Secret(proof), RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: srv.URL + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), NextPollAt: time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)}
		return s.Save(st)
	}); e != nil {
		t.Fatal(e)
	}
	_, e := VerifyControlLogin(context.Background(), srv.URL, handle, true)
	var wait *ControlWait
	if !errors.As(e, &wait) || wait.After != 60*time.Second {
		t.Fatalf("wrong throttling result: %v", e)
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		next, e := time.Parse(time.RFC3339Nano, st.Pending[handle].NextPollAt)
		if e != nil || time.Until(next) < 58*time.Second {
			return fmt.Errorf("retry deadline not stored")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	_, e = VerifyControlLogin(context.Background(), srv.URL, handle, true)
	if !errors.As(e, &wait) || calls.Load() != 1 {
		t.Fatal("throttled request was retried too early")
	}
}

func TestControlLoginCreateThrottlePersistence(t *testing.T) {
	controlFlowTestConfig(t)
	var creates atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			_ = json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}, IntervalSeconds: 5})
		case controlwire.RequestsPath:
			creates.Add(1)
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"slow_down","retry_after_seconds":60}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	prepared, e := PrepareControlLogin(srv.URL, "mcp", true)
	if e != nil {
		t.Fatal(e)
	}
	result, e := ResumeControlLogin(context.Background(), srv.URL, prepared.Handle, true)
	var wait *ControlWait
	if !errors.As(e, &wait) || wait.After != 60*time.Second || result.Handle != prepared.Handle || result.ControlURL != srv.URL {
		t.Fatalf("wrong throttle result: %v", e)
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		next, e := time.Parse(time.RFC3339Nano, st.Pending[prepared.Handle].NextCreateAt)
		if e != nil || time.Until(next) < 58*time.Second {
			return fmt.Errorf("create retry deadline not stored")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	_, e = ResumeControlLogin(context.Background(), srv.URL, prepared.Handle, true)
	if !errors.As(e, &wait) || creates.Load() != 1 {
		t.Fatal("creation retried before deadline")
	}
}

func TestControlLoginFailureTruthfulness(t *testing.T) {
	for _, tc := range []struct {
		err       error
		word      string
		resumable bool
	}{{&api.ControlError{Code: "access_denied"}, "denied", false}, {&api.ControlError{Code: "request_expired"}, "expired", false}, {&api.ControlError{Code: "restart_required"}, "revoked", false}, {&api.ControlError{Code: "authority_unavailable"}, "unavailable", true}, {errControlExpired, "expired", false}, {errControlCredentialExpired, "credential expired", false}, {errors.New("synthetic-secret"), "could not be completed", true}} {
		message, resumable := ControlFailure(tc.err)
		if !strings.Contains(message, tc.word) || resumable != tc.resumable || strings.Contains(message, "synthetic-secret") {
			t.Fatalf("incorrect failure description for %s", tc.word)
		}
	}
}

func TestControlLoginMissingCapabilityNoCreate(t *testing.T) {
	controlFlowTestConfig(t)
	var creates atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Path == controlwire.RequestsPath {
			creates.Add(1)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"authority_unavailable"}`))
	}))
	defer srv.Close()
	prepared, e := PrepareControlLogin(srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	result, e := ResumeControlLogin(context.Background(), srv.URL, prepared.Handle, true)
	if e == nil || result.Handle != prepared.Handle || creates.Load() != 0 {
		t.Fatal("missing capability did not stop creation")
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if st.Pending[prepared.Handle].Proof == "" {
			return fmt.Errorf("pending proof lost")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestControlLoginRejectsSecretInAccountLabel(t *testing.T) {
	for _, kind := range []string{"proof", "credential", "credential-secret"} {
		t.Run(kind, func(t *testing.T) {
			controlFlowTestConfig(t)
			const token = "ccl1.clcred_QkJCQkJCQkJCQkJCQkJCQg.ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8"
			const proof = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
			const handle = "QUFBQUFBQUFBQUFBQUFBQQ"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request controlwire.PollRequest
				_ = json.NewDecoder(r.Body).Decode(&request)
				label := request.Proof
				if kind == "credential" {
					label = token
				}
				if kind == "credential-secret" {
					label = strings.Split(token, ".")[2]
				}
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: token, CredentialID: "clcred_QkJCQkJCQkJCQkJCQkJCQg", UserID: label, ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
			}))
			defer srv.Close()
			if e := config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
				st, e := s.Load()
				if e != nil {
					return e
				}
				st.Pending[handle] = config.ControlPending{Handle: handle, Kind: "cli", Proof: ui.Secret(proof), RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: srv.URL + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), NextPollAt: time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)}
				return s.Save(st)
			}); e != nil {
				t.Fatal(e)
			}
			_, e := VerifyControlLogin(context.Background(), srv.URL, handle, true)
			if e == nil || strings.Contains(e.Error(), proof) || strings.Contains(e.Error(), token) {
				t.Fatal("secret-bearing account label was accepted or exposed")
			}
			if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
				st, e := s.Load()
				if e != nil {
					return e
				}
				if st.Pending[handle].Proof == "" || len(st.Credentials) != 0 {
					return fmt.Errorf("invalid response changed credential state")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		})
	}
}

// An unresolved marker represents either an issuance or a throttle whose
// response could not be saved; neither permits another early poll.
func TestControlLoginUncertainThrottleAndIssuance(t *testing.T) {
	for _, outcome := range []string{"throttle", "issued"} {
		t.Run(outcome, func(t *testing.T) {
			controlFlowTestConfig(t)
			var statePath string
			var polls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				switch r.URL.Path {
				case controlwire.CapabilitiesPath:
					_ = json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}, IntervalSeconds: 5})
				case controlwire.RequestsPath:
					_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
				case controlwire.PollPath:
					polls.Add(1)
					if e := os.Rename(statePath, statePath+".backup"); e != nil {
						t.Error(e)
						return
					}
					if e := os.Mkdir(statePath, 0700); e != nil {
						t.Error(e)
						return
					}
					if outcome == "throttle" {
						w.Header().Set("Retry-After", "600")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = w.Write([]byte(`{"error":"slow_down","retry_after_seconds":600}`))
					} else {
						_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1.clcred_QkJCQkJCQkJCQkJCQkJCQg.ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8", CredentialID: "clcred_QkJCQkJCQkJCQkJCQkJCQg", UserID: "user-one", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
					}
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			ctx := context.Background()
			req, e := StartControlLogin(ctx, srv.URL, "cli", true)
			if e != nil {
				t.Fatal(e)
			}
			paths, _ := filepath.Glob(filepath.Join(filepath.Dir(os.Getenv("CURIOUS_CONFIG")), "control", "*.json"))
			if len(paths) != 1 {
				t.Fatal("missing state")
			}
			statePath = paths[0]
			var proof ui.Secret
			if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
				st, err := s.Load()
				if err != nil {
					return err
				}
				p := st.Pending[req.Handle]
				proof = p.Proof
				p.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
				st.Pending[req.Handle] = p
				return s.Save(st)
			}); e != nil {
				t.Fatal(e)
			}
			if _, e = VerifyControlLogin(ctx, srv.URL, req.Handle, true); e == nil {
				t.Fatal("outcome reported success despite save failure")
			}
			if e = os.Remove(statePath); e != nil {
				t.Fatal(e)
			}
			if e = os.Rename(statePath+".backup", statePath); e != nil {
				t.Fatal(e)
			}
			if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
				st, err := s.Load()
				if err != nil {
					return err
				}
				p := st.Pending[req.Handle]
				if !p.Unresolved || p.Proof != proof || len(st.Credentials) != 0 {
					return errors.New("ambiguous state was not preserved")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			childCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestControlLoginUnresolvedRestart$", "-test.count=1")
			cmd.Env = append(os.Environ(), "CURIOUS_UNRESOLVED_ORIGIN="+srv.URL, "CURIOUS_UNRESOLVED_HANDLE="+req.Handle, "CURIOUS_UNRESOLVED_MODE=poll")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("restart did not refuse uncertain outcome: %v: %s", err, output)
			}
			if _, e = VerifyControlLogin(ctx, srv.URL, req.Handle, true); !errors.Is(e, errControlUncertain) {
				t.Fatalf("uncertain outcome was not refused: %v", e)
			}
			if polls.Load() != 1 {
				t.Fatalf("unexpected extra polls: %d", polls.Load())
			}
		})
	}
}

func TestControlLoginUnresolvedRestart(t *testing.T) {
	if origin := os.Getenv("CURIOUS_UNRESOLVED_ORIGIN"); origin != "" {
		var e error
		if os.Getenv("CURIOUS_UNRESOLVED_MODE") == "poll" {
			_, e = VerifyControlLogin(context.Background(), origin, os.Getenv("CURIOUS_UNRESOLVED_HANDLE"), true)
		} else {
			_, e = ResumeControlLogin(context.Background(), origin, os.Getenv("CURIOUS_UNRESOLVED_HANDLE"), true)
		}
		if !errors.Is(e, errControlUncertain) {
			t.Fatalf("restart did not refuse uncertain operation: %v", e)
		}
		return
	}
	controlFlowTestConfig(t)
	srv, creates, _ := controlFixture(t, false, false, nil)
	defer srv.Close()
	prepared, e := PrepareControlLogin(srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	var savedProof ui.Secret
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, err := s.Load()
		if err != nil {
			return err
		}
		p := st.Pending[prepared.Handle]
		savedProof = p.Proof
		p.Unresolved = true
		st.Pending[prepared.Handle] = p
		return s.Save(st)
	}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlLoginUnresolvedRestart$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CURIOUS_UNRESOLVED_ORIGIN="+srv.URL, "CURIOUS_UNRESOLVED_HANDLE="+prepared.Handle)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restart: %v: %s", err, out)
	}
	if len(*creates) != 0 {
		t.Fatal("uncertain restart created a request")
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, err := s.Load()
		if err != nil {
			return err
		}
		p := st.Pending[prepared.Handle]
		if !p.Unresolved || p.Proof != savedProof {
			return errors.New("uncertain identity was lost")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestControlLoginCredentialSavedBeforeCleanup(t *testing.T) {
	controlFlowTestConfig(t)
	srv, _, polls := controlFixture(t, false, false, nil)
	defer srv.Close()
	req, e := StartControlLogin(context.Background(), srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, err := s.Load()
		if err != nil {
			return err
		}
		p := st.Pending[req.Handle]
		st.Credentials["id"] = config.ControlCredential{CredentialID: "id", RequestHandle: req.Handle, RequestID: p.RequestID, UserID: "saved-user", Token: "saved-token", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
		return s.Save(st)
	}); e != nil {
		t.Fatal(e)
	}
	account, e := VerifyControlLogin(context.Background(), srv.URL, req.Handle, true)
	if e != nil || !account.RecoveredFromStorage || account.UserID != "saved-user" || len(*polls) != 0 {
		t.Fatalf("durable credential recovery: %+v %v", account, e)
	}
}

func TestControlLoginMarkerSaveFailureSendsNoCreate(t *testing.T) {
	controlFlowTestConfig(t)
	var path string
	var creates atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			if err := os.Rename(path, path+".backup"); err != nil {
				t.Error(err)
				return
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Error(err)
				return
			}
			_ = json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}, IntervalSeconds: 5})
		case controlwire.RequestsPath:
			creates.Add(1)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	prepared, e := PrepareControlLogin(srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(os.Getenv("CURIOUS_CONFIG")), "control", "*.json"))
	if len(paths) != 1 {
		t.Fatal("missing state")
	}
	path = paths[0]
	if _, e = ResumeControlLogin(context.Background(), srv.URL, prepared.Handle, true); e == nil {
		t.Fatal("failed marker save was ignored")
	}
	if creates.Load() != 0 {
		t.Fatal("create was sent after failed marker save")
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(path+".backup", path); e != nil {
		t.Fatal(e)
	}
	if e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, err := s.Load()
		if err != nil {
			return err
		}
		p := st.Pending[prepared.Handle]
		if p.Proof == "" || p.Unresolved {
			return errors.New("prepared proof changed after failed marker save")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func controlExpiryCapabilities(w http.ResponseWriter) {
	json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, IntervalSeconds: 5, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}})
}
func TestControlLoginExpiryDuringCapabilities(t *testing.T) {
	controlFlowTestConfig(t)
	deadline := time.Now().UTC().Add(2 * time.Second).Truncate(time.Second)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Path == controlwire.CapabilitiesPath {
			time.Sleep(time.Until(deadline.Add(20 * time.Millisecond)))
			controlExpiryCapabilities(w)
			return
		}
		if r.URL.Path == controlwire.RequestsPath {
			calls.Add(1)
			json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
			return
		}
		t.Error("unexpected route")
	}))
	defer srv.Close()
	p, e := PrepareControlLogin(srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		x := st.Pending[p.Handle]
		x.ExpiresAt = deadline.Format(time.RFC3339)
		st.Pending[p.Handle] = x
		return s.Save(st)
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = ResumeControlLogin(context.Background(), srv.URL, p.Handle, true)
	t.Logf("create_calls_after_deadline=%d outcome=%v", calls.Load(), e)
	if calls.Load() != 0 {
		t.Fatal("create reused proof after original stored deadline")
	}
}
func TestControlLoginTruncatedThrottleRetainsDelay(t *testing.T) {
	controlFlowTestConfig(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Retry-After", "600")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"slow_down","retry_after_seconds":600}`)
	}))
	defer srv.Close()
	p, e := PrepareControlLogin(srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		x := st.Pending[p.Handle]
		x.RequestID = "clreq_QUFBQUFBQUFBQUFBQUFBQQ"
		x.UserCode = "ABCD-EFGH"
		x.VerificationURI = srv.URL + controlwire.VerificationPath + "?code=ABCD-EFGH"
		x.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		st.Pending[p.Handle] = x
		return s.Save(st)
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = VerifyControlLogin(context.Background(), srv.URL, p.Handle, true)
	t.Logf("calls=%d outcome=%v", calls.Load(), e)
	e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		x := st.Pending[p.Handle]
		next, _ := time.Parse(time.RFC3339Nano, x.NextPollAt)
		t.Logf("unresolved=%t persisted_wait_seconds=%.3f", x.Unresolved, time.Until(next).Seconds())
		if !x.Unresolved && time.Until(next) < 590*time.Second {
			t.Error("known429/600 became resolved ordinary5second retry after body read error")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestIndependentCredentialSecretLabel(t *testing.T) {
	controlFlowTestConfig(t)
	const secret = "ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8"
	const id = "clcred_QkJCQkJCQkJCQkJCQkJCQg"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1." + id + "." + secret, CredentialID: id, UserID: secret, ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
	}))
	defer srv.Close()
	p, e := PrepareControlLogin(srv.URL, "cli", true)
	if e != nil {
		t.Fatal(e)
	}
	e = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		x := st.Pending[p.Handle]
		x.RequestID = "clreq_QUFBQUFBQUFBQUFBQUFBQQ"
		x.UserCode = "ABCD-EFGH"
		x.VerificationURI = srv.URL + controlwire.VerificationPath + "?code=ABCD-EFGH"
		x.NextPollAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
		st.Pending[p.Handle] = x
		return s.Save(st)
	})
	if e != nil {
		t.Fatal(e)
	}
	a, e := VerifyControlLogin(context.Background(), srv.URL, p.Handle, true)
	if e == nil && a.UserID == secret && a.CredentialID == id {
		t.Fatal("accepted secret component as displayable UserID: output has everything needed to reconstruct full credential")
	}
}

func TestControlLoginConcurrentAccountProcesses(t *testing.T) {
	if origin := os.Getenv("CURIOUS_LOGIN_ACCOUNT_ORIGIN"); origin != "" {
		req, err := StartControlLogin(context.Background(), origin, "cli", true)
		if err != nil {
			t.Fatal(err)
		}
		var proofDigest string
		if err = config.ControlWithLock(origin, func(store *config.ControlStore) error {
			st, e := store.Load()
			if e != nil {
				return e
			}
			p := st.Pending[req.Handle]
			raw, err := base64.RawURLEncoding.DecodeString(string(p.Proof))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			proofDigest = base64.RawURLEncoding.EncodeToString(sum[:])
			p.NextPollAt = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
			st.Pending[req.Handle] = p
			return store.Save(st)
		}); err != nil {
			t.Fatal(err)
		}
		account, err := VerifyControlLogin(context.Background(), origin, req.Handle, true)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(struct{ Handle, Digest, UserID, CredentialID string }{req.Handle, proofDigest, account.UserID, account.CredentialID})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(os.Getenv("CURIOUS_LOGIN_ACCOUNT_RESULT"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	controlFlowTestConfig(t)
	var mu sync.Mutex
	digests := map[string]int{}
	byID := map[string]string{}
	var mismatches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			controlExpiryCapabilities(w)
		case controlwire.RequestsPath:
			var q controlwire.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&q)
			mu.Lock()
			n, exists := digests[q.ProofSHA256]
			if !exists {
				n = len(digests) + 1
				digests[q.ProofSHA256] = n
			}
			id := "clreq_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('A'+n)), 16)))
			byID[id] = q.ProofSHA256
			mu.Unlock()
			code := "ABCD-EFGH"
			if n == 2 {
				code = "JKLM-NPQR"
			}
			_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: id, UserCode: code, VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=" + code, ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
		case controlwire.PollPath:
			var q controlwire.PollRequest
			_ = json.NewDecoder(r.Body).Decode(&q)
			raw, err := base64.RawURLEncoding.DecodeString(q.Proof)
			if err != nil {
				mismatches.Add(1)
				return
			}
			digest := sha256.Sum256(raw)
			mu.Lock()
			expected := byID[q.RequestID]
			n := digests[expected]
			mu.Unlock()
			if expected != base64.RawURLEncoding.EncodeToString(digest[:]) || n < 1 || n > 2 {
				mismatches.Add(1)
				return
			}
			id := "clcred_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('K'+n)), 16)))
			secret := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('Q'+n)), 32)))
			_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1." + id + "." + secret, CredentialID: id, UserID: fmt.Sprintf("account-%d", n), ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	resultDir := t.TempDir()
	for index := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlLoginConcurrentAccountProcesses$", "-test.count=1")
			cmd.Env = append(os.Environ(), "CURIOUS_LOGIN_ACCOUNT_ORIGIN="+srv.URL, "CURIOUS_LOGIN_ACCOUNT_RESULT="+filepath.Join(resultDir, fmt.Sprint(index)))
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("child: %v: %s", err, out)
			}
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	if mismatches.Load() != 0 || len(digests) != 2 {
		t.Fatal("proofs were not bound to separate requests")
	}
	if err := config.ControlWithLock(srv.URL, func(store *config.ControlStore) error {
		st, e := store.Load()
		if e != nil {
			return e
		}
		if len(st.Credentials) != 2 || len(st.Completed) != 2 || len(st.Pending) != 0 {
			return errors.New("accounts were not durably preserved")
		}
		for index := range 2 {
			data, err := os.ReadFile(filepath.Join(resultDir, fmt.Sprint(index)))
			if err != nil {
				return err
			}
			var result struct{ Handle, Digest, UserID, CredentialID string }
			if err = json.Unmarshal(data, &result); err != nil {
				return err
			}
			credential, ok := st.Credentials[result.CredentialID]
			if !ok || st.Completed[result.Handle] != result.CredentialID || credential.RequestHandle != result.Handle || credential.UserID != result.UserID || byID[credential.RequestID] != result.Digest {
				return errors.New("child returned an account for a different request")
			}
			n := digests[result.Digest]
			expectedID := "clcred_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('K'+n)), 16)))
			if result.UserID != fmt.Sprintf("account-%d", n) || result.CredentialID != expectedID {
				return errors.New("returned identity does not match its proof")
			}
		}
		users := map[string]bool{}
		for _, c := range st.Credentials {
			users[c.UserID] = true
		}
		if !users["account-1"] || !users["account-2"] {
			return errors.New("account identities changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestControlLoginCrashDuringPoll(t *testing.T) {
	if origin := os.Getenv("CURIOUS_CRASH_POLL_ORIGIN"); origin != "" {
		_, _ = VerifyControlLogin(context.Background(), origin, os.Getenv("CURIOUS_CRASH_POLL_HANDLE"), true)
		return
	}
	for _, outcome := range []string{"throttle", "issuance"} {
		t.Run(outcome, func(t *testing.T) {
			controlFlowTestConfig(t)
			reached := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			finish := func() { releaseOnce.Do(func() { close(release) }) }
			defer finish()
			var polls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				switch r.URL.Path {
				case controlwire.CapabilitiesPath:
					controlExpiryCapabilities(w)
				case controlwire.RequestsPath:
					_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
				case controlwire.PollPath:
					polls.Add(1)
					w.Header().Set("Content-Length", "1000")
					if outcome == "throttle" {
						w.Header().Set("Retry-After", "600")
						w.WriteHeader(429)
						_, _ = w.Write([]byte(`{"error":"slow_down","retry_after_seconds":600}`))
					} else {
						_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1.clcred_QkJCQkJCQkJCQkJCQkJCQg.ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8", CredentialID: "clcred_QkJCQkJCQkJCQkJCQkJCQg", UserID: "user-one", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
					}
					w.(http.Flusher).Flush()
					reached <- struct{}{}
					<-release
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			req, err := StartControlLogin(context.Background(), srv.URL, "cli", true)
			if err != nil {
				t.Fatal(err)
			}
			if err = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
				st, e := s.Load()
				if e != nil {
					return e
				}
				p := st.Pending[req.Handle]
				p.NextPollAt = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
				st.Pending[req.Handle] = p
				return s.Save(st)
			}); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestControlLoginCrashDuringPoll$", "-test.count=1")
			cmd.Env = append(os.Environ(), "CURIOUS_CRASH_POLL_ORIGIN="+srv.URL, "CURIOUS_CRASH_POLL_HANDLE="+req.Handle)
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			select {
			case <-reached:
			case <-time.After(10 * time.Second):
				finish()
				t.Fatal("poll did not reach response barrier")
			}
			if err = cmd.Process.Kill(); err != nil {
				finish()
				t.Fatal(err)
			}
			_ = cmd.Wait()
			finish()
			if _, err = VerifyControlLogin(context.Background(), srv.URL, req.Handle, true); !errors.Is(err, errControlUncertain) {
				t.Fatalf("crash outcome not safely refused: %v", err)
			}
			if polls.Load() != 1 {
				t.Fatal("sent another poll after uncertain crash")
			}
			if err = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
				st, e := s.Load()
				if e != nil {
					return e
				}
				p := st.Pending[req.Handle]
				if !p.Unresolved || p.Proof == "" || len(st.Credentials) != 0 {
					return errors.New("uncertain proof not preserved")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestControlLoginCrashAfterSaveBeforeOutput(t *testing.T) {
	if origin := os.Getenv("CURIOUS_SAVED_ORIGIN"); origin != "" {
		if _, err := VerifyControlLogin(context.Background(), origin, os.Getenv("CURIOUS_SAVED_HANDLE"), true); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("CURIOUS_SAVED_MARKER"), []byte("saved"), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		return
	}
	controlFlowTestConfig(t)
	srv, _, polls := controlFixture(t, false, false, nil)
	defer srv.Close()
	req, err := StartControlLogin(context.Background(), srv.URL, "cli", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.ControlWithLock(srv.URL, func(s *config.ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		p := st.Pending[req.Handle]
		p.NextPollAt = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
		st.Pending[req.Handle] = p
		return s.Save(st)
	}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "saved")
	cmd := exec.Command(os.Args[0], "-test.run=^TestControlLoginCrashAfterSaveBeforeOutput$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CURIOUS_SAVED_ORIGIN="+srv.URL, "CURIOUS_SAVED_HANDLE="+req.Handle, "CURIOUS_SAVED_MARKER="+marker)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err = os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("credential was not saved")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	account, err := VerifyControlLogin(context.Background(), srv.URL, req.Handle, true)
	if err != nil || !account.RecoveredFromStorage || account.UserID != "user-one" || len(*polls) != 1 {
		t.Fatalf("durable result was not recovered: %+v %v", account, err)
	}
}
