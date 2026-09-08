// Package citations holds the subword tokeniser that this repository's
// rules about its own published text are built on.
//
// WHY IT IS A PACKAGE OF ITS OWN, and not a function inside the guards
// that use it. internal/guard contains nothing but _test.go files, which
// is a property worth keeping — it is where rules ABOUT this repository
// live, and nothing should be able to import it. A test file cannot be
// imported by anything, so a second consumer of the tokeniser could not
// reach it there, and giving that package production code to export
// would make it importable and invite exactly what its shape prevents.
//
// The two consumers are the content rule, which reads what files SAY, and
// the surface check under tools/, which reads what a commit message, a
// branch name and a tag say. Those are different surfaces of one
// repository and they must tokenise identically, or a name forbidden in a
// comment is spelled into a commit message instead.
package citations

import (
	"regexp"
	"strings"
)

// SplitSubwords splits one alphanumeric run the way an identifier is
// actually built: an acronym run, a capitalised word, a lowercase word,
// or a bare digit run. Digits stay attached to the letters they follow,
// so a name ending in a digit survives as one subword.
//
// Written by hand rather than as a pattern because the natural
// expression for the acronym boundary needs a negative lookahead, and
// RE2 — Go's engine, chosen for its linear-time guarantee — does not
// have one. The first version of this used one and panicked at init.
func SplitSubwords(run string) []string {
	isUpper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	isLower := func(b byte) bool { return b >= 'a' && b <= 'z' }
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }

	var out []string
	for i := 0; i < len(run); {
		start := i
		switch {
		case isUpper(run[i]):
			for i < len(run) && isUpper(run[i]) {
				i++
			}
			// An uppercase run followed by lowercase is an acronym whose
			// last letter opens the next word: a run then a capitalised
			// word splits between them, not after them.
			if i-start > 1 && i < len(run) && isLower(run[i]) {
				i--
			}
			for i < len(run) && (isLower(run[i]) || isDigit(run[i])) {
				i++
			}
		case isLower(run[i]):
			for i < len(run) && (isLower(run[i]) || isDigit(run[i])) {
				i++
			}
		default:
			for i < len(run) && isDigit(run[i]) {
				i++
			}
		}
		out = append(out, run[start:i])
	}
	return out
}

// alphanumericRun finds the maximal runs a line is tokenised from.
var alphanumericRun = regexp.MustCompile(`[A-Za-z0-9]+`)

// IdentifierTokens returns every whole subword of a line, plus every
// CONTIGUOUS JOIN of adjacent subwords.
//
// This is the whole of why a vocabulary rule built on it is not a regular
// expression, and both halves are load bearing.
//
// SPLITTING is what catches the real spellings. A word-boundary pattern
// sees no boundary inside an identifier, so every camelCase and
// snake_case spelling of a forbidden name walked straight past the
// pattern that replaced it — which is how the rule shipped evadable in
// the first place.
//
// JOINING is what catches a name that is itself split by the convention:
// a two-part product name written in camelCase arrives as two subwords
// and matches neither, until the adjacent pair is rejoined.
//
// And matching a whole subword rather than a SUBSTRING is what keeps the
// rule quiet: an ordinary English word for a defect contains one of the
// forbidden names outright, and a substring match reds on it. Splitting
// distinguishes an identifier that NAMES something from a word that
// merely contains those letters.
func IdentifierTokens(line string) map[string]bool {
	out := map[string]bool{}
	for _, run := range alphanumericRun.FindAllString(line, -1) {
		subs := SplitSubwords(run)
		for i := range subs {
			joined := ""
			for j := i; j < len(subs); j++ {
				joined += strings.ToLower(subs[j])
				out[joined] = true
			}
		}
	}
	return out
}
