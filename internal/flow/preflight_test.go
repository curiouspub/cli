package flow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/ui"
)

// recorder stands in for the terminal. It writes narration to a buffer,
// so a row can compare BYTES rather than a reconstruction of them, and
// counts prompts, which is the only way to state "one question covering
// all the warnings" as an assertion.
type recorder struct {
	out      bytes.Buffer
	prompts  []string
	answer   bool
	answerTo error
}

func (r *recorder) Step(format string, args ...any) {
	fmt.Fprintf(&r.out, format+"\n", args...)
}

func (r *recorder) Confirm(question string, defaultYes bool) (bool, error) {
	r.prompts = append(r.prompts, question)
	if r.answerTo != nil {
		return false, r.answerTo
	}
	return r.answer, nil
}

// The real terminal type must satisfy the seam this package asks for.
// Without this the interface could drift into something only the test
// double implements, and the drift would not show up until the deploy
// sequence tried to pass a real one.
var _ Prompter = (*ui.UI)(nil)

// gatedDeploy is what a caller does with the renderer's answer: pack
// only if pre-flight said it was safe to. The sequence itself belongs to
// a later change; this is the smallest honest stand-in for it, and it
// exists so "the packer is not invoked" is a fact about a caller rather
// than a claim about one.
func gatedDeploy(p Prompter, findings []check.Finding, manifest check.Manifest, elapsed time.Duration) (packed bool, err error) {
	if err := RenderPreflight(p, findings, manifest, elapsed); err != nil {
		return false, err
	}
	return true, nil
}

// exitCodeFor puts an error through the program's real exit-code
// mapping. The UI is built against the null device because ExitCode
// PRINTS as it decides, and this row is about the number rather than the
// copy — the copy has its own golden files in the package that owns it.
func exitCodeFor(t *testing.T, err error) int {
	t.Helper()
	devnull, openErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if openErr != nil {
		t.Fatalf("opening the null device: %v", openErr)
	}
	defer func() { _ = devnull.Close() }()

	realErr, realOut := os.Stderr, os.Stdout
	os.Stderr, os.Stdout = devnull, devnull
	defer func() { os.Stderr, os.Stdout = realErr, realOut }()

	return ui.New().ExitCode(err)
}

func hardStop(id, message string) check.Finding {
	return check.Finding{CheckID: id, Severity: check.SeverityHardStop, Message: message}
}

func warning(id, message string) check.Finding {
	return check.Finding{CheckID: id, Severity: check.SeverityWarning, Message: message}
}

func fullManifest(ids ...string) check.Manifest {
	var m check.Manifest
	for _, id := range ids {
		m = append(m, check.Ran{CheckID: id, Ran: true})
	}
	return m
}

// ---------------------------------------------------------------------
// Hard stops
// ---------------------------------------------------------------------

// TestPreflightRenderReportsEveryHardStopAndNeverPrompts is the pair of
// rules that make a hard stop different from a warning. Every hard stop
// is reported in one run, and NO prompt is rendered at all — there is
// nothing to decide, and asking a question whose only answer is "no" is
// how people learn to press Enter without reading.
//
// The warning is in the fixture deliberately: a hard stop present
// silences the warning prompt too.
//
// MUTATION: prompt whenever any warning exists. The prompt count reds.
func TestPreflightRenderReportsEveryHardStopAndNeverPrompts(t *testing.T) {
	r := &recorder{answer: true}
	findings := []check.Finding{
		hardStop(check.CheckIDAstroDep, "This doesn't look like an Astro project."),
		hardStop(check.CheckIDLockfile, "No lockfile found."),
		warning(check.CheckIDLocalhost, "A development URL is hard-coded."),
	}

	packed, err := gatedDeploy(r, findings, fullManifest(check.CheckIDAstroDep,
		check.CheckIDLockfile, check.CheckIDLocalhost), 0)

	if packed {
		t.Error("the packer ran after a hard stop")
	}
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %v, want none — a hard stop leaves nothing to decide", r.prompts)
	}

	var failure *ui.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %#v, want a failure carrying the copy", err)
	}
	for _, want := range []string{"This doesn't look like an Astro project.", "No lockfile found."} {
		if !strings.Contains(failure.Why, want) {
			t.Errorf("the reported copy does not contain %q:\n%s", want, failure.Why)
		}
	}
	if failure.Next == "" {
		t.Error("the failure names no action; a hard stop that does not say what to do next " +
			"leaves a first-timer guessing")
	}
	if code := exitCodeFor(t, err); code == 0 {
		t.Errorf("exit code = %d, want non-zero", code)
	}
}

