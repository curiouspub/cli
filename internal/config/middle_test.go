package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveSweepsItsOwnTempLitter covers the crash the deferred remove
// cannot see.
//
// The write goes to a temp file beside the config and becomes the config
// in one rename. The deferred remove covers the ERROR path; a crash
// between the write and the rename is a different thing entirely, and a
// defer never runs for it. What is left behind is a 0600 file holding
// the complete token under a name nothing ever looks at again, so it
// survives every later rotation of the credential it holds.
//
// STATED RESIDUE, because this is best effort and saying so is part of
// the fix: a crash during the sweep leaves litter, and a sweep that
// fails never fails a save. The file being written is worth more than
// the files being tidied.
//
// REQUIRED MUTATIONS, three, one per half of the rule:
//
//  1. In Save (config.go), delete the sweep call. This row reds with
//     both orphans still present and nothing else moves.
//  2. In sweepTempLitter, match "*" instead of the temp pattern. This
//     row reds on the bystanders, and so does most of the package — the
//     sweep eats the config file it has just written, which is a red
//     worth seeing at least once.
//  3. In sweepTempLitter, stop skipping directories. Only this row
//     reds, on the directory bystander.
//
// All three run, all three observed, and the file restored from a
// checksum-verified copy after each.
func TestSaveSweepsItsOwnTempLitter(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":1,"token":"old","api_url":"`+endpoint+`"}`)
	dir := filepath.Dir(path)

	// Two orphans in exactly the shape a crash leaves them, and two
	// things that merely live in the same directory. The second pair is
	// the distinctness half: a sweep that removed those would be
	// deleting a stranger's files out of a shared directory.
	orphans := []string{".config-853498953.json", ".config-11.json"}
	for _, name := range orphans {
		if err := os.WriteFile(filepath.Join(dir, name),
			[]byte(`{"token":"a-real-token-left-lying-about"}`), 0o600); err != nil {
			t.Fatalf("writing the orphan %s: %v", name, err)
		}
	}
	bystanders := []string{"notes.txt", "config.json.bak", ".configrc"}
	for _, name := range bystanders {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not ours"), 0o600); err != nil {
			t.Fatalf("writing the bystander %s: %v", name, err)
		}
	}
	// A DIRECTORY whose name matches the pattern. Removing one would
	// need a different call and would be a different kind of mistake, so
	// the sweep has to leave it alone.
	if err := os.Mkdir(filepath.Join(dir, ".config-adirectory.json"), 0o700); err != nil {
		t.Fatalf("making the directory bystander: %v", err)
	}

	cfg := &Config{Path: path}
	if err := cfg.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	for _, name := range orphans {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s is still there after a save, and it holds a complete token "+
				"under a name nothing will ever look at again", name)
		}
	}
	for _, name := range append(bystanders, ".config-adirectory.json") {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the save removed %s, which is not ours: %v", name, err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the config itself is gone: %v", err)
	}
}

// TestLoadSaysANewerFileIsNewerRatherThanCorrupt fixes a message the code
// already knew was wrong when it printed it: the map decode had
// succeeded and the version field had been read before anything called
// the file unreadable.
//
// REQUIRED MUTATION: in Load (config.go), drop the version gate. All
// three legs red — a version this build has never written is accepted
// and its token used. Nothing else moves; a file whose version field is
// absent still reads, which is the case the gate must not catch. Run,
// observed red, and the file restored from a checksum-verified copy.
func TestLoadSaysANewerFileIsNewerRatherThanCorrupt(t *testing.T) {
	const endpoint = "https://api.example.com"

	for _, tc := range []struct {
		name    string
		version string
		expect  string
	}{
		{"a schema from a later release", "2", "newer"},
		{"a schema far in the future", "99", "newer"},
		// A negative version was never written by anything, so "newer"
		// would be a guess. It gets the other sentence.
		{"a version nothing has ever written", "-7", "delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, `{"version":`+tc.version+`,"token":"`+testToken+
				`","api_url":"`+endpoint+`"}`)
			before := readFile(t, path)

			loaded, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if loaded.Token != "" {
				t.Errorf("a file this build does not understand handed back a token")
			}
			if loaded.NoTokenReason == nil {
				t.Fatalf("nothing was reported to the user")
			}
			msg := loaded.NoTokenReason.Error()
			if !strings.Contains(msg, path) {
				t.Errorf("the message does not name the file: %q", msg)
			}
			if !strings.Contains(msg, tc.expect) {
				t.Errorf("the message does not contain %q: %q", tc.expect, msg)
			}
			if strings.Contains(msg, "corrupt") {
				t.Errorf("the file is reported as corrupted when the code had already "+
					"read its version and knew otherwise: %q", msg)
			}
			if !strings.Contains(msg, tc.version) {
				t.Errorf("the message does not say which version it found: %q", msg)
			}
			if after := readFile(t, path); string(after) != string(before) {
				t.Errorf("the load path modified the config file")
			}
			// The version is carried on the value, which is what lets
			// the refusal below happen at all.
			if fmt.Sprint(loaded.Version) != tc.version {
				t.Errorf("Version = %d, want %s — a build that forgets which version "+
					"it declined to read is a build that overwrites it",
					loaded.Version, tc.version)
			}
		})
	}
}

