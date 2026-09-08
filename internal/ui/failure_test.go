package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readGolden returns the recorded bytes of one expected rendering.
func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading the golden file: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("%s is empty — a golden test against nothing passes against nothing", name)
	}
	return string(data)
}

// THE RENDERER FIXTURES. Every row in this file that used to reach for a
// published example now uses one of these, and the reason is that all of
// them were about the RENDERER rather than about the words: how three
// parts are laid out, that styling changes nothing but escape sequences,
// that a golden comparison can tell two strings apart.
//
// The product copy those rows borrowed has moved to the checks that own
// its conditions, and dragging it along would have left this package
// holding a second copy of it — the exact divergence the move was made
// to end. So these say nothing about anybody's project: their words are
// deliberately unremarkable, because a fixture whose text reads like
// product copy invites the next reader to edit it as if it were.
//
// They are VALUES, and a pointer is taken where one is needed. The row
// that reworded a published example took a copy of the POINTER and then
// wrote through it, permanently editing the package-level copy for every
// row that ran afterwards; nothing failed, because the rows that ran
// later did not assert on the part that had changed. A value copy cannot
// do that.
var (
	// threePartSample fills all three parts, which is the layout the
	// renderer's ordinary output has.
	threePartSample = Failure{
		What: "Something in this project needs a look.",
		Why: "This is a fixture for the renderer rather than copy about anybody's\n" +
			"project, and its words are chosen to be unremarkable.",
		Next: "Compare it against the golden file beside this test.",
	}

	// whatAndNextSample leaves the middle part empty, which is the shape
	// renderFailure drops rather than rendering as a blank paragraph.
	// Nothing this package publishes has an empty part, so without a
	// fixture that does, the branch that drops one has no golden at all.
	whatAndNextSample = Failure{
		What: "A failure that never worked out its middle paragraph.",
		Next: "The empty part is dropped rather than printed as a blank line, and\n" +
			"this golden is what says so.",
	}
)

// TestFailureGoldens pins the RENDERING byte for byte: what separates
// the parts, what happens to a part that is empty, and where the output
// ends. A change to any of that is then a visible diff in review rather
// than a layout that quietly drifts.
//
// IT IS ABOUT THE LAYOUT AND NOT ABOUT ANY PARTICULAR WORDS, which is
// the change here. These rows used to be driven by the two published
// examples, and they were never assertions about that copy — a golden
// cannot tell a good sentence from a bad one, only a changed one. The
// copy has gone to the checks that own its conditions, and the promise
// it carries with it — that this program and the build agent tell a user
// the same story about the same condition — is asserted there, beside
// the words it is about, rather than here beside a renderer.
//
// Both golden files were TYPED, not generated from this package's
// output. A golden captured from the code it checks asserts only that
// the code is unchanged; one written by hand asserts that the code
// produces what somebody meant.
func TestFailureGoldens(t *testing.T) {
	cases := []struct {
		name    string
		failure *Failure
		golden  string
	}{
		{"all three parts", &threePartSample, "three-part-failure.golden"},
		{"an empty middle part", &whatAndNextSample, "what-and-next-only.golden"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _, _ := testUI("", false, nil)
			want := readGolden(t, tc.golden)

			// REQUIRED MUTATION: change any word of either fixture
			// above, or make renderFailure join the parts with a single
			// newline instead of a blank line.
			if got := u.renderFailure(tc.failure); got != want {
				t.Errorf("rendering does not match %s\n--- got ---\n%s\n--- want ---\n%s",
					tc.golden, got, want)
			}
		})
	}
}

// TestGoldenComparisonCanFail is the positive control for the test
// above. An assertion that two things match is worth nothing until
// something proves it can tell them apart — and a golden test is the
// classic place for that to go unnoticed, because a comparison against a
// file that was never going to differ passes for the wrong reason
// forever.
func TestGoldenComparisonCanFail(t *testing.T) {
	u, _, _ := testUI("", false, nil)

	reworded := threePartSample
	reworded.Why = strings.Replace(reworded.Why, "unremarkable", "ordinary", 1)

	if u.renderFailure(&reworded) == readGolden(t, "three-part-failure.golden") {
		t.Error("a reworded failure still matched the golden file — the comparison " +
			"in TestFailureGoldens cannot detect a change and proves nothing")
	}
}

