package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/ui"
)

func controlTestPath(t *testing.T) string {
	t.Helper()
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return filepath.Join(dir, "config.json")
}
func TestControlLoginConcurrentProcesses(t *testing.T) {
	if id := os.Getenv("CURIOUS_CONTROL_CHILD"); id != "" {
		if id == "hold" {
			err := ControlWithLock("https://example.test", func(*ControlStore) error {
				if e := os.WriteFile(os.Getenv("CURIOUS_CONTROL_MARKER"), []byte("locked"), 0600); e != nil {
					return e
				}
				time.Sleep(30 * time.Second)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		e := ControlWithLock("https://example.test", func(s *ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			st.Pending[id] = ControlPending{Handle: id, Kind: "cli", Proof: ui.Secret("synthetic-proof")}
			st.Credentials[id] = ControlCredential{CredentialID: id, UserID: "user-" + id, Token: ui.Secret("synthetic-token")}
			return s.Save(st)
		})
		if e != nil {
			t.Fatal(e)
		}
		return
	}
	t.Setenv("CURIOUS_CONFIG", controlTestPath(t))
	const children = 6
	var wg sync.WaitGroup
	errs := make(chan error, children)
	for i := 0; i < children; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlLoginConcurrentProcesses$", "-test.count=1")
			cmd.Env = append(os.Environ(), fmt.Sprintf("CURIOUS_CONTROL_CHILD=%d", i+1))
			out, e := cmd.CombinedOutput()
			if e != nil {
				errs <- fmt.Errorf("child: %w: %s", e, out)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if t.Failed() {
		return
	}
	if e := ControlWithLock("https://example.test", func(s *ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		if len(st.Pending) != children || len(st.Credentials) != children {
			return fmt.Errorf("got %d requests and %d credentials", len(st.Pending), len(st.Credentials))
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(filepath.Dir(os.Getenv("CURIOUS_CONFIG")), "lock-held")
	hold := exec.Command(os.Args[0], "-test.run=^TestControlLoginConcurrentProcesses$", "-test.count=1")
	hold.Env = append(os.Environ(), "CURIOUS_CONTROL_CHILD=hold", "CURIOUS_CONTROL_MARKER="+marker)
	if e := hold.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = hold.Process.Kill(); _ = hold.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e := os.Stat(marker); e == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = hold.Process.Kill()
			_ = hold.Wait()
			t.Fatal("child never acquired lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := hold.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = hold.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	after := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlLoginConcurrentProcesses$", "-test.count=1")
	after.Env = append(os.Environ(), "CURIOUS_CONTROL_CHILD=after")
	if out, e := after.CombinedOutput(); e != nil {
		t.Fatalf("lock not recovered after process death: %v: %s", e, out)
	}
}

func TestControlStateAmbiguousJSONRejected(t *testing.T) {
	deep := json.NewDecoder(strings.NewReader(strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40)))
	if controlUniqueValue(deep, 0) == nil {
		t.Fatal("accepted excessive state nesting")
	}
	var invalid controlDiskState
	if controlDecode([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, &invalid) == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	for _, value := range []string{`{"origin":"a","origin":"b","pending":{},"credentials":{},"completed":{}}`, `{"origin":"a","pending":{},"credentials":{},"completed":{},"extra":true}`, `{"origin":"a","pending":{},"credentials":{},"completed":{}} {}`} {
		var state controlDiskState
		if controlDecode([]byte(value), &state) == nil {
			t.Fatal("accepted ambiguous state")
		}
	}
}

func TestControlStateSizeBound(t *testing.T) {
	t.Setenv("CURIOUS_CONFIG", controlTestPath(t))
	err := ControlWithLock("https://example.test", func(s *ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		st.Pending["old"] = ControlPending{Handle: "old", Kind: "cli", Proof: "synthetic"}
		if e = s.Save(st); e != nil {
			return e
		}
		st.Pending["too-large"] = ControlPending{Handle: "too-large", Kind: "cli", Proof: ui.Secret(strings.Repeat("x", 1<<20))}
		if e = s.Save(st); e == nil {
			return fmt.Errorf("oversized state accepted")
		}
		saved, e := s.Load()
		if e != nil {
			return e
		}
		if len(saved.Pending) != 1 || saved.Pending["old"].Handle != "old" {
			return fmt.Errorf("existing state changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestControlStateRedactsSecrets(t *testing.T) {
	state := ControlState{Pending: map[string]ControlPending{"handle": {Handle: "handle", Proof: ui.Secret("synthetic-proof-canary")}}, Credentials: map[string]ControlCredential{"id": {CredentialID: "id", UserID: "user", Token: ui.Secret("synthetic-token-canary")}}}
	encoded, e := json.Marshal(state)
	if e != nil {
		t.Fatal(e)
	}
	rendered := fmt.Sprintf("%v %+v %#v %s", state, state, state, encoded)
	if strings.Contains(rendered, "synthetic-proof-canary") || strings.Contains(rendered, "synthetic-token-canary") {
		t.Fatal("Control state exposed a secret")
	}
}

func TestControlLoginOriginIsolation(t *testing.T) {
	t.Setenv("CURIOUS_CONFIG", controlTestPath(t))
	var original []byte
	if e := ControlWithLock("https://first.example.test", func(s *ControlStore) error {
		state, e := s.Load()
		if e != nil {
			return e
		}
		state.Credentials["one"] = ControlCredential{CredentialID: "one", UserID: "first", Token: "synthetic-token"}
		if e = s.Save(state); e != nil {
			return e
		}
		original, e = os.ReadFile(s.path)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := ControlWithLock("https://second.example.test", func(s *ControlStore) error {
		state, e := s.Load()
		if e != nil {
			return e
		}
		if len(state.Credentials) != 0 {
			return fmt.Errorf("other origin exposed a credential")
		}
		if e = controlAtomicWrite(s.path, original); e != nil {
			return e
		}
		copied, readErr := controlRead(s.path)
		if readErr != nil {
			return fmt.Errorf("copied fixture not securely readable: %w", readErr)
		}
		if !bytes.Equal(copied, original) {
			return fmt.Errorf("copied fixture changed")
		}
		if _, e = s.Load(); e == nil {
			return fmt.Errorf("copied state accepted for wrong origin")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestControlLoginStateRejectsFieldAliases(t *testing.T) {
	for _, body := range []string{
		`{"origin":"https://first.example.test","ORIGIN":"https://second.example.test","pending":{},"credentials":{},"completed":{}}`,
		`{"origin":"https://first.example.test","pending":{"x":{"handle":"x","HANDLE":"other","proof":"p"}},"credentials":{},"completed":{}}`,
		`{"origin":"https://first.example.test","pending":null,"credentials":{},"completed":{}}`,
	} {
		var state controlDiskState
		if controlDecode([]byte(body), &state) == nil {
			t.Fatal("accepted ambiguous or null state")
		}
	}
}