// TestPreflightRenderSaysHowManyProblems covers the singular and the
// plural, because "1 problems" in the first sentence a new user reads is
// the kind of detail that makes a tool look unfinished.
func TestPreflightRenderSaysHowManyProblems(t *testing.T) {
	cases := []struct {
		name     string
		findings []check.Finding
		want     string
	}{
		{"one", []check.Finding{hardStop(check.CheckIDAstroDep, "a")}, "1 thing"},
		{"two", []check.Finding{
			hardStop(check.CheckIDAstroDep, "a"),
			hardStop(check.CheckIDLockfile, "b"),
		}, "2 things"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{}
			err := RenderPreflight(r, tc.findings, nil, 0)

			var failure *ui.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %#v, want a failure", err)
			}
			if !strings.Contains(failure.Why, tc.want) {
				t.Errorf("copy = %q, want it to contain %q", failure.Why, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Warnings
// ---------------------------------------------------------------------

// TestPreflightRenderAsksOnceForEveryWarning is the count assertion, and
// the count is the whole rule. Three hard-coded development URLs in
// three files is ONE decision; three prompts is how people learn to hit
// Enter without reading, which turns the warning into noise and the
// broken deploy into the user's fault.
//
// MUTATION: prompt inside the loop over warnings. The count reds at 3.
func TestPreflightRenderAsksOnceForEveryWarning(t *testing.T) {
	r := &recorder{answer: true}
	findings := []check.Finding{
		warning(check.CheckIDLocalhost, "src/pages/index.astro has a development URL."),
		warning(check.CheckIDLocalhost, "src/lib/api.ts has a development URL."),
		warning(check.CheckIDBuildFormat, "build.format will not produce routable URLs."),
	}

	packed, err := gatedDeploy(r, findings, fullManifest(check.CheckIDLocalhost,
		check.CheckIDBuildFormat), 0)

	if err != nil {
		t.Fatalf("error = %v, want nil after the user agreed to continue", err)
	}
	if !packed {
		t.Error("the packer did not run after the user agreed to continue")
	}
	if len(r.prompts) != 1 {
		t.Fatalf("prompts = %d %v, want exactly one covering all three warnings",
			len(r.prompts), r.prompts)
	}
	if !strings.Contains(r.prompts[0], "Continue anyway?") {
		t.Errorf("prompt = %q, want it to ask whether to continue", r.prompts[0])
	}
	for _, want := range []string{"index.astro", "api.ts", "build.format"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("output does not mention %q — a question about warnings nobody was "+
				"shown is not a decision:\n%s", want, r.out.String())
		}
	}
}

// TestPreflightRenderDeclinedExitsZero: the user was asked, the user said
// no, the tool obeyed. That is the program working, and a non-zero code
// would make every wrapper script treat a deliberate decision as a fault.
//
// MUTATION: return nil when the answer is no. Both the packer row and
// the exit-code row red — and the packer row is the one that matters,
// because a nil error there means the deploy carries on.
func TestPreflightRenderDeclinedExitsZero(t *testing.T) {
	r := &recorder{answer: false}

	packed, err := gatedDeploy(r, []check.Finding{
		warning(check.CheckIDLocalhost, "A development URL is hard-coded."),
	}, fullManifest(check.CheckIDLocalhost), 0)

	if packed {
		t.Error("the packer ran after the user declined")
	}
	if !errors.Is(err, ui.ErrAborted) {
		t.Errorf("error = %#v, want the cancellation sentinel", err)
	}
	if code := exitCodeFor(t, err); code != 0 {
		t.Errorf("exit code = %d, want 0 — the user made a choice and the tool obeyed", code)
	}
}

// TestPreflightRenderNonInteractiveNeitherContinuesNorAborts. With
// warnings pending and nobody to ask, the honest answer is to say so.
// Auto-continuing publishes a site the user never approved; auto-aborting
// refuses a deploy nobody objected to. Both are the program deciding
// something it was not asked to decide.
//
// MUTATION: treat the missing terminal as agreement. The packer row reds.
func TestPreflightRenderNonInteractiveNeitherContinuesNorAborts(t *testing.T) {
	r := &recorder{answerTo: ui.ErrNotInteractive}

	packed, err := gatedDeploy(r, []check.Finding{
		warning(check.CheckIDLocalhost, "A development URL is hard-coded."),
	}, fullManifest(check.CheckIDLocalhost), 0)

	if packed {
		t.Error("the packer ran with warnings nobody could be asked about")
	}
	if !errors.Is(err, ui.ErrNotInteractive) {
		t.Errorf("error = %#v, want the no-terminal sentinel", err)
	}
	if code := exitCodeFor(t, err); code == 0 {
		t.Errorf("exit code = %d, want non-zero", code)
	}
}

// TestPreflightRenderCleanProjectAsksNothing. Nothing found, nothing
// said, nothing asked — the case that has to stay silent or every deploy
// grows a question.
func TestPreflightRenderCleanProjectAsksNothing(t *testing.T) {
	r := &recorder{}

	packed, err := gatedDeploy(r, nil, fullManifest(check.CheckIDAstroDep,
		check.CheckIDLockfile, check.CheckIDPagesDir, check.CheckIDBuildFormat,
		check.CheckIDLocalhost), 0)

	if err != nil || !packed {
		t.Fatalf("packed = %v, err = %v, want the deploy to carry on", packed, err)
	}
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %v, want none", r.prompts)
	}
	if r.out.Len() != 0 {
		t.Errorf("output = %q, want silence on a clean project", r.out.String())
	}
}

// ---------------------------------------------------------------------
// The manifest, and what it stops the renderer getting wrong
// ---------------------------------------------------------------------

// TestPreflightRenderNamesChecksThatDidNotRun. The renderer reads not-run
// from the MANIFEST and never from the absence of a finding — under a
// findings-only stream those two states are the same silence, and a
// skipped check rendered as a tick is a lie the user will act on.
//
// The fixture has findings AND a skipped check on purpose: a renderer
// that inferred "did not run" from an empty finding slice would pass
// every other row in this file and fail this one.
//
// MUTATION: derive the not-run list from len(findings) == 0. Reds here
// and nowhere else.
func TestPreflightRenderNamesChecksThatDidNotRun(t *testing.T) {
	r := &recorder{answer: true}
	manifest := check.Manifest{
		{CheckID: check.CheckIDAstroDep, Ran: true},
		{CheckID: check.CheckIDLockfile, Ran: false, Reason: "couldn't read package.json"},
		{CheckID: check.CheckIDPagesDir, Ran: true},
	}

	err := RenderPreflight(r, []check.Finding{
		warning(check.CheckIDPagesDir, "Couldn't find src/pages."),
	}, manifest, 0)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}

	out := r.out.String()
	for _, want := range []string{check.CheckIDLockfile, "couldn't read package.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, check.CheckIDAstroDep) {
		t.Errorf("output names a check that DID run, which turns the skipped list into "+
			"noise nobody reads:\n%s", out)
	}
}

// TestPreflightRenderKeepsNotesOutOfSight. A note is a fact a check
// established and deliberately did not raise; showing it makes every
// deploy noisier, and — worse — a note counted as a warning would prompt.
//
// MUTATION: render the raw findings slice instead of the advisories.
// Both halves red: the note's text appears, and a prompt is asked.
func TestPreflightRenderKeepsNotesOutOfSight(t *testing.T) {
	r := &recorder{answer: true}

	err := RenderPreflight(r, []check.Finding{{
		CheckID:  check.CheckIDBuildFormat,
		Severity: check.SeverityNote,
		Message:  "couldn't confirm build.format from this config",
	}}, fullManifest(check.CheckIDBuildFormat), 0)

	if err != nil {
		t.Fatalf("error = %v, want nil — a note is not a warning", err)
	}
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %v, want none — a note is not something to decide about", r.prompts)
	}
	if strings.Contains(r.out.String(), "couldn't confirm") {
		t.Errorf("the note was shown:\n%s", r.out.String())
	}
}