// TestEveryPublishedFailureNamesAnAction holds the rule that makes these
// three fields worth having: a hard stop that does not name something to
// do is a fact, not a message. The published examples are checked as a
// SET rather than one representative, because a list is one fact with
// several parts and testing one part is not sampling the list.
func TestEveryPublishedFailureNamesAnAction(t *testing.T) {
	published := map[string]*Failure{
		"notInteractiveFailure": notInteractiveFailure,
		"noAnswerFailure":       noAnswerFailure,
		"serverClosedFailure":   serverClosedFailure,
	}
	for name, f := range published {
		if strings.TrimSpace(f.What) == "" {
			t.Errorf("%s has no What — the reader is not told what happened", name)
		}
		if strings.TrimSpace(f.Why) == "" {
			t.Errorf("%s has no Why — the reader is not told why", name)
		}
		// REQUIRED MUTATION: blank the Next field of any of the three.
		if strings.TrimSpace(f.Next) == "" {
			t.Errorf("%s has no Next — it stops the run without naming an action, "+
				"which is the one part of this shape that is the product", name)
		}
	}
}

// developerText matches the shapes this package's copy must never
// contain: a Go type or package-qualified name, a source position, a
// pointer address, or a goroutine dump.
var developerText = regexp.MustCompile(
	`\*?\b[a-z][a-z0-9]*\.[A-Z][A-Za-z0-9]*|\.go:[0-9]+|0x[0-9a-f]{4,}|goroutine [0-9]+`)

// TestCopyIsWrittenForAPerson enforces the rule that a hard stop reads
// as a message rather than as a diagnostic: no stack traces, no Go type
// names, and no sentence beginning "failed to".
//
// The instrument is checked in the same test, immediately below the
// assertion it serves. A scan for absence that has never been shown to
// find anything is indistinguishable from a scan that matches nothing at
// all, and the second one passes forever.
func TestCopyIsWrittenForAPerson(t *testing.T) {
	u, _, errOut := testUI("", false, nil)
	u.Fail(notInteractiveFailure)
	u.Fail(noAnswerFailure)
	u.Fail(serverClosedFailure)
	u.Internal(errors.New("an internal problem nobody anticipated"))
	u.Cancelled()

	shipped := errOut.String()

	if m := developerText.FindString(shipped); m != "" {
		t.Errorf("this package's copy contains developer text (%q):\n%s", m, shipped)
	}
	for _, line := range strings.Split(shipped, "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "failed to") {
			t.Errorf("a line begins \"failed to\": %q — that is a report about the "+
				"program, not a message to the person reading it", line)
		}
	}

	// The positive controls. Both run against fabricated input so the
	// two checks above are known to be capable of firing.
	for _, sample := range []string{
		"*ui.Failure",
		"prompt.go:41",
		"0xc000123456",
		"goroutine 17 [running]",
	} {
		if !developerText.MatchString(sample) {
			t.Errorf("the developer-text scan does not match %q, so its silence above "+
				"is not evidence of anything", sample)
		}
	}
	if !strings.HasPrefix(strings.ToLower("Failed to open the thing"), "failed to") {
		t.Error("the failed-to check cannot recognise its own subject")
	}
}

// TestInternalHidesDetailUntilAsked covers the one switch that adds
// detail. Both directions are asserted in one test on purpose: "the
// detail is hidden" and "the detail is never rendered at all" look
// identical from the first half alone.
func TestInternalHidesDetailUntilAsked(t *testing.T) {
	const detail = "dial the moon: no such moon"

	t.Run("hidden by default", func(t *testing.T) {
		u, _, errOut := testUI("", false, nil)
		u.Internal(errors.New(detail))

		// REQUIRED MUTATION: drop the `if u.debug` condition in
		// Internal so the detail is always rendered.
		if strings.Contains(errOut.String(), detail) {
			t.Errorf("the raw error reached the terminal without being asked for:\n%s",
				errOut.String())
		}
		if !strings.Contains(errOut.String(), debugEnvVar) {
			t.Errorf("the message does not name the switch that would show the "+
				"detail:\n%s", errOut.String())
		}
	})

	t.Run("shown when the switch is set", func(t *testing.T) {
		u, _, errOut := testUI("", false, map[string]string{debugEnvVar: "1"})
		u.Internal(errors.New(detail))

		// REQUIRED MUTATION: ignore lookupEnv in newUI and leave debug
		// false. This row reds while the row above stays green, which is
		// what makes the pair a measurement rather than a coincidence.
		if !strings.Contains(errOut.String(), detail) {
			t.Errorf("the detail was withheld even with %s set:\n%s",
				debugEnvVar, errOut.String())
		}
	})

	t.Run("a nil error does not render an empty paragraph", func(t *testing.T) {
		u, _, errOut := testUI("", false, map[string]string{debugEnvVar: "1"})
		u.Internal(nil)
		if strings.Contains(errOut.String(), "\n\n\n") {
			t.Errorf("a nil error rendered as a blank paragraph:\n%q", errOut.String())
		}
	})
}

