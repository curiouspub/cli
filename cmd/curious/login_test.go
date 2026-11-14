package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/pkg/controlwire"
)

type failingHandleWriter struct{ short bool }

func (w failingHandleWriter) Write(b []byte) (int, error) {
	if w.short && len(b) > 0 {
		return len(b) - 1, nil
	}
	return 0, fmt.Errorf("write failed")
}
func TestControlLoginCommand(t *testing.T) {
	if mode := os.Getenv("CURIOUS_CONTROL_CLI_CHILD"); mode != "" {
		controlBrowserOpen = func(string) error { return fmt.Errorf("browser unavailable") }
		controlPrepare = func(origin string) (flow.ControlRequest, error) { return flow.PrepareControlLogin(origin, "cli", true) }
		controlResume = func(ctx context.Context, origin, handle string) (flow.ControlRequest, error) {
			return flow.ResumeControlLogin(ctx, origin, handle, true)
		}
		controlVerify = func(ctx context.Context, origin, handle string) (flow.ControlAccount, error) {
			return flow.VerifyControlLogin(ctx, origin, handle, true)
		}
		controlLogout = func(ctx context.Context, origin, id string, localOnly bool) error {
			return flow.LogoutControl(ctx, origin, id, localOnly, true)
		}

		if mode == "before" {
			controlPrepare = func(origin string) (flow.ControlRequest, error) {
				req, e := flow.PrepareControlLogin(origin, "cli", true)
				if e == nil {
					if err := os.WriteFile(os.Getenv("CURIOUS_CONTROL_MARKER"), []byte("prepared"), 0600); err != nil {
						return req, err
					}
					time.Sleep(30 * time.Second)
				}
				return req, e
			}
			_ = run([]string{"login", "--control-url", os.Getenv("CURIOUS_CONTROL_URL")}, strings.NewReader(""), io.Discard, io.Discard)
			return
		}
		if mode == "after" {
			controlResume = func(context.Context, string, string) (flow.ControlRequest, error) {
				if err := os.WriteFile(os.Getenv("CURIOUS_CONTROL_MARKER"), []byte("output"), 0600); err != nil {
					return flow.ControlRequest{}, err
				}
				time.Sleep(30 * time.Second)
				return flow.ControlRequest{}, fmt.Errorf("interrupted")
			}
		}
		if mode == "after" || mode == "during" {
			output, err := os.OpenFile(os.Getenv("CURIOUS_CONTROL_OUTPUT"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			_ = run([]string{"login", "--control-url", os.Getenv("CURIOUS_CONTROL_URL")}, strings.NewReader(""), io.Discard, output)
			return
		}
		var out, errout bytes.Buffer
		code := run([]string{"login", "--control-url", os.Getenv("CURIOUS_CONTROL_URL")}, strings.NewReader(""), &out, &errout)
		if code != 0 || !strings.Contains(out.String(), "user-one") || !strings.Contains(errout.String(), "ABCD-EFGH") || !strings.Contains(errout.String(), "browser could not be opened") {
			t.Fatalf("login failed: exit=%d stdout=%q stderr=%q", code, out.String(), errout.String())
		}
		if strings.Contains(out.String()+errout.String(), "ICEiIyQlJicoKSor") {
			t.Fatal("secret in output")
		}
		out.Reset()
		errout.Reset()
		code = run([]string{"logout", "--control-url", os.Getenv("CURIOUS_CONTROL_URL")}, strings.NewReader(""), &out, &errout)
		if code != 0 || !strings.Contains(out.String(), "revoked") || strings.Contains(out.String()+errout.String(), "ICEiIyQlJicoKSor") {
			t.Fatal("logout failed or exposed a credential")
		}
		return
	}
	t.Run("handle output failure prevents create", func(t *testing.T) {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprint(short), func(t *testing.T) {
				dir, e := filepath.EvalSymlinks(t.TempDir())
				if e != nil {
					t.Fatal(e)
				}
				t.Setenv("CURIOUS_CONFIG", filepath.Join(dir, "config.json"))
				oldPrepare, oldResume := controlPrepare, controlResume
				defer func() { controlPrepare, controlResume = oldPrepare, oldResume }()
				controlPrepare = func(origin string) (flow.ControlRequest, error) { return flow.PrepareControlLogin(origin, "cli", true) }
				called := false
				controlResume = func(context.Context, string, string) (flow.ControlRequest, error) {
					called = true
					return flow.ControlRequest{}, fmt.Errorf("unexpected create")
				}
				code := run([]string{"login", "--control-url", "http://127.0.0.1:9000"}, strings.NewReader(""), &bytes.Buffer{}, failingHandleWriter{short: short})
				if code != 1 || called {
					t.Fatalf("code=%d create=%v", code, called)
				}
			})
		}
	})
	t.Run("crash before handle output makes no request", testControlCrashBeforeOutput)
	t.Run("crash after output recovers same proof", func(t *testing.T) { testControlCrashRecovery(t, "after") })
	t.Run("crash during create retains proof and refuses unsafe retry", func(t *testing.T) { testControlCrashRecovery(t, "during") })
	var legacyCalls atomic.Int32
	legacy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { legacyCalls.Add(1) }))
	defer legacy.Close()
	t.Setenv("CURIOUS_API_URL", legacy.URL)
	for _, args := range [][]string{{"login"}, {"logout"}} {
		var out, errout bytes.Buffer
		code := run(args, strings.NewReader(""), &out, &errout)
		if code != 2 {
			t.Errorf("%s: exit %d", args[0], code)
		}
		if !strings.Contains(errout.String(), "origin is required") {
			t.Errorf("missing refusal for %s", args[0])
		}
	}
	if legacyCalls.Load() != 0 {
		t.Fatal("missing origin contacted legacy service")
	}
	var outHTTP, errHTTP bytes.Buffer
	if code := run([]string{"login", "--control-url", legacy.URL}, strings.NewReader(""), &outHTTP, &errHTTP); code == 0 || legacyCalls.Load() != 0 {
		t.Fatal("production command accepted HTTP loopback")
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case controlwire.CapabilitiesPath:
			_ = json.NewEncoder(w).Encode(controlwire.Capabilities{Version: 1, ClientKinds: []string{"cli", "mcp"}, Endpoints: controlwire.Endpoints{Capabilities: controlwire.CapabilitiesPath, Requests: controlwire.RequestsPath, Verification: controlwire.VerificationPath, Decision: controlwire.DecisionPath, Poll: controlwire.PollPath, Session: controlwire.SessionPath, Revoke: controlwire.RevokePath}, IntervalSeconds: 5})
		case controlwire.RequestsPath:
			_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(9 * time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
		case controlwire.RevokePath:
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ccl1.") {
				t.Error("missing logout credential")
			}
			w.Header().Del("Content-Type")
			w.WriteHeader(http.StatusNoContent)
		case controlwire.PollPath:
			_ = json.NewEncoder(w).Encode(controlwire.IssuanceResponse{Credential: "ccl1.clcred_QkJCQkJCQkJCQkJCQkJCQg.ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8", CredentialID: "clcred_QkJCQkJCQkJCQkJCQkJCQg", UserID: "user-one", ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339)})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestControlLoginCommand$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CURIOUS_CONTROL_CLI_CHILD=1", "CURIOUS_CONTROL_URL="+srv.URL, "CURIOUS_CONFIG="+filepath.Join(dir, "config.json"))
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("command child failed: %v: %s", e, out)
	}
}