// TestPreflightRenderNamesTheFilesAFindingIsAbout. Paths exists so a
// path never has to be dug back out of prose; a renderer that ignores it
// puts the field's whole purpose back where it started.
//
// MUTATION: render Message alone. Reds on both file names.
func TestPreflightRenderNamesTheFilesAFindingIsAbout(t *testing.T) {
	r := &recorder{answer: true}

	err := RenderPreflight(r, []check.Finding{{
		CheckID:  check.CheckIDLocalhost,
		Severity: check.SeverityWarning,
		Message:  "A development URL is hard-coded.",
		Paths:    []string{"src/pages/index.astro", "src/lib/api.ts"},
	}}, fullManifest(check.CheckIDLocalhost), 0)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}

	for _, want := range []string{"src/pages/index.astro", "src/lib/api.ts"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("output does not name %q:\n%s", want, r.out.String())
		}
	}
}

// ---------------------------------------------------------------------
// Timing and determinism
// ---------------------------------------------------------------------

// TestPreflightRenderReportsDurationOnlyWhenSlow. A silent pause reads as
// a hang, and scanning a huge tree is the one plausible slow spot. Below
// the threshold the line is noise on every single deploy, which is why
// this is a threshold rather than an always.
//
// The duration ARRIVES AS AN ARGUMENT rather than being measured here,
// so the rows state what they mean instead of racing a clock.
//
// MUTATION: report unconditionally. The fast row reds.
func TestPreflightRenderReportsDurationOnlyWhenSlow(t *testing.T) {
	cases := []struct {
		name    string
		elapsed time.Duration
		reports bool
	}{
		{"instant", 3 * time.Millisecond, false},
		{"just under", 1900 * time.Millisecond, false},
		{"slow", 4200 * time.Millisecond, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{}
			if err := RenderPreflight(r, nil, fullManifest(check.CheckIDAstroDep), tc.elapsed); err != nil {
				t.Fatalf("error = %v", err)
			}
			mentioned := strings.Contains(r.out.String(), "Pre-flight took")
			if mentioned != tc.reports {
				t.Errorf("duration reported = %v, want %v (output %q)",
					mentioned, tc.reports, r.out.String())
			}
		})
	}
}