// TestInternalRedactsASecretCarriedByAnError is the crossing point of
// two rules: the detail switch prints an error's own text, and a token
// must never reach a terminal. An error built with a Secret in it stays
// redacted on the way through — which is the property that lets the
// switch exist at all.
func TestInternalRedactsASecretCarriedByAnError(t *testing.T) {
	const token = "secret-abc123"
	u, _, errOut := testUI("", false, map[string]string{debugEnvVar: "1"})

	u.Internal(fmt.Errorf("the upload was refused, using %v", Secret(token)))

	if strings.Contains(errOut.String(), token) {
		t.Errorf("a Secret carried inside an error reached the terminal:\n%s", errOut.String())
	}
	// The positive control: the rest of the error's text must be there,
	// or "the token is absent" would also be true of a renderer that
	// printed nothing.
	if !strings.Contains(errOut.String(), "the upload was refused") {
		t.Errorf("the error's own message did not reach the terminal at all:\n%s",
			errOut.String())
	}
	if !strings.Contains(errOut.String(), redactedPlaceholder) {
		t.Errorf("the placeholder is missing, so the value was dropped rather than "+
			"redacted:\n%s", errOut.String())
	}
}

// TestExitCode covers the mapping from a failure to what the process
// reports. It is one function so the answer cannot differ between
// commands, and the cancellation row is the one that matters most: a
// user who pressed Ctrl-D asked for the run to stop, and a program that
// reports a fault for that is disagreeing with them about whose decision
// it was.
func TestExitCode(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantCode    int
		wantOnErr   string
		wantAbsent  string
		wantNoTrace bool
	}{
		{
			name:     "no error prints nothing and exits 0",
			err:      nil,
			wantCode: 0,
		},
		{
			// REQUIRED MUTATION: return 1 from the ErrAborted branch.
			name:        "a cancellation is not a fault",
			err:         ErrAborted,
			wantCode:    0,
			wantOnErr:   "cancelled",
			wantNoTrace: true,
		},
		{
			name:      "a wrapped cancellation is still a cancellation",
			err:       fmt.Errorf("asking for the code: %w", ErrAborted),
			wantCode:  0,
			wantOnErr: "cancelled",
		},
		{
			name:      "no terminal renders the terminal message",
			err:       ErrNotInteractive,
			wantCode:  1,
			wantOnErr: "needs a terminal",
		},
		{
			// THE ROW THAT WAS MISSING, and its absence is the whole
			// finding. Every other sentinel this package exports had a
			// row here; this one did not, and it was also the only one
			// ExitCode did not map — so four unusable answers were
			// rendered as "a fault in curious … nothing here for you to
			// fix", with an invitation to file a bug report about their
			// own typing.
			//
			// REQUIRED MUTATION: delete the ErrNoAnswer branch from
			// ExitCode. This row reds on both halves — the copy that
			// should be there is missing, and the internal-fault copy
			// that should not be there appears.
			name:       "running out of answers is the user's business, not a fault",
			err:        ErrNoAnswer,
			wantCode:   1,
			wantOnErr:  "Didn't catch that.",
			wantAbsent: internalWhat,
		},
		{
			name:       "a wrapped no-answer is still not a fault",
			err:        fmt.Errorf("confirming the deploy: %w", ErrNoAnswer),
			wantCode:   1,
			wantOnErr:  "Didn't catch that.",
			wantAbsent: internalWhat,
		},
		{
			// A Failure built by a caller rather than published here.
			// Before the receiver became a pointer, `&ui.Failure{...}`
			// compiled, satisfied error, and then did NOT match the
			// errors.As target — so the caller's copy was silently
			// replaced by the internal-fault copy. There is now one
			// spelling and the compiler enforces it; this row is the
			// floor under that.
			name:       "a Failure a caller constructed renders its own copy",
			err:        NewFailure("Something specific went wrong.", "Because of this.", "Do that."),
			wantCode:   1,
			wantOnErr:  "Something specific went wrong.",
			wantAbsent: internalWhat,
		},
		{
			// REQUIRED MUTATION: delete the errors.As branch, so a
			// Failure falls through to Internal. This row reds on the
			// missing copy, and the row below reds too.
			name:       "a Failure renders its own copy",
			err:        &threePartSample,
			wantCode:   1,
			wantOnErr:  threePartSample.What,
			wantAbsent: internalWhat,
		},
		{
			name:      "a wrapped Failure renders its own copy",
			err:       fmt.Errorf("checking the project: %w", &threePartSample),
			wantCode:  1,
			wantOnErr: threePartSample.What,
		},
		{
			// THE THIRD EXIT CODE, and the whole of what it means is in
			// this package's doc comment: the server is closed to you
			// right now. A bare number would be something every later
			// surface has to guess at; a number with a sentence beside
			// it is one they can route to without deciding again.
			//
			// REQUIRED MUTATION: return 1 from the ErrServerClosed
			// branch in ExitCode. This row reds on the code.
			name:       "a closed door costs its own code",
			err:        ErrServerClosed,
			wantCode:   ExitServerClosed,
			wantOnErr:  "isn't taking this right now",
			wantAbsent: internalWhat,
		},
		{
			// The sentinel decides the COST; the wrapped error decides
			// what is SAID. A caller that knows why the door is shut,
			// and when it reopens, keeps its own words.
			//
			// REQUIRED MUTATION: in ExitCode's ErrServerClosed branch,
			// render serverClosedFailure unconditionally instead of the
			// caller's copy. This row reds on the missing copy.
			name:       "a marked Failure keeps its own copy and still costs the code",
			err:        ServerClosed(NewFailure("We're full for today.", "Because of this.", "Come back at 12:15.")),
			wantCode:   ExitServerClosed,
			wantOnErr:  "Come back at 12:15.",
			wantAbsent: internalWhat,
		},
		{
			// The mark survives the ordinary wrapping a call stack does
			// to an error on its way up, which is the only reason
			// errors.Is is the right question to ask about it.
			name:      "a wrapped closed door is still a closed door",
			err:       fmt.Errorf("verifying the code: %w", ServerClosed(NewFailure("Full.", "Why.", "Next."))),
			wantCode:  ExitServerClosed,
			wantOnErr: "Full.",
		},
		{
			// THE NEGATIVE CONTROL FOR THE SCOPE. An ordinary Failure
			// costs 1, so "everything that stops costs 3" and "a closed
			// door costs 3" are distinguishable — without this row they
			// are not, and the scope would be described rather than
			// asserted.
			name:       "a stop that is not a closed door costs the ordinary code",
			err:        NewFailure("Too many requests from here.", "Because of this.", "Try later."),
			wantCode:   1,
			wantAbsent: internalWhat,
			wantOnErr:  "Too many requests from here.",
		},
		{
			name:      "anything else is an internal fault",
			err:       errors.New("something nobody anticipated"),
			wantCode:  1,
			wantOnErr: internalWhat,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, out, errOut := testUI("", false, nil)

			if got := u.ExitCode(tc.err); got != tc.wantCode {
				t.Errorf("ExitCode = %d, want %d", got, tc.wantCode)
			}
			if tc.wantOnErr != "" && !strings.Contains(errOut.String(), tc.wantOnErr) {
				t.Errorf("stderr %q does not contain %q", errOut.String(), tc.wantOnErr)
			}
			if tc.wantOnErr == "" && errOut.Len() != 0 {
				t.Errorf("stderr should have been empty, got %q", errOut.String())
			}
			if tc.wantAbsent != "" && strings.Contains(errOut.String(), tc.wantAbsent) {
				t.Errorf("stderr %q contains %q, which belongs to a different failure "+
					"class", errOut.String(), tc.wantAbsent)
			}
			if tc.wantNoTrace && developerText.MatchString(errOut.String()) {
				t.Errorf("a cancellation produced developer text: %q", errOut.String())
			}
			if out.Len() != 0 {
				t.Errorf("a failure reached stdout: %q", out.String())
			}
		})
	}
}

