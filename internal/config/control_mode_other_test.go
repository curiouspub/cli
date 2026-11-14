//go:build !windows

package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/ui"
	"golang.org/x/sys/unix"
)

func TestControlCredentialStoragePermissions(t *testing.T) {
	if os.Getenv("CURIOUS_CONTROL_UMASK_CHILD") == "1" {
		previous := unix.Umask(0777)
		defer unix.Umask(previous)
		if e := ControlWithLock("https://example.test", func(s *ControlStore) error {
			st, e := s.Load()
			if e != nil {
				return e
			}
			st.Pending["x"] = ControlPending{Handle: "x", Kind: "cli", Proof: ui.Secret("synthetic")}
			return s.Save(st)
		}); e != nil {
			t.Fatal(e)
		}
		root := filepath.Join(filepath.Dir(os.Getenv("CURIOUS_CONFIG")), "control")
		fi, e := os.Stat(root)
		if e != nil || fi.Mode().Perm() != 0700 {
			t.Fatal("wrong directory mode")
		}
		entries, e := os.ReadDir(root)
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			fi, e = entry.Info()
			if e != nil || fi.Mode().Perm() != 0600 {
				t.Fatal("wrong file mode")
			}
		}
		return
	}
	t.Run("restrictive umask", func(t *testing.T) {
		dir, e := filepath.EvalSymlinks(t.TempDir())
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlCredentialStoragePermissions$", "-test.count=1")
		cmd.Env = append(os.Environ(), "CURIOUS_CONTROL_UMASK_CHILD=1", "CURIOUS_CONFIG="+filepath.Join(dir, "config.json"))
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("restrictive umask child failed: %v: %s", e, out)
		}
	})
	configPath := controlTestPath(t)
	t.Setenv("CURIOUS_CONFIG", configPath)
	err := ControlWithLock("https://example.test", func(s *ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		st.Pending["one"] = ControlPending{Handle: "one", Kind: "cli", Proof: ui.Secret("synthetic-proof")}
		return s.Save(st)
	})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(configPath), "control")
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v %v", info, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("file permissions: %v %v", info, err)
		}
	}
	// A symlink at the state destination must never be followed or replaced.
	var statePath string
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			statePath = filepath.Join(root, entry.Name())
		}
	}
	linked := filepath.Join(root, "linked-state")
	if err = os.Link(statePath, linked); err != nil {
		t.Fatal(err)
	}
	if err = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); err == nil {
		t.Fatal("accepted multiply linked state")
	}
	if err = os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(statePath, 0644); err != nil {
		t.Fatal(err)
	}
	if err = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); err == nil {
		t.Fatal("accepted insecure state file")
	}
	if err = os.Chmod(statePath, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(statePath, []byte(`{"origin":"https://example.test","origin":"https://example.test","pending":{},"credentials":{},"completed":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); err == nil {
		t.Fatal("accepted duplicate state field")
	}
	if err = os.WriteFile(statePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(root), "target")
	if err = os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, statePath); err != nil {
		t.Fatal(err)
	}
	if err = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); err == nil {
		t.Fatal("accepted symlink")
	}
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "untouched" {
		t.Fatal("symlink target changed")
	}
	if err = os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err = ControlWithLock("https://example.test", func(*ControlStore) error { t.Error("entered insecure state"); return nil }); err == nil {
		t.Fatal("accepted insecure directory")
	}
}
