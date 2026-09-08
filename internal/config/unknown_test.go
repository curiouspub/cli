package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// futureSecret stands in for the field this build has never heard of and
// the next one will add — a refresh token, a device key, whatever it
// turns out to be. It is shaped so that nothing can match it by
// accident.
const futureSecret = "FUTURE-SECRET-XYZ-not-a-real-credential"

// secretForms is every shape a rendering can carry the value in, and the
// reason this test is not a substring search for the plaintext.
//
// Measured: %s prints a future field readably, and %v, %+v and %#v print
// its BYTES — decimal from the first two, hexadecimal from the third.
// One verb leaks it readably and three leak it recoverably, so a row
// looking only for the plaintext would pass over three of the four.
func secretForms(secret string) map[string]string {
	b := []byte(secret)
	decimal := make([]string, len(b))
	hexadecimal := make([]string, len(b))
	for i, c := range b {
		decimal[i] = strconv.Itoa(int(c))
		hexadecimal[i] = fmt.Sprintf("%#x", c)
	}
	return map[string]string{
		"in plain text":        secret,
		"as decimal bytes":     strings.Join(decimal, " "),
		"as hexadecimal bytes": strings.Join(hexadecimal, ", "),
	}
}

// TestUnknownFieldsRenderKeysAndNeverValues closes the one-way door.
//
// The field that exists precisely to carry what this build does NOT
// understand is the one field no redaction covered, in a package whose
// entire subject is a secret. The type is a map of raw JSON, so the
// guard that forbids an unexported field reaching the secret type cannot
// see it, and the row that pins redaction covers only the fields this
// build knows.
//
// It ships now rather than when a field with a credential in it actually
// arrives, because the order is fixed and cannot be changed later: the
// binaries that will read that field's value are RELEASED BEFORE THE
// FIELD EXISTS. This build is the only one that can close it.
//
// The keys are kept, and that is the design rather than a compromise.
// WHICH unknown fields a file carries is the half a person debugging
// needs; the values are the half that can be a credential.
//
// REQUIRED MUTATIONS, three, because they fail independently and the
// third is the structural one:
//
//  1. In UnknownFields.Format (unknown.go), write the map itself —
//     Fprintf(f, "%v", map[string]json.RawMessage(u)) — which is what
//     the field rendered before this type existed. This row reds, on
//     the byte forms rather than on the plaintext, and so do both legs
//     of TestUnknownFieldsSayNothingIsThereWhenNothingIs.
//  2. In MarshalJSON, hand back the raw map. Only this row reds, on the
//     JSON renderings — the fmt paths are unaffected, which is why the
//     encoders are covered separately rather than assumed to follow.
//  3. In Config (config.go), make the field unexported again. Only this
//     row reds, and it is the mutation worth having: the methods above
//     are all still there and fmt cannot call any of them, because it
//     cannot call a method on a value it reached by reflecting an
//     unexported field. The redaction is a property of the FIELD's
//     visibility as much as of the type.
//
// All three run, all three observed, and the files restored from
// checksum-verified copies.
func TestUnknownFieldsRenderKeysAndNeverValues(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, fmt.Sprintf(
		`{"version":1,"token":%q,"api_url":%q,"refresh_token":%q}`,
		testToken, endpoint, futureSecret))

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Token == "" {
		t.Fatalf("the fixture did not load: %v", loaded.NoTokenReason)
	}
	if len(loaded.Unknown) == 0 {
		t.Fatalf("the fixture's future field was not preserved, so this row is " +
			"searching renderings of a value that has nothing to leak")
	}

	forms := secretForms(futureSecret)

	// POSITIVE CONTROL FIRST, because a search that can never match
	// anything satisfies every assertion below. This is the rendering
	// the field had before it had a type of its own, and each of the
	// three forms has to be findable in one of them.
	bare := map[string]json.RawMessage{"refresh_token": json.RawMessage(`"` + futureSecret + `"`)}
	controls := []string{
		fmt.Sprintf("%s", bare),
		fmt.Sprintf("%v", bare),
		fmt.Sprintf("%#v", bare),
	}
	for name, form := range forms {
		found := false
		for _, control := range controls {
			if strings.Contains(control, form) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the control renderings do not contain the secret %s, so this "+
				"test cannot detect what it exists to detect", name)
		}
	}

	// %s of the whole value goes through an interface because go vet
	// refuses the direct call — this type has no String method — and
	// that indirection is the realistic shape rather than a workaround.
	// The debug print that leaks is a value handed to something with a
	// ...any parameter, which is exactly where vet cannot look either.
	var asAny any = loaded
	renderings := map[string]string{
		"%s of the config":         fmt.Sprintf("%s", asAny),
		"%v of the config":         fmt.Sprintf("%v", loaded),
		"%+v of the config":        fmt.Sprintf("%+v", loaded),
		"%v of the value":          fmt.Sprintf("%v", *loaded),
		"%+v of the value":         fmt.Sprintf("%+v", *loaded),
		"%#v of the value":         fmt.Sprintf("%#v", *loaded),
		"Sprint of the value":      fmt.Sprint(*loaded),
		"%s of the field alone":    fmt.Sprintf("%s", loaded.Unknown),
		"%v of the field alone":    fmt.Sprintf("%v", loaded.Unknown),
		"%+v of the field alone":   fmt.Sprintf("%+v", loaded.Unknown),
		"%#v of the field alone":   fmt.Sprintf("%#v", loaded.Unknown),
		"%d of the field alone":    fmt.Sprintf("%d", loaded.Unknown),
		"%x of the field alone":    fmt.Sprintf("%x", loaded.Unknown),
		"Sprint of the field":      fmt.Sprint(loaded.Unknown),
		"the field's text form":    mustText(t, loaded.Unknown),
		"the field's binary form":  mustBinary(t, loaded.Unknown),
		"JSON of the config":       mustJSON(t, loaded),
		"JSON of the field alone":  mustJSON(t, loaded.Unknown),
		"JSON of a map holding it": mustJSON(t, map[string]UnknownFields{"cfg": loaded.Unknown}),
	}
	for where, rendered := range renderings {
		for name, form := range forms {
			if strings.Contains(rendered, form) {
				t.Errorf("%s carries the future field's value %s: %s", where, name, rendered)
			}
		}
	}

	// The KEYS are the useful half and they have to survive, or this
	// stops being a redaction and becomes a deletion.
	for where, rendered := range renderings {
		if !strings.Contains(rendered, "refresh_token") {
			t.Errorf("%s does not name the unknown field at all: %s — which fields a "+
				"file carries is exactly what a person debugging needs", where, rendered)
		}
	}
}

