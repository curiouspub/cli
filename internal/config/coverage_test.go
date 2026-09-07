package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadUnusableEffectiveEndpointIsAMismatchNotAFailure covers a
// branch that existed, was correct, and had nothing exercising it.
//
// The asymmetry it sits inside is deliberate and worth stating, because
// a reader meets it as an inconsistency: an EMPTY effective endpoint is
// a hard error that ends the call, while an unparseable one is a soft
// mismatch that costs a login. Empty can only be a caller that forgot to
// wire the value through — a bug nobody would ever see, because every
// run would just log in again and the symptom would look like a server
// problem. Garbage most likely arrives from the user's own environment
// variable, where a hard failure would strand them with no way past it.
//
// REQUIRED MUTATION: in endpointMismatch (config.go), return nil when
// api.CanonicalKey fails on the EFFECTIVE value — the "cannot tell, so
// allow it" reading, which is the same mistake the stored-value row
// beside this one exists for, one argument over. All four rows here
// red, and so does the effective-endpoint leg of
// TestNoPrintedURLCarriesUserinfo — which reaches the same branch by a
// different door and is worth knowing about, since it means that row
// would have caught this too. The stored-value row does not move. Run,
// observed red, and the file restored from a checksum-verified copy.
func TestLoadUnusableEffectiveEndpointIsAMismatchNotAFailure(t *testing.T) {
	const stored = "https://api.example.com"

	for _, effective := range []string{
		"not a url",
		"ftp://files.example.com",
		"https://",
		"://api.example.com",
	} {
		t.Run(effective, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path,
				`{"version":1,"token":"`+testToken+`","api_url":"`+stored+`"}`)
			before := readFile(t, path)

			loaded, err := Load(effective)
			if err != nil {
				t.Fatalf("Load() ended the run (%v) — an endpoint this client cannot "+
					"read costs a login, it does not stop the command", err)
			}
			if loaded.Token != "" {
				t.Errorf("the token was handed to a run whose own endpoint could not " +
					"be read, so nothing could have compared it to the one the token " +
					"was issued against")
			}
			if loaded.NoTokenReason == nil {
				t.Fatalf("nothing was reported to the user")
			}
			if !strings.Contains(loaded.NoTokenReason.Error(), effective) {
				t.Errorf("the message does not name the endpoint that could not be "+
					"read: %q", loaded.NoTokenReason)
			}
			if after := readFile(t, path); string(after) != string(before) {
				t.Errorf("the file was modified: the never-repair rule holds here too")
			}
		})
	}
}

// TestSaveWritesItsTempFileBesideTheTarget pins the one property that
// makes the atomic write atomic at all.
//
// A rename is atomic only within a filesystem, so a temp file written
// anywhere else cannot become the config by renaming — and the fallback
// everyone reaches for then is a copy, which is exactly the non-atomic
// write this design exists to avoid.
//
// It also asserts the temp file is named by the pattern the sweep looks
// for. That is the same fact twice on purpose: a sweep searching for a
// shape the writer had stopped using would find nothing and report
// success for ever.
//
// REQUIRED MUTATIONS, two:
//
//  1. In Save (config.go), create the temp file in os.TempDir() instead
//     of the target's directory. This reds on the directory assertion —
//     and on a machine where the two are the same filesystem the save
//     still SUCCEEDS, which is why the property is asserted rather than
//     inferred from a save that worked.
//  2. In Save, create it under a name the sweep's pattern does not
//     match. Only the pattern assertion here reds, and
//     TestSaveSweepsItsOwnTempLitter stays green — it plants its own
//     litter and never looks at what this call wrote.
//
// Both run, both observed, and the file restored from a
// checksum-verified copy after each.
func TestSaveWritesItsTempFileBesideTheTarget(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)

	original := renameFile
	t.Cleanup(func() { renameFile = original })
	var from, to string
	renameFile = func(source, target string) error {
		from, to = source, target
		return original(source, target)
	}

	cfg := &Config{}
	if err := cfg.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	if from == "" {
		t.Fatal("the rename seam was never reached, so this row observed nothing")
	}
	if got, want := filepath.Dir(from), filepath.Dir(path); got != want {
		t.Errorf("the temp file was written in %q and the config lives in %q — a "+
			"rename across filesystems fails, and the usual repair for that is a "+
			"copy, which is the non-atomic write this design exists to avoid",
			got, want)
	}
	if to != path {
		t.Errorf("the rename target is %q, want the config path %q", to, path)
	}
	matched, err := filepath.Match(tempPattern, filepath.Base(from))
	if err != nil {
		t.Fatalf("matching %q against %q: %v", tempPattern, filepath.Base(from), err)
	}
	if !matched {
		t.Errorf("the temp file %q is not named by the pattern the sweep looks for "+
			"(%q), so a crash before the rename would leave a token behind that "+
			"nothing ever collects", filepath.Base(from), tempPattern)
	}
	if _, err := os.Stat(from); err == nil {
		t.Errorf("the temp file %q still exists after a successful save", from)
	}
}
