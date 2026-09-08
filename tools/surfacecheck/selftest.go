package main

import (
	"fmt"
	"io"
)

// The commits this check proves itself against, before it is trusted with
// a range.
//
// THEY ARE SHAS AND NOTHING ELSE. The messages behind them carry the very
// identifiers this check exists to keep out of new ones, and a fixture
// file holding one would be the leak arriving through the check that
// hunts it. They are already in this repository's history, where the
// checker can read them at run time and no new copy is made — so what is
// written down here, and what this check ever prints about them, is a sha
// and a count. Both name nothing.
//
// citedCommits must every one of them REPORT. cleanCommit must PASS. A
// checker that can be made to red on demand and green on demand, in that
// order, before it is asked the real question, can be neither of the two
// things a range check fails as: one that always finds something, and one
// that never does. Neither failure is visible to any other row here —
// "an empty range fails" is satisfied by a check that always fails, and
// "a clean message passes" by one that always passes.
//
// ONLY ONE CITED COMMIT IS REACHABLE, and that is a measurement rather
// than a choice. Two instances of this defect are on the record. The
// first was the tip when it was found, so it was amended away and its
// original is now reachable from no ref at all — a fresh clone does not
// have the object, and a check that asked for it would fail everywhere
// except on the machine that did the rewrite. The second was already deep
// in the history, where correcting it would have moved every sha after
// it, so it stands. That one is below.
//
// cleanCommit is the REPAIRED form of the first instance: a real message,
// of the same shape and length as the one that failed, minus the
// identifier. It is a stronger control than an invented clean string
// would be, because it also asserts this check stays quiet on ordinary
// repository prose — and a check that reds on ordinary prose is a check
// somebody switches off.
var (
	citedCommits = []string{"0abb576493e0fea0fdf64fbd258bad30d7dd6cdf"}
	cleanCommit  = "ff554c46438c6e7d4d84ec396d452c88c2b62c25"
)

// selfTest runs before any range is examined.
//
// Recorded in a commit message instead, this control would protect
// nothing against a later change to the range logic or to the loader.
// Run first in every job, it is a regression test.
func selfTest(r repo, rules Rules, out io.Writer) error {
	if len(citedCommits) == 0 {
		return fmt.Errorf("this check has no commit it must report on, so its red half " +
			"proves nothing and a checker that never finds anything would pass it")
	}

	for _, sha := range citedCommits {
		found, err := countFindings(r, rules, sha)
		if err != nil {
			return err
		}
		if found == 0 {
			return fmt.Errorf("the self-test read %s and reported nothing. That message is "+
				"known to carry what this check looks for, so either the vocabulary no longer "+
				"contains the rule that caught it or the checker has stopped finding anything "+
				"at all. Nothing below this line would notice the second one", short(sha))
		}
		fmt.Fprintf(out, "self-test: %s reported %d finding(s), as it must\n", short(sha), found)
	}

	found, err := countFindings(r, rules, cleanCommit)
	if err != nil {
		return err
	}
	if found > 0 {
		return fmt.Errorf("the self-test read %s and reported %d finding(s). That message is "+
			"known to be clean, so this checker is reporting on text that does not violate "+
			"anything — and a check that finds something in every range is one nobody can act "+
			"on", short(cleanCommit), found)
	}
	fmt.Fprintf(out, "self-test: %s reported nothing, as it must\n", short(cleanCommit))
	return nil
}

// countFindings is the only thing the self-test learns about a historical
// message: how many findings it produced. The findings themselves are
// discarded here rather than rendered, because rendering one would write
// the identifier into a log — which is a new copy of exactly the thing
// this check exists to stop being copied.
func countFindings(r repo, rules Rules, sha string) (int, error) {
	if _, err := r.resolve(sha); err != nil {
		return 0, fmt.Errorf("the self-test needs %s and this checkout does not have it. "+
			"That is not a pass: a shallow checkout cannot answer whether this check works, "+
			"and the range below would be measured by an instrument nobody tested. Check out "+
			"the full history and run again (%w)", short(sha), err)
	}
	message, err := r.message(sha)
	if err != nil {
		return 0, err
	}
	return len(rules.Scan("self-test", message)), nil
}

// short renders a sha the way a person reads one.
func short(sha string) string {
	const shownCharacters = 7 // what this repository's own log prints
	if len(sha) <= shownCharacters {
		return sha
	}
	return sha[:shownCharacters]
}
