package flow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		hardStop(check.IDAstroDep, "This doesn't look like an Astro project."),
		hardStop(check.IDLockfile, "No lockfile found."),
		warning(check.IDLocalhost, "A development URL is hard-coded."),
	}

	packed, err := gatedDeploy(r, findings, fullManifest(check.IDAstroDep,
		check.IDLockfile, check.IDLocalhost), 0)

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
		{"one", []check.Finding{hardStop(check.IDAstroDep, "a")}, "1 thing"},
		{"two", []check.Finding{
			hardStop(check.IDAstroDep, "a"),
			hardStop(check.IDLockfile, "b"),
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
		warning(check.IDLocalhost, "src/pages/index.astro has a development URL."),
		warning(check.IDLocalhost, "src/lib/api.ts has a development URL."),
		warning(check.IDBuildFormat, "build.format will not produce routable URLs."),
	}

	packed, err := gatedDeploy(r, findings, fullManifest(check.IDLocalhost,
		check.IDBuildFormat), 0)

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
		warning(check.IDLocalhost, "A development URL is hard-coded."),
	}, fullManifest(check.IDLocalhost), 0)

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
		warning(check.IDLocalhost, "A development URL is hard-coded."),
	}, fullManifest(check.IDLocalhost), 0)

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

	packed, err := gatedDeploy(r, nil, fullManifest(check.IDAstroDep,
		check.IDLockfile, check.IDPagesDir, check.IDBuildFormat,
		check.IDLocalhost), 0)

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
		{CheckID: check.IDAstroDep, Ran: true},
		{CheckID: check.IDLockfile, Ran: false, Reason: "couldn't read package.json"},
		{CheckID: check.IDPagesDir, Ran: true},
	}

	err := RenderPreflight(r, []check.Finding{
		warning(check.IDPagesDir, "Couldn't find src/pages."),
	}, manifest, 0)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}

	out := r.out.String()
	for _, want := range []string{check.IDLockfile, "couldn't read package.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, check.IDAstroDep) {
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
		CheckID:  check.IDBuildFormat,
		Severity: check.SeverityNote,
		Message:  "couldn't confirm build.format from this config",
	}}, fullManifest(check.IDBuildFormat), 0)

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
		CheckID:  check.IDLocalhost,
		Severity: check.SeverityWarning,
		Message:  "A development URL is hard-coded.",
		Paths:    []string{"src/pages/index.astro", "src/lib/api.ts"},
	}}, fullManifest(check.IDLocalhost), 0)
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
			if err := RenderPreflight(r, nil, fullManifest(check.IDAstroDep), tc.elapsed); err != nil {
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
		warning(check.IDLocalhost, "A development URL is hard-coded."),
		warning(check.IDBuildFormat, "build.format will not produce routable URLs."),
		{CheckID: check.IDPagesDir, Severity: check.SeverityNote, Message: "unresolved"},
	}
	manifest := check.Manifest{
		{CheckID: check.IDAstroDep, Ran: true},
		{CheckID: check.IDLockfile, Ran: false, Reason: "couldn't read package.json"},
		{CheckID: check.IDPagesDir, Ran: false, Reason: "couldn't read astro.config"},
		{CheckID: check.IDLocalhost, Ran: true},
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
		{"warning accepted", []check.Finding{warning(check.IDLocalhost, "w")}, true, nil, 0},
		{"warning declined", []check.Finding{warning(check.IDLocalhost, "w")}, false, nil, 0},
		{"hard stop", []check.Finding{hardStop(check.IDAstroDep, "h")}, true, nil, 1},
		{"no terminal", []check.Finding{warning(check.IDLocalhost, "w")}, false,
			ui.ErrNotInteractive, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{answer: tc.answer, answerTo: tc.answerTo}
			err := RenderPreflight(r, tc.findings, fullManifest(check.IDLocalhost), 0)
			if code := exitCodeFor(t, err); code != tc.want {
				t.Errorf("exit code = %d, want %d (error %#v)", code, tc.want, err)
			}
		})
	}
}

// ---------------------------------------------------------------------
// A lone hard finding renders its own copy
// ---------------------------------------------------------------------