// TestSaveRefusesToDowngradeAFileItCouldNotRead is the other half, and
// without it the half above is decoration.
//
// The load path declined to repair a file from the future, and the login
// flow's next save quietly rewrote it as this build's own version — one
// call later, with the newer release's fields still in it and its
// version number replaced by a lie about what they are.
//
// REQUIRED MUTATION: in Save (config.go), drop the version refusal. This
// row reds: the save succeeds and the file comes back claiming this
// build's schema. TestSaveLoadRoundTrip and the fresh-value rows stay
// green, because a value that read no file has no version to contradict.
// Run, observed red, and the file restored from a checksum-verified
// copy.
func TestSaveRefusesToDowngradeAFileItCouldNotRead(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":2,"token":"whatever-a-newer-build-stores",`+
		`"api_url":"`+endpoint+`","future_shape":{"a":1}}`)
	before := readFile(t, path)

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if err := loaded.Save(testToken, endpoint); err == nil {
		t.Error("Save() rewrote a file whose schema this build cannot read")
	}
	if after := readFile(t, path); string(after) != string(before) {
		t.Errorf("the file was rewritten\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestLoadOnADirectoryAdvisesTheRightThing covers the natural mistake:
// the base-directory variable takes a DIRECTORY, so a user pointing the
// override at one is following the convention rather than misreading it.
//
// What they used to get was advice to change the permissions of a
// directory and then to delete their config, neither of which is the
// problem.
//
// REQUIRED MUTATION: in Load (config.go), drop the IsDir branch. This
// row reds on the advice assertions — the read fails with the operating
// system's own "is a directory" and the generic message tells the user
// to fix permissions or delete it. Run, observed red, and the file
// restored from a checksum-verified copy.
func TestLoadOnADirectoryAdvisesTheRightThing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envConfigPath, dir)

	loaded, err := Load("https://api.example.com")
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Token != "" {
		t.Errorf("a directory yielded a token")
	}
	if loaded.NoTokenReason == nil {
		t.Fatalf("nothing was reported to the user")
	}
	msg := loaded.NoTokenReason.Error()
	if !strings.Contains(msg, dir) {
		t.Errorf("the message does not name the path: %q", msg)
	}
	if !strings.Contains(msg, "directory") {
		t.Errorf("the message does not say what is actually wrong: %q", msg)
	}
	if !strings.Contains(msg, envConfigPath) {
		t.Errorf("the message does not name the variable that has to change: %q", msg)
	}
	if strings.Contains(msg, "chmod") {
		t.Errorf("the message advises a permission change for something that is not "+
			"a permission problem: %q", msg)
	}
	if len(loaded.Warnings) != 0 {
		t.Errorf("a directory produced a file-permission warning: %q", loaded.Warnings)
	}
	if loaded.PermissionsChecked {
		t.Error("PermissionsChecked = true for a path whose mode was never the " +
			"question")
	}
}

// TestLoadToleratesAByteOrderMark exists because the comments in this
// package say, correctly, that a person will open this file by hand —
// and some editors write a mark at the front of a UTF-8 file when they
// save it. A config that stops working after somebody looked at it is
// the worst kind of bug to be on the receiving end of.
//
// REQUIRED MUTATION: in Load (config.go), drop the mark-trimming call.
// The first row reds, reported as a corrupt file. The two rows below it
// stay green, which is the pair: a mark at the front is tolerated and a
// file that is not otherwise valid JSON is still not valid JSON. Run,
// observed red, and the file restored from a checksum-verified copy.
func TestLoadToleratesAByteOrderMark(t *testing.T) {
	const endpoint = "https://api.example.com"
	const mark = "\xef\xbb\xbf"
	body := `{"version":1,"token":"` + testToken + `","api_url":"` + endpoint + `"}`

	t.Run("a mark at the front is tolerated", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, mark+body)

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if string(loaded.Token) != testToken {
			t.Errorf("a file an editor added a byte-order mark to was rejected: %v",
				loaded.NoTokenReason)
		}
	})

	// The distinctness rows. Tolerating a mark must not become
	// tolerating anything that happens to sit in front of the JSON.
	t.Run("a mark in the middle is still corrupt", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, `{"version":1,`+mark+`"token":"x"}`)

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if loaded.NoTokenReason == nil {
			t.Error("a file with a mark in the middle of it was accepted")
		}
	})

	t.Run("a file that is nothing but a mark is still corrupt", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, mark)

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if loaded.NoTokenReason == nil {
			t.Error("an empty file was accepted")
		}
	})
}

