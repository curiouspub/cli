package preflight

import "strings"

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
// (with its escapes already resolved into text and its interpolation
// flag set), or one punctuation character this check's shapes need.
type token struct {
	kind             tokenKind
	text             string // ident text, string content, or the single punct byte
	hasInterpolation bool   // set only for a backtick literal containing a literal "${"
}

// tokenPunct is the fixed set of punctuation characters this check ever
// needs to recognise: object/array/call delimiters, comma, dot and
// colon. Anything else that is not whitespace, a comment, a string or an
// identifier is insignificant to every shape below and is skipped.
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
func tokenize(src []byte) []token {
	var toks []token
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
			// A number, an operator, or any other byte this check has no
			// shape for. Skipping one byte at a time rather than trying
			// to tokenise it properly is safe here: none of the shapes
			// this check recognises ever need to inspect one.
			i++
		}
	}

	return toks
}

// scanString reads a single-quoted, double-quoted or backtick literal
// starting at src[start] (which must be the opening quote) and returns
// its token plus the index of the byte after the closing quote — or
// after the end of src, for an unterminated literal.
//
// A backslash escapes the very next byte; the escaped byte is copied
// into the token's text with the backslash dropped, which is enough to
// let an escaped quote not end the literal early and is deliberately not
// a full escape-sequence decoder — this check only ever compares the
// result against exact key names or uses it as a literal path, and
// neither needs \n or \u sequences resolved.
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

	for i < n && src[i] != quote {
		if src[i] == '\\' && i+1 < n {
			content.WriteByte(src[i+1])
			i += 2
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

	return token{kind: tokString, text: content.String(), hasInterpolation: hasInterp}, i
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

// findKeyOccurrences returns the index of every token in toks that is a
// legitimate key named name immediately followed by ":". Counting every
// occurrence, rather than stopping at the first, is what lets the caller
// tell "one key" from "ambiguous" apart.
func findKeyOccurrences(toks []token, name string) []int {
	var positions []int
	for idx := 0; idx < len(toks)-1; idx++ {
		if isKeyToken(toks[idx], name) && toks[idx+1].kind == tokPunct && toks[idx+1].text == ":" {
			positions = append(positions, idx)
		}
	}
	return positions
}

// matchingClose returns the index of the token that closes the bracket
// opened at toks[open] — "{", "(" or "[" — tracking nested depth across
// all three bracket kinds together, or -1 if src ends before it closes
// or the shapes don't nest correctly. Either failure is treated the same
// way by every caller: as "could not determine", never as a guess.
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
func matchFileURLIdiom(toks []token, p int) (value string, ok bool) {
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
			return "", false
		}
		idx++
	}

	if idx >= len(toks) || toks[idx].kind != tokString || toks[idx].hasInterpolation {
		return "", false
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
			return "", false
		}
		idx++
	}
	return value, true
}

func tokenEquals(toks []token, idx int, want token) bool {
	return idx < len(toks) && toks[idx].kind == want.kind && toks[idx].text == want.text
}

// parseAstroConfig extracts both keys this check reads from one already
// comment-stripped-by-construction token stream: srcDir (defaulting to
// "src" when no live key exists at all) and build.format (found only
// when nested one level inside a top-level build object literal).
func parseAstroConfig(content []byte) astroConfig {
	toks := tokenize(content)

	cfg := astroConfig{srcDirResolved: true, srcDirValue: "src"}

	srcDirKeys := findKeyOccurrences(toks, "srcDir")
	switch len(srcDirKeys) {
	case 0:
		// No live key: the default above already applies.
	case 1:
		cfg.srcDirFound = true
		cfg.srcDirResolved = false
		p := srcDirKeys[0] + 2
		if p < len(toks) && toks[p].kind == tokString && !toks[p].hasInterpolation {
			cfg.srcDirResolved = true
			cfg.srcDirValue = toks[p].text
		} else if value, ok := matchFileURLIdiom(toks, p); ok {
			cfg.srcDirResolved = true
			cfg.srcDirValue = value
		}
	default:
		cfg.srcDirAmbiguous = true
		cfg.srcDirResolved = false
	}

	if value, found, resolved := findBuildFormatValue(toks); found {
		cfg.buildFormatFound = true
		cfg.buildFormatResolved = resolved
		cfg.buildFormatValue = value
	}

	return cfg
}

// findBuildFormatValue looks for a format key nested exactly one level
// inside a top-level "build: { ... }" object literal. found reports
// whether such a key exists at all; resolved reports whether its value
// is a plain string literal (value is meaningful only then). A build key
// that isn't there, isn't followed by an object literal, or has no
// format key at its own top level reports found=false; a format key
// whose value isn't a plain string (a variable, a member expression, …)
// reports found=true, resolved=false — the state buildFormatFinding's
// inverted convention keeps silent, and the one the mutation named on
// that function flips to a warning.
func findBuildFormatValue(toks []token) (value string, found bool, resolved bool) {
	buildKeys := findKeyOccurrences(toks, "build")
	if len(buildKeys) == 0 {
		return "", false, false
	}
	p := buildKeys[0] + 2
	if p >= len(toks) || toks[p].kind != tokPunct || toks[p].text != "{" {
		return "", false, false
	}
	end := matchingClose(toks, p)
	if end < 0 {
		return "", false, false
	}

	depth := 0
	for idx := p + 1; idx < end; idx++ {
		tk := toks[idx]
		if depth == 0 && isKeyToken(tk, "format") && idx+1 < end &&
			toks[idx+1].kind == tokPunct && toks[idx+1].text == ":" {
			v := idx + 2
			if v < end && toks[v].kind == tokString && !toks[v].hasInterpolation {
				return toks[v].text, true, true
			}
			return "", true, false
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
	return "", false, false
}
