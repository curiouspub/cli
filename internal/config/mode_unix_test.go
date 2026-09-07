//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadWarnsWhenOtherUsersCanReadTheToken covers the file that is
// already wrong when we find it — written by an earlier build, restored
// from a backup, or copied between machines with a tar that flattened
// the mode.
//
// It WARNS and carries on. Refusing would strand somebody with a
// perfectly good token and no way to get past the refusal; the warning
// names the file and the exact command that fixes it, which is the
// version of this that ends with the file actually being fixed.
//
// REQUIRED MUTATION: in modeWarning (mode_other.go), narrow the mask
// from 0o077 to 0o007. The group-readable row reds and the world-readable
// row stays GREEN — which is the reason both are in the table rather than
// one standing in for "someone else can read it". Run, observed red, and
// the file restored from a checksum-verified copy.
func TestLoadWarnsWhenOtherUsersCanReadTheToken(t *testing.T) {
	const endpoint = "https://api.example.com"

	for _, tc := range []struct {
		name     string
		mode     os.FileMode
		wantWarn bool
	}{
		{"group readable", 0o640, true},
		{"world readable", 0o604, true},
		{"the mode this package writes", 0o600, false},
		// The distinctness row: a file NARROWER than 0600 is nobody
		// else's business either, and warning about it would be noise
		// that teaches people to ignore the warning that matters.
		{"narrower than we would write", 0o400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, `{"version":1,"token":"`+testToken+
				`","api_url":"`+endpoint+`"}`)
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatalf("chmod %s: %v", path, err)
			}

			loaded, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}

			// The token comes back either way. A permissions problem is
			// the user's to fix, not a reason to throw away a credential
			// that is still theirs.
			if loaded.Token == "" {
				t.Errorf("the token was withheld over a file mode: %v", loaded.NoTokenReason)
			}
			if !loaded.PermissionsChecked {
				t.Errorf("PermissionsChecked = false on a platform where the mode " +
					"bits mean exactly what this check reads them to mean")
			}

			warned := strings.Join(loaded.Warnings, "\n")
			if tc.wantWarn {
				if len(loaded.Warnings) == 0 {
					t.Fatalf("mode %04o produced no warning at all", tc.mode)
				}
				if !strings.Contains(warned, path) {
					t.Errorf("the warning does not name the file: %q", warned)
				}
				if !strings.Contains(warned, "chmod 600") {
					t.Errorf("the warning does not give the command that fixes it: %q", warned)
				}
			} else if len(loaded.Warnings) != 0 {
				t.Errorf("mode %04o warned when nothing is wrong: %q — a warning "+
					"that fires on a correct file is one people learn to ignore",
					tc.mode, warned)
			}
		})
	}
}

// TestSaveIntoAnUnwritableDirectoryLeavesTheOriginalUntouched is the
// same never-damage-the-original claim as the injected-rename row, made
// with real permissions and no seam at all. The save fails earlier here
// — at the temp file rather than at the rename — which is exactly why
// both exist: this one proves the property against the operating system,
// and the other proves it at the one instant a seam can reach.
//
// REQUIRED MUTATION: in Save (config.go), write straight to the target
// — os.Create(c.Path) in place of the temp file, and no rename. Run, and
// what it reds with is better than what was predicted here: the save
// SUCCEEDS. A read-only directory does not stop a write to a file that
// already exists inside it, because the permission consulted is the
// FILE's. So the truncate-in-place quietly replaced a good config in
// exactly the situation the atomic version refuses, and both assertions
// below fired. Restored from a checksum-verified copy.
func TestSaveIntoAnUnwritableDirectoryLeavesTheOriginalUntouched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the permission bits this test relies on are not " +
			"enforced against the superuser, so the failure it needs cannot happen")
	}

	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":1,"token":"the-original-token","api_url":"https://api.example.com"}`)
	before := readFile(t, path)

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	// Put it back before the temp directory's own cleanup runs, or the
	// harness cannot delete what it made.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	cfg := &Config{Path: path}
	if err := cfg.Save(testToken, "https://api.example.com"); err == nil {
		t.Error("Save() into a directory it cannot write to reported success")
	}
	if after := readFile(t, path); string(after) != string(before) {
		t.Errorf("a failed save damaged the existing config\nbefore: %s\nafter:  %s",
			before, after)
	}
}

// TestPathPlatformDefaultOnUnix pins the one leg of the resolution order
// whose answer is different on every operating system.
//
// The path here is deliberately NOT os.UserConfigDir(). On macOS that
// returns the Application Support directory, and this tool keeps its
// config in ~/.config on both Unixes on purpose: it is where developer
// CLIs live, it is what the documentation will tell people to look at,
// and one path across both is one support answer instead of two.
//
// REQUIRED MUTATION: in defaultConfigDir (path_other.go), return
// os.UserConfigDir(). This test reds on macOS — with the Application
// Support path — and stays GREEN on Linux, where the two answers
// coincide. That asymmetry is worth knowing about: the leg of the matrix
// that can see this mistake is the one that is not the developer's
// default assumption.
func TestPathPlatformDefaultOnUnix(t *testing.T) {
	home := t.TempDir()
	t.Setenv(envConfigPath, "")
	t.Setenv(envXDGConfigHome, "")
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".config", dirName, fileName)
	got, err := Path()
	if err != nil {
		t.Fatalf("Path(): %v", err)
	}
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}

	// And the whole flow really does land there, rather than the path
	// merely being computed correctly and then ignored.
	cfg := &Config{}
	if err := cfg.Save(testToken, "https://api.example.com"); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("nothing was written to the platform default path: %v", err)
	}
}