// readGolden reads a golden file and refuses an empty one, because a
// golden test against nothing passes against nothing.
func readGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading the golden file: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("%s is empty", name)
	}
	return string(data)
}

// renderedBytes puts an error through the program's REAL rendering and
// returns exactly what a person would see, plus the exit code.
//
// Through the real one, deliberately. A helper here that reassembled the
// three parts itself would agree with itself by construction and would
// go on agreeing after the terminal package changed how a failure is
// laid out. Stderr is redirected to a file rather than captured, because
// that type writes to the streams it was built with and this is the only
// portable way to hand it one a test can read back.
func renderedBytes(t *testing.T, err error) (string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr")
	sink, openErr := os.Create(path)
	if openErr != nil {
		t.Fatalf("creating the capture file: %v", openErr)
	}

	realErr, realOut := os.Stderr, os.Stdout
	os.Stderr, os.Stdout = sink, sink
	code := ui.New().ExitCode(err)
	os.Stderr, os.Stdout = realErr, realOut

	if closeErr := sink.Close(); closeErr != nil {
		t.Fatalf("closing the capture file: %v", closeErr)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("reading the capture file: %v", readErr)
	}
	return string(data), code
}

// The lockfile copy, as a check that owns the condition would carry it.
// The words are the ones the terminal package already holds as a worked
// example — the point of this round is that a finding can now DELIVER
// them, which nothing could before.
func lockfileFinding() check.Finding {
	return check.Finding{
		CheckID:  check.IDLockfile,
		Severity: check.SeverityHardStop,
		Message:  "no lockfile found",
		What:     "No lockfile found.",
		Why: "curious installs your dependencies from a lockfile, so it builds the\n" +
			"exact versions you tested. Your project has package.json but no\n" +
			"package-lock.json, npm-shrinkwrap.json or pnpm-lock.yaml.",
		Next: "Run `npm install` (or `pnpm install`), commit the lockfile it\n" +
			"creates, and try again.",
	}
}

// TestPreflightRenderLoneHardFindingRendersItsOwnCopy is the case the
// synthesis was getting wrong, and it is the commonest one. A person
// with exactly one problem should read the sentence the check's author
// wrote about that problem — not that sentence wrapped in a summary
// explaining that there is 1 thing to fix.
//
// GOLDEN, and the golden was TYPED from the copy rather than captured
// from this code. One captured from the output asserts only that the
// output is unchanged; one written from the copy as authored asserts
// that the code says what the copy says.
//
// REQUIRED MUTATION: make the lone-finding path synthesise anyway. This
// row reds; the multiples row does not move.
func TestPreflightRenderLoneHardFindingRendersItsOwnCopy(t *testing.T) {
	r := &recorder{}

	err := RenderPreflight(r, []check.Finding{lockfileFinding()},
		fullManifest(check.IDLockfile), 0)

	got, code := renderedBytes(t, err)
	if want := readGolden(t, "lone-hard-finding-with-copy.golden"); got != want {
		t.Errorf("rendering:\n%s\nwant:\n%s", got, want)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %v, want none", r.prompts)
	}
}

