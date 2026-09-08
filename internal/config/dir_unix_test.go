//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestSaveAndTheUmaskAboveOurOwnDirectory records what a first run does
// when the shared config root does not exist yet, under four masks.
//
// The rule it measures: THIS COMMAND SETS THE MODE OF ITS OWN DIRECTORY
// AND OF NOTHING ABOVE IT. The leaf holds a bearer token and is pinned
// to 0700 whatever the umask; the level above it is shared with every
// other tool the user runs, belongs to them, and is created with the
// conventional mode so that THE UMASK GOVERNS what it becomes.
//
// MEASURED, not predicted — this table is output:
//
//	umask   Save    shared root  our directory  file
//	0022    ok      0755         0700           0600
//	0077    ok      0700         0700           0600
//	0177    fails   0600         absent         absent
//	0277    fails   0500         absent         absent
//
// The two tight masks cannot get there, and that is the accepted cost
// rather than a defect. A umask stripping the owner's execute or write
// bit makes every directory that user creates unusable to them —
// everywhere, not only here — so the only way to succeed would be to
// widen a directory this command does not own, which is a tool deciding
// it knows better than the umask. An earlier version did exactly that
// and bought these two rows by making the shared root 0755 under every
// mask, including 0077, where the user's own umask had said 0700.
//
// So the failure is honest instead: the message names the level this
// call created, the mode it actually has, and what to do about it. The
// last row below asserts that, because "it fails" is only acceptable if
// the failure is actionable.
//
// This test may not call t.Parallel, at any level: the umask is process
// global and a parallel sibling would read the mask this test set.
func TestSaveAndTheUmaskAboveOurOwnDirectory(t *testing.T) {
	entryMask := syscall.Umask(0)
	syscall.Umask(entryMask)

	const endpoint = "https://api.example.com"

	for _, tc := range []struct {
		mask int
		// wantSaved is whether a first run can store a token at all.
		wantSaved bool
		// shared is the mode the level above ours ends up with, which is
		// exactly what the mask left of the mode it was created with.
		shared os.FileMode
	}{
		{0o022, true, 0o755},
		{0o077, true, 0o700},
		{0o177, false, 0o600},
		{0o277, false, 0o500},
	} {
		t.Run(fmt.Sprintf("umask %04o", tc.mask), func(t *testing.T) {
			// The harness's own directory is made BEFORE the mask
			// changes, so what the mask affects is only what Save
			// creates. Everything below home is absent, which is the
			// state of a fresh account.
			home := t.TempDir()
			t.Setenv(envConfigPath, "")
			t.Setenv(envXDGConfigHome, "")
			t.Setenv("HOME", home)

			previous := syscall.Umask(tc.mask)
			defer syscall.Umask(previous)

			path, err := Path()
			if err != nil {
				t.Fatalf("Path(): %v", err)
			}
			leaf := filepath.Dir(path)
			shared := filepath.Dir(leaf)

			cfg := &Config{}
			saveErr := cfg.Save(testToken, endpoint)

			// The shared root is created either way, and its mode is
			// the umask's answer rather than ours. This assertion is
			// the one that would red if this command started
			// re-permissioning a directory it does not own.
			fi, err := os.Stat(shared)
			if err != nil {
				t.Fatalf("stat %s: %v", shared, err)
			}
			if perm := fi.Mode().Perm(); perm != tc.shared {
				t.Errorf("the shared config root came back at %04o under umask %04o, "+
					"want %04o — which is what the mask leaves of the mode it was "+
					"created with. Anything else means this command set the mode of a "+
					"directory it does not own", perm, tc.mask, tc.shared)
			}

			if !tc.wantSaved {
				if saveErr == nil {
					t.Fatalf("Save() succeeded under umask %04o — the only way to get "+
						"here is to widen a directory this command does not own",
						tc.mask)
				}
				// A failure is acceptable only if the user can act on
				// it. The operating system's own error names the inner
				// directory and no cause at all.
				msg := saveErr.Error()
				for _, want := range []string{
					shared,                         // the level that is actually wrong
					fmt.Sprintf("%04o", tc.shared), // the mode it actually has
					"umask",                        // what did it
					envConfigPath,                  // one of the ways out
				} {
					if !strings.Contains(msg, want) {
						t.Errorf("the failure does not mention %q, so the user cannot "+
							"act on it: %v", want, saveErr)
					}
				}
				if _, err := os.Stat(leaf); err == nil {
					t.Errorf("our own directory exists after a save that failed to " +
						"create it")
				}
				return
			}

			if saveErr != nil {
				t.Fatalf("Save() under umask %04o: %v", tc.mask, saveErr)
			}
			for _, want := range []struct {
				what string
				path string
				mode os.FileMode
			}{
				{"the config file", path, 0o600},
				{"this tool's own directory", leaf, 0o700},
			} {
				fi, err := os.Stat(want.path)
				if err != nil {
					t.Fatalf("stat %s (%s): %v", want.path, want.what, err)
				}
				if perm := fi.Mode().Perm(); perm != want.mode {
					t.Errorf("%s (%s) has mode %04o under umask %04o, want %04o — this "+
						"one IS ours, and the umask does not get a say in it",
						want.what, want.path, perm, tc.mask, want.mode)
				}
			}

			loaded, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if string(loaded.Token) != testToken {
				t.Errorf("the token did not survive a first run under umask %04o: %v",
					tc.mask, loaded.NoTokenReason)
			}
		})
	}

	// THE ORDINARY CASE, and it is why the two failures above are a cost
	// rather than a break. When the shared root already exists — which it
	// does on any machine that has ever run another tool that uses it —
	// every mask works, because the only directory this command creates
	// is its own and it sets that mode itself.
	t.Run("an existing shared root works under every mask", func(t *testing.T) {
		for _, mask := range []int{0o022, 0o077, 0o177, 0o277} {
			home := t.TempDir()
			shared := filepath.Join(home, ".config")
			if err := os.Mkdir(shared, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", shared, err)
			}
			if err := os.Chmod(shared, 0o755); err != nil {
				t.Fatalf("chmod %s: %v", shared, err)
			}
			t.Setenv(envConfigPath, "")
			t.Setenv(envXDGConfigHome, "")
			t.Setenv("HOME", home)

			previous := syscall.Umask(mask)
			cfg := &Config{}
			err := cfg.Save(testToken, endpoint)
			syscall.Umask(previous)
			if err != nil {
				t.Errorf("Save() under umask %04o with the shared root already there: "+
					"%v", mask, err)
			}
		}
	})

	// The distinctness row for the rule: a directory that was already
	// there keeps its mode exactly. A fix that corrected every level
	// rather than only its own would pass every assertion above and
	// silently re-permission a directory the user pointed this tool at.
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
