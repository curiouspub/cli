package preflight

import (
	"net/url"
	"strings"
	"unicode/utf16"
)

// This file is the whole of the lexical pass this check is allowed to
// make over an astro.config file. It never executes anything and never
// resolves an import — see tokenize's own doc comment for what that
// buys and what it costs.

// tokenKind classifies one token produced by tokenize.
type tokenKind int

const (
	tokIdent tokenKind = iota
	tokString
	tokPunct
)

// token is one lexical unit: an identifier, a string/template literal
// (with its escapes already decoded into text and its interpolation
// flag set), or one punctuation character this check's shapes need.
type token struct {
	kind             tokenKind
	text             string // ident text, decoded string content, or the single punct byte
	hasInterpolation bool   // set only for a backtick literal containing a literal "${"
	escapeUnresolved bool   // set when a string contained an escape this scanner could not
	// decode with confidence (a malformed \x or \u sequence). A value
	// carrying this flag must never be read as a resolved literal —
	// this check does not guess at what a broken escape was supposed
	// to mean, it reports unresolved instead.
}

// tokenPunct is the fixed set of punctuation characters this check ever
// needs to recognise: object/array/call delimiters, comma, dot and
// colon. Anything else that is not whitespace, a comment, a string or an
// identifier is insignificant to every shape below and is skipped —
// including "=", which is why "module.exports = { ... }" and
// "module.exports={...}" tokenize identically: the "=" is never a token
// at all, so the object literal simply follows "exports" either way.
const tokenPunct = "{}(),.:[]"

