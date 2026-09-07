package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

// testToken is deliberately not shaped like anything real. Every
// assertion about redaction and about what reaches the disk searches for
// this exact string, so it has to be one that cannot occur by accident.
const testToken = "token-for-tests-not-a-real-credential"

// hermeticPath points this package at a file inside the test's own temp
// directory and returns it. Nothing in this suite may read or write the
// developer's real config: a test that touches it would either fail on
// one machine and pass on another, or — worse — overwrite a working
// token while proving something about a different file.
//
// The nested "curious" directory is not decoration. It is the directory
// Save has to create, which is the only way the mode it creates it with
// is ever exercised.
func hermeticPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "curious", "config.json")
	t.Setenv(envConfigPath, path)
	return path
}

// writeConfigFile puts exact bytes on disk, so a test can describe a file
// this package would never write — a corrupt one, one from a future
// release, one whose endpoint no longer matches.
func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// TestPathPrecedence pins the resolution order the whole package rests
// on. It matters beyond tidiness: the first entry is what makes every
// test in this file hermetic, so if it stopped winning, this suite would
// quietly start reading and writing the developer's real token file.
//
// The platform default is asserted in the platform-specific files beside
// this one — it is the one leg whose expected value is different on
// every operating system.
//
// REQUIRED MUTATION: in Path (config.go), move the CURIOUS_CONFIG block
// below the XDG_CONFIG_HOME block. The first subtest reds with the XDG
// path where the explicit file was asked for. Run, observed red, and the
// file restored from a checksum-verified copy.
func TestPathPrecedence(t *testing.T) {
	t.Run("CURIOUS_CONFIG beats XDG_CONFIG_HOME", func(t *testing.T) {
		explicit := filepath.Join(t.TempDir(), "somewhere", "else.json")
		t.Setenv(envConfigPath, explicit)
		t.Setenv(envXDGConfigHome, t.TempDir())

		got, err := Path()
		if err != nil {
			t.Fatalf("Path(): %v", err)
		}
		if got != explicit {
			t.Errorf("Path() = %q, want %q — an explicit file path must win over "+
				"every other rule, and it is also the escape hatch this suite needs",
				got, explicit)
		}
	})

	t.Run("XDG_CONFIG_HOME beats the platform default", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv(envConfigPath, "")
		t.Setenv(envXDGConfigHome, xdg)

		want := filepath.Join(xdg, dirName, fileName)
		got, err := Path()
		if err != nil {
			t.Fatalf("Path(): %v", err)
		}
		if got != want {
			t.Errorf("Path() = %q, want %q — a user who has moved their config tree "+
				"has said where it goes", got, want)
		}
	})

	// The two variables are treated differently on purpose, so the
	// difference is asserted rather than left to the doc comment. The
	// standard's variable is read out of an environment this program did
	// not set up; the override is something a person typed for this
	// command, and quietly resolving it would put the token somewhere
	// other than where they said.
	t.Run("a relative CURIOUS_CONFIG is honoured exactly as given", func(t *testing.T) {
		for _, given := range []string{"config.json", "./cfg/config.json", "~/cfg.json"} {
			t.Setenv(envConfigPath, given)
			t.Setenv(envXDGConfigHome, t.TempDir())

			got, err := Path()
			if err != nil {
				t.Fatalf("Path(): %v", err)
			}
			if got != given {
				t.Errorf("Path() = %q, want %q exactly — the override names the file "+
					"and is used as given", got, given)
			}
		}
	})

	t.Run("a relative XDG_CONFIG_HOME is ignored", func(t *testing.T) {
		t.Setenv(envConfigPath, "")
		t.Setenv(envXDGConfigHome, "relative/path")

		got, err := Path()
		if err != nil {
			t.Fatalf("Path(): %v", err)
		}
		if strings.HasPrefix(got, "relative") {
			t.Errorf("Path() = %q — a relative base would resolve against the working "+
				"directory, scattering token files wherever the user happened to be "+
				"standing; the base-directory standard says to ignore it", got)
		}
	})
}

