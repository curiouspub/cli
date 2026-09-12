package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestTheSelfTestProvesTheCheckerBothWays drives the control that runs
// before any range is examined.
//
// It is the only thing here that can see either of the two opposite
// failures. "An empty range fails" is satisfied by a checker that always
// fails; "a clean message passes" by one that always passes. Nothing else
// in this package can tell either from working code.
//
// MUTATIONS RUN AGAINST THE REAL CHECKER, with what each one ACTUALLY
// reddened. Files preserved by copy, restores verified by checksum.
//
//   - Scan made to return one finding for every input -> this control's
//     CLEAN half reds, saying the repaired commit reported a finding and
//     is the control that must report none.
//   - Scan made to return nothing at all -> its HISTORICAL half reds,
//     saying the commit that must report something reported nothing.
//   - the self-test's call removed from the command, leaving the range
//     check to run alone -> exactly ONE row in this package reds, the one
//     asserting the order, and it is in the range test rather than here.
//     Every range row stays green, and so does every row in this file,
//     because they call the control directly. That is the whole point
//     restated as a measurement: nothing about the range can see whether
//     this ran. It was predicted that NOTHING would red, which was very
//     nearly right — the order row is the one thing standing between that
//     prediction and a control that can be deleted in silence.
//   - the revision listing stubbed to answer with no commits -> three rows
//     here red, each saying the listing answered with zero surfaces where
//     one was asked for. Run against the control as it stood BEFORE it
//     went through the range path, the same mutation left every row here
//     green while the check could no longer read a range at all: a
//     control that took a shortcut past the plumbing certified the
//     scanner and said nothing about the check.
func TestTheSelfTestProvesTheCheckerBothWays(t *testing.T) {
	r := repo{dir: moduleRoot(t)}
	rules := realRules(t)

	t.Run("it passes against this repository", func(t *testing.T) {
		var out bytes.Buffer
		if err := selfTest(r, rules, &out); err != nil {
			t.Fatalf("the self-test failed in this checkout: %v", err)
		}
		for _, sha := range append(append([]string{}, citedCommits...), cleanCommit) {
			if !strings.Contains(out.String(), short(sha)) {
				t.Errorf("the self-test's output does not name %s, so a reader cannot tell "+
					"which half ran:\n%s", short(sha), out.String())
			}
		}
		if !strings.Contains(out.String(), vendorControlID+" reported one infrastructure finding") {
			t.Errorf("the self-test output does not show that the vendor half ran:\n%s", out.String())
		}
	})

	t.Run("each half reports what it must, and the other half does not", func(t *testing.T) {
		// BOTH DIRECTIONS FROM ONE PLACE. Without the second half, the
		// first is satisfied by a checker that reports on everything.
		for _, sha := range citedCommits {
			found, err := scanCommit(r, rules, sha)
			if err != nil {
				t.Fatalf("reading %s: %v", short(sha), err)
			}
			if len(found) == 0 {
				t.Errorf("%s reported nothing and is on the list because it must report "+
					"something", short(sha))
			}
			// AND IT CAME BACK UNDER THE SUBJECT A REPORT WOULD NAME IT
			// BY. A control that scanned the same text under a private
			// subject of its own would pass while the range path — the
			// listing, the message reading, the naming — went untouched.
			want := "commit " + short(sha)
			if found[0].Subject != want {
				t.Errorf("the self-test's finding is named %q, want %q: this control has to "+
					"travel the road a real range travels, or it certifies the scanner and "+
					"says nothing about the check", found[0].Subject, want)
			}
		}
		found, err := scanCommit(r, rules, cleanCommit)
		if err != nil {
			t.Fatalf("reading %s: %v", short(cleanCommit), err)
		}
		if len(found) != 0 {
			t.Errorf("%s reported %d finding(s) and is the control that must report none",
				short(cleanCommit), len(found))
		}
	})

	t.Run("its output names a sha and a count and nothing else", func(t *testing.T) {
		// THE HAZARD IS IN THE CONTROL ITSELF. The messages it reads
		// carry the identifiers this check exists to keep out of new
		// ones, and a report rendering one would write a fresh copy into
		// a log that is as public as the history it came from. What is
		// printed is a sha and a count, which name nothing.
		var out bytes.Buffer
		if err := selfTest(r, rules, &out); err != nil {
			t.Fatalf("the self-test failed in this checkout: %v", err)
		}
		for _, sha := range citedCommits {
			message, err := r.message(sha)
			if err != nil {
				t.Fatalf("reading %s: %v", short(sha), err)
			}
			// THE LINE THAT TRIPPED, rather than the text that matched. A
			// finding no longer carries the matched string — the struct
			// that cannot hold it cannot leak it — so what this row has to
			// work with is WHERE it fired, and the line at that place is a
			// strictly larger piece of the message. If the output contains
			// none of those lines it contains none of the matches either,
			// and it is the sharper question besides: a short term can sit
			// inside an ordinary word of the report's own prose, and a
			// whole line of somebody's commit message cannot.
			lines := strings.Split(message, "\n")
			tripped := 0
			for _, f := range rules.Scan("audit", message) {
				if f.Line < 1 || f.Line > len(lines) {
					continue
				}
				line := strings.TrimSpace(lines[f.Line-1])
				if line == "" {
					continue
				}
				tripped++
				if strings.Contains(out.String(), line) {
					// NAMED BY SHA, NOT QUOTED. A failure message that
					// printed the leaked text would be the same leak,
					// arriving through the row that reports it.
					t.Fatalf("the self-test's output reproduces a line of the message in %s; "+
						"the recorded evidence is meant to be shas and counts", short(sha))
				}
			}
			if tripped == 0 {
				t.Fatalf("%s produced no finding this row could check the output against, so "+
					"the absence above is satisfied by a scan that found nothing", short(sha))
			}
		}
		for term, id := range rules.VendorTerms() {
			if id == vendorControlID && strings.Contains(strings.ToLower(out.String()), term) {
				t.Fatal("the self-test output reproduces the pinned provider term; the control " +
					"must name only its public rule id")
			}
		}
	})

	t.Run("a checkout without the history is refused, not passed", func(t *testing.T) {
		// REPORTING AN ABSENCE WHEN THE HONEST ANSWER IS "I COULD NOT
		// LOOK" is the failure this row exists for. A shallow checkout
		// has no answer about whether this checker works, and a run that
		// treated the missing object as a clean message would go on to
		// measure the range with an instrument nobody tested.
		empty := newFixture(t)
		empty.write("notes.txt", "a repository with none of this project's history\n")
		empty.commit("first", "notes.txt")

		var out bytes.Buffer
		err := selfTest(empty.repo, rules, &out)
		if err == nil {
			t.Fatal("the self-test passed in a checkout that does not carry the commits it " +
				"reads")
		}
		if !strings.Contains(err.Error(), "full history") {
			t.Errorf("the refusal does not tell the operator what to do about it: %v", err)
		}
	})

	t.Run("the list it proves itself against is not empty", func(t *testing.T) {
		if len(citedCommits) == 0 {
			t.Error("no commit is required to report, so the red half of this control is " +
				"vacuous and a checker that finds nothing would pass it")
		}
		if cleanCommit == "" {
			t.Error("no commit is required to pass, so the green half is vacuous and a " +
				"checker that reports everything would pass it")
		}
	})
}
