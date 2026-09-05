// Package ui holds the small rendering types the interactive CLI and its
// non-interactive modes share — starting with Secret, the type any
// sensitive value must be stored as so it can never accidentally reach a
// terminal, a log line or a JSON blob.
package ui

import "encoding/json"

// redactedPlaceholder is what Secret renders everywhere, instead of the
// value it wraps.
const redactedPlaceholder = "[redacted]"

// Secret wraps a sensitive string — a bearer token, a presigned URL — so
// that formatting it can never print the real value. Every rendering path
// a Go program has for a string is covered deliberately: fmt's %s, %v and
// %q verbs all resolve through String(); %#v resolves through GoString();
// and encoding/json resolves through MarshalJSON(). A type that redacts
// only some of these is not safer than one that redacts none, because
// whichever path was missed is the one that eventually gets used by
// accident — a debug Printf, a struct dumped as JSON for a log line.
//
// There is deliberately no accessor that hands the wrapped value back —
// converting through string(secret) at the one call site that actually
// needs it (an Authorization header, say) is the only way out, so a leak
// cannot hide behind a method that looks like a getter.
type Secret string

// String implements fmt.Stringer, which fmt's %s, %v and %q verbs all
// resolve through before formatting.
func (Secret) String() string { return redactedPlaceholder }

// GoString implements fmt.GoStringer, which the %#v verb resolves through
// instead of falling back to the underlying string type's Go-syntax
// representation — the real value, quoted.
func (Secret) GoString() string { return redactedPlaceholder }

// MarshalJSON implements json.Marshaler, so a Secret embedded in any
// struct that gets logged or serialized as JSON renders the placeholder
// too, not the value the field holds.
func (Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedPlaceholder)
}
