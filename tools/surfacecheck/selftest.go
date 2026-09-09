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
		found, err := scanCommit(r, rules, sha)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return fmt.Errorf("the self-test read %s and reported nothing. That message is "+
				"known to carry what this check looks for, so either the vocabulary no longer "+
				"contains the rule that caught it or the checker has stopped finding anything "+
				"at all. Nothing below this line would notice the second one", short(sha))
		}
		fmt.Fprintf(out, "self-test: %s reported %d finding(s), as it must\n", short(sha), len(found))
	}

	found, err := scanCommit(r, rules, cleanCommit)
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return fmt.Errorf("the self-test read %s and reported %d finding(s). That message is "+
			"known to be clean, so this checker is reporting on text that does not violate "+
			"anything — and a check that finds something in every range is one nobody can act "+
			"on", short(cleanCommit), len(found))
	}
	fmt.Fprintf(out, "self-test: %s reported nothing, as it must\n", short(cleanCommit))
	return nil
}

// scanCommit reads one historical commit BY THE ROAD A REAL RANGE TAKES:
// the same enumeration, the same message reading, the same subject a
// finding would be named by in a report.
//
// IT USED TO CALL THE SCANNER DIRECTLY, with a subject of its own, and
// that made the control blind to the machinery it exists to vouch for. A
// range enumerator returning nothing passed it — measured, with the
// enumerator stubbed to return no commits: every row of the self-test
// stayed green while the check could no longer read a range at all. A
// scanner that special-cased its private subject would have passed it
// too. A control that takes a shortcut past the plumbing certifies the
// scanner and says nothing about the check.
//
// The findings come back so the caller can COUNT them. They are never
// rendered: the messages behind these shas carry the identifiers this
// check exists to keep out of new ones, and printing one would write a
// fresh copy into a log as public as the history it came from.
func scanCommit(r repo, rules Rules, sha string) ([]Finding, error) {
	if _, err := r.resolve(sha); err != nil {
		return nil, fmt.Errorf("the self-test needs %s and this checkout does not have it. "+
			"That is not a pass: a shallow checkout cannot answer whether this check works, "+
			"and the range below would be measured by an instrument nobody tested. Check out "+
			"the full history and run again (%w)", short(sha), err)
	}
	findings, examined, err := examine(r, rules, request{}, func() ([]string, error) {
		return r.only(sha)
	})
	if err != nil {
		return nil, err
	}
	if examined != 1 {
		// THE FLOOR, and it is the half a count of findings cannot supply.
		// Zero findings is the right answer for the clean commit and the
		// wrong one for the cited commit, but zero SURFACES is neither —
		// it is the listing having gone quiet, and it looks exactly like
		// a clean message from here.
		return nil, fmt.Errorf("the self-test asked for one commit, %s, and the listing "+
			"answered with %d surface(s). Nothing below this line reads a range that was "+
			"enumerated any other way, so a listing that has stopped answering would be "+
			"discovered by the measurement rather than by the control", short(sha), examined)
	}
	return findings, nil
}

// short renders a sha the way a person reads one.
func short(sha string) string {
	const shownCharacters = 7 // what this repository's own log prints
	if len(sha) <= shownCharacters {
		return sha
	}
	return sha[:shownCharacters]
}
