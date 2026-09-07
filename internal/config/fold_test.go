package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode"
)

// THE SPELLINGS ARE FIXTURE DATA, AS BYTES, and that is not fastidious.
// One of them renders on screen as an ordinary capital K and is not one:
// U+212A KELVIN SIGN is a different code point that folds onto k through
// unicode.SimpleFold's cycle, so a reader comparing two source lines by
// eye cannot tell them apart and a reader comparing two byte tables can.
// The first probe of this defect transliterated the sign to an ASCII K,
// which sorts the other way, and the finding did not reproduce.
//
// The two consequences differ by exactly which side of "token" the odd
// spelling sorts to, because a re-saved file writes its keys in sorted
// order and a JSON decoder takes the LAST match it sees:
//
//	spelling            bytes                 sorts   the run then uses
//	token               74 6f 6b 65 6e        —       —
//	toKen  (ASCII K)    74 6f 4b 65 6e        before  the fresh token, and
//	                                                  the old one stays on
//	                                                  disk for ever
//	toKen  (U+212A)     74 6f e2 84 aa 65 6e  after   the STALE token, on
//	                                                  every run after the
//	                                                  login that replaced it
var tokenKeySpellings = []struct {
	name  string
	bytes []byte
	// foldEqual is what the JSON format itself thinks: is this a
	// spelling of the token field? It is written down rather than
	// computed so that the agreement row below has an independent
	// third opinion to hold the other two against.
	foldEqual bool
}{
	{"token, exactly as this build writes it",
		[]byte{0x74, 0x6f, 0x6b, 0x65, 0x6e}, true},
	{"toKen, with an ASCII capital K",
		[]byte{0x74, 0x6f, 0x4b, 0x65, 0x6e}, true},
	{"toKen, with U+212A KELVIN SIGN where the K appears to be",
		[]byte{0x74, 0x6f, 0xe2, 0x84, 0xaa, 0x65, 0x6e}, true},
	{"TOKEN, in capitals",
		[]byte{0x54, 0x4f, 0x4b, 0x45, 0x4e}, true},
	// The distinctness rows. Without them, "the decoder and the
	// stripper agree" is satisfied by a relation that says yes to
	// everything — which is the shape of the defect in the other
	// direction, and would silently discard a field this build has
	// never heard of.
	{"tokens, which is a different field with a common prefix",
		[]byte{0x74, 0x6f, 0x6b, 0x65, 0x6e, 0x73}, false},
	{"token with a Greek omicron, which looks the part and folds onto nothing",
		[]byte{0x74, 0xce, 0xbf, 0x6b, 0x65, 0x6e}, false},
}

// configWithKey renders a config file whose token is carried by exactly
// the bytes given. The key is embedded raw rather than escaped, because
// which BYTES are on disk is the whole subject here.
func configWithKey(key []byte, value, endpoint string) string {
	return fmt.Sprintf(`{"version":1,"%s":%q,"api_url":%q}`, key, value, endpoint)
}

// TestKnownKeysAreStrippedByTheDecodersOwnRelation is the row that
// survives a change to the decoder.
//
// "Known" was computed twice by two different relations: the JSON
// decoder fills the struct by a Unicode fold match, and the code that
// removes known keys from the preserved map used an exact string
// compare. A key known to one and unknown to the other is therefore
// USED and PRESERVED — read as the token, and written back as though it
// were a field from the future.
//
// This asserts the agreement itself rather than either side of it: for
// every spelling in the table, the decoder filling the field and the
// stripper removing the key are the same answer. A Go release that
// changed how the decoder folds a name would red here, which is the only
// place that could notice.
//
// REQUIRED MUTATION: in stripKnownKeys (config.go), compare with
// k == known instead of strings.EqualFold. The three rows whose spelling
// is not byte-identical to the field name red, and the three that are
// stay green. Run, observed red, and the file restored from a
// checksum-verified copy.
func TestKnownKeysAreStrippedByTheDecodersOwnRelation(t *testing.T) {
	const endpoint = "https://api.example.com"

	for _, spelling := range tokenKeySpellings {
		t.Run(spelling.name, func(t *testing.T) {
			body := []byte(configWithKey(spelling.bytes, testToken, endpoint))

			var fc fileConfig
			if err := json.Unmarshal(body, &fc); err != nil {
				t.Fatalf("decoding %s: %v", body, err)
			}
			decoderFilledTheField := fc.Token == testToken

			var raw map[string]json.RawMessage
			if err := json.Unmarshal(body, &raw); err != nil {
				t.Fatalf("decoding %s into a map: %v", body, err)
			}
			stripKnownKeys(raw)
			_, keyKept := raw[string(spelling.bytes)]
			stripperRemovedTheKey := !keyKept

			if decoderFilledTheField != spelling.foldEqual {
				t.Errorf("the JSON decoder %s this key, and the table says it %s — "+
					"the table is the thing being held against the format here, so "+
					"one of them has moved",
					didOrDidNot(decoderFilledTheField, "filled the token field from"),
					didOrDidNot(spelling.foldEqual, "should"))
			}
			if stripperRemovedTheKey != decoderFilledTheField {
				t.Errorf("the decoder %s and the stripper %s: a key known to one and "+
					"unknown to the other is read as the token AND written back as a "+
					"field from the future, which is how a freshly-saved token comes "+
					"to be ignored on every later run\nkey bytes: % x",
					didOrDidNot(decoderFilledTheField, "filled the token field"),
					didOrDidNot(stripperRemovedTheKey, "removed the key"),
					spelling.bytes)
			}
		})
	}
}

