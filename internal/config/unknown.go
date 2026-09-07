package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// noUnknownFields is what an empty set renders as. It is a sentence
// rather than an empty string or a pair of brackets, because in the
// middle of a struct dump both of those read as a field that is missing
// rather than a field that is empty.
const noUnknownFields = "no unknown fields"

// UnknownFields holds every top-level field of the config file that this
// build does not understand, exactly as it was read, so a save can write
// it back. Losing it would mean an older binary run once silently
// deletes a newer one's state.
//
// # It renders its KEYS and never its VALUES
//
// This is the field that exists precisely to carry what this build does
// not understand, in a package whose whole subject is a secret — and it
// was the one field no redaction covered. The type is a map of raw JSON,
// so the guard that forbids an unexported field reaching the secret type
// cannot see it, and the row pinning redaction covers only the fields
// this build knows about. Measured on a file carrying a future
// refresh_token: %s printed it in plain text, and %v, %+v and %#v
// printed its bytes, decimal or hexadecimal. One verb leaks it readably
// and three leak it recoverably.
//
// The keys are kept because they are the useful half: WHICH unknown
// fields a file carries is what a person debugging wants, and the values
// are the half that can be a credential.
//
// # Why this is here before there is anything to hide
//
// The order cannot be changed later. A field that leaks is read by the
// binaries that shipped BEFORE it existed, so by the time the field is
// real, every build that will mishandle it is already on people's
// machines. This build is the only one that can close it, which makes
// this a one-way door and the cheapest possible moment the only moment.
//
// # The methods, and why this set
//
// The same set the secret type carries, for the same reasons recorded
// there: fmt resolves Formatter before anything else and for EVERY verb,
// including the mismatched ones whose diagnostics embed the value;
// Stringer is what every other consumer uses; encoding/json resolves
// Marshaler; and an encoder that speaks only TextMarshaler or
// BinaryMarshaler — gob deliberately ignores the text one — reaches
// neither of the first two.
//
// # What is deliberately NOT redacted
//
// The write path. The values go to disk verbatim, because preserving
// them is the entire reason this field exists — a redaction that reached
// the file would silently destroy the state it is here to protect. The
// two directions are pinned by a row each.
type UnknownFields map[string]json.RawMessage

// String renders the keys, sorted, and nothing else.
//
// The keys are escaped to ASCII rather than printed as they are. A field
// name comes out of a file this build did not write and has never
// validated: it can carry a newline that would break the line this sits
// on, or two spellings that render identically and would read as one
// name twice.
func (u UnknownFields) String() string {
	if len(u) == 0 {
		return noUnknownFields
	}
	keys := make([]string, 0, len(u))
	for key := range u {
		keys = append(keys, fmt.Sprintf("%+q", key))
	}
	slices.Sort(keys)
	return "unknown fields: " + strings.Join(keys, ", ")
}

// Format implements fmt.Formatter, which fmt consults BEFORE Stringer or
// GoStringer and for EVERY verb.
//
// The verb is ignored on purpose. A mismatched verb otherwise produces
// fmt's own diagnostic, and that diagnostic embeds the value — which is
// how a typo in a debug print becomes a credential in a terminal.
func (u UnknownFields) Format(f fmt.State, verb rune) {
	_, _ = f.Write([]byte(u.String()))
}

// GoString implements fmt.GoStringer, which %#v resolves through rather
// than falling back to the Go-syntax representation of the underlying
// map — where the values appear as their bytes in hexadecimal.
func (u UnknownFields) GoString() string { return u.String() }

// MarshalJSON renders the key names as an array.
//
// A Config dumped as JSON for a log line is exactly the accident this
// guards against, and the array says which fields were present without
// saying what was in them. Nothing reads this back: the file is written
// by the merge in marshal, which copies the raw values and never comes
// through here.
func (u UnknownFields) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(u))
	for key := range u {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return json.Marshal(keys)
}

// MarshalText implements encoding.TextMarshaler, which is what every
// encoder outside encoding/json reaches for first, and what encoding/json
// itself uses for a map KEY.
func (u UnknownFields) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

// MarshalBinary implements encoding.BinaryMarshaler, which is what gob
// honours — it deliberately does not consult the text one, so without
// this a gob-encoded Config would carry the values in full.
func (u UnknownFields) MarshalBinary() ([]byte, error) { return []byte(u.String()), nil }
