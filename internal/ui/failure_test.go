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

// TestFailureGoldens pins the two worked examples byte for byte, so a
// reword is a visible diff in review rather than an invisible change of
// tone. Copy drifts one considerate edit at a time, and nobody notices
// until the program sounds like several people.
//
// Both golden files were TYPED, not generated from this package's
// output. A golden captured from the code it checks asserts only that
// the code is unchanged; one written from the copy as authored asserts
// that the code says what the copy says.
//
// AUTHORITY FOR THE LOCKFILE MESSAGE. The build agent reaches this same
// condition from the other side and emits its own message when it does —
// and because it does that whether or not anybody ran this CLI, its
// authored text is the authority here. Its wording names three lockfile
// spellings it will install from: package-lock.json, npm-shrinkwrap.json
// and pnpm-lock.yaml. So does this.
//
// The obligation is that the two SAY THE SAME THING — same claim, same
// named cause, same instruction — and deliberately not that they are
// byte-identical: this renderer emits three paragraphs to a terminal and
// the agent emits one compact line into a log stream, so byte-identity
// would have this file arguing with a log line about where to wrap. What
// it forbids is the real failure, which is a user reading one story here
// and a different one from the build and concluding that the two halves
// of this product disagree about what happened. A project carrying only
// an npm-shrinkwrap.json builds perfectly well; a version of this
// message naming two spellings would tell its owner they have no
// lockfile.
func TestFailureGoldens(t *testing.T) {
	cases := []struct {
		name    string
		failure Failure
		golden  string
	}{
		{"no lockfile", NoLockfile, "no-lockfile.golden"},
		{"not an Astro project", NotAnAstroProject, "not-an-astro-project.golden"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _, _ := testUI("", false, nil)
			want := readGolden(t, tc.golden)

			// REQUIRED MUTATION: change any word of NoLockfile or
			// NotAnAstroProject in failure.go — dropping
			// "npm-shrinkwrap.json" from the lockfile list is the edit
			// this row exists for.
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

	reworded := NoLockfile
	reworded.Why = strings.Replace(reworded.Why, "npm-shrinkwrap.json or ", "", 1)

	if u.renderFailure(reworded) == readGolden(t, "no-lockfile.golden") {
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
	published := map[string]Failure{
		"NoLockfile":            NoLockfile,
		"NotAnAstroProject":     NotAnAstroProject,
		"notInteractiveFailure": notInteractiveFailure,
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
	u.Fail(NoLockfile)
	u.Fail(NotAnAstroProject)
	u.Fail(notInteractiveFailure)
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
			// REQUIRED MUTATION: delete the errors.As branch, so a
			// Failure falls through to Internal. This row reds on the
			// missing copy, and the row below reds too.
			name:       "a Failure renders its own copy",
			err:        NoLockfile,
			wantCode:   1,
			wantOnErr:  "No lockfile found.",
			wantAbsent: internalWhat,
		},
		{
			name:      "a wrapped Failure renders its own copy",
			err:       fmt.Errorf("checking the project: %w", NoLockfile),
			wantCode:  1,
			wantOnErr: "No lockfile found.",
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