// tokenize walks src once and splits it into identifiers, string and
// template literals, and punctuation — skipping whitespace and comments
// as it goes. It is a SCANNER, not a parser: it has no notion of
// expressions, precedence, statements or scope. That is exactly enough
// to find "key: value" shapes textually and nothing more, which is the
// point — this package parses someone else's JavaScript or TypeScript
// lexically and never evaluates it. Executing the file (what Astro
// itself does) would run arbitrary code out of a directory the user has
// not yet agreed to deploy; embedding a JS engine would still execute
// user code and still couldn't resolve an import. Both are refused at
// any price. A lexical scan is honest about being partial — it cannot
// see through a variable, a function call it doesn't specifically know,
// or an import — and every such case is classified as unresolved rather
// than guessed, which is what keeps this check from ever hard-stopping a
// project it simply couldn't read.
//
// # THE SUBSET GATE
//
// tokenize recognises exactly this ENUMERATED set of constructs, and
// nothing else:
//
//   - whitespace (space, tab, CR, LF);
//   - a "//" line comment, running to end of line;
//   - a "/*" block comment, running to the matching "*/" or to EOF;
//   - a single-quoted, double-quoted or backtick string/template
//     literal, with its escapes decoded by scanString;
//   - an identifier (isIdentStart/isIdentPart);
//   - the fixed punctuation set in tokenPunct: "{}(),.:[]" — every
//     bracket this scanner tracks depth with, plus comma, dot and colon;
//   - every other byte, treated as INERT filler and skipped one at a
//     time without producing a token — this is where numbers, "=", ";",
//     "+", "-", "<", ">", "!", "&", "|", "^", "~", "@" and "#" all land.
//     They are safe to drop silently because none of them can ever be
//     mistaken, alone or in sequence, for a quote, a comment opener or a
//     byte in tokenPunct — nothing downstream of this function inspects
//     them, and dropping one can never shift a bracket count or invent a
//     key.
//
// Two bytes are carved OUT of that last inert bucket and treated as
// UNKNOWN instead, because — unlike every other byte this scanner
// silently drops — they are not provably inert against this scanner's
// own structural model:
//
//   - a bare "/" that does not open "//" or "/*". It is a regex literal
//     or a division operator — this scanner does not parse either — and
//     a regex BODY can itself contain quotes, braces, brackets and
//     colons that are not real JavaScript structure at all. Finding 1
//     and Finding 2 of the maximum-rigour review are both this: a
//     regex's escaped closer or embedded quote leaking structural bytes
//     into the token stream, and a regex ending in "\/" producing the
//     exact byte pair that opens a line comment. There is no way to scan
//     a regex body correctly without a real regex grammar, and building
//     one just to throw the result away is effort spent making the
//     unsafe case LOOK safe.
//   - a bare "?". Its ternary partner ":" is lexically identical to
//     this scanner's own property-colon token, so past a "?" this
//     scanner can no longer tell "key: value" from "condition ? a : b"
//     — the exact mechanism behind the ternary finding.
//
// When tokenize meets either one, it does not try to skip past it,
// re-synchronise, or guess how far the damage reaches — it stops
// immediately and reports the whole file UNRESOLVED, for both keys this
// check reads, not only the one nearest the unknown byte. That is what
// "for everything, not locally" means in practice: the scanner has no
// way to bound a desynchronisation it cannot see the shape of, so it
// makes no claim past the point where its own model stopped applying.
//
// This is deliberately NOT four special cases bolted onto four findings.
// It is one rule — enumerate what is provably safe to drop, and treat
// everything else as unknown — that happens to close all four regex/
// quote/ternary findings through the same two lines, and will close the
// next one nobody has found yet the same way, provided it also shows up
// as a "/" or a "?" this scanner wasn't expecting. A construct that
// introduces some OTHER byte capable of forging a quote, a bracket, a
// comment opener or a colon would need its own carve-out reasoned about
// on the same terms — the gate is a method, not a closed list.
func tokenize(src []byte) (toks []token, unresolved bool, reason string) {
	i, n := 0, len(src)

	for i < n {
		c := src[i]

		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++

		case c == '/' && i+1 < n && src[i+1] == '/':
			// Line comment: to end of line. A commented-out key must
			// never be found, so its bytes never reach the scanners
			// below.
			i += 2
			for i < n && src[i] != '\n' {
				i++
			}

		case c == '/' && i+1 < n && src[i+1] == '*':
			// Block comment. An unterminated one runs to EOF rather than
			// looping forever — a config that malformed is already going
			// to fail on every other ground.
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < n {
				i += 2
			} else {
				i = n
			}

		case c == '/':
			// A bare "/", not part of "//" or "/*": a regex literal or a
			// division operator. See the SUBSET GATE doc comment above —
			// this is one of the two bytes this scanner refuses to guess
			// past, because a regex body can contain this scanner's own
			// structural bytes without them meaning what this scanner
			// would think they mean.
			return nil, true, "a `/` outside a comment (a regex literal or division — this check can't tell the two apart and doesn't parse either one)"

		case c == '?':
			// A bare "?": a ternary, optional chaining or nullish
			// coalescing. The ternary's ":" is indistinguishable from a
			// property colon to this scanner — see the SUBSET GATE doc
			// comment above.
			return nil, true, "a `?` (a conditional expression, whose `:` this check can't tell apart from a property colon)"

		case c == '\'' || c == '"' || c == '`':
			tok, next := scanString(src, i)
			toks = append(toks, tok)
			i = next

		case strings.IndexByte(tokenPunct, c) >= 0:
			toks = append(toks, token{kind: tokPunct, text: string(c)})
			i++

		case isIdentStart(c):
			start := i
			i++
			for i < n && isIdentPart(src[i]) {
				i++
			}
			toks = append(toks, token{kind: tokIdent, text: string(src[start:i])})

		default:
			// Everything else this scanner has a shape for skipping: a
			// number, "=", ";", "+", "-", "<", ">", "!", "&", "|", "^",
			// "~", "@", "#", and so on. Skipping one byte at a time
			// rather than trying to tokenise it properly is safe here —
			// see the SUBSET GATE doc comment above for why this bucket,
			// unlike "/" and "?", is provably inert. This is also what
			// makes "'./source' + '/nested'" safe rather than silently
			// readable as "./source" — the "+" is skipped invisibly, so
			// the token immediately after the first string is the *next*
			// string, never a punctuation mark a value check would
			// accept as "nothing more follows".
			i++
		}
	}

	return toks, false, ""
}

