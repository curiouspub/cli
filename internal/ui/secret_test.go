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