// TestPreflightRenderLoneHardFindingWithoutCopyIsSynthesised. Most
// checks will not have worked three paragraphs out, and the summary has
// to carry them — so the synthesis stays for exactly that case rather
// than being replaced by it.
func TestPreflightRenderLoneHardFindingWithoutCopyIsSynthesised(t *testing.T) {
	r := &recorder{}

	err := RenderPreflight(r, []check.Finding{
		hardStop(check.IDAstroDep, "This doesn't look like an Astro project."),
	}, fullManifest(check.IDAstroDep), 0)

	got, code := renderedBytes(t, err)
	if want := readGolden(t, "lone-hard-finding-without-copy.golden"); got != want {
		t.Errorf("rendering:\n%s\nwant:\n%s", got, want)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

// TestPreflightRenderTwoHardFindingsAlwaysSynthesise, whether or not
// either carries copy. A renderer that quietly preferred a carried copy
// would show one problem to somebody who has two, and the second would
// be discovered only after the first was fixed — the round-trip the
// whole engine exists to prevent.
//
// BOTH ORDERS ARE RUN, and the second case is here because a mutation
// went green without it. The row originally put the copy-carrying
// finding second, so "prefer the first finding's copy" changed nothing
// and passed — the row could not catch the thing it was written to
// catch, and its own comment said otherwise. Position is exactly what
// such a preference keys on, so position is what the table varies.
//
// Each case asserts the joined summaries as bytes, so a preference reds
// rather than merely looking different.
func TestPreflightRenderTwoHardFindingsAlwaysSynthesise(t *testing.T) {
	withCopy := lockfileFinding()
	withCopy.Message = "No lockfile found."
	bare := hardStop(check.IDAstroDep, "This doesn't look like an Astro project.")

	cases := []struct {
		name     string
		findings []check.Finding
		golden   string
	}{
		{"copy second", []check.Finding{bare, withCopy}, "two-hard-findings.golden"},
		{"copy first", []check.Finding{withCopy, bare}, "two-hard-findings-copy-first.golden"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{}
			err := RenderPreflight(r, tc.findings,
				fullManifest(check.IDAstroDep, check.IDLockfile), 0)

			got, code := renderedBytes(t, err)
			if want := readGolden(t, tc.golden); got != want {
				t.Errorf("rendering:\n%s\nwant:\n%s", got, want)
			}
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			for _, summary := range []string{
				"This doesn't look like an Astro project.", "No lockfile found.",
			} {
				if !strings.Contains(got, summary) {
					t.Errorf("rendering does not carry the summary %q:\n%s", summary, got)
				}
			}
			if strings.Contains(got, "curious installs your dependencies") {
				t.Errorf("a finding's own copy was preferred over the summary of both:\n%s", got)
			}
		})
	}
}

// TestPreflightRenderFillsAMissingHeadlineFromTheSummary. Copy is
// optional part by part, and a check that wrote only a reason has still
// worked its copy out — so the headline falls back to the required
// one-line summary rather than the whole message falling back to
// synthesis and throwing that reason away.
//
// MUTATION: leave What empty rather than filling it. The golden reds on
// the missing headline.
func TestPreflightRenderFillsAMissingHeadlineFromTheSummary(t *testing.T) {
	r := &recorder{}

	err := RenderPreflight(r, []check.Finding{{
		CheckID:  check.IDLockfile,
		Severity: check.SeverityHardStop,
		Message:  "No lockfile found.",
		Why: "curious installs your dependencies from a lockfile, so it builds the\n" +
			"exact versions you tested.",
	}}, fullManifest(check.IDLockfile), 0)

	got, _ := renderedBytes(t, err)
	if want := readGolden(t, "lone-hard-finding-partial-copy.golden"); got != want {
		t.Errorf("rendering:\n%s\nwant:\n%s", got, want)
	}
}

// TestPreflightRenderCopyIsVerbatimAndDoesNotGrowPaths pins a real
// asymmetry rather than papering over it. A finding WITHOUT copy has its
// paths listed under the summary, because nothing else would name them.
// A finding WITH copy is rendered exactly as its author wrote it — the
// author had the paths and chose what to say about them, and a renderer
// appending a list underneath would be editing somebody's prose.
func TestPreflightRenderCopyIsVerbatimAndDoesNotGrowPaths(t *testing.T) {
	withCopy := lockfileFinding()
	withCopy.Paths = []string{"package.json"}

	err := RenderPreflight(&recorder{}, []check.Finding{withCopy},
		fullManifest(check.IDLockfile), 0)

	got, _ := renderedBytes(t, err)
	if want := readGolden(t, "lone-hard-finding-with-copy.golden"); got != want {
		t.Errorf("copy was not rendered verbatim:\n%s\nwant:\n%s", got, want)
	}

	bare := check.Finding{
		CheckID:  check.IDLockfile,
		Severity: check.SeverityHardStop,
		Message:  "No lockfile found.",
		Paths:    []string{"package.json"},
	}
	synthesised, _ := renderedBytes(t, RenderPreflight(&recorder{},
		[]check.Finding{bare}, fullManifest(check.IDLockfile), 0))
	if !strings.Contains(synthesised, "package.json") {
		t.Errorf("a finding with no copy of its own lost the file it is about:\n%s", synthesised)
	}
}