// TestTheClosedDoorCodeIsThree pins the NUMBER, and it is a separate row
// because every other assertion about it names the constant on both
// sides — so they all keep passing with the constant set to anything at
// all, 1 included. Measured: changing the constant to 1 moved no row in
// either package until this one existed.
//
// The value is contract in the way a status code is contract. It is read
// by things that are not reading messages — a wrapper script, a CI step,
// an agent deciding whether to wait — and none of them can be updated
// when it changes, which is the whole reason it is worth pinning rather
// than deriving.
//
// Three, and not 2: a shell reserves 2 for a usage error, and this
// program already exits 2 for one.
//
// REQUIRED MUTATION, RUN: set ExitServerClosed to any other value. This
// row reds alone, which is also the measurement that nothing else was
// watching the number.
func TestTheClosedDoorCodeIsThree(t *testing.T) {
	if ExitServerClosed != 3 {
		t.Errorf("ExitServerClosed = %d, want 3", ExitServerClosed)
	}
	// And distinct from the two costs that already exist, since a code
	// that collides with one of them conveys nothing.
	for name, other := range map[string]int{"success or cancellation": 0, "an ordinary failure": 1} {
		if ExitServerClosed == other {
			t.Errorf("ExitServerClosed is %d, which is also what %s costs — a "+
				"caller cannot act on a code it cannot tell apart", other, name)
		}
	}
}