// scanString reads a single-quoted, double-quoted or backtick literal
// starting at src[start] (which must be the opening quote) and returns
// its token plus the index of the byte after the closing quote — or
// after the end of src, for an unterminated literal.
//
// Escapes are DECODED, not merely de-slashed: \n, \t, \r, \b, \f, \v and
// \0 become their real bytes; \<newline> and \<CR><newline> (a line
// continuation) are elided entirely, joining the two source lines with
// nothing inserted between them; \xNN and \uNNNN (and the \u{...} long
// form) become the CODE POINT they name, correctly UTF-8-encoded via
// WriteRune — \xe9 is U+00E9 (é), not the raw byte 0xE9, and a \uNNNN
// high surrogate is combined with an immediately following \uNNNN low
// surrogate into the one astral character the pair actually names (an
// emoji among them), never written as two independent, invalid runes;
// anything else this scanner has no specific rule for — including an
// escaped quote, an escaped backslash, or an escaped backtick or "$" —
// copies through with the backslash dropped, which is both the old
// behaviour for those cases and what an identity escape means in real
// JavaScript. A malformed \x or \u (wrong digit count, non-hex digits, a
// code point above 0x10FFFF, or a surrogate that is lone or unpaired)
// sets escapeUnresolved instead of guessing at a byte, because producing
// a plausible-looking wrong path is worse than admitting the scan
// couldn't read this one.
//
// For a backtick literal specifically, a literal "${" anywhere in its
// body sets hasInterpolation, which is what makes a template WITH
// interpolation unresolved while a plain backtick string is accepted
// like any other string literal.
func scanString(src []byte, start int) (token, int) {
	quote := src[start]
	n := len(src)
	i := start + 1

	var content strings.Builder
	hasInterp := false
	escapeUnresolved := false

	for i < n && src[i] != quote {
		if src[i] == '\\' && i+1 < n {
			switch src[i+1] {
			case 'n':
				content.WriteByte('\n')
				i += 2
			case 't':
				content.WriteByte('\t')
				i += 2
			case 'r':
				content.WriteByte('\r')
				i += 2
			case 'b':
				content.WriteByte('\b')
				i += 2
			case 'f':
				content.WriteByte('\f')
				i += 2
			case 'v':
				content.WriteByte('\v')
				i += 2
			case '0':
				content.WriteByte(0)
				i += 2
			case '\n':
				// Line continuation: the backslash and the newline it
				// escapes both disappear, joining the source lines with
				// nothing inserted — not the newline byte itself, which
				// is the bug this case exists to avoid.
				i += 2
			case '\r':
				i += 2
				if i < n && src[i] == '\n' {
					i++
				}
			case 'x':
				// \xNN names a Unicode CODE POINT 0-255, not a raw byte —
				// \xe9 means U+00E9 (é), which the JavaScript source file
				// this came from would itself have UTF-8-encoded as 0xC3
				// 0xA9 wherever it appeared unescaped. WriteRune performs
				// that same encoding; WriteByte would instead emit the
				// single byte 0xE9, which is not valid UTF-8 on its own
				// and can never match a real "café" on disk. This was the
				// review's confirmed finding: caf\xe9 used to warn about
				// "caf?/pages", a directory that was never the one on
				// disk.
				if i+3 < n && isHexDigit(src[i+2]) && isHexDigit(src[i+3]) {
					content.WriteRune(rune(hexVal(src[i+2])<<4 | hexVal(src[i+3])))
					i += 4
				} else {
					escapeUnresolved = true
					i += 2
				}
			case 'u':
				// \uNNNN and \u{...} both decode to a code point via
				// decodeUnicodeEscape, but a code point in the surrogate
				// range (0xD800-0xDFFF) is not a valid standalone Unicode
				// scalar value — WriteRune on one alone would encode
				// utf8.RuneError, matching nothing on disk, which is
				// exactly the class of confident-but-wrong claim this
				// scanner exists to refuse. JavaScript represents an
				// astral character (outside the BMP, "😀" among them) as
				// a HIGH surrogate (\uD800-\uDBFF) immediately followed
				// by a LOW surrogate (\uDC00-\uDFFF): the review's second
				// confirmed finding is that the previous decoder wrote
				// each half through WriteRune independently, with no
				// pairing, producing "??" for an emoji directory that
				// really existed on disk. This decodes a lone \uNNNN
				// normally, but on seeing a high surrogate it looks ahead
				// for exactly one more \u escape and combines the pair
				// with utf16.DecodeRune before writing a single, correct
				// rune. A high surrogate with no valid low-surrogate
				// partner, a lone low surrogate, or a \u{...} form that
				// names a surrogate code point directly all report
				// escapeUnresolved instead of guessing — this scanner
				// does not know what an unpaired surrogate was supposed
				// to mean, so it says so rather than emitting a rune that
				// cannot correspond to anything on disk.
				if consumed, r, ok := decodeUnicodeEscape(src, i+2); ok {
					switch {
					case !utf16.IsSurrogate(r):
						content.WriteRune(r)
						i = consumed
					case r >= 0xD800 && r <= 0xDBFF && consumed+1 < n && src[consumed] == '\\' && src[consumed+1] == 'u':
						if consumedLow, low, okLow := decodeUnicodeEscape(src, consumed+2); okLow && low >= 0xDC00 && low <= 0xDFFF {
							content.WriteRune(utf16.DecodeRune(r, low))
							i = consumedLow
						} else {
							escapeUnresolved = true
							i = consumed
						}
					default:
						// A lone low surrogate, or a high surrogate not
						// followed by a valid low-surrogate escape.
						escapeUnresolved = true
						i = consumed
					}
				} else {
					escapeUnresolved = true
					i = consumed
				}
			default:
				// Any escaped byte this scanner has no specific rule
				// for — including a quote, a backslash, a backtick or a
				// "$" — copies through with the backslash dropped. That
				// is enough to keep an escaped quote from ending the
				// literal early without over-claiming a decode this
				// scanner isn't doing.
				content.WriteByte(src[i+1])
				i += 2
			}
			continue
		}
		if quote == '`' && src[i] == '$' && i+1 < n && src[i+1] == '{' {
			hasInterp = true
		}
		content.WriteByte(src[i])
		i++
	}
	if i < n {
		i++ // consume the closing quote
	}

	return token{
		kind:             tokString,
		text:             content.String(),
		hasInterpolation: hasInterp,
		escapeUnresolved: escapeUnresolved,
	}, i
}

