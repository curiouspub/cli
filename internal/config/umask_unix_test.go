//go:build unix

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestUmaskDoesNotWidenTheConfigFile is the row this package's file mode
// actually rests on.
//
// The mode passed when a file or directory is created is masked by the
// process umask, which makes it a CEILING rather than a setting: the
// same code produces different permissions on two machines whose users
// have configured different umasks, and neither of them is wrong about
// their own umask. So the mode is set explicitly after creation, and
// this test runs the whole save under four masks to prove it.
//
// The four masks are chosen, not sampled, and two of them exist only
// because a mutation proved the other two could not see the defect:
//
//   - 0000 is the permissive extreme. Nothing is removed, so the code
//     gets exactly what it asks for — this is the leg that catches a
//     mode that was too wide in the first place.
//   - 0077 is the ordinary careful setting, and the one under which
//     almost any implementation looks perfect.
//   - 0177 masks the owner's EXECUTE bit. A directory created 0700 comes
//     out 0600, and nothing can be created inside a directory with no
//     execute bit — so this is the leg where the explicit chmod of the
//     DIRECTORY has to do real work.
//   - 0277 masks the owner's WRITE bit. A file created 0600 comes out
//     0400 — so this is the leg where the explicit chmod of the FILE has
//     to do real work.
//
// THE LAST TWO ARE HERE BECAUSE THE FIRST DRAFT OF THIS COMMENT WAS
// WRONG, and the mutation is what said so. It claimed 0177 would leave a
// created file at 0400 and therefore red if the file's chmod were
// removed. It does not: 0177 masks the execute bit, which a 0600 create
// never asked for, so 0600 &^ 0177 is still 0600. Deleting the file's
// chmod passed this table in full until 0277 was added. A comment that
// predicts a red is a claim about coverage, and an unrun one teaches the
// next reader a false model of what their own table sees.
//
// This test may not call t.Parallel, at any level. The umask is process
// global: a parallel sibling would read the mask this test set, and the
// failure would be a flake that depends on scheduling.
//
// REQUIRED MUTATION (file mode): in chmodFile (mode_other.go), return
// nil without calling f.Chmod. ONLY the 0277 leg reds, with mode 0400.
// Run, observed — first green against three masks, then red once the
// fourth existed — and the file restored from a checksum-verified copy.
//
// SECOND REQUIRED MUTATION (file mode, the other direction): make
// chmodFile ask for 0o644. EVERY leg reds with mode 0644, including
// 0077. That is worth knowing and was also mispredicted here: a chmod is
// not masked by the umask, only a create is, so a mode set explicitly
// after creation is exactly what it says whatever the mask.
//
// THIRD REQUIRED MUTATION (directory mode): in createConfigDir
// (config.go), disable the chmod of the leaf. THREE legs red, and the
// count moved when the creating mode did — the leaf is now created with
// the conventional directory mode and pinned to 0700 afterwards, so
// under umask 0000 it stays 0755 and reds on the mode assertion, while
// 0177 and 0277 red at Save() with a permission error because the temp
// file cannot be created inside the directory at all. Umask 0077 stays
// GREEN, because there the mask happens to produce 0700 by itself —
// which is precisely why a table of one mask proves nothing here.
func TestUmaskDoesNotWidenTheConfigFile(t *testing.T) {
	// The umask this process started with, read the only way the API
	// allows — by setting it and putting it straight back.
	entryMask := syscall.Umask(0)
	syscall.Umask(entryMask)

	for _, mask := range []int{0, 0o077, 0o177, 0o277} {
		t.Run(maskName(mask), func(t *testing.T) {
			// The temp directory is created BEFORE the mask is changed:
			// it belongs to the harness, and creating it under 0177 would
			// leave the test unable to write into its own fixture.
			dir := t.TempDir()
			path := filepath.Join(dir, "curious", "config.json")
			t.Setenv(envConfigPath, path)

			previous := syscall.Umask(mask)
			defer syscall.Umask(previous)

			cfg := &Config{}
			if err := cfg.Save(testToken, "https://api.example.com"); err != nil {
				t.Fatalf("Save() under umask %04o: %v", mask, err)
			}

			fi, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", path, err)
			}
			if perm := fi.Mode().Perm(); perm != 0o600 {
				t.Errorf("config file mode = %04o under umask %04o, want 0600 — this "+
					"file holds a bearer token and every other user on the machine "+
					"can read anything wider", perm, mask)
			}

			di, err := os.Stat(filepath.Dir(path))
			if err != nil {
				t.Fatalf("stat %s: %v", filepath.Dir(path), err)
			}
			if perm := di.Mode().Perm(); perm != 0o700 {
				t.Errorf("config directory mode = %04o under umask %04o, want 0700 — "+
					"a directory anyone can list is a token anyone can find", perm, mask)
			}
		})
	}

	// POSITIVE CONTROL for the mode instrument. os.Stat().Mode().Perm()
	// has to be able to report something other than 0600, or every
	// assertion above is satisfied by an instrument that always says
	// "0600" and nothing here would notice.
	t.Run("positive control: the instrument can see a wider mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "widened.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o644 {
			t.Errorf("the instrument reported %04o for a file this test just made "+
				"0644 — it cannot observe the failure the rows above look for", perm)
		}
	})

	// The umask is process global, so leaving it changed would corrupt
	// every test that runs after this one — in this package or, through
	// the same process, anywhere else. Restoring it is a claim, so it is
	// checked.
	if exitMask := syscall.Umask(0); exitMask != entryMask {
		syscall.Umask(entryMask)
		t.Errorf("this test left the process umask at %04o, having found it at "+
			"%04o", exitMask, entryMask)
	} else {
		syscall.Umask(entryMask)
	}
}

func maskName(mask int) string {
	switch mask {
	case 0:
		return "umask 0000, nothing is masked out"
	case 0o077:
		return "umask 0077, the careful default"
	case 0o177:
		return "umask 0177, which masks the execute bit the directory needs"
	default:
		return "umask 0277, which masks the write bit the file needs"
	}
}