// TestEverySentinelIsMapped is the row that would have caught the
// no-answer defect without anybody thinking of no answers, and it is
// here because of how that defect was found.
//
// A reviewer noticed the tell before the behaviour: this package exports
// three sentinels, ExitCode had branches for two, and the test table
// above had rows for the same two. THE ONLY UNMAPPED SENTINEL WAS THE
// ONLY ONE MISSING FROM THE TABLE — the gap in the code and the gap in
// its tests had the same shape, because both were written by asking
// "what can go wrong" and answering from the same imagination.
//
// So this asserts the SET rather than its members. A fourth sentinel
// added tomorrow reds here on the day it is added, whether or not
// anybody remembers to write a row for it, and it reds for the right
// reason: not "you forgot a test" but "a person can reach a message that
// blames this program for something they did".
//
// The sentinels are listed here rather than discovered by reflection —
// Go gives no way to enumerate a package's exported vars from inside it —
// so this list is itself something that can go stale. That is why the
// assertion is on the OUTCOME (does the sentinel render as an internal
// fault?) rather than on the branch: a sentinel added to this list
// without a branch reds immediately, which is the direction that matters.
//
// REQUIRED MUTATION: delete any one of ExitCode's sentinel branches. The
// corresponding subtest reds with the internal-fault copy in its output.
func TestEverySentinelIsMapped(t *testing.T) {
	sentinels := map[string]error{
		"ErrNotInteractive": ErrNotInteractive,
		"ErrAborted":        ErrAborted,
		"ErrNoAnswer":       ErrNoAnswer,
		"ErrServerClosed":   ErrServerClosed,
	}

	for name, sentinel := range sentinels {
		t.Run(name, func(t *testing.T) {
			u, _, errOut := testUI("", false, nil)
			u.ExitCode(sentinel)

			if strings.Contains(errOut.String(), internalWhat) {
				t.Errorf("%s renders as a fault in curious: %q\n"+
					"Every sentinel this package exports is a thing that HAPPENED, not a "+
					"thing that broke. Falling through to the internal-fault copy tells a "+
					"person their own action was a bug in the program and asks them to "+
					"report it.", name, errOut.String())
			}
			if errOut.Len() == 0 {
				t.Errorf("%s renders nothing at all, so the check above cannot observe "+
					"whether it renders the WRONG thing", name)
			}
		})
	}
}

// The pointer receiver's guarantee is a COMPILE-TIME one, and this is
// where that is written down, because no mutation can show it.
//
// With a value receiver both Failure and *Failure satisfied error, so
// `return &ui.Failure{...}` compiled and then silently failed to match
// the errors.As target — a caller's own copy replaced by the
// internal-fault copy, with nothing reporting the substitution. Making
// the receiver a pointer means there is one spelling and the compiler
// rejects the other.
//
// A test cannot assert that something does not compile, so the assertion
// below is the half that can be made — *Failure is an error — and the
// half that cannot is recorded here rather than left to be rediscovered:
//
//	var _ error = Failure{}   // does not compile, and that is the point
//
// Reverting the receiver to a value leaves every row in this file green.
// That is not a gap in the rows; it is the guarantee living somewhere
// tests do not reach.
var _ error = (*Failure)(nil)