// TestPreflightRenderIsByteIdenticalAcrossRuns. Two runs over one
// project produce the same bytes, which is what makes the output
// something a person can diff and a test can pin.
func TestPreflightRenderIsByteIdenticalAcrossRuns(t *testing.T) {
	findings := []check.Finding{
		warning(check.CheckIDLocalhost, "A development URL is hard-coded."),
		warning(check.CheckIDBuildFormat, "build.format will not produce routable URLs."),
		{CheckID: check.CheckIDPagesDir, Severity: check.SeverityNote, Message: "unresolved"},
	}
	manifest := check.Manifest{
		{CheckID: check.CheckIDAstroDep, Ran: true},
		{CheckID: check.CheckIDLockfile, Ran: false, Reason: "couldn't read package.json"},
		{CheckID: check.CheckIDPagesDir, Ran: false, Reason: "couldn't read astro.config"},
		{CheckID: check.CheckIDLocalhost, Ran: true},
	}

	run := func() string {
		r := &recorder{answer: true}
		if err := RenderPreflight(r, findings, manifest, 5*time.Second); err != nil {
			t.Fatalf("error = %v", err)
		}
		return r.out.String()
	}

	first := run()
	for i := 0; i < 8; i++ {
		if again := run(); again != first {
			t.Fatalf("run %d differs:\n--- first ---\n%s\n--- again ---\n%s", i+2, first, again)
		}
	}
	if first == "" {
		t.Fatal("nothing was rendered, so the comparison proves nothing")
	}
}

// TestPreflightRenderExitCodes lines every outcome up against the code a
// script will branch on. Exit codes are contract for a public CLI, and
// the mapping is asserted through the program's REAL one rather than a
// restatement of it — a restatement agrees with itself by construction.
func TestPreflightRenderExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		findings []check.Finding
		answer   bool
		answerTo error
		want     int
	}{
		{"clean", nil, true, nil, 0},
		{"warning accepted", []check.Finding{warning(check.CheckIDLocalhost, "w")}, true, nil, 0},
		{"warning declined", []check.Finding{warning(check.CheckIDLocalhost, "w")}, false, nil, 0},
		{"hard stop", []check.Finding{hardStop(check.CheckIDAstroDep, "h")}, true, nil, 1},
		{"no terminal", []check.Finding{warning(check.CheckIDLocalhost, "w")}, false,
			ui.ErrNotInteractive, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{answer: tc.answer, answerTo: tc.answerTo}
			err := RenderPreflight(r, tc.findings, fullManifest(check.CheckIDLocalhost), 0)
			if code := exitCodeFor(t, err); code != tc.want {
				t.Errorf("exit code = %d, want %d (error %#v)", code, tc.want, err)
			}
		})
	}
}
