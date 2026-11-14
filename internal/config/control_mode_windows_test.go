//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
	"golang.org/x/sys/windows"
)

func TestControlCredentialStoragePermissions(t *testing.T) {
	configPath := controlTestPath(t)
	t.Setenv("CURIOUS_CONFIG", configPath)
	if e := ControlWithLock("https://example.test", func(s *ControlStore) error {
		st, e := s.Load()
		if e != nil {
			return e
		}
		st.Pending["one"] = ControlPending{Handle: "one", Kind: "cli", Proof: ui.Secret("synthetic-proof")}
		return s.Save(st)
	}); e != nil {
		t.Fatal(e)
	}
	root := filepath.Join(filepath.Dir(configPath), "control")
	entries, e := os.ReadDir(root)
	if e != nil {
		t.Fatal(e)
	}
	var statePath string
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		sd, e := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if e != nil {
			t.Fatal(e)
		}
		flags, _, e := sd.Control()
		if e != nil || flags&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("missing protected DACL")
		}
		acl, _, e := sd.DACL()
		if e != nil || acl == nil || acl.AceCount != 1 {
			t.Fatal("unexpected DACL")
		}
		if strings.HasSuffix(path, ".json") {
			statePath = path
		}
	}
	if statePath == "" {
		t.Fatal("missing state file")
	}
	linked := filepath.Join(root, "linked-state")
	if e = os.Link(statePath, linked); e != nil {
		t.Fatalf("cannot establish hard-link fixture: %v", e)
	}
	if e = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); e == nil {
		t.Fatal("accepted multiply linked state")
	}
	if e = os.Remove(linked); e != nil {
		t.Fatal(e)
	}

	bad, e := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if e != nil {
		t.Fatal(e)
	}
	acl, _, e := bad.DACL()
	if e != nil {
		t.Fatal(e)
	}
	if e = windows.SetNamedSecurityInfo(statePath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(statePath)
	if e != nil {
		t.Fatal(e)
	}
	if e = ControlWithLock("https://example.test", func(s *ControlStore) error { _, e := s.Load(); return e }); e == nil {
		t.Fatal("accepted broad file DACL")
	}
	if e = ControlWithLock("https://example.test", func(s *ControlStore) error {
		return s.Save(ControlState{Pending: map[string]ControlPending{"two": {Handle: "two", Kind: "cli", Proof: ui.Secret("different-synthetic-proof")}}, Credentials: map[string]ControlCredential{}})
	}); e == nil {
		t.Fatal("wrote to insecure state")
	}
	after, e := os.ReadFile(statePath)
	if e != nil || string(after) != string(before) {
		t.Fatal("insecure state changed")
	}
	t.Run("reparse parent rejected", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		link := filepath.Join(dir, "link")
		if e := os.Mkdir(target, 0700); e != nil {
			t.Fatal(e)
		}
		if e := controlPrepareDir(filepath.Join(target, "positive")); e != nil {
			t.Fatalf("ordinary parent failed: %v", e)
		}
		if e := os.Symlink(target, link); e != nil {
			t.Fatalf("cannot establish reparse-point fixture: %v", e)
		}
		if e := controlPrepareDir(filepath.Join(link, "through-link")); e == nil {
			t.Fatal("accepted reparse parent")
		}
		if _, e := os.Stat(filepath.Join(target, "through-link")); !os.IsNotExist(e) {
			t.Fatal("created state through reparse point")
		}
	})
}
