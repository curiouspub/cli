package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The bytes this file talks about, named once so a row cannot disagree
// with the code about which byte it means.
const (
	escByte  = 0x1b
	delByte  = 0x7f
	firstPrn = 0x20
	lastPrn  = 0x7e
)

// controlBytes is the set the sanitiser is required to escape: C0
// (0x00-0x1F) and DEL. It is BUILT rather than listed, so a byte cannot
// be missed here by being forgotten — the same reason the end-to-end row
// in the deploy sequence ranges it rather than spelling it out.
func controlBytes() []byte {
	var out []byte
	for b := 0; b < firstPrn; b++ {
		out = append(out, byte(b))
	}
	return append(out, delByte)
}

// TestSanitizeEscapesEveryControlByteAndLeavesEveryOtherByteAlone walks
// the WHOLE byte universe, which is what makes it more than a restatement
// of the escape table.
//
// It is the row that can see the difference between escaping bytes and
// escaping runes. Every byte above 0x7F is a continuation or a leading
// byte of a multibyte sequence and must pass through untouched — and a
// lone one of those is not valid UTF-8, which is exactly the input a
// JSON payload cannot carry, so the end-to-end rows in the deploy
// sequence cannot ask this question at all.
//
// EVERY ESCAPE IS ASSERTED TWO-SIDED: the raw byte is gone AND the result
// is non-empty printable text. Dropping the byte satisfies "the raw byte
// is absent" on its own, and dropping the whole line satisfies it better.
func TestSanitizeEscapesEveryControlByteAndLeavesEveryOtherByteAlone(t *testing.T) {
	escaped := 0
	for _, b := range controlBytes() {
		in := "a" + string([]byte{b}) + "z"
		got := Sanitize(in)
		if strings.ContainsRune(got, rune(b)) {
			t.Errorf("byte %#02x survived: %q", b, got)
			continue
		}
		if got == "az" {
			t.Errorf("byte %#02x was dropped rather than escaped: %q — a renderer "+
				"that deletes the byte passes an absence check and loses the line", b, got)
			continue
		}
		if !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "z") {
			t.Errorf("byte %#02x took its neighbours with it: %q", b, got)
			continue
		}
		for i := 0; i < len(got); i++ {
			if got[i] < firstPrn || got[i] > lastPrn {
				t.Errorf("the escape for %#02x is not printable: %q", b, got)
				break
			}
		}
		escaped++
	}
	if escaped != 33 {
		t.Errorf("%d of the 33 control bytes escaped cleanly", escaped)
	}

	// The other half of the universe. A printable byte passes through,
	// and so does every byte above 0x7F — those are the halves of a
	// multibyte sequence, and a pass that touched one would corrupt
	// ordinary text in every language that needs more than ASCII.
	untouched := 0
	for b := firstPrn; b <= 0xff; b++ {
		if b == delByte {
			continue
		}
		in := "a" + string([]byte{byte(b)}) + "z"
		if got := Sanitize(in); got != in {
			t.Errorf("byte %#02x was rewritten to %q, want it left alone", b, got)
			continue
		}
		untouched++
	}
	if untouched != 0xff-firstPrn {
		t.Errorf("only %d bytes were checked for passing through", untouched)
	}
}

// TestSanitizeKeepsValidUTF8Intact is the positive control for the row
// above and for every end-to-end escape row: without it, a sanitiser that
// escaped everything would pass all of them.
func TestSanitizeKeepsValidUTF8Intact(t *testing.T) {
	for _, in := range []string{
		"ordinary build output",
		"düğüm — built in 1.2s",
		"日本語のログ行",
		"emoji 🚀 and a combining é",
		"",
	} {
		got := Sanitize(in)
		if got != in {
			t.Errorf("Sanitize(%q) = %q, want it unchanged", in, got)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Sanitize(%q) produced invalid UTF-8: %q", in, got)
		}
	}
}

// TestSanitizeIsIdempotent, on input that actually contains ESC and other
// C0 bytes. On already-clean input an identity function passes, which is
// why the fixture is dirty and why the first result is asserted exactly
// before the second is compared with it.
//
// The property is load-bearing rather than tidy: the far end of the
// stream escapes the same vocabulary, so nearly every line reaching this
// function has already been through an instance of it, and a pass that
// grew a backslash each time would mangle every one of them.
func TestSanitizeIsIdempotent(t *testing.T) {
	in := "before\x1b[2Jafter\x00\x07\x7f end"

	once := Sanitize(in)
	if once == in {
		t.Fatalf("the fixture came through unchanged, so this row is about clean "+
			"input: %q", once)
	}
	if strings.ContainsRune(once, escByte) {
		t.Fatalf("the first pass left a raw ESC: %q", once)
	}

	twice := Sanitize(once)
	if twice != once {
		t.Errorf("Sanitize is not idempotent:\n  once:  %q\n  twice: %q", once, twice)
	}
}

// TestSanitizeDoesNotEscapeTheBackslash is the reason idempotence holds,
// stated as its own row so that "escape the backslash too, for an
// unambiguous rendering" cannot be done without something going red. It
// is a real temptation — the rendering IS ambiguous — and taking it costs
// the property above, which matters more.
func TestSanitizeDoesNotEscapeTheBackslash(t *testing.T) {
	in := `C:\Users\build\x1b`
	if got := Sanitize(in); got != in {
		t.Errorf("Sanitize(%q) = %q — escaping the backslash makes every line grow "+
			"one on each pass, and the far end has already made one", in, got)
	}
}