// TestSaveLoadRoundTrip is the whole point of the package in one row:
// what Save writes, Load returns.
//
// REQUIRED MUTATION: in Config.marshal (config.go), replace
// `keyToken: string(c.Token)` with `keyToken: c.Token`. It compiles —
// the map is map[string]any — and it writes the redaction placeholder to
// disk instead of the token, which is the exact defect this package's
// on-disk struct exists to prevent. Both the token assertion here and
// the on-disk assertion in the next test red. Run, observed red, and the
// file restored from a checksum-verified copy.
func TestSaveLoadRoundTrip(t *testing.T) {
	path := hermeticPath(t)
	const endpoint = "https://api.example.com"

	saved := &Config{}
	if err := saved.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.NoTokenReason != nil {
		t.Fatalf("Load() reported no usable token: %v", loaded.NoTokenReason)
	}
	if string(loaded.Token) != testToken {
		t.Errorf("loaded token does not match the saved one — a round trip that "+
			"loses the token is a login that never sticks (got %d bytes, want %d)",
			len(loaded.Token), len(testToken))
	}
	if loaded.APIURL != endpoint {
		t.Errorf("loaded APIURL = %q, want %q", loaded.APIURL, endpoint)
	}
	if loaded.Path != path {
		t.Errorf("loaded Path = %q, want %q", loaded.Path, path)
	}

	// The file is JSON, and it carries a schema version from its first
	// write. Adding a version later means guessing what the versionless
	// files meant.
	var onDisk map[string]any
	if err := json.Unmarshal(readFile(t, path), &onDisk); err != nil {
		t.Fatalf("the file this package wrote is not valid JSON: %v", err)
	}
	if v, ok := onDisk[keyVersion].(float64); !ok || int(v) != SchemaVersion {
		t.Errorf("on-disk %q = %v, want %d", keyVersion, onDisk[keyVersion], SchemaVersion)
	}
}

// TestSaveWritesThePlainTokenNotThePlaceholder is the single most
// valuable row in this file, because the bug it pins passes every other
// check: the file is valid JSON, correctly permissioned, atomically
// renamed, and completely useless.
//
// ui.Secret redacts itself through every marshaller it has — JSON, text
// and binary — so a struct holding one marshals the placeholder into the
// file. The POSITIVE CONTROL below is what makes this assertion worth
// anything: it marshals a Secret directly and proves the search really
// can find a placeholder when one is there. Without it, "the placeholder
// is not in the file" is equally satisfied by a search that never
// matches anything.
//
// REQUIRED MUTATION: the same one as the round trip above — pass c.Token
// rather than string(c.Token) into the merge map. Run, observed red on
// both halves of this test, and the file restored from a
// checksum-verified copy.
func TestSaveWritesThePlainTokenNotThePlaceholder(t *testing.T) {
	path := hermeticPath(t)

	cfg := &Config{}
	if err := cfg.Save(testToken, "https://api.example.com"); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	written := string(readFile(t, path))

	// POSITIVE CONTROL: the instrument can observe the failure it is
	// looking for. A struct holding a ui.Secret really does marshal to
	// the placeholder, so a placeholder in the file below would be found.
	control, err := json.Marshal(struct {
		Token ui.Secret `json:"token"`
	}{Token: testToken})
	if err != nil {
		t.Fatalf("marshalling the control: %v", err)
	}
	placeholder := strings.TrimSuffix(strings.TrimPrefix(string(control), `{"token":"`), `"}`)
	if placeholder == testToken || placeholder == "" {
		t.Fatalf("the control did not redact anything (%s) — this test cannot "+
			"detect what it exists to detect", control)
	}

	if strings.Contains(written, placeholder) {
		t.Errorf("the config file contains the redaction placeholder %q instead of "+
			"the token: the write succeeds, the file looks right, and every later "+
			"run authenticates with a placeholder and is told to log in again",
			placeholder)
	}
	if !strings.Contains(written, testToken) {
		t.Errorf("the config file does not contain the token that was saved")
	}
}

