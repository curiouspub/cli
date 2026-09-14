package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/leakcheck"
	"github.com/curiouspub/cli/internal/rulefile"
)

// renderReport runs the real report writer and returns what a workflow
// log would receive.
func renderReport(t *testing.T, findings []Finding, narrowings []Narrowing) string {
	t.Helper()
	var out bytes.Buffer
	report(&out, findings, narrowings)
	return out.String()
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
// THE ROW CHANGED SHAPE WHEN THE FIELD WENT, and the new shape is the
// stronger claim. It used to render the same findings twice — once with
// the real matched text and once with a stand-in — and require the two
// pages to be byte-identical, which is how you ask "did this text reach
// the page" without guessing what a coincidence looks like. That oracle
// needed a field holding the text. There is no such field now: a Finding
// carries the surface, the line and the rule's id, so the renderer
// CANNOT print what matched, and no caller written later can either.
// What is asserted here instead is that absence, at the type, plus the
// behaviour that follows from it.
//
// MUTATION RUN, and what actually reddened: adding a Match field back to
// Finding and restoring `(matched %q)` to Finding.String reds the first
// subtest naming the extra field, and reds the second when the fixture's
// own text appears on the page. Nothing else in the package moved.
func TestTheReportNamesThePlaceAndNeverTheTextThatTrippedIt(t *testing.T) {
	rules := realRules(t)
	phrase, rule := aCitedPhrase(t, rules)
	term, _, _ := aForbiddenName(t, rules)

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

	t.Run("a finding cannot hold the text that tripped it", func(t *testing.T) {
		// THE STRUCTURAL HALF, and it is the one that holds for callers
		// nobody has written. A renderer can be trusted not to print a
		// field; it cannot be trusted forever, and the next reader of
		// these findings is a different program.
		want := map[string]bool{"Subject": true, "Line": true, "PatternID": true,
			"Infrastructure": true}
		typ := reflect.TypeOf(Finding{})
		for i := 0; i < typ.NumField(); i++ {
			if name := typ.Field(i).Name; !want[name] {
				t.Errorf("Finding carries a field %q that this row does not know about.\n"+
					"The text a rule matched IS the private citation or the provider name, "+
					"and every consumer of a finding writes somewhere public — a run's log "+
					"on a fork's pull request, or a committed record. A struct that cannot "+
					"hold the string cannot leak it; if this field is meant to be here, it "+
					"is this row that has to change, deliberately.", name)
			}
			delete(want, typ.Field(i).Name)
		}
		for name := range want {
			t.Errorf("Finding no longer has a field %q, so the rows below are asserting "+
				"about a shape that has moved", name)
		}
	})

	t.Run("and the page carries neither of the fixture's own strings", func(t *testing.T) {
		// THE BEHAVIOURAL FLOOR. The row above says the text cannot be
		// carried; this one says nothing reconstructed it on the way out.
		for _, secret := range []string{phrase, spelled} {
			if strings.Contains(rendered, secret) {
				t.Errorf("the rendered report reproduces text from the surface it read.\n"+
					"A run's log is more public than the message it came from, so a report "+
					"carrying what it caught publishes it to a wider audience than the "+
					"surface it was defending.\n%s", rendered)
			}
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

// TestTheNarrowingOutputSaysWhatHappensNext covers the two things a
// narrowing report has to tell the person reading it, neither of which
// follows from the narrowing itself.
//
// THE FIRST IS THAT THE RED IS THE PATH. A narrowing is a finding and
// findings fail, so the change that retires a rule is merged over a
// failing check. That is deliberate — there is no way to retire a rule
// without the run that retires it saying so — and left unsaid it reads as
// a mistake to the one person who has to decide whether to press the
// button anyway.
//
// THE SECOND IS WHAT ELSE THE LINE WAS HOLDING UP. This check proves
// itself before every run against a commit in this repository's history
// that a rule must catch, and exactly one rule catches it: measured by
// scanning that commit against the real vocabulary. Retire that rule and
// every later run fails undetermined, saying only that the commit
// reported nothing — with nothing connecting it to a change made weeks
// earlier. The moment to say so is while the person who can act on it is
// still there.
//
// MUTATION RUN: delete either paragraph. This row reds naming the missing
// one, and nothing else in the package moves.
func TestTheNarrowingOutputSaysWhatHappensNext(t *testing.T) {
	narrowed := renderReport(t, nil, []Narrowing{
		{Path: citationPatternsPath, Rule: "a-rule-that-used-to-be-here"},
	})

	if !strings.Contains(narrowed, "a-rule-that-used-to-be-here") ||
		!strings.Contains(narrowed, citationPatternsPath) {
		t.Fatalf("the narrowing does not name the file and the line that went:\n%s", narrowed)
	}
	for what, phrase := range map[string]string{
		"that the red is the designed path":                "designed path",
		"that the self-test's own commit may depend on it": "proves itself against a commit",
	} {
		if !strings.Contains(narrowed, phrase) {
			t.Errorf("the narrowing report does not say %s:\n%s", what, narrowed)
		}
	}

	// THE CONTROL. A run with nothing to report says none of it — without
	// this, the row above is satisfied by a report that prints the whole
	// paragraph every time, which would train its reader to skip it.
	clean := renderReport(t, nil, nil)
	for _, phrase := range []string{"designed path", "proves itself against a commit"} {
		if strings.Contains(clean, phrase) {
			t.Errorf("a clean run carries the narrowing copy anyway:\n%s", clean)
		}
	}
}

// TestTheSelfTestSaysHowToRepairItself is the other half of the same
// hazard, at the other end of it in time.
//
// A rule retired on purpose makes this check fail on every run
// afterwards, and the failure says the recorded commit reported nothing —
// which reads as a bug in the check rather than as the second half of
// somebody's decision. The message has to carry the repair, because it is
// the only place the two events ever meet.
//
// THE VOCABULARY HERE IS REAL IN SHAPE AND MATCHES NOTHING, which is the
// only way to reach this branch without editing the repository's own rule
// files: it stands in for a vocabulary that has lost the one line that
// catches the recorded commit.
//
// MUTATION RUN: drop the repair sentence from the message. This row reds;
// the control below stays green, because it never reaches that branch.
func TestTheSelfTestSaysHowToRepairItself(t *testing.T) {
	r := repo{dir: moduleRoot(t)}

	engine, err := leakcheck.New(
		[]rulefile.Rule{{ID: "matches-nothing", Text: `zzz-this-pattern-matches-nothing-zzz`}},
		[]rulefile.Rule{{ID: "term-matches-nothing", Text: "zzzthistermmatchesnothingzzz"}},
	)
	if err != nil {
		t.Fatalf("building a vocabulary that matches nothing: %v", err)
	}
	blind := Rules{Rules: engine}
	var out bytes.Buffer
	err = selfTest(r, blind, &out)
	if err == nil {
		t.Fatal("a vocabulary that catches nothing passed the self-test, so the control " +
			"that must red on demand cannot")
	}
	if !strings.Contains(err.Error(), "retired on purpose") {
		t.Errorf("the failure does not connect itself to the change that causes it: %v\n"+
			"Whoever hits this is looking at a run that fails for a decision somebody else "+
			"made, and nothing else anywhere says the two are the same event.", err)
	}

	// THE CONTROL. The real vocabulary passes, so the row above is about
	// the missing rule rather than about a self-test that always fails.
	var green bytes.Buffer
	if err := selfTest(r, realRules(t), &green); err != nil {
		t.Fatalf("the self-test failed against this repository's real vocabulary: %v", err)
	}
}

// TestTheRecoveryLineAppearsOnlyWhereTheRecoveryIsCounterIntuitive
// asserts the payload-sourced recovery advice, and asserts its ABSENCE
// everywhere else.
//
// The absence half is the load-bearing one. Printed on every failure the
// line would be noise on the common case — a commit message, fixed by
// the push that fixes everything else — and advice that appears when it
// does not apply is advice a reader learns to skip, which costs exactly
// the case it was written for.
//
// Why the line exists: the surfaces a pull request contributes come from
// the event payload, so a re-run replays the event it was created from
// and reports the identical finding at the identical line. Somebody who
// has just corrected the body reads that as the fix having failed. This
// was measured on a real run before the line was written.
func TestTheRecoveryLineAppearsOnlyWhereTheRecoveryIsCounterIntuitive(t *testing.T) {
	rules := realRules(t)
	phrase, _ := aCitedPhrase(t, rules)
	text := "as " + phrase + " says\n"

	// One phrase, scanned under three subjects, so the only thing that
	// differs between the three reports is WHICH SURFACE it was on.
	const marker = "the only path to green"

	for _, tc := range []struct {
		subject string
		want    bool
		why     string
	}{
		{subjectPullRequestBody, true,
			"a body is edited in place, so its author will re-run and read the same finding"},
		{subjectPullRequestTitle, true,
			"a title is edited in place for the same reason"},
		{"commit abcdef0", false,
			"a commit message is corrected by a push, which produces the new event anyway"},
		{"branch name", false,
			"a branch is renamed by a push, same as a commit"},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			findings := rules.Scan(tc.subject, text)
			if len(findings) == 0 {
				t.Fatalf("the fixture phrase %q produced no finding on %q, so this row "+
					"would pass without rendering anything", phrase, tc.subject)
			}
			var out strings.Builder
			report(&out, findings, nil)
			got := strings.Contains(out.String(), marker)
			if got != tc.want {
				t.Errorf("recovery line present = %v, want %v, on %q — %s\n%s",
					got, tc.want, tc.subject, tc.why, out.String())
			}
		})
	}
}
