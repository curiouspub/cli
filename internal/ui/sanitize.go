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
// cost one pass and no allocation. It reads the same table the rewrite
// does, so the two cannot disagree about which bytes are in the set.
func needsEscaping(s string) bool {
	for i := 0; i < len(s); i++ {
		if escapes[s[i]] != "" {
			return true
		}
	}
	return false
}

// escapeFor is the printable form of one byte, or the empty string for a
// byte that passes through untouched.
func escapeFor(c byte) string { return escapes[c] }

// escapes is THE set: the printable form of every byte this package
// rewrites, empty for every byte it leaves alone.
//
// It is a table rather than a range test for two reasons, and the second
// is the one worth writing down.
//
// The first is that the named forms have to live somewhere. A tab of
// indentation and a newline that survived somebody's own formatting are
// the two control bytes that turn up in real build output, and "\t" is
// legible where "\x09" is a puzzle.
//
// The second is that A SET BUILT ENTIRELY BY A RANGE HAS NO MEMBER
// ANYBODY CAN REMOVE. ESC is the byte this whole mechanism exists for —
// every cursor movement, screen clear, colour change and window-title
// rewrite begins with it — and a claim that the set covers it can only be
// proved by taking it out and watching something go red. Under a range
// test there is nothing to take out: deleting a line that names ESC
// changes no behaviour, so the proof cannot be run and the coverage is
// asserted rather than shown. So the range below SKIPS it and the entry
// is written on its own line.
//
// Every form here is printable ASCII and none contains a byte in this
// set, which is the whole of the idempotence argument.
var escapes = buildEscapes()

func buildEscapes() [256]string {
	var table [256]string
	for b := 0; b < firstPrintable; b++ {
		if b == esc {
			// Written below, on purpose. See the doc comment above.
			continue
		}
		table[b] = hexEscape(byte(b))
	}
	table[del] = hexEscape(del)

	table[esc] = `\x1b`

	table['\a'] = `\a`
	table['\b'] = `\b`
	table['\t'] = `\t`
	table['\n'] = `\n`
	table['\v'] = `\v`
	table['\f'] = `\f`
	table['\r'] = `\r`
	return table
}

// hexEscape is the general form, for the bytes with no name worth
// learning.
func hexEscape(c byte) string {
	return `\x` + string(hexDigits[c>>4]) + string(hexDigits[c&0x0f])
}

// hexDigits is lowercase on purpose: the named escapes above are
// lowercase, and two spellings of one byte in one line of output is a
// difference a reader would try to interpret.
const hexDigits = "0123456789abcdef"

// sanitizeLines is Sanitize for text that is allowed to have LINES in
// it, and it is the form the rendering boundary uses.
//
// WHY A SECOND ENTRY POINT AND NOT A SECOND VOCABULARY. Sanitize escapes
// the whole C0 set, newline included, which is exactly right for one
// line of somebody else's build output and exactly wrong for a rendered
// failure — three paragraphs whose blank lines are the layout. This
// splits on the newlines the LAYOUT owns, hands every remaining byte to
// Sanitize unchanged, and puts the layout back. There is one escape
// table and one function that applies it; this decides only which bytes
// are structure and which are content.
//
// A carriage return is content, not structure: it is escaped like any
// other C0 byte, because a lone CR walks the cursor back over the line a
// person just read.
//
// It inherits idempotence from Sanitize, which is what lets the boundary
// run over text that has already been through the same table — the
// ordinary case rather than the exotic one, since the far end escapes
// this vocabulary too.
func sanitizeLines(s string) string {
	if !strings.Contains(s, "\n") {
		return Sanitize(s)
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = Sanitize(line)
	}
	return strings.Join(lines, "\n")
}