func didOrDidNot(b bool, what string) string {
	if b {
		return what
	}
	return "did not " + what
}

// TestLoadRefusesTwoSpellingsOfOneField covers the file that carries
// both, which is what a login produces once the two relations have
// disagreed even once.
//
// There is no merge and no precedence rule here on purpose. Two
// spellings of one field is a file nobody can read the intent of, and
// picking one silently is exactly how the stale-token consequence arose
// — the run kept saving a fresh token and kept sending an old one.
//
// The message names BOTH spellings, escaped, because they can render
// identically: an unescaped report would read "the file contains token
// and token", which tells the user nothing they can act on.
//
// REQUIRED MUTATION: in Load (config.go), drop the ambiguousSpellings
// branch. Both rows here red — the file is read, a token comes back, and
// with the Kelvin spelling it is the STALE one. Run, observed red, and
// the file restored from a checksum-verified copy.
func TestLoadRefusesTwoSpellingsOfOneField(t *testing.T) {
	const endpoint = "https://api.example.com"

	for _, tc := range []struct {
		name string
		// second is the odd spelling, as bytes.
		second []byte
		// escaped is how the message has to spell it: identical glyphs
		// are the reason the report escapes at all.
		escaped string
	}{
		{
			name:    "an ASCII capital K, which sorts before token and strands the old secret",
			second:  []byte{0x74, 0x6f, 0x4b, 0x65, 0x6e},
			escaped: `"toKen"`,
		},
		{
			name:   "U+212A, which sorts after token and makes the run send the stale one",
			second: []byte{0x74, 0x6f, 0xe2, 0x84, 0xaa, 0x65, 0x6e},
			// The message escapes it to ASCII, which is the only way a
			// person reading the report can see that this is not the
			// row above. Written here as the escape sequence itself:
			// a raw sign in this literal would render identically to
			// the K one line up and assert nothing.
			escaped: `"to\u212aen"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// THE EXPECTATION ITSELF HAS TO BE ASCII, and this is not
			// belt-and-braces: a raw Kelvin sign in the literal above
			// renders identically to the capital K one row up, would
			// match a message naming the other spelling, and would
			// assert nothing. It happened while this file was being
			// written, which is the whole argument for byte tables.
			for _, r := range tc.escaped {
				if r > unicode.MaxASCII {
					t.Fatalf("the expected spelling %+q is not ASCII, so it cannot "+
						"tell this row from the one beside it", tc.escaped)
				}
			}

			path := hermeticPath(t)
			writeConfigFile(t, path, fmt.Sprintf(
				`{"version":1,"token":%q,"%s":%q,"api_url":%q}`,
				testToken, tc.second, "the-secret-this-build-must-not-choose", endpoint))
			before := readFile(t, path)

			loaded, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load() ended the run (%v) — an unreadable file costs a "+
					"login, it does not stop the command", err)
			}
			if loaded.Token != "" {
				t.Errorf("a file carrying two spellings of the token field yielded a " +
					"token: whichever one it picked, it picked without being able to " +
					"know which the user meant")
			}
			if loaded.NoTokenReason == nil {
				t.Fatalf("nothing was reported to the user")
			}
			msg := loaded.NoTokenReason.Error()
			if !strings.Contains(msg, path) {
				t.Errorf("the message does not name the file: %q", msg)
			}
			for _, want := range []string{`"token"`, tc.escaped} {
				if !strings.Contains(msg, want) {
					t.Errorf("the message does not name the spelling %s, so the user "+
						"cannot see which two keys to choose between: %q", want, msg)
				}
			}
			if after := readFile(t, path); string(after) != string(before) {
				t.Errorf("the load path modified the config file: the never-repair "+
					"rule holds here too\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

// TestLoadNormalisesALoneOddSpelling is the other half, and it is what
// keeps the refusal above from being a refusal of everything.
//
// A file carrying ONE spelling of the token field is not ambiguous,
// however it is spelled. The decoder reads it, and — this is the part
// that was broken — the key is removed from the preserved map, so the
// next save writes the field once, under the name this build uses,
// rather than twice under two names that fold together.
//
// REQUIRED MUTATION: the same one as the agreement row — an exact
// compare in stripKnownKeys. This reds on the second assertion: the odd
// spelling survives into the rewritten file beside the ordinary one, and
// the file now carries the trap the row above refuses. Run, observed
// red, and the file restored from a checksum-verified copy.
func TestLoadNormalisesALoneOddSpelling(t *testing.T) {
	const endpoint = "https://api.example.com"
	odd := []byte{0x74, 0x6f, 0xe2, 0x84, 0xaa, 0x65, 0x6e}

	path := hermeticPath(t)
	writeConfigFile(t, path, configWithKey(odd, testToken, endpoint))

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if string(loaded.Token) != testToken {
		t.Fatalf("the token was not read from a file that spells the field in a way "+
			"the format accepts: %v", loaded.NoTokenReason)
	}

	if err := loaded.Save(); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	var reread map[string]json.RawMessage
	if err := json.Unmarshal(readFile(t, path), &reread); err != nil {
		t.Fatalf("re-reading the rewritten file: %v", err)
	}
	if _, ok := reread[keyToken]; !ok {
		t.Errorf("the rewritten file has no %q field at all", keyToken)
	}
	if _, ok := reread[string(odd)]; ok {
		t.Errorf("the odd spelling survived into the rewritten file, so the file now " +
			"carries two keys the format folds together — which is the state the " +
			"refusal row exists for, arriving through this package's own write")
	}
}

// TestSaveDoesNotWriteAFileItWouldRefuseToRead is the round-trip form of
// the two rows above, kept separate because it is the invariant rather
// than a case: whatever this package writes, this package can read.
//
// REQUIRED MUTATION: the same exact-compare mutation in stripKnownKeys.
// Run, and what it reds with is more than was predicted here: all THREE
// fold-equal spellings red, not only the one whose sort order changes
// which token wins. The reload reports the file this package has just
// written as carrying two spellings of one field — so the refusal is
// what catches it, and it catches every spelling rather than the one
// with a visible symptom. The two rows that are not fold-equal stay
// green. Restored from a checksum-verified copy.
func TestSaveDoesNotWriteAFileItWouldRefuseToRead(t *testing.T) {
	const endpoint = "https://api.example.com"

	for _, spelling := range tokenKeySpellings {
		t.Run(spelling.name, func(t *testing.T) {
			path := hermeticPath(t)
			writeConfigFile(t, path, configWithKey(spelling.bytes, testToken, endpoint))

			loaded, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			// A spelling the format does not fold onto the token field
			// is an unknown field, and the file then has no token at
			// all — which is a different, already-covered report.
			if !spelling.foldEqual {
				if loaded.NoTokenReason == nil {
					t.Fatalf("a file whose only token-ish key is not one the format " +
						"folds onto the field reported no problem")
				}
				return
			}
			// A SECOND, DIFFERENT token, because that is what a
			// re-login writes and because it is the only way this row
			// can see the consequence: with the same value in both
			// keys, a file carrying two spellings round-trips
			// perfectly and proves nothing.
			const relogin = "the-token-this-run-just-obtained"
			loaded.Token = relogin
			if err := loaded.Save(); err != nil {
				t.Fatalf("Save(): %v", err)
			}

			again, err := Load(endpoint)
			if err != nil {
				t.Fatalf("Load() after Save(): %v", err)
			}
			if again.NoTokenReason != nil {
				t.Fatalf("this package wrote a file it then refused to read: %v",
					again.NoTokenReason)
			}
			if string(again.Token) != relogin {
				t.Errorf("the run came back with %d bytes of token after a login "+
					"wrote %d — the file records the new one on every login and the "+
					"run sends the old one",
					len(again.Token), len(relogin))
			}
		})
	}
}

// TestLoadRefusesTwoSpellingsOfTheEndpointToo pins that the rule is
// about the FIELD SET and not about the token alone. The endpoint
// decides whether a stored token is offered back at all, so a file
// recording two of them is exactly as unreadable.
//
// REQUIRED MUTATION: in ambiguousSpellings (config.go), replace
// knownKeys with a slice holding only keyToken. This row reds and
// TestLoadRefusesTwoSpellingsOfOneField stays green. Run, observed red,
// and the file restored from a checksum-verified copy.
func TestLoadRefusesTwoSpellingsOfTheEndpointToo(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, fmt.Sprintf(
		`{"version":1,"token":%q,"api_url":%q,"API_url":%q}`,
		testToken, endpoint, "https://api.example.org"))

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Token != "" {
		t.Errorf("a file recording two endpoints handed back a token; which server " +
			"it was issued against is exactly what could not be determined")
	}
	if loaded.NoTokenReason == nil {
		t.Fatalf("nothing was reported to the user")
	}
	msg := loaded.NoTokenReason.Error()
	for _, want := range []string{`"api_url"`, `"API_url"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not name the spelling %s: %q", want, msg)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file is gone after a refused load: %v", err)
	}
}