// TestLoadPreservesUnknownFields keeps an older binary from destroying a
// newer one's state. A release adds a field, a user runs an older
// release once, and the field has to still be there afterwards.
//
// REQUIRED MUTATION: in Config.marshal (config.go), delete the loop that
// copies c.unknown into the output map. This test reds with the future
// field gone from the rewritten file. Run, observed red, and the file
// restored from a checksum-verified copy.
func TestLoadPreservesUnknownFields(t *testing.T) {
	path := hermeticPath(t)
	const endpoint = "https://api.example.com"
	writeConfigFile(t, path, `{
	  "version": 1,
	  "token": "`+testToken+`",
	  "api_url": "`+endpoint+`",
	  "future_flag": true,
	  "future_object": {"nested": ["a", 2]}
	}`)

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.NoTokenReason != nil {
		t.Fatalf("Load() reported no usable token: %v", loaded.NoTokenReason)
	}
	if err := loaded.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	var reread map[string]any
	if err := json.Unmarshal(readFile(t, path), &reread); err != nil {
		t.Fatalf("re-reading the rewritten file: %v", err)
	}
	if flag, ok := reread["future_flag"].(bool); !ok || !flag {
		t.Errorf("future_flag = %v after a load-and-save round trip, want true — a "+
			"field this build has never heard of must survive it", reread["future_flag"])
	}
	if _, ok := reread["future_object"].(map[string]any); !ok {
		t.Errorf("future_object = %v after a round trip, want the object that was "+
			"there before", reread["future_object"])
	}
	// The known fields still have to be right afterwards: preserving the
	// unknown ones by writing the file back verbatim would pass the two
	// assertions above and defeat the purpose of saving at all.
	if reread[keyToken] != testToken {
		t.Errorf("the token did not survive the round trip")
	}
}

// TestLoadCorruptFileIsReportedAndLeftAlone covers the two halves of the
// never-repair rule: say something useful, and change nothing.
//
// The file is the only copy of a credential. A load path that "fixes" it
// destroys it, and the user cannot tell the difference between a repair
// and a bug — so the code says what is wrong, names the file, and stops.
//
// REQUIRED MUTATIONS, two, because the halves fail independently:
//
//  1. In corruptError (config.go), drop the word "delete" from the
//     message. The advice assertion reds. Run and observed.
//  2. In Load (config.go), add os.Remove(path) to the corrupt branch —
//     a plausible "self-healing" change somebody would write on purpose.
//     Run and observed: the red arrives from the read that follows,
//     reporting the file as gone, which is the unchanged-bytes check
//     seeing the strongest possible version of a change.
//
// Both restored from a checksum-verified copy.
func TestLoadCorruptFileIsReportedAndLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed JSON", `{not json`},
		{"a JSON array where an object belongs", `["token"]`},
		{"a field of the wrong type", `{"version": "one", "token": "x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, tc.body)
			before := readFile(t, path)

			loaded, err := Load("https://api.example.com")
			if err != nil {
				t.Fatalf("Load() returned a fatal error for a corrupt file (%v) — a "+
					"corrupt config must not end the run, it must send the user "+
					"through a login", err)
			}
			if loaded.Token != "" {
				t.Errorf("a corrupt file yielded a token")
			}
			if loaded.NoTokenReason == nil {
				t.Fatalf("a corrupt file produced no reason to report")
			}
			msg := loaded.NoTokenReason.Error()
			if !strings.Contains(msg, path) {
				t.Errorf("the message does not name the file: %q", msg)
			}
			if !strings.Contains(msg, "delete") {
				t.Errorf("the message does not tell the user what to do about it "+
					"(no instruction to delete the file): %q", msg)
			}

			if after := readFile(t, path); string(after) != string(before) {
				t.Errorf("the load path modified the config file: it is the only copy "+
					"of a credential and an automatic repair that guesses wrong "+
					"destroys it\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}

	// POSITIVE CONTROL for the "unchanged" assertion above. The same
	// read-compare instrument, pointed at a file something really does
	// rewrite: if this does not report a change, then neither could the
	// rows above, and their green means nothing.
	t.Run("positive control: the same check sees a real change", func(t *testing.T) {
		path := hermeticPath(t)
		writeConfigFile(t, path, `{"version":1,"token":"old","api_url":"https://api.example.com"}`)
		before := readFile(t, path)

		cfg := &Config{Path: path}
		if err := cfg.Save(testToken, "https://api.example.com"); err != nil {
			t.Fatalf("Save(): %v", err)
		}
		if after := readFile(t, path); string(after) == string(before) {
			t.Errorf("a successful Save left the file byte-identical — the instrument " +
				"the rows above rely on cannot see a change")
		}
	})
}

// TestLoadEmptyTokenIsTreatedAsCorrupt closes the gap between "valid
// JSON" and "usable". A file whose token is empty parses perfectly and
// is worth nothing; handing back an empty token quietly would send an
// empty bearer to the server and turn a local problem into a remote
// error message.
//
// REQUIRED MUTATION: in Load (config.go), change the empty-token branch
// to compare against a value no file will ever hold, so it cannot fire.
// Every subtest reds — the token comes back empty with nothing to
// report. (Deleting the branch outright was tried first and is not a
// mutation: it leaves an import unused, so the package does not build,
// and a build failure proves nothing about an assertion.) Run, observed
// red, and the file restored from a checksum-verified copy.
func TestLoadEmptyTokenIsTreatedAsCorrupt(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty string", `{"version":1,"token":"","api_url":"https://api.example.com"}`},
		{"whitespace only", `{"version":1,"token":"   ","api_url":"https://api.example.com"}`},
		{"absent", `{"version":1,"api_url":"https://api.example.com"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, tc.body)

			loaded, err := Load("https://api.example.com")
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if loaded.Token != "" {
				t.Errorf("a file with no usable token yielded one")
			}
			if loaded.NoTokenReason == nil {
				t.Fatalf("a file with no usable token produced no reason to report")
			}
			if !strings.Contains(loaded.NoTokenReason.Error(), path) {
				t.Errorf("the message does not name the file: %q", loaded.NoTokenReason)
			}
		})
	}
}