// decodeUnicodeEscape decodes a \u escape whose "u" sits at src[at-1]
// (so scanning starts at src[at]): either the fixed 4-hex-digit form
// (\uNNNN) or the braced, variable-length form (\u{N...N}). It reports
// the index to resume scanning from either way, so a malformed escape
// advances the scanner past what it could tell was an attempt at one
// rather than reprocessing the same bytes as literal text.
func decodeUnicodeEscape(src []byte, at int) (next int, r rune, ok bool) {
	n := len(src)
	if at < n && src[at] == '{' {
		j := at + 1
		for j < n && src[j] != '}' {
			j++
		}
		if j >= n || j == at+1 {
			end := j
			if end < n {
				end++
			}
			return end, 0, false
		}
		v, valid := parseHexRune(src[at+1 : j])
		return j + 1, v, valid
	}
	if at+4 <= n && isHexDigit(src[at]) && isHexDigit(src[at+1]) && isHexDigit(src[at+2]) && isHexDigit(src[at+3]) {
		v := hexVal(src[at])<<12 | hexVal(src[at+1])<<8 | hexVal(src[at+2])<<4 | hexVal(src[at+3])
		return at + 4, rune(v), true
	}
	return at, 0, false
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func hexVal(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	default:
		return int(b-'A') + 10
	}
}

// parseHexRune decodes digits (1 to 6 hex characters, the \u{...} form's
// body) into a single Unicode code point, rejecting anything non-hex or
// out of Unicode's range rather than guessing.
func parseHexRune(digits []byte) (rune, bool) {
	if len(digits) == 0 || len(digits) > 6 {
		return 0, false
	}
	v := 0
	for _, d := range digits {
		if !isHexDigit(d) {
			return 0, false
		}
		v = v*16 + hexVal(d)
	}
	if v > 0x10FFFF {
		return 0, false
	}
	return rune(v), true
}

