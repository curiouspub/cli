//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUmaskWindowsCounterpart is the Windows leg of the file-mode work,
// and it asserts the CHECKABLE SUBSET rather than skipping.
//
// There is no umask on this platform: syscall.Umask does not exist here,
// which is why the test that uses it sits behind a build tag. A runtime
// skip would not have been enough — the file would still reference the
// symbol, so it would fail to COMPILE here and the whole package would
// red before any skip could run.
//
// The tempting shape is then a file that only skips. That is worse than
// it looks: nothing on this platform would ever exercise the package's
// real entry points, so a change that breaks Save or Load on Windows
// reports green, and the matrix leg is measuring build tags instead of
// code. So this asserts everything Windows CAN check — the file is
// created where the platform's own config directory says, it exists, it
// is readable, and the token survives the round trip.
//
// STATED RESIDUE, so that a green run here is not read as more than it
// is: this test proves NOTHING about who else on the machine can read
// the token file. File protection on this platform is an access control
// list, which this build neither sets nor reads; the file inherits
// whatever its directory carries. That is a deliberate scope decision
// rather than an oversight, and it is written down in the README as
// plainly as it is here.
//
// REQUIRED MUTATION: in defaultConfigDir (path_windows.go), return
// os.UserHomeDir() instead. The path assertion reds, because the file
// then lands in the profile root rather than in the directory this
// platform names for application configuration. (Recorded rather than
// run: this branch cannot execute on the developer's machine. It is
// type-checked for this platform on every push, and the matrix runs it.)
func TestUmaskWindowsCounterpart(t *testing.T) {
	appData := t.TempDir()

	// Clear the two overrides BEFORE setting anything, so no name has a
	// window in which it could collide. Environment variable names are
	// case-insensitive on this platform, which makes the order of these
	// three calls a real question rather than a stylistic one.
	t.Setenv(envConfigPath, "")
	t.Setenv(envXDGConfigHome, "")
	t.Setenv("AppData", appData)

	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir(): %v", err)
	}
	path, err := Path()
	if err != nil {
		t.Fatalf("Path(): %v", err)
	}
	if !strings.HasPrefix(path, root) {
		t.Errorf("Path() = %q, want it inside the directory this platform names for "+
			"application configuration (%q)", path, root)
	}
	if want := filepath.Join(root, dirName, fileName); path != want {
		t.Errorf("Path() = %q, want %q", path, want)
	}

	const endpoint = "https://api.example.com"
	cfg := &Config{}
	if err := cfg.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the config file was not created at %s: %v", path, err)
	}
	if data, err := os.ReadFile(path); err != nil {
		t.Errorf("the config file is not readable: %v", err)
	} else if !strings.Contains(string(data), testToken) {
		t.Errorf("the config file does not contain the token that was saved — the " +
			"redaction placeholder reaching the disk is the failure this looks for")
	}

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if string(loaded.Token) != testToken {
		t.Errorf("the token did not survive a save-and-load round trip on this " +
			"platform")
	}
}