// TestLoadEndpointSpellingsThatNameOneServer is the tolerance half of the
// endpoint check: a stored URL and an effective URL that differ only in
// spelling name the same server, and the token stays usable.
//
// Every pair here is a way a person actually types a URL differently on
// two occasions — a trailing slash left on an environment variable, a
// host typed in a different case, a port written out that the scheme
// implies anyway. Left to a byte comparison, each one of them silently
// logs the user out.
//
// REQUIRED MUTATION: in endpointMismatch (config.go), give storedKey
// and effectiveKey the raw strings instead of the canonical form, so the
// comparison becomes a byte comparison. All six rows here red. Run,
// observed red, and the file restored from a checksum-verified copy.
//
// This test CANNOT see the opposite defect. A comparison that answers
// "same" to everything — a canonical form that collapses two real
// endpoints, or simply a dropped check — passes every row below, because
// a class stays internally consistent under any change applied to all of
// it. That is what the distinctness test underneath is for, and it is
// not there for completeness.
func TestLoadEndpointSpellingsThatNameOneServer(t *testing.T) {
	for _, pair := range [][2]string{
		{"http://localhost:8080", "http://localhost:8080/"},
		{"http://localhost:8080/", "http://localhost:8080"},
		{"http://LOCALHOST:8080", "http://localhost:8080"},
		{"https://api.example.com", "https://api.example.com:443"},
		{"https://API.Example.com.", "https://api.example.com"},
		{"https://api.example.com/v1/", "https://api.example.com/v1"},
	} {
		stored, effective := pair[0], pair[1]
		t.Run(stored+" vs "+effective, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, fmt.Sprintf(
				`{"version":1,"token":%q,"api_url":%q}`, testToken, stored))

			loaded, err := Load(effective)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if loaded.Token == "" {
				t.Errorf("the token was discarded: %v\nthese two spellings name one "+
					"server, and treating them as different costs a login every time "+
					"somebody's environment variable carries a trailing slash",
					loaded.NoTokenReason)
			}
		})
	}
}