// TestUnknownFieldsSayNothingIsThereWhenNothingIs keeps the rendering
// honest in the ordinary case. A value with no unknown fields must not
// print something a reader could take for one.
func TestUnknownFieldsSayNothingIsThereWhenNothingIs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value UnknownFields
	}{
		{"a nil map, which is what a value built fresh has", nil},
		{"an empty map, which is what a file with no extra fields gives", UnknownFields{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := fmt.Sprintf("%v", tc.value)
			if rendered == "" {
				t.Errorf("an empty set of unknown fields rendered as nothing at all, " +
					"which in the middle of a struct dump reads as a missing field")
			}
			if strings.Contains(rendered, "map[") {
				t.Errorf("rendered as %q — the map shape is what leaked the values, "+
					"and it should not come back for the empty case", rendered)
			}
		})
	}
}

// TestUnknownFieldsStillReachTheFileVerbatim is the distinctness row for
// the redaction above, and it is the one that stops the fix from
// becoming the defect.
//
// A redaction that reached the WRITE path would silently destroy the
// state it exists to preserve — an older binary would run once and drop
// the newer one's field, which is exactly what preserving unknown fields
// is for. The values are hidden from every RENDERING and untouched on
// the way to disk.
//
// REQUIRED MUTATION: in marshal (config.go), copy the unknown fields
// through their own rendering rather than their raw bytes. This row reds,
// and so do TestLoadPreservesUnknownFields and the load-then-modify half
// of TestSaveFromAFreshValueClaimsNothingAboutUnknownFields — every row
// that asserts a future field survives a round trip. Every row asserting
// the redaction stays green, which is the pair worth having. Run,
// observed red, and the file restored from a checksum-verified copy.
func TestUnknownFieldsStillReachTheFileVerbatim(t *testing.T) {
	const endpoint = "https://api.example.com"
	path := hermeticPath(t)
	writeConfigFile(t, path, fmt.Sprintf(
		`{"version":1,"token":%q,"api_url":%q,"refresh_token":%q}`,
		testToken, endpoint, futureSecret))

	loaded, err := Load(endpoint)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if err := loaded.Save(testToken, endpoint); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	var reread map[string]any
	if err := json.Unmarshal(readFile(t, path), &reread); err != nil {
		t.Fatalf("re-reading the rewritten file: %v", err)
	}
	if reread["refresh_token"] != futureSecret {
		t.Errorf("the future field came back as %v after a round trip, want the "+
			"value that was there — a redaction that reached the write path would "+
			"destroy the very state this field exists to preserve",
			reread["refresh_token"])
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %T: %v", v, err)
	}
	return string(encoded)
}

func mustText(t *testing.T, u UnknownFields) string {
	t.Helper()
	b, err := u.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText(): %v", err)
	}
	return string(b)
}

func mustBinary(t *testing.T, u UnknownFields) string {
	t.Helper()
	b, err := u.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary(): %v", err)
	}
	return string(b)
}
