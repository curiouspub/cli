package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSaveSweepsItsOwnTempLitter covers the crash the deferred remove
// cannot see, and — just as much — the files it must not touch.
//
// The write goes to a temp file beside the config and becomes the config
// in one rename. The deferred remove covers the ERROR path; a crash
// between the write and the rename is a different thing and a defer
// never runs for it. What is left behind is a 0600 file holding the
// complete token under a name nothing ever looks at again, so it
// survives every later rotation of the credential it holds.
//
// TWO THINGS MAKE THE SWEEP SAFE, and the first draft had neither.
//
// A DISTINCTIVE PREFIX. The pattern swept was the pattern the temp files
// happened to use, and nothing separated a file this process created
// from one that merely matched. Measured after one save on that version:
// .config-backup.json deleted, .config-2024.json deleted. A user who
// backs their config up under an obvious name loses it on their next
// login, and finds out when they go looking for it.
//
// AN AGE GATE. A second process writing its own config at this instant
// has an in-flight temp file in the same directory. An hour is far
// longer than any write takes and far shorter than a leaked token should
// live.
//
// STATED RESIDUE, because this is best effort: a crash during the sweep
// leaves litter, a sweep that fails never fails a save, and litter
// younger than the gate waits for the next save an hour later rather
// than being collected now.
//
// REQUIRED MUTATIONS, four, and each names what it must not move:
//
//  1. In Save (config.go), delete the sweep call. The old-orphan
//     assertion reds; nothing else moves.
//  2. In sweepTempLitter, drop the age gate. Only the fresh-orphan row
//     reds — the one standing in for a concurrent writer.
//  3. In sweepTempLitter, match ".config-*.json", the pattern this used
//     to use. The two user-backup rows red and the orphan rows stay
//     green.
//  4. In sweepTempLitter, stop skipping directories. Only the directory
//     bystander reds — and it only reds because the fixture ages that
//     directory past the gate. A first attempt left it at its creation
//     time, where the age check excludes it before the directory check
//     is reached, and this mutation came back GREEN over a skip that had
//     stopped being load-bearing.
//
// All four run, all four observed, and the file restored from a
// checksum-verified copy after each.
func TestSaveSweepsItsOwnTempLitter(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":1,"token":"old","api_url":"`+endpoint+`"}`)
	dir := filepath.Dir(path)

	write := func(name, body string, age time.Duration) string {
		t.Helper()
		full := filepath.Join(dir, name)
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(full, when, when); err != nil {
			t.Fatalf("ageing %s: %v", name, err)
		}
		return full
	}

	const leftByACrash = `{"token":"a-real-token-left-lying-about"}`
	// Litter in exactly the shape a crash leaves it, old enough to be
	// nobody's business but ours.
	oldOrphans := []string{
		write(".curious-config-tmp-853498953", leftByACrash, 2*time.Hour),
		write(".curious-config-tmp-11", leftByACrash, 90*time.Minute),
	}
	// The same shape, written a moment ago: this is what a second
	// process's in-flight write looks like, and removing it would fail
	// that process's save.
	freshOrphan := write(".curious-config-tmp-777", leftByACrash, 0)
	// Files that merely live in the same directory. The first two are
	// the measured regression: they matched the pattern this used to
	// sweep, and they are somebody's backup of their own config.
	bystanders := []string{
		write(".config-backup.json", "a user's backup", 30*24*time.Hour),
		write(".config-2024.json", "a user's older backup", 30*24*time.Hour),
		write("keep.json", "not ours", 30*24*time.Hour),
		write(".config-x.txt", "not ours", 30*24*time.Hour),
		write("config.json.bak", "not ours", 30*24*time.Hour),
	}
	// A DIRECTORY whose name matches, AGED PAST THE GATE. Removing one
	// needs a different call and would be a different kind of mistake —
	// and an empty directory is exactly what os.Remove will happily take
	// away. The ageing is load-bearing: left at its creation time the
	// age gate excludes it first, the directory check is never reached,
	// and this row cannot see whether it exists at all. Measured, by
	// mutating the check away and watching the suite stay green.
	matchingDir := filepath.Join(dir, ".curious-config-tmp-adirectory")
	if err := os.Mkdir(matchingDir, 0o700); err != nil {
		t.Fatalf("making the directory bystander: %v", err)
	}
	aged := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(matchingDir, aged, aged); err != nil {
		t.Fatalf("ageing the directory bystander: %v", err)
	}

	cfg := &Config{Path: path}
	if err := cfg.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	for _, full := range oldOrphans {
		if _, err := os.Stat(full); err == nil {
			t.Errorf("%s is still there after a save, and it holds a complete token "+
				"under a name nothing will ever look at again", filepath.Base(full))
		}
	}
	if _, err := os.Stat(freshOrphan); err != nil {
		t.Errorf("the sweep removed %s, which is the shape a second process's "+
			"in-flight write has: %v", filepath.Base(freshOrphan), err)
	}
	for _, full := range append(bystanders, matchingDir) {
		if _, err := os.Stat(full); err != nil {
			t.Errorf("the save removed %s, which is not ours: %v",
				filepath.Base(full), err)
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
		key     string
		version string
		expect  string
	}{
		{"a schema from a later release", "version", "2", "newer"},
		{"a schema far in the future", "version", "99", "newer"},
		// A negative version was never written by anything, so "newer"
		// would be a guess. It gets the other sentence.
		{"a version nothing has ever written", "version", "-7", "delete"},
		// The decoder fills the struct from this spelling, so anything
		// reading the field back by an exact name would decide the file
		// carries no version and read a newer release's file as its own.
		{"a later schema under a folded spelling", "VERSION", "2", "newer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, `{"`+tc.key+`":`+tc.version+`,"token":"`+testToken+
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

// TestLoadRefusesAVersionThatIsNotOne separates three spellings that
// were being read as one.
//
// The decoded version was only assigned when it was non-zero, so ABSENT,
// ZERO and NULL all arrived at the same place: treated as "no version",
// and accepted as this build's own. Two of those three are a field that
// is PRESENT and says something, and one of them — zero — is a real
// number that no release has ever written. A file claiming a schema
// version that does not exist is not a file to guess about; guessing
// here means reading a stranger's format as if it were ours.
//
// Absent keeps its meaning, and that is the distinctness row: the field
// missing altogether is the only one of the three that says nothing, and
// it is read as this build's schema exactly as before.
//
// REQUIRED MUTATION: in Config.read (config.go), accept the version
// whenever the decoded value is non-zero, without asking whether the
// field was present — which is the shape this replaces. Both refusal
// rows red and the two rows that must still load stay green. Run,
// observed red, and
// the file restored from a checksum-verified copy.
func TestLoadRefusesAVersionThatIsNotOne(t *testing.T) {
	const endpoint = "https://api.example.com"

	for _, tc := range []struct {
		name     string
		body     string
		accepted bool
		// literal is what the message has to quote back, so the user can
		// see which of the three spellings their file has.
		literal string
	}{
		{
			name:    "a version of zero, which is a number no release has written",
			body:    `{"version":0,"token":"%s","api_url":"%s"}`,
			literal: "0",
		},
		{
			name:    "a version of null, which is a field that says nothing",
			body:    `{"version":null,"token":"%s","api_url":"%s"}`,
			literal: "null",
		},
		// THE FIELD IS FOUND THE WAY THE DECODER FINDS IT. The struct
		// is filled by a fold match, so a file spelling the key with a
		// capital still SETS the version — and a check that looked the
		// key up byte for byte would find nothing, conclude the field
		// was absent, and read a file claiming a schema that does not
		// exist as though it claimed ours. That is the same two-relation
		// disagreement this package refuses elsewhere, arriving in the
		// field that decides whether the rest is readable at all.
		{
			name:    "a zero under a spelling the decoder folds onto the field",
			body:    `{"Version":0,"token":"%s","api_url":"%s"}`,
			literal: "0",
		},
		// The distinctness rows: absent is not the same as present and
		// empty, and the version this build writes still works.
		{
			name:     "no version field at all",
			body:     `{"token":"%s","api_url":"%s"}`,
			accepted: true,
		},
		{
			name:     "the version this build writes",
			body:     `{"version":1,"token":"%s","api_url":"%s"}`,
			accepted: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, fmt.Sprintf(tc.body, testToken, endpoint))
			before := readFile(t, path)

			loaded, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if tc.accepted {
				if loaded.NoTokenReason != nil {
					t.Fatalf("the file was refused: %v", loaded.NoTokenReason)
				}
				if string(loaded.Token) != testToken {
					t.Errorf("the token was not returned")
				}
				if loaded.Version != SchemaVersion {
					t.Errorf("Version = %d, want %d", loaded.Version, SchemaVersion)
				}
				return
			}

			if loaded.Token != "" {
				t.Errorf("a token came back out of a file claiming a schema version " +
					"that has never existed")
			}
			if loaded.NoTokenReason == nil {
				t.Fatalf("nothing was reported to the user")
			}
			msg := loaded.NoTokenReason.Error()
			if !strings.Contains(msg, path) {
				t.Errorf("the message does not name the file: %q", msg)
			}
			if !strings.Contains(msg, tc.literal) {
				t.Errorf("the message does not quote back what the file actually "+
					"says (%q), so the user cannot tell which of the two spellings "+
					"they have: %q", tc.literal, msg)
			}
			if after := readFile(t, path); string(after) != string(before) {
				t.Errorf("the load path modified the config file")
			}
		})
	}
}
