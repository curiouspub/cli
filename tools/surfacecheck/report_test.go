package main

import (
	"bytes"
	"strings"
	"testing"
)

// renderReport runs the real report writer and returns what a workflow
// log would receive.
func renderReport(t *testing.T, findings []Finding, narrowings []Narrowing) string {
	t.Helper()
	var out bytes.Buffer
	report(&out, findings, narrowings)
	return out.String()
}

// withoutTheMatches returns the same findings with the matched text
// replaced, and nothing else changed.
//
// It is how the row below asks its question without having to guess what
// a coincidence looks like. Searching the report for a short matched term
// cannot tell a leak from that term happening to sit inside an ordinary
// word of the report's own prose; rendering the SAME findings twice, once
// with the real text and once with text that shares none of it, can — the
// two are byte-identical exactly when the matched text does not reach the
// page.
func withoutTheMatches(findings []Finding) []Finding {
	out := make([]Finding, len(findings))
	for i, f := range findings {
		f.Match = "\x01stand-in\x01"
		out[i] = f
	}
	return out
}

// TestTheReportNamesThePlaceAndNeverTheTextThatTrippedIt is the row for
// the hazard this whole check is built around, arriving through the check
// itself.
//
// A finding's matched text IS the private citation or the provider name.
// The report lands in a run's log, and on a pull request from a fork that
// log is more public than the message being checked — so a report
// carrying the match publishes the thing it caught, and does it in a
// place with a wider audience than the surface it was defending.
//
// The same program already refuses this thirty lines away: the self-test
// discards its findings and prints a sha and a count, because rendering
// one "would write the identifier into a log". The hazard was understood
// and guarded in one place. This row is what makes the report agree.
//
// BOTH HALVES, because an absence assertion on its own is satisfied by a
// report that prints nothing at all — which would be a leak fixed by
// making the check useless. The report must still name the surface, the
// line and the rule, or an author cannot act on it.
//
// MUTATION RUN, and what actually reddened: restoring `(matched %q)` to
// Finding.String —
//
//	--- FAIL: TestTheReportNamesThePlaceAndNeverTheTextThatTrippedIt/the_matched_text_does_not_reach_the_page
//	    report_test.go:88: the rendered report changes when only the matched text changes, so
//	    it carries that text into the log.
//
// and nothing else in the package moved, which is the measurement that
// says no other row could see this.
func TestTheReportNamesThePlaceAndNeverTheTextThatTrippedIt(t *testing.T) {
	rules := realRules(t)
	phrase, rule := aCitedPhrase(t, rules)
	term, _ := aForbiddenName(t, rules)

	// A real scan, not hand-built findings: the property is about what
	// the pipeline renders, and a fixture assembled here would prove it
	// of a struct nobody produces.
	message := "tidy up the walk\n\nas " + phrase + " says\n"
	spelled := "handoff to " + strings.ToUpper(term[:1]) + term[1:] + "Runtime"
	findings := append(
		rules.Scan("commit abcdef0", message),
		rules.Scan("branch name", spelled)...)
	if len(findings) < 2 {
		t.Fatalf("the fixture produced %d finding(s); this row needs both a citation and a "+
			"vendor term or it tests one rendering path", len(findings))
	}

	rendered := renderReport(t, findings, nil)

	t.Run("the matched text does not reach the page", func(t *testing.T) {
		if standIn := renderReport(t, withoutTheMatches(findings), nil); rendered != standIn {
			t.Errorf("the rendered report changes when only the matched text changes, so it "+
				"carries that text into the log.\nA finding's match is the private citation or "+
				"the provider name this check exists to keep out of published text, and a run's "+
				"log is more public than the message it came from.\ngot:\n%s\nwith the text "+
				"replaced:\n%s", rendered, standIn)
		}
	})

	t.Run("and it still says where to look and which rule fired", func(t *testing.T) {
		// THE PRESENCE, and without it the row above is satisfied by a
		// report that prints nothing — a leak closed by making the check
		// useless.
		for _, want := range []string{"commit abcdef0", "line 3", rule, "branch name",
			"names infrastructure"} {
			if !strings.Contains(rendered, want) {
				t.Errorf("the report does not name %q, so an author cannot act on it:\n%s",
					want, rendered)
			}
		}
	})
}
