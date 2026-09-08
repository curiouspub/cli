package ui

import "strings"

// The bytes this file is about. C0 is 0x00 through 0x1F and DEL is 0x7F:
// 33 bytes, none of which a terminal prints, and several of which it
// obeys.
const (
	esc            = 0x1b
	del            = 0x7f
	firstPrintable = 0x20
)

// Sanitize renders one line safe to write to a terminal: every C0 control
// byte and DEL becomes a printable escape, and every other byte is left
// exactly as it was.
//
// # Why this exists at all
//
// This program prints bytes a build produced somewhere else. The package
// manager and the site builder are pass-throughs for arbitrary program
// output, so a project can print an escape sequence into its own build
// log — and without this, that sequence reaches the reader's terminal,
// their scrollback, the screenshot they take of it and the bug report
// they paste it into. ESC is the injection vector; the rest of the set
// comes with it because a byte a terminal does not print is a byte
// nobody can audit by looking.
//
// The service that produces those lines escapes the same vocabulary at
// its own end, and this is deliberately not redundant with it. A LOCAL
// SAFETY PROPERTY MUST NOT REST ON A REMOTE GUARANTEE: this program
// cannot verify the far end ran, cannot see it regress, and does not know
// which version of the server it is talking to — while what is at stake
// is the terminal in front of whoever ran the command. Defence in depth,
// said honestly rather than by assuming somebody else got it wrong.
//
// # Bytes, not runes
//
// The scan is byte-oriented and every byte it rewrites is below 0x80, so
// no byte of a multibyte sequence can be reached: valid UTF-8 passes
// through intact, and so does input that is not valid UTF-8 at all, which
// is the honest answer for a byte stream nobody here decoded.
//
// # It is idempotent, and that is a requirement
//
// Almost every line arriving here has already been through the far end's
// instance of this vocabulary, so escaping twice would be the ordinary
// case rather than the exotic one. Nothing this function writes is itself
// escapable — in particular THE BACKSLASH IS LEFT ALONE — which is what
// makes Sanitize(Sanitize(x)) equal Sanitize(x). The cost is a rendering
// that cannot distinguish a real backslash-x-1-b in somebody's source
// from an escaped ESC. That ambiguity is smaller than a line that grows a
// backslash every time it is handled.
//
// # No length cap
//
// Escaping is lossless and truncation is not, and the output being capped
// here is the output somebody is watching BECAUSE something went wrong.
// If a bound is ever genuinely needed it belongs somewhere that can say
// what it bounds, and an over-long line must still arrive — with a
// visible marker — rather than be dropped or end a stream.
func Sanitize(s string) string {
	if !needsEscaping(s) {
		return s
	}

	var b strings.Builder
	// One escape is four bytes at worst. Reserving a little over the
	// input saves the re-allocations a line full of control bytes would
	// otherwise cost, and costs nothing on a line with two.
	b.Grow(len(s) + len(s)/4 + 8)
	for i := 0; i < len(s); i++ {
		if escape := escapeFor(s[i]); escape != "" {
			b.WriteString(escape)
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// needsEscaping is the fast path: the overwhelming majority of lines
// carry nothing to escape, and returning the original string means they
// cost one pass and no allocation.
func needsEscaping(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < firstPrintable || s[i] == del {
			return true
		}
	}
	return false
}

// escapeFor is the printable form of one byte, or the empty string for a
// byte that passes through untouched.
//
// The named forms are here for the two that occur in real build output —
// a tab of indentation and a newline that survived somebody's own
// formatting — because "\t" is legible where "\x09" is a puzzle. Every
// form is made of printable ASCII and none of them contains a byte this
// function escapes, which is the whole of the idempotence argument.
func escapeFor(c byte) string {
	switch c {
	// ESC HAS ITS OWN CASE, deliberately not folded into the range
	// below. It is the byte the whole mechanism exists for — every
	// cursor movement, screen clear, colour change and window-title
	// rewrite begins with it — so a reader looking for what stops an
	// escape sequence finds it named here, and a change that removes
	// this byte alone from the set is a change one row can see.
	case esc:
		return `\x1b`
	case '\a':
		return `\a`
	case '\b':
		return `\b`
	case '\t':
		return `\t`
	case '\n':
		return `\n`
	case '\v':
		return `\v`
	case '\f':
		return `\f`
	case '\r':
		return `\r`
	}
	if c < firstPrintable || c == del {
		return `\x` + string(hexDigits[c>>4]) + string(hexDigits[c&0x0f])
	}
	return ""
}

// hexDigits is lowercase on purpose: the escapes above are lowercase, and
// two spellings of one byte in one line of output is a difference a
// reader would try to interpret.
const hexDigits = "0123456789abcdef"