// TestLoadEndpointsThatDifferKeepTheirTokensApart is the discrimination
// half, and it is the one that detects the dangerous direction. A check
// that collapses two endpoints sends a token issued against one to the
// other — which is the entire failure the recorded api_url exists to
// prevent, arriving through the mechanism meant to prevent it.
//
// REQUIRED MUTATION: in Load (config.go), delete the endpointMismatch
// call and its branch. Every row here reds; every row in the equivalence
// test above stays green. Run, observed, and the file restored from a
// checksum-verified copy.
func TestLoadEndpointsThatDifferKeepTheirTokensApart(t *testing.T) {
	for _, pair := range [][2]string{
		{"http://localhost:8080", "http://localhost:9090"},
		{"http://localhost:8080", "https://localhost:8080"},
		{"https://api.example.com", "https://api.example.org"},
		{"https://api.example.com", "https://api.example.com:8443"},
		{"https://api.example.com/v1", "https://api.example.com/v2"},
		{"https://api.example.com", "https://staging.api.example.com"},
	} {
		stored, effective := pair[0], pair[1]
		t.Run(stored+" vs "+effective, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, fmt.Sprintf(
				`{"version":1,"token":%q,"api_url":%q}`, testToken, stored))

			loaded, err := Load(effective)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if loaded.Token != "" {
				t.Fatalf("a token issued against %s was handed to a run talking to "+
					"%s", stored, effective)
			}
			if loaded.NoTokenReason == nil {
				t.Fatalf("the token was discarded with nothing to tell the user — " +
					"they are about to be asked to log in and deserve to know why")
			}
			msg := loaded.NoTokenReason.Error()
			if !strings.Contains(msg, stored) || !strings.Contains(msg, effective) {
				t.Errorf("the message names neither both endpoints nor enough to act "+
					"on: %q", msg)
			}
			// The file is evidence, not a problem: a mismatch must not
			// touch it. The user may be switching between two servers and
			// the token they came back to is still theirs.
			if loaded.APIURL != stored {
				t.Errorf("APIURL = %q, want the endpoint the file records (%q)",
					loaded.APIURL, stored)
			}
		})
	}
}