// TestNoPrintedURLCarriesUserinfo is the one this package argues for
// itself and did not do.
//
// The canonical form refuses a URL carrying a username or password, and
// says in terms that this keeps a credential out of a comparison string.
// It was in the PRINTED string instead — measured twice in one report,
// once from the stored value and once from inside the wrapped error that
// explains it. Printed strings are where terminals, CI logs and
// screenshots are.
//
// REQUIRED MUTATION: in redactUserinfo (config.go), return the string
// unchanged. All four rows here red, and
// TestRedactUserinfoLeavesOrdinaryStringsAlone stays GREEN — which is
// the pair worth having, since the identity function satisfies every
// assertion in it. Run, observed red, and the file restored from a
// checksum-verified copy.
func TestNoPrintedURLCarriesUserinfo(t *testing.T) {
	const password = "hunter2-not-a-real-password"
	const stored = "https://someone:" + password + "@api.example.com"
	const endpoint = "https://api.example.com"

	check := func(t *testing.T, where, host, msg string) {
		t.Helper()
		if strings.Contains(msg, password) {
			t.Errorf("%s carries the password from the stored URL: %s", where, msg)
		}
		if !strings.Contains(msg, host) {
			t.Errorf("%s does not name the host at all, which is the part the user "+
				"needs: %s", where, msg)
		}
	}

	t.Run("the reason a stored endpoint could not be used", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, fmt.Sprintf(
			`{"version":1,"token":%q,"api_url":%q}`, testToken, stored))

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if loaded.NoTokenReason == nil {
			t.Fatalf("a stored URL carrying credentials was accepted")
		}
		check(t, "the load message", "api.example.com", loaded.NoTokenReason.Error())
	})

	t.Run("the reason this run's own endpoint could not be used", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, fmt.Sprintf(
			`{"version":1,"token":%q,"api_url":%q}`, testToken, endpoint))

		loaded, err := Load(stored)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if loaded.NoTokenReason == nil {
			t.Fatalf("an effective URL carrying credentials was accepted")
		}
		check(t, "the effective-endpoint message", "api.example.com", loaded.NoTokenReason.Error())
	})

	t.Run("the refusal to save against such an endpoint", func(t *testing.T) {
		path := hermeticPath(t)
		cfg := &Config{Path: path}
		err := cfg.Save(testToken, stored)
		if err == nil {
			t.Fatalf("Save() accepted an endpoint carrying credentials")
		}
		check(t, "the save refusal", "api.example.com", err.Error())
	})

	// WHAT THIS ROW ACTUALLY OBSERVES, stated because its name would
	// otherwise promise more. A URL carrying credentials can never reach
	// the two-endpoints message: the canonical form refuses userinfo
	// before any comparison happens, so every such value lands in the
	// branch above. The mismatch message is redacted anyway, and this
	// row confirms the redaction on a second host rather than a second
	// branch. The reason for redacting an unreachable path is that the
	// refusal making it unreachable lives in another package, and a
	// guard that moves should not silently open a leak here.
	t.Run("a stored endpoint on another host, redacted the same way", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, fmt.Sprintf(
			`{"version":1,"token":%q,"api_url":%q}`, testToken,
			"https://someone:"+password+"@api.example.org"))

		loaded, err := Load(endpoint)
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if loaded.NoTokenReason == nil {
			t.Fatalf("nothing was reported")
		}
		check(t, "the message", "api.example.org", loaded.NoTokenReason.Error())
	})

	// POSITIVE CONTROL for the search. The instrument has to be able to
	// find this password in a string that really contains it, or every
	// assertion above is satisfied by a search that never matches.
	t.Run("positive control: the search can see the password", func(t *testing.T) {
		if !strings.Contains(fmt.Sprintf("the stored URL is %q", stored), password) {
			t.Error("the search cannot find the password in a string that has it")
		}
	})
}

// TestRedactUserinfoLeavesOrdinaryStringsAlone is the distinctness half
// of the helper: a scrub that changed anything else would corrupt the
// messages it is supposed to be protecting.
func TestRedactUserinfoLeavesOrdinaryStringsAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"https://api.example.com",
		"http://localhost:8080/v1",
		`the config file at /home/someone/.config/curious/config.json is unreadable`,
		"https://api.example.com/a@b",
		"someone@example.com",
	} {
		if got := redactUserinfo(s); got != s {
			t.Errorf("redactUserinfo(%q) = %q, want it unchanged", s, got)
		}
	}
}

// TestSaveKeepsTheFileValid is the round trip for everything above at
// once: after all of it, what this package writes is still what it reads.
func TestSaveKeepsTheFileValid(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, "\xef\xbb\xbf"+`{"version":1,"token":"old","api_url":"`+
		endpoint+`","future_flag":true}`)

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if err := loaded.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	data := readFile(t, path)
	if strings.HasPrefix(string(data), "\xef\xbb\xbf") {
		t.Error("the rewritten file starts with a byte-order mark, which this " +
			"package never writes")
	}
	var reread map[string]json.RawMessage
	if err := json.Unmarshal(data, &reread); err != nil {
		t.Fatalf("the rewritten file is not valid JSON: %v", err)
	}
	again, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load() after Save(): %v", err)
	}
	if string(again.Token) != testToken || again.NoTokenReason != nil {
		t.Errorf("the round trip did not come back: %v", again.NoTokenReason)
	}
}
