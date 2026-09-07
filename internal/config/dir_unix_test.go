//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSaveCreatesEveryMissingDirectoryLevelUsably is the row for the
// first run on a fresh account, and it is the one that found a login
// which cannot save at all.
//
// The platform default is TWO levels deep — a shared config root, and
// this tool's own directory inside it — and on a machine where nobody
// has ever run a tool that uses the shared root, BOTH are missing. The
// mode a directory is created with is masked by the process umask
// exactly as a file's is, so under a mask that removes the owner's
// execute or write bit the outer level comes out unusable and nothing
// can be created inside it. Correcting only the leaf is too late: the
// leaf is what could not be created.
//
// Measured on the delivered code, before the fix, with the shared root
// absent:
//
//	umask 0022  ok                       .config rwx------  curious rwx------
//	umask 0077  ok                       .config rwx------  curious rwx------
//	umask 0177  mkdir …/curious: denied  .config rw-------  curious —
//	umask 0277  mkdir …/curious: denied  .config r-x------  curious —
//
// The two that fail are the two masks the file-mode table beside this
// one was written for, which is the finding inside the finding: the
// existing table cannot see this because its fixture puts the leaf one
// level under a directory the harness already created.
//
// THE TWO LEVELS GET DIFFERENT MODES, and the asymmetry is the point.
// The leaf is ours and holds a token, so it is 0700. The levels above it
// are SHARED — other tools keep their configuration there — and a tool
// that narrows a directory it does not own on the way past has changed
// something that was not its business. So a level this code creates
// above the leaf gets 0755, which is what creating it by hand under an
// ordinary mask would have produced.
//
// This test may not call t.Parallel, at any level: the umask is process
// global and a parallel sibling would read the mask this test set.
func TestSaveCreatesEveryMissingDirectoryLevelUsably(t *testing.T) {
	entryMask := syscall.Umask(0)
	syscall.Umask(entryMask)

	const endpoint = "https://api.example.com"

	for _, mask := range []int{0o022, 0o077, 0o177, 0o277} {
		t.Run(fmt.Sprintf("umask %04o", mask), func(t *testing.T) {
			// The harness's own directory is made BEFORE the mask
			// changes, so what the mask affects is only what Save
			// creates. Everything below home is absent, which is the
			// state of a fresh account.
			home := t.TempDir()
			t.Setenv(envConfigPath, "")
			t.Setenv(envXDGConfigHome, "")
			t.Setenv("HOME", home)

			previous := syscall.Umask(mask)
			defer syscall.Umask(previous)

			path, err := Path()
			if err != nil {
				t.Fatalf("Path(): %v", err)
			}
			leaf := filepath.Dir(path)
			shared := filepath.Dir(leaf)

			cfg := &Config{}
			if err := cfg.Save(testToken, endpoint); err != nil {
				t.Fatalf("Save() under umask %04o: %v — a first run on an account "+
					"whose shared config root does not exist yet cannot store a "+
					"token at all, and every later run asks for another login",
					mask, err)
			}

			for _, want := range []struct {
				what string
				path string
				mode os.FileMode
			}{
				{"the config file", path, 0o600},
				{"this tool's own directory", leaf, 0o700},
				{"the shared config root this code created", shared, 0o755},
			} {
				fi, err := os.Stat(want.path)
				if err != nil {
					t.Fatalf("stat %s (%s): %v", want.path, want.what, err)
				}
				if perm := fi.Mode().Perm(); perm != want.mode {
					t.Errorf("%s (%s) has mode %04o under umask %04o, want %04o",
						want.what, want.path, perm, mask, want.mode)
				}
			}

			// The whole point of the modes above is that the file is
			// usable afterwards, so the round trip is asserted rather
			// than inferred from three stat calls.
			loaded, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if string(loaded.Token) != testToken {
				t.Errorf("the token did not survive a first run under umask %04o: %v",
					mask, loaded.NoTokenReason)
			}
		})
	}

	// A directory that was ALREADY there keeps its mode. This is the
	// distinctness row for the rule above: a fix that corrected every
	// level rather than only the levels it created would pass every
	// assertion above and silently re-permission a directory the user
	// pointed this tool at.
	t.Run("a directory that already existed is left exactly as it was", func(t *testing.T) {
		home := t.TempDir()
		shared := filepath.Join(home, ".config")
		if err := os.Mkdir(shared, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", shared, err)
		}
		if err := os.Chmod(shared, 0o750); err != nil {
			t.Fatalf("chmod %s: %v", shared, err)
		}
		t.Setenv(envConfigPath, "")
		t.Setenv(envXDGConfigHome, "")
		t.Setenv("HOME", home)

		cfg := &Config{}
		if err := cfg.Save(testToken, endpoint); err != nil {
			t.Fatalf("Save(): %v", err)
		}
		fi, err := os.Stat(shared)
		if err != nil {
			t.Fatalf("stat %s: %v", shared, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o750 {
			t.Errorf("a config root that already existed came back at %04o, want "+
				"0750 — re-permissioning somebody else's directory is not this "+
				"tool's business", perm)
		}
	})

	if exitMask := syscall.Umask(0); exitMask != entryMask {
		syscall.Umask(entryMask)
		t.Errorf("this test left the process umask at %04o, having found it at %04o",
			exitMask, entryMask)
	} else {
		syscall.Umask(entryMask)
	}
}