// TestLoadUnusableStoredEndpointIsAMismatchNotAFailure pins the
// direction the error is read in. A stored api_url that cannot be
// canonicalised is not evidence that the token belongs here, and
// unusable evidence has to be treated as negative evidence: it costs a
// login, where the other reading spends a real token against a server it
// was never issued for.
//
// REQUIRED MUTATION: in endpointMismatch (config.go), return nil instead
// of an error when api.CanonicalKey fails on the stored value — the
// "cannot tell, so allow it" reading. Every row reds. Run, observed red,
// and the file restored from a checksum-verified copy.
func TestLoadUnusableStoredEndpointIsAMismatchNotAFailure(t *testing.T) {
	for _, tc := range []struct{ name, storedJSON string }{
		{"no api_url at all", `{"version":1,"token":"` + testToken + `"}`},
		{"empty api_url", `{"version":1,"token":"` + testToken + `","api_url":""}`},
		{"not an address", `{"version":1,"token":"` + testToken + `","api_url":"not a url"}`},
		{"a scheme this client does not speak",
			`{"version":1,"token":"` + testToken + `","api_url":"ftp://files.example.com"}`},
		{"carrying credentials",
			`{"version":1,"token":"` + testToken + `","api_url":"https://user:pass@api.example.com"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, tc.storedJSON)
			before := readFile(t, path)

			loaded, err := Load("https://api.example.com")
			if err != nil {
				t.Fatalf("Load() ended the run (%v) — an unreadable stored endpoint "+
					"costs a login, it does not stop the command", err)
			}
			if loaded.Token != "" {
				t.Errorf("the token was used even though the file gives no usable " +
					"evidence about which server issued it")
			}
			if loaded.NoTokenReason == nil {
				t.Errorf("nothing was reported to the user")
			}
			if after := readFile(t, path); string(after) != string(before) {
				t.Errorf("the file was modified: the never-repair rule holds here too")
			}
		})
	}
}

// TestLoadMissingFileIsAFirstRun keeps the ordinary case quiet. Somebody
// who has never logged in has nothing wrong with their machine, and a
// warning or an error on a first run trains people to ignore both.
//
// REQUIRED MUTATION: in Load (config.go), change the fs.ErrNotExist
// branch to fall through to the generic read-failure branch below it.
// This test reds on the reported reason. Run, observed red, and the file
// restored from a checksum-verified copy.
func TestLoadMissingFileIsAFirstRun(t *testing.T) {
	path := hermeticPath(t)

	loaded, err := Load("https://api.example.com")
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Token != "" {
		t.Errorf("a token appeared out of a file that does not exist")
	}
	if loaded.NoTokenReason != nil {
		t.Errorf("a first run reported a problem: %v", loaded.NoTokenReason)
	}
	if len(loaded.Warnings) != 0 {
		t.Errorf("a first run produced warnings: %v", loaded.Warnings)
	}
	if loaded.Path != path {
		t.Errorf("Path = %q, want %q — the caller still needs to know where the "+
			"file will go", loaded.Path, path)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load created something at %s; reading must not write", path)
	}
}

// TestLoadRefusesAnEmptyEffectiveEndpoint makes a wiring mistake loud.
//
// If this returned "no token" instead, a caller that forgot to pass the
// endpoint would work perfectly — every run would just ask for a fresh
// login, forever, and the bug would look like a server problem or an
// expiring token. Failing at the call is the only version of this that
// gets diagnosed.
//
// REQUIRED MUTATION: in Load (config.go), delete the empty-endpoint
// guard. This test reds. Run, observed red, and the file restored from a
// checksum-verified copy.
func TestLoadRefusesAnEmptyEffectiveEndpoint(t *testing.T) {
	hermeticPath(t)
	if _, err := Load(""); err == nil {
		t.Fatal("Load(\"\") succeeded — a caller that cannot say which server it is " +
			"talking to cannot be told whether the stored token belongs to it")
	}
}

// TestSaveRefusesAPairTheNextLoadWouldReject holds the two halves of the
// pair to the SAME standard the read path applies, which is the whole
// point of them arriving together.
//
// A save used to refuse an empty token and nothing else, while a load
// required a non-whitespace token AND an endpoint that canonicalises. So
// three caller mistakes passed the write and were rejected by the next
// read — each having already replaced a file that worked. Every row
// below is one of them, and every row also asserts that the good file is
// still there afterwards: a caller's bug must not cost the user their
// credential.
//
// REQUIRED MUTATIONS, two, because the halves fail independently:
//
//  1. In Save (config.go), drop the token guard. The two token rows red
//     on both assertions and the endpoint rows stay green.
//  2. In Save, drop the api.CanonicalKey guard. The four endpoint rows
//     red and the token rows stay green.
//
// Both run, both observed, and the file restored from a
// checksum-verified copy after each.
func TestSaveRefusesAPairTheNextLoadWouldReject(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    ui.Secret
		endpoint string
	}{
		{"an empty token", "", "https://api.example.com"},
		// Whitespace is the sharp one: it is not empty, so the old
		// guard let it through, and the next run reports a file with
		// nothing in it.
		{"a token that is only whitespace", "   ", "https://api.example.com"},
		{"a forgotten endpoint", testToken, ""},
		{"an endpoint that is not an address", testToken, "not a url"},
		{"a scheme this client does not speak", testToken, "ftp://files.example.com"},
		{"an endpoint carrying credentials", testToken, "https://user:pass@api.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path,
				`{"version":1,"token":"`+testToken+`","api_url":"https://api.example.com"}`)
			before := readFile(t, path)

			cfg := &Config{Path: path}
			if err := cfg.Save(tc.token, tc.endpoint); err == nil {
				t.Error("Save() wrote a config file the next run would refuse to use")
			}
			if after := readFile(t, path); string(after) != string(before) {
				t.Error("the refused Save still overwrote the existing config — a " +
					"caller's bug must not cost the user their token")
			}
			// A refused Save must not leave the value claiming a
			// credential was stored either, or a caller that checks the
			// Config rather than the error is told the opposite of what
			// happened.
			if cfg.Token != "" || cfg.APIURL != "" {
				t.Errorf("a refused Save left the value reporting token=%v endpoint=%q",
					cfg.Token, cfg.APIURL)
			}
		})
	}
}

// TestSaveRecordsTheEndpointItWasGiven is the trap that made the pair a
// shape rather than a check, and it is the realistic one.
//
// The natural login flow is: load, find no usable token, obtain one,
// save. If the endpoint came from the loaded value, that save would
// record the NEW token against the OLD endpoint — and the next run would
// be a mismatch again, on a file that looks perfectly correct. There is
// no order of field assignments that can produce it now, because the
// endpoint is not a field this call reads.
//
// REQUIRED MUTATION: in Save (config.go), pass c.APIURL to marshal in
// place of the issuedAgainst argument. Run, and the blast radius is
// larger than was predicted here, for a reason worth keeping: every
// fixture in this package now builds the value FRESH and hands the pair
// to Save, so c.APIURL is empty at the point the mutation reads it and
// the endpoint goes to disk empty everywhere. This row reds, and so do
// TestSaveLoadRoundTrip and all four legs of
// TestSaveCreatesEveryMissingDirectoryLevelUsably. The prediction that
// only this row would move was written against the old fixture shape,
// where the two values agreed. Restored from a checksum-verified copy.
func TestSaveRecordsTheEndpointItWasGiven(t *testing.T) {
	const oldEndpoint = "http://localhost:8080"
	const newEndpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path,
		`{"version":1,"token":"the-development-token","api_url":"`+oldEndpoint+`"}`)

	loaded, err := Load(newEndpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Token != "" {
		t.Fatalf("the fixture did not produce the mismatch it exists for")
	}
	if loaded.APIURL != oldEndpoint {
		t.Fatalf("the loaded value does not carry the old endpoint, so this row "+
			"cannot see the trap: %q", loaded.APIURL)
	}

	if err := loaded.Save(testToken, newEndpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	var onDisk map[string]any
	if err := json.Unmarshal(readFile(t, path), &onDisk); err != nil {
		t.Fatalf("re-reading the rewritten file: %v", err)
	}
	if onDisk[keyAPIURL] != newEndpoint {
		t.Errorf("the file records %v as the server the token was issued against, "+
			"want %q — a new token filed under the old endpoint is a login loop with "+
			"a perfect-looking file", onDisk[keyAPIURL], newEndpoint)
	}

	again, err := Load(newEndpoint)
	if err != nil {
		t.Fatalf("Load() after Save(): %v", err)
	}
	if string(again.Token) != testToken {
		t.Errorf("the token saved against this run's endpoint was not offered back "+
			"to it: %v", again.NoTokenReason)
	}

}

// TestSaveFromAFreshValueClaimsNothingAboutUnknownFields makes the doc's
// distinction checkable rather than merely written down.
//
// The forward-compatibility promise — an older release runs once and a
// newer release's field survives — is true of the LOAD-THEN-MODIFY flow
// and only of it, because the fields being preserved are the ones the
// read put there. A Config built fresh has none, so it writes the three
// this build knows and claims nothing about any others. That was
// ambiguous for as long as nothing said which shape was correct, and an
// ambiguity in the only flow that ever saves is not a documentation
// problem.
//
// REQUIRED MUTATION: none is possible for the first half — a fresh value
// has no unknown map to lose. The second half is the mutation-bearing
// one and it already has a row: deleting the unknown-copying loop in
// marshal reds TestLoadPreservesUnknownFields. Recorded here rather than
// invented, because a row asserting that another row cannot exist is a
// guard for a guard.
func TestSaveFromAFreshValueClaimsNothingAboutUnknownFields(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":1,"token":"old","api_url":"`+endpoint+
		`","future_flag":true}`)

	// A FRESH value, which is what a caller that did not read first
	// has. The future field is on disk and this value knows nothing
	// about it.
	fresh := &Config{Path: path}
	if fresh.Version != 0 {
		t.Fatalf("the fixture starts with a version already set, so the assertion " +
			"below cannot see anything")
	}
	if err := fresh.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	// The value describes the file after a successful save, and the
	// version is part of the file. A fresh value writes this build's
	// schema and has to know it did: the refusal to downgrade a file
	// from the future is decided by exactly this field, so one that
	// never learned what was written is one whose next save is deciding
	// on a value nothing put there.
	if fresh.Version != SchemaVersion {
		t.Errorf("Version = %d after a save that wrote %d — the value no longer "+
			"describes the file it just wrote", fresh.Version, SchemaVersion)
	}
	var afterFresh map[string]any
	if err := json.Unmarshal(readFile(t, path), &afterFresh); err != nil {
		t.Fatalf("re-reading the rewritten file: %v", err)
	}
	if _, ok := afterFresh["future_flag"]; ok {
		t.Errorf("a value that never read the file preserved a field out of it, " +
			"which it has no way to know about — the promise this package makes is " +
			"about the flow that reads first, and a stronger-looking claim here " +
			"would be one nothing supports")
	}

	// And the named flow, on the same fixture, keeps it.
	writeConfigFile(t, path, `{"version":1,"token":"old","api_url":"`+endpoint+
		`","future_flag":true}`)
	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if err := loaded.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	var afterLoad map[string]any
	if err := json.Unmarshal(readFile(t, path), &afterLoad); err != nil {
		t.Fatalf("re-reading the rewritten file: %v", err)
	}
	if flag, ok := afterLoad["future_flag"].(bool); !ok || !flag {
		t.Errorf("future_flag = %v after the load-then-modify flow, want true",
			afterLoad["future_flag"])
	}
}

// TestSaveFailedRenameLeavesTheOriginalUntouched is the crash-safety
// claim, checked rather than asserted in a comment.
//
// The write goes to a temp file in the same directory and becomes the
// config in one rename. Everything before the rename can fail — a full
// disk, a killed process, a permissions change under our feet — and the
// existing config has to still be exactly what it was. The alternative,
// truncating the real file and writing into it, leaves an empty config
// on any interruption, and an empty config reads as "logged out" and
// costs the user another login.
//
// The failure is injected through the renameFile variable because that
// is the only way to be standing between "the new bytes exist" and "the
// new bytes are the config". A read-only directory (exercised on the
// platforms that have one, beside this file) fails earlier, at the temp
// file, and so cannot test this seam at all.
//
// REQUIRED MUTATION: in Save (config.go), write straight to the target
// — os.Create(c.Path) in place of the temp file, and no rename — which
// is the truncate-in-place this design exists to avoid. This test reds
// twice: the save reports success although the rename it was told to do
// failed, and the original config has been replaced by the new content.
// Run, observed red, and the file restored from a checksum-verified
// copy.
func TestSaveFailedRenameLeavesTheOriginalUntouched(t *testing.T) {
	path := hermeticPath(t)
	writeConfigFile(t, path, `{"version":1,"token":"the-original-token","api_url":"https://api.example.com"}`)
	before := readFile(t, path)

	original := renameFile
	t.Cleanup(func() { renameFile = original })
	renameFile = func(string, string) error {
		return errors.New("injected failure standing exactly where a crash would")
	}

	cfg := &Config{Path: path}
	if err := cfg.Save(testToken, "https://api.example.com"); err == nil {
		t.Error("Save() reported success even though the rename failed")
	}
	// The value must not claim a credential is stored either. This is
	// the seam where that matters: the guards at the top of Save reject
	// their rows before anything could be assigned, so a failure AFTER
	// them is the only place the ordering can be observed.
	if cfg.Token != "" || cfg.APIURL != "" {
		t.Errorf("a Save that failed left the value reporting token=%v endpoint=%q — "+
			"a caller that checks the Config rather than the error is then told the "+
			"opposite of what happened", cfg.Token, cfg.APIURL)
	}

	if after := readFile(t, path); string(after) != string(before) {
		t.Errorf("the existing config was damaged by a save that failed\nbefore: %s\nafter:  %s",
			before, after)
	}

	// A temp file left behind holds a real token in the same directory
	// under a name nothing will ever clean up.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("reading the config directory: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("a failed save left %q behind, and it holds a token", e.Name())
		}
	}
}