// TestSavePreservesAStricterExistingMode stops this package widening the
// very mode it calls nobody else's business.
//
// A file at 0400 is narrower than this code would write, and the load
// path deliberately does not warn about it: a user who tightened their
// own config has done nothing wrong. The next save then widened it back
// to 0600, so the tightening survived exactly until the next login.
//
// Only a STRICTER mode is kept, and only one this package can still
// read. A wider mode is not preserved — the file is being replaced and
// 0600 is what it should be — and a mode with no owner-read bit is not a
// stricter setting but a file nobody can read, which is not a thing to
// reproduce deliberately.
//
// REQUIRED MUTATIONS, two, one for each limit:
//
//  1. In Save (config.go), pass configFileMode to chmodFile
//     unconditionally. Only the 0400 row reds, at 0600; the other four
//     stay green.
//  2. In fileModeFor, preserve whatever mode is already there. The
//     wider-than-ours row and the could-not-read-back row red, and the
//     0400 row stays green — which is the pair, since "preserve
//     everything" satisfies the first mutation's row on its own.
//
// Both run, both observed, and the file restored from a
// checksum-verified copy after each.
func TestSavePreservesAStricterExistingMode(t *testing.T) {
	const endpoint = "https://api.example.com"

	for _, tc := range []struct {
		name     string
		existing os.FileMode
		// zero means there is no file there to begin with.
		want os.FileMode
	}{
		{"a mode the user tightened by hand", 0o400, 0o400},
		{"the mode this package writes", 0o600, 0o600},
		{"a mode wider than ours, which is not preserved", 0o644, 0o600},
		{"no file at all", 0, 0o600},
		// Owner-write-only is narrower in the strict sense and is NOT
		// kept: reproducing it would mean writing a file this package
		// could never read back.
		{"a mode this package could not read back", 0o200, 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			if tc.existing != 0 {
				writeConfigFile(t, path, `{"version":1,"token":"old","api_url":"`+
					endpoint+`"}`)
				if err := os.Chmod(path, tc.existing); err != nil {
					t.Fatalf("chmod %s: %v", path, err)
				}
			}

			cfg := &Config{Path: path}
			if err := cfg.Save(testToken, endpoint); err != nil {
				t.Fatalf("Save(): %v", err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", path, err)
			}
			if perm := fi.Mode().Perm(); perm != tc.want {
				t.Errorf("the file came back at %04o, want %04o", perm, tc.want)
			}
		})
	}
}

// TestModeWarningSaysOnlyWhatItKnows covers two sentences that were
// wrong in opposite directions.
//
// The warning claimed the file "holds an access token" whatever was in
// it, including a file with no token at all — which is the state this
// package reports as unusable one line later. And the command it offers
// was unquoted, so for the perfectly ordinary case of a path containing
// a space it was not something a person could paste.
//
// REQUIRED MUTATIONS, two:
//
//  1. In modeWarning (mode_other.go), ignore the holdsToken argument and
//     always claim the file holds a token. The token-less row reds and
//     the others stay green.
//  2. In modeWarning, write the path unquoted. The spaced-path row reds
//     and the rest stay green.
//  3. In Config.read (config.go), report that a token is present
//     whatever the file held. The token-less row here reds — and so do
//     three rows elsewhere, because that one computation also decides
//     whether the file is reported as having nothing in it. That is
//     worth knowing rather than avoiding: the warning and the report
//     agree BY CONSTRUCTION, which is what makes them unable to
//     contradict each other.
//
// All three run, all three observed, and the file restored from a
// checksum-verified copy after each.
func TestModeWarningSaysOnlyWhatItKnows(t *testing.T) {
	const endpoint = "https://api.example.com"

	t.Run("a file with no token in it is not said to hold one", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, `{"version":1,"token":"","api_url":"`+endpoint+`"}`)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		warned := strings.Join(loaded.Warnings, "\n")
		if warned == "" {
			t.Fatalf("a world-readable file produced no warning at all")
		}
		if strings.Contains(warned, "access token") {
			t.Errorf("the warning says the file holds an access token, and this "+
				"package is about to report that it does not: %q", warned)
		}
		if !strings.Contains(warned, "chmod 600") {
			t.Errorf("the warning stopped giving the command that fixes it: %q", warned)
		}
	})

	t.Run("a file that does hold one still says so", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, `{"version":1,"token":"`+testToken+`","api_url":"`+
			endpoint+`"}`)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		warned := strings.Join(loaded.Warnings, "\n")
		if !strings.Contains(warned, "access token") {
			t.Errorf("the warning no longer says what is at stake: %q", warned)
		}
	})

	t.Run("the command it offers is pasteable for a path with a space", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "my configs", "config.json")
		t.Setenv(envConfigPath, path)
		writeConfigFile(t, path, `{"version":1,"token":"`+testToken+`","api_url":"`+
			endpoint+`"}`)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		warned := strings.Join(loaded.Warnings, "\n")
		if !strings.Contains(warned, `chmod 600 "`+path+`"`) {
			t.Errorf("the command in the warning is not quoted, so a shell would "+
				"read this path as two arguments and the advice does not work: %q",
				warned)
		}
	})
}
