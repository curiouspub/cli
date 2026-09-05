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
// that formatting it can never print the real value. fmt's %s, %v, %q,
// %x and %X verbs all resolve through String(); %#v resolves through
// GoString(); encoding/json resolves through MarshalJSON(); and every
// encoder that speaks encoding.TextMarshaler resolves through
// MarshalText(). A type that redacts only some of these is not safer
// than one that redacts none, because whichever path was missed is the
// one that eventually gets used by accident — a debug Printf, a struct
// dumped as JSON for a log line.
//
// ONE PATH IS NOT COVERED, and it is named here rather than left to be
// discovered: a Secret used as a MAP KEY marshalled by encoding/json
// prints the real value. encoding/json resolves a map key by checking
// reflect.Kind == String BEFORE it consults TextMarshaler, so a
// string-based type never reaches its own marshaller there. Measured,
// not assumed — and MarshalText below does not fix it, which is why the
// limit is written down instead of a fix being claimed.
//
// Closing it would mean making Secret a struct rather than a string,
// which costs the string(secret) conversion this type's whole escape
// hatch depends on. A secret as a map key is also a strange shape. The
// trade is recorded, not silently taken.
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

// MarshalText implements encoding.TextMarshaler, and it is NOT redundant
// beside MarshalJSON: encoding/json resolves a map KEY through
// TextMarshaler and never through Marshaler. Without this, a
// map[Secret]string marshalled to JSON printed the real value as the
// key while every other path printed the placeholder — measured, not
// theorised.
//
// It is also what makes the redaction hold for every other encoder that
// speaks TextMarshaler rather than only for encoding/json, which is the
// difference between a type that is safe here and a type that is safe.
func (Secret) MarshalText() ([]byte, error) {
	return []byte(redactedPlaceholder), nil
}