// TestConfigFormattingNeverPrintsTheToken is the last line of defence
// for the accident this project's Secret type exists to survive: a debug
// print of the whole config, in the shape somebody reaches for while
// chasing an unrelated bug.
//
// REQUIRED MUTATION: in Config (config.go), change the Token field's
// type from ui.Secret to string. Three more edits are needed before it
// builds — the conversion in marshal loses its string(), the assignment
// in Load loses its ui.Secret(), and the now-unused import needs a blank
// reference — and with those done EVERY ONE of the eight renderings
// below printed the token in full, including the JSON one. Run, observed
// red, and the file restored from a checksum-verified copy.
func TestConfigFormattingNeverPrintsTheToken(t *testing.T) {
	path := hermeticPath(t)
	const endpoint = "https://api.example.com"
	writeConfigFile(t, path, fmt.Sprintf(
		`{"version":1,"token":%q,"api_url":%q,"future_flag":true}`, testToken, endpoint))

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Token == "" {
		t.Fatalf("the fixture did not load: %v", loaded.NoTokenReason)
	}

	for _, rendered := range []string{
		fmt.Sprintf("%+v", loaded),
		fmt.Sprintf("%v", loaded),
		fmt.Sprintf("%+v", *loaded),
		fmt.Sprintf("%#v", *loaded),
		fmt.Sprintf("%s", loaded.Token),
		fmt.Sprintf("%q", loaded.Token),
		fmt.Sprint(loaded.Token),
		func() string {
			encoded, err := json.Marshal(loaded)
			if err != nil {
				t.Fatalf("marshalling the config: %v", err)
			}
			return string(encoded)
		}(),
	} {
		if strings.Contains(rendered, testToken) {
			t.Errorf("a formatting path printed the token in full: %s", rendered)
		}
	}
}
