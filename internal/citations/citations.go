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
//
// THE JOIN IS BOUNDED, and MaxTokenLength below says by what.
func IdentifierTokens(line string) map[string]bool {
	out := map[string]bool{}
	for _, run := range alphanumericRun.FindAllString(line, -1) {
		subs := SplitSubwords(run)
		for i := range subs {
			joined := ""
			for j := i; j < len(subs); j++ {
				if len(joined)+len(subs[j]) > MaxTokenLength {
					break
				}
				joined += strings.ToLower(subs[j])
				out[joined] = true
			}
		}
	}
	return out
}

// MaxTokenLength is the longest token this tokeniser will build, in
// bytes. Every whole subword and every contiguous join is emitted up to
// this length and none beyond it.
//
// WHAT IT BOUNDS. A token exists to be looked up in a vocabulary of
// forbidden names. The longest name any of them declares today is
// SIXTEEN bytes — measured off the vocabulary file rather than guessed,
// and the row beside that file fails if a longer one is ever added, so
// this number can never go quietly wrong. Thirty-two is twice that: room
// for the vocabulary to grow without this constant having to move, and
// still a bound. Every join longer than the longest name in the
// vocabulary is a string that can match nothing, computed anyway.
//
// WHY IT IS NOT A TUNING KNOB. Without a bound the joins are every
// contiguous run of adjacent subwords, so the work grows as the CUBE of
// the input — measured on one machine, on a line of alternating case:
// 2 KiB took 71 ms, 4 KiB 339 ms, 8 KiB 2.4 s and 16 KiB 19 s. A pull
// request's body may be 64 KiB and is written by whoever opened the pull
// request; extrapolating those numbers puts one line of that size at
// roughly twenty minutes of processor time. The reported token count
// stays linear, because the map deduplicates, so nothing runs out of
// memory — it simply holds a machine for as long as the author of the
// text likes.
//
// That is not a defect of the check that reads commit messages. This
// tokeniser has always been the one the file-contents rule uses, and file
// contents in a pull request from a stranger are exactly as
// attacker-controlled as a title. The bound belongs here, where both
// readers get it.
const MaxTokenLength = 32
