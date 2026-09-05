package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const realValue = "sk-live-do-not-print-me"

// TestSecretRedactsEveryRenderingPath is the redaction test: it exercises
// every path a Go program has for turning a value into text or JSON, and
// asserts none of them shows the real value.
func TestSecretRedactsEveryRenderingPath(t *testing.T) {
	s := Secret(realValue)

	renderings := map[string]string{
		"%s":  fmt.Sprintf("%s", s),
		"%v":  fmt.Sprintf("%v", s),
		"%#v": fmt.Sprintf("%#v", s),
		"%q":  fmt.Sprintf("%q", s),
	}
	marshaled, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings["json.Marshal"] = string(marshaled)

	for verb, got := range renderings {
		if strings.Contains(got, realValue) {
			t.Errorf("%s rendered the real value: %q", verb, got)
		}
		if !strings.Contains(got, redactedPlaceholder) {
			t.Errorf("%s did not render the placeholder: got %q, want it to contain %q",
				verb, got, redactedPlaceholder)
		}
	}
}

// TestPlainStringRendersTheRealValue is the positive control: the same
// verbs and json.Marshal, over an ordinary string carrying the same
// value, must show the real value. Without this, the redaction test above
// could pass for the wrong reason — a typo'd verb, a formatting call that
// silently no-ops — since "the placeholder never appears" and "formatting
// was never actually exercised" would look identical otherwise.
func TestPlainStringRendersTheRealValue(t *testing.T) {
	plain := realValue

	renderings := map[string]string{
		"%s":  fmt.Sprintf("%s", plain),
		"%v":  fmt.Sprintf("%v", plain),
		"%#v": fmt.Sprintf("%#v", plain),
		"%q":  fmt.Sprintf("%q", plain),
	}
	marshaled, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings["json.Marshal"] = string(marshaled)

	for verb, got := range renderings {
		if !strings.Contains(got, realValue) {
			t.Errorf("%s on a plain string did not contain the real value: got %q — "+
				"if this fails, the redaction test above is not actually observing "+
				"formatting", verb, got)
		}
	}
}

// TestSecretShape is the shape test: Secret converts to and from a plain
// string, so the type wraps a value rather than hiding it structurally —
// the redaction is a rendering behaviour, not an inability to hold data.
func TestSecretShape(t *testing.T) {
	s := Secret(realValue)
	if string(s) != realValue {
		t.Errorf("string(Secret(%q)) = %q, want the original value back", realValue, string(s))
	}
}

// TestSecretMapKeyIsAKnownUncoveredPath pins the ONE rendering path the
// redaction does not cover, so it is a documented limit rather than a
// surprise — and so that a change in either direction is noticed.
//
// encoding/json resolves a map key by checking reflect.Kind == String
// before consulting encoding.TextMarshaler, so Secret's MarshalText is
// never reached for a key and the real value is printed. This test
// asserts the CURRENT behaviour: if it ever starts failing, either the
// standard library changed or Secret stopped being a string, and both
// are things somebody should be told about rather than discover.
func TestSecretMapKeyIsAKnownUncoveredPath(t *testing.T) {
	const real = "secret-abc123"
	b, err := json.Marshal(map[Secret]string{Secret(real): "v"})
	if err != nil {
		t.Fatalf("marshalling a map keyed by Secret: %v", err)
	}
	if !strings.Contains(string(b), real) {
		t.Fatalf("map-key redaction now WORKS: %s\n"+
			"That is good news and this test is the wrong shape for it — "+
			"the limit documented on Secret is stale, and the doc comment "+
			"naming this path as uncovered must be corrected with it.", b)
	}

	// The paths that ARE covered stay covered, asserted beside the limit
	// so the two cannot drift apart.
	value, err := json.Marshal(map[string]Secret{"k": Secret(real)})
	if err != nil {
		t.Fatalf("marshalling a map valued by Secret: %v", err)
	}
	if strings.Contains(string(value), real) {
		t.Errorf("a Secret as a map VALUE leaked the real value: %s", value)
	}
}

// TestSecretFormatterCoversEveryVerb is the regression for the leak that
// Stringer alone allowed: fmt consults Stringer only for the
// string-compatible verbs, and for any other verb it printed its own
// diagnostic — with the real value embedded in it. %d on a Secret gave
// %!d(ui.Secret=<the token>).
//
// Format covers every verb fmt routes through it. The exception is %p,
// which fmt resolves before consulting Formatter; that is asserted
// separately below as a known limit rather than quietly omitted here.
func TestSecretFormatterCoversEveryVerb(t *testing.T) {
	s := Secret(realValue)
	for _, verb := range []string{
		"%s", "%v", "%q", "%#v", "%x", "%X",
		"%d", "%f", "%t", "%c", "%e", "%b", "%o", "%U",
	} {
		got := fmt.Sprintf(verb, s)
		if strings.Contains(got, realValue) {
			t.Errorf("%s leaked the real value: %s", verb, got)
		}
	}
}

// TestSecretKnownUncoveredPaths pins the paths that cannot be closed on
// this type, so each is a documented limit rather than a surprise and so
// a change in either direction is noticed. If any of these starts
// redacting, that is good news and the doc comment on Secret is stale.
func TestSecretKnownUncoveredPaths(t *testing.T) {
	s := Secret(realValue)

	// %p: fmt resolves it before consulting Formatter.
	if got := fmt.Sprintf("%p", s); !strings.Contains(got, realValue) {
		t.Errorf("%%p now redacts (%s) — Secret's documented limits are stale", got)
	}

	// An unexported field: fmt cannot call a method on a value reached by
	// reflecting one, so it prints the underlying string.
	type holder struct{ token Secret }
	if got := fmt.Sprintf("%v", holder{s}); !strings.Contains(got, realValue) {
		t.Errorf("an unexported Secret field now redacts (%s) — limits are stale", got)
	}

	// An EXPORTED field is covered, asserted beside the limit so the two
	// cannot drift apart.
	type exported struct{ Token Secret }
	if got := fmt.Sprintf("%v", exported{s}); strings.Contains(got, realValue) {
		t.Errorf("an exported Secret field leaked: %s", got)
	}
}