func isIdentStart(b byte) bool {
	return b == '_' || b == '$' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isIdentPart(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

// isKeyToken reports whether tok is a legitimate spelling of an object
// key named name: a bare identifier equal to name, or a non-interpolated
// string literal whose entire content equals name. An arbitrary string
// that merely contains name somewhere in its text — a URL, a sentence in
// a description field — never matches, because the comparison is exact
// rather than a substring search. That is what keeps "the word inside a
// string" from ever being read as a key.
func isKeyToken(tok token, name string) bool {
	switch tok.kind {
	case tokIdent:
		return tok.text == name
	case tokString:
		return !tok.hasInterpolation && tok.text == name
	default:
		return false
	}
}

// isValueTerminator reports whether tok is a token that may legitimately
// follow a plain value: the next property's comma, or the closing
// bracket of the object or call the value sits inside. Anything else —
// most commonly another string, which is what "'./source' + '/nested'"
// produces immediately after the first literal, since this scanner has
// no token for "+" at all — means the literal only BEGINS an expression
// rather than being the value itself.
func isValueTerminator(tok token) bool {
	return tok.kind == tokPunct && (tok.text == "," || tok.text == "}" || tok.text == ")")
}

// readStringLiteralValue reads a plain string value at toks[p]: a
// non-interpolated, cleanly-decoded string literal immediately followed
// by a value terminator. Anything else — an identifier, a call, a
// template with interpolation, an escape this scanner couldn't decode,
// or a literal that turns out to be the start of a concatenation —
// reports false rather than guessing.
func readStringLiteralValue(toks []token, p int) (string, bool) {
	if p >= len(toks) || toks[p].kind != tokString || toks[p].hasInterpolation || toks[p].escapeUnresolved {
		return "", false
	}
	if p+1 >= len(toks) || !isValueTerminator(toks[p+1]) {
		return "", false
	}
	return toks[p].text, true
}

// findTopLevelKeyOccurrences returns the index of every token in the
// half-open token range (open, close) — open is the index of an object
// literal's "{" and close the index of its matching "}" — that is a
// legitimate key named name at DEPTH 0 relative to that object: a direct
// property of the object literal itself, counting only brackets between
// open and close. A key nested inside a property's own value — an
// integration's options, a nested "vite: { build: {...} }" block, a
// local object that happens to sit next to the real export — is at
// depth 1 or deeper and is not returned, PROVIDED the token stream's
// bracket structure genuinely reflects the source — a precondition this
// function assumes rather than checks.
//
// That precondition used to be false, and silently: a regex literal
// upstream could emit a bare "}" or "{" from its own body (an escaped
// closer, or a quote whose contents leaked back into live code), which
// this depth counter cannot tell apart from a real bracket. Every depth
// after the corrupted one shifts by one, and a key nested one level down
// — an integration's options, a markdown block — reads as depth 0 and
// comes back from this function as if it were a direct property of the
// exported config. That was Finding 1 of the maximum-rigour review: a
// nested "wrong" value promoted to a live, confidently-reported srcDir.
//
// The precondition now holds by CONSTRUCTION rather than by inspection
// here: tokenize's own subset gate refuses to produce a token stream at
// all from a construct — a bare "/" or "?" among them — it cannot
// account for, rather than producing one and hoping the depth count
// still lines up. This function has no way to detect a corrupted stream
// on its own; it relies entirely on never being handed one.
func findTopLevelKeyOccurrences(toks []token, open, close int, name string) []int {
	var positions []int
	depth := 0
	for idx := open + 1; idx < close; idx++ {
		tk := toks[idx]
		if depth == 0 && isKeyToken(tk, name) && idx+1 < close &&
			toks[idx+1].kind == tokPunct && toks[idx+1].text == ":" {
			positions = append(positions, idx)
		}
		if tk.kind == tokPunct {
			switch tk.text {
			case "{", "(", "[":
				depth++
			case "}", ")", "]":
				depth--
			}
		}
	}
	return positions
}

// matchingClose returns the index of the token that closes the bracket
// opened at toks[open] — "{", "(" or "[" — tracking nested depth across
// all three bracket kinds together, or -1 when the token stream runs out
// before depth returns to zero, or when the closer it does find is the
// wrong shape for the opener (a "]" closing a "{", say).
//
// -1 was never this function's only failure mode, and saying so was the
// lie: depth counting alone cannot tell a real bracket from a bracket-
// shaped byte a corrupted token stream invented — a regex literal's own
// body doing exactly that was Finding 1 of the maximum-rigour review.
// Given such a stream, this function can return a WRONG index with no
// error at all, closing the wrong "{" and handing the caller an object
// boundary that was never really there. It does not guess at that today
// only because tokenize's own subset gate stops a corrupting construct
// from ever reaching a token stream in the first place; this function
// itself has no way to detect the corruption if that guarantee were ever
// weakened, and every caller's "-1 means could not determine" is only as
// true as that upstream guarantee holds.
func matchingClose(toks []token, open int) int {
	closing := map[string]string{"{": "}", "(": ")", "[": "]"}
	want := closing[toks[open].text]
	depth := 1

	for idx := open + 1; idx < len(toks); idx++ {
		if toks[idx].kind != tokPunct {
			continue
		}
		switch toks[idx].text {
		case "{", "(", "[":
			depth++
		case "}", ")", "]":
			depth--
			if depth == 0 {
				if toks[idx].text != want {
					return -1
				}
				return idx
			}
		}
	}
	return -1
}

// matchFileURLIdiom recognises exactly
// fileURLToPath(new URL(<string literal>, import.meta.url)), starting at
// toks[p], token by token. It is deliberately this narrow — the idiom
// Astro's own docs and templates use for srcDir — rather than a general
// call-expression parser: this check reads someone else's JavaScript
// lexically and never evaluates it, so it can only recognise shapes it
// was told to expect, not reason about arbitrary code.
//
// The string literal is the idiom's argument, which URL treats as a URL
// reference rather than as raw text: a percent-escape in it (the space
// in "./source%20files") is part of URL syntax, not JavaScript syntax,
// so it survives scanString's decode untouched and must be resolved
// separately by the caller. end is the index of the token immediately
// after the idiom's closing ")", for the caller to apply the same
// value-terminator rule every other value shape gets.
func matchFileURLIdiom(toks []token, p int) (value string, end int, ok bool) {
	want := []token{
		{kind: tokIdent, text: "fileURLToPath"},
		{kind: tokPunct, text: "("},
		{kind: tokIdent, text: "new"},
		{kind: tokIdent, text: "URL"},
		{kind: tokPunct, text: "("},
	}
	idx := p
	for _, w := range want {
		if !tokenEquals(toks, idx, w) {
			return "", 0, false
		}
		idx++
	}

	if idx >= len(toks) || toks[idx].kind != tokString || toks[idx].hasInterpolation || toks[idx].escapeUnresolved {
		return "", 0, false
	}
	value = toks[idx].text
	idx++

	rest := []token{
		{kind: tokPunct, text: ","},
		{kind: tokIdent, text: "import"},
		{kind: tokPunct, text: "."},
		{kind: tokIdent, text: "meta"},
		{kind: tokPunct, text: "."},
		{kind: tokIdent, text: "url"},
		{kind: tokPunct, text: ")"},
		{kind: tokPunct, text: ")"},
	}
	for _, w := range rest {
		if !tokenEquals(toks, idx, w) {
			return "", 0, false
		}
		idx++
	}
	return value, idx, true
}

func tokenEquals(toks []token, idx int, want token) bool {
	return idx < len(toks) && toks[idx].kind == want.kind && toks[idx].text == want.text
}

// decodeURLPathname resolves the fileURLToPath(new URL(<raw>, ...))
// idiom's raw string argument the way that call chain actually would: as
// a URL reference, where a "%20" is a space rather than the four literal
// characters '%', '2', '0'. Astro's own docs write these idioms with
// plain, unencoded relative paths, but a path containing a character
// URL syntax reserves — a space chief among them — has to be
// percent-encoded to be a valid URL reference at all, and
// fileURLToPath's whole job is turning that encoded reference back into
// a real filesystem path. Taking raw instead would read a literal "%20"
// straight into a path Astro would never actually look for. A malformed
// percent-escape reports unresolved rather than a best-effort guess.
func decodeURLPathname(raw string) (string, bool) {
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", false
	}
	return decoded, true
}

// findExportedConfigObject locates the object literal this check treats
// as Astro's actual config: the argument of "export default { ... }" or
// "module.exports = { ... }", including the one-level call-wrapped form
// either uses ("export default defineConfig({ ... })" being how nearly
// every real Astro project writes it). "=" is never a token at all — see
// tokenPunct's own comment — so "module.exports = {" and the object
// literal's "{" are adjacent in the token stream with nothing to match
// between them.
//
// Returning ok=false — no export/module.exports found, the exported
// value isn't a literal object or a single-object-argument call, an
// import re-exported wholesale, a spread from another module — is
// deliberate and is what scope anchoring rests on: every key search
// below only ever looks INSIDE this object, so code that never resolves
// to it (a local object that happens to declare "srcDir" and is never
// exported, an integration's own options nested inside a real config)
// cannot be read as the config no matter what it contains.
func findExportedConfigObject(toks []token) (open, close int, ok bool) {
	for i := 0; i < len(toks); i++ {
		switch {
		case tokenEquals(toks, i, token{kind: tokIdent, text: "export"}) &&
			tokenEquals(toks, i+1, token{kind: tokIdent, text: "default"}):
			if s, e, matched := matchConfigValue(toks, i+2); matched {
				return s, e, true
			}
		case tokenEquals(toks, i, token{kind: tokIdent, text: "module"}) &&
			tokenEquals(toks, i+1, token{kind: tokPunct, text: "."}) &&
			tokenEquals(toks, i+2, token{kind: tokIdent, text: "exports"}):
			if s, e, matched := matchConfigValue(toks, i+3); matched {
				return s, e, true
			}
		}
	}
	return 0, 0, false
}

// matchConfigValue recognises, starting at toks[p], either a bare object
// literal or a call whose sole argument is one: IDENT ( { ... } ). It
// returns the open/close indices of the object literal itself either
// way, so the caller never has to know which shape it saw.
func matchConfigValue(toks []token, p int) (open, close int, ok bool) {
	if p < len(toks) && toks[p].kind == tokPunct && toks[p].text == "{" {
		end := matchingClose(toks, p)
		if end < 0 {
			return 0, 0, false
		}
		return p, end, true
	}
	if p+2 < len(toks) &&
		toks[p].kind == tokIdent &&
		toks[p+1].kind == tokPunct && toks[p+1].text == "(" &&
		toks[p+2].kind == tokPunct && toks[p+2].text == "{" {
		objOpen := p + 2
		objClose := matchingClose(toks, objOpen)
		if objClose < 0 {
			return 0, 0, false
		}
		if objClose+1 < len(toks) && toks[objClose+1].kind == tokPunct && toks[objClose+1].text == ")" {
			return objOpen, objClose, true
		}
	}
	return 0, 0, false
}

// parseAstroConfig extracts both keys this check reads from one file's
// content: srcDir and build.format, each only ever read as a direct,
// top-level property of the object literal that is actually exported.
//
// tokenize runs first and is allowed to refuse: when it meets a
// construct outside the enumerated subset — see its own doc comment —
// this function stops immediately and reports BOTH keys unresolved,
// with the reason tokenize gave. It does not attempt to use whatever
// tokens were produced before the refusal point, because a scan that
// stopped trusting its own model partway through has nothing downstream
// of that point worth reading either.
func parseAstroConfig(content []byte) astroConfig {
	toks, unresolved, reason := tokenize(content)
	if unresolved {
		return astroConfig{unresolved: true, unresolvedReason: reason}
	}

	var cfg astroConfig

	objOpen, objClose, ok := findExportedConfigObject(toks)
	if !ok {
		// No literal exported config object: an identifier re-export, a
		// spread-only default export, a call this check doesn't
		// recognise, or no export at all. This is handled identically
		// to "the object exists but has no live key" — both mean this
		// scan cannot confirm what Astro would actually see, which is
		// the one thing a lexical scan is never allowed to guess at. It
		// is NOT the same state as cfg.unresolved above: the token
		// stream itself is trustworthy here, there simply isn't an
		// exported object shape this check recognises inside it.
		return cfg
	}

	srcDirKeys := findTopLevelKeyOccurrences(toks, objOpen, objClose, "srcDir")
	switch len(srcDirKeys) {
	case 0:
		// No live top-level key: everything stays at its zero value —
		// srcDirFound stays false, which the caller reads as "unresolved,
		// not absent" rather than "the default applies".
	case 1:
		cfg.srcDirFound = true
		p := srcDirKeys[0] + 2
		if v, isValue := readStringLiteralValue(toks, p); isValue {
			cfg.srcDirResolved = true
			cfg.srcDirValue = v
		} else if raw, end, matched := matchFileURLIdiom(toks, p); matched &&
			end < len(toks) && isValueTerminator(toks[end]) {
			if decoded, ok := decodeURLPathname(raw); ok {
				cfg.srcDirResolved = true
				cfg.srcDirValue = decoded
			}
		}
	default:
		cfg.srcDirAmbiguous = true
	}

	value, found, resolved, ambiguous := findBuildFormatValue(toks, objOpen, objClose)
	switch {
	case ambiguous:
		cfg.buildFormatAmbiguous = true
	case found:
		cfg.buildFormatFound = true
		cfg.buildFormatResolved = resolved
		cfg.buildFormatValue = value
	}

	return cfg
}

// findBuildFormatValue looks for a format key nested exactly one level
// inside a "build: { ... }" object literal that is itself a top-level
// property of the exported config object (objOpen, objClose). Scoping
// the search for "build" to that object's own depth 0 is what keeps a
// nested "vite: { build: { cssMinify: true } }" from ever being read as
// this key: vite's own "build" sits at depth 1 relative to the exported
// object, one level inside "vite" itself, so it is never a candidate —
// the real top-level "build" is found instead, wherever in the object it
// appears. found reports whether a format key exists at all inside that
// block; resolved reports whether its value is a plain string literal
// (value is meaningful only then). A format key whose value isn't a
// plain string (a variable, a member expression, …) reports
// found=true, resolved=false — the state buildFormatFinding's inverted
// convention keeps silent, and the one the mutation named on that
// function flips to a warning.
//
// ambiguous reports a duplicate "build" key, or a duplicate "format" key
// inside the (single) live "build" block — JavaScript takes the LAST of
// either; this scanner used to silently take the FIRST, which is the
// same "wrong but confident" failure mode srcDir's own duplicate-key
// handling already refuses to make. When ambiguous is true, value/found/
// resolved are meaningless: the caller treats ambiguity as its own
// state, exactly parallel to how srcDirAmbiguous already overrides
// srcDirFound/srcDirResolved above.
func findBuildFormatValue(toks []token, objOpen, objClose int) (value string, found, resolved, ambiguous bool) {
	buildKeys := findTopLevelKeyOccurrences(toks, objOpen, objClose, "build")
	if len(buildKeys) == 0 {
		return "", false, false, false
	}
	if len(buildKeys) > 1 {
		return "", false, false, true
	}
	p := buildKeys[0] + 2
	if p >= objClose || toks[p].kind != tokPunct || toks[p].text != "{" {
		return "", false, false, false
	}
	end := matchingClose(toks, p)
	if end < 0 || end > objClose {
		return "", false, false, false
	}

	formatKeys := findTopLevelKeyOccurrences(toks, p, end, "format")
	if len(formatKeys) == 0 {
		return "", false, false, false
	}
	if len(formatKeys) > 1 {
		return "", false, false, true
	}
	v := formatKeys[0] + 2
	if val, isValue := readStringLiteralValue(toks, v); isValue {
		return val, true, true, false
	}
	return "", true, false, false
}