func testControlCrashRecovery(t *testing.T, stage string) {
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	configPath := filepath.Join(dir, "config.json")
	t.Setenv("CURIOUS_CONFIG", configPath)
	requests := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWait := func() { releaseOnce.Do(func() { close(release) }) }
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
			requests <- q.ProofSHA256
			if count.Add(1) == 1 && stage == "during" {
				<-release
				return
			}
			_ = json.NewEncoder(w).Encode(controlwire.CreateResponse{RequestID: "clreq_QUFBQUFBQUFBQUFBQUFBQQ", UserCode: "ABCD-EFGH", VerificationURI: "http://" + r.Host + controlwire.VerificationPath, VerificationURIComplete: "http://" + r.Host + controlwire.VerificationPath + "?code=ABCD-EFGH", ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339), IntervalSeconds: 5})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	defer releaseWait()
	cmd := exec.Command(os.Args[0], "-test.run=^TestControlLoginCommand$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CURIOUS_CONTROL_CLI_CHILD="+stage, "CURIOUS_CONTROL_URL="+srv.URL, "CURIOUS_CONFIG="+configPath, "CURIOUS_CONTROL_MARKER="+filepath.Join(dir, "after-output"), "CURIOUS_CONTROL_OUTPUT="+filepath.Join(dir, "output"))
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var line string
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(dir, "output"))
		if err == nil && bytes.Contains(b, []byte("\n")) {
			line = strings.SplitN(string(b), "\n", 2)[0] + "\n"
			break
		}
		if time.Now().After(deadline) {
			releaseWait()
			t.Fatal("handle was not output before create")
		}
		time.Sleep(10 * time.Millisecond)
	}

	prefix := "If interrupted, resume with request handle "
	if !strings.HasPrefix(line, prefix) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		releaseWait()
		t.Fatalf("unexpected initial output %q", line)
	}
	handle := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, prefix)), ".")
	if handle == "" {
		t.Fatal("missing handle")
	}
	var first string
	if stage == "during" {
		select {
		case first = <-requests:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			releaseWait()
			t.Fatal("create was not reached")
		}
	} else {
		marker := filepath.Join(dir, "after-output")
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, e = os.Stat(marker); e == nil {
				break
			}
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatal("post-output checkpoint not reached")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if count.Load() != 0 {
			t.Fatal("create preceded checkpoint")
		}
	}
	if e = cmd.Process.Kill(); e != nil {
		releaseWait()
		t.Fatal(e)
	}
	_ = cmd.Wait()
	releaseWait()
	var proof string
	if e = config.ControlWithLock(srv.URL, func(store *config.ControlStore) error {
		state, e := store.Load()
		if e != nil {
			return e
		}
		proof = string(state.Pending[handle].Proof)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	raw, e := base64.RawURLEncoding.DecodeString(proof)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(raw)
	savedDigest := base64.RawURLEncoding.EncodeToString(digest[:])
	if stage == "during" && first != savedDigest {
		t.Fatal("request did not use stored proof")
	}
	request, e := flow.ResumeControlLogin(context.Background(), srv.URL, handle, true)
	if stage == "during" {
		if e == nil {
			t.Fatal("uncertain in-flight create reported success")
		}
		if count.Load() != 1 {
			t.Fatal("uncertain in-flight create was retried")
		}
		return
	}
	if e != nil || request.UserCode != "ABCD-EFGH" {
		t.Fatalf("resume failed: %v", e)
	}
	select {
	case second := <-requests:
		if savedDigest != second {
			t.Fatal("retry changed proof")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("resume did not reach create")
	}
}

func testControlCrashBeforeOutput(t *testing.T) {
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(dir, "prepared")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestControlLoginCommand$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CURIOUS_CONTROL_CLI_CHILD=before", "CURIOUS_CONTROL_URL="+srv.URL, "CURIOUS_CONTROL_MARKER="+marker, "CURIOUS_CONFIG="+filepath.Join(dir, "config.json"))
	var errout bytes.Buffer
	cmd.Stderr = &errout
	cmd.Stdout = io.Discard
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e = os.Stat(marker); e == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("preparation was not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	if calls.Load() != 0 || errout.Len() != 0 {
		t.Fatal("request or handle output preceded preparation completion")
	}
}
