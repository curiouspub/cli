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
func gatedDeploy(p Prompter, report check.Report, elapsed time.Duration) (packed bool, err error) {
	if err := RenderPreflight(p, report, elapsed); err != nil {
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

	return ui.New(os.Stdin, os.Stdout, os.Stderr).ExitCode(err)
}

func hardStop(id, message string) check.Finding {
	return check.Finding{CheckID: id, Severity: check.SeverityHardStop, Message: message}
}

func warning(id, message string) check.Finding {
	return check.Finding{CheckID: id, Severity: check.SeverityWarning, Message: message}
}

// environmental and byDesign build the two kinds of decline. Callers
// build rows through these rather than by hand, because a hand-written
// row that forgets its status reads as a TICK — and a check reported as
// having answered when it did not is the lie this whole manifest exists
// to prevent.
func environmental(id, reason string) check.Status {
	return check.Status{CheckID: id, Outcome: check.Declined, Kind: check.Environmental, Reason: reason}
}

func byDesign(id, reason string) check.Status {
	return check.Status{CheckID: id, Outcome: check.Declined, Kind: check.ByDesign, Reason: reason}
}

// reportOf builds the validated article the renderer requires, THROUGH
// THE REAL GATE. Every row in this file therefore renders something a
// caller could actually have produced — a manifest covering the whole
// declared universe, findings under claimed ids, severities that exist.
//
// A helper that assembled a Report directly would be a second door, and
// the whole point of the type is that there is one.
func reportOf(t *testing.T, findings []check.Finding, notRun ...check.Status) check.Report {
	t.Helper()
	skipped := map[string]check.Status{}
	for _, row := range notRun {
		skipped[row.CheckID] = row
	}

	var m check.Manifest
	for _, id := range check.DeclaredOrder() {
		if row, ok := skipped[id]; ok {
			m = append(m, row)
			continue
		}
		m = append(m, check.Status{CheckID: id})
	}

	report, err := check.Combine(check.Results{Findings: findings, Manifest: m})
	if err != nil {
		t.Fatalf("building the report: %v", err)
	}
	return report
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

	packed, err := gatedDeploy(r, reportOf(t, findings), 0)

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
			err := RenderPreflight(r, reportOf(t, tc.findings), 0)

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

	packed, err := gatedDeploy(r, reportOf(t, findings), 0)

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

	packed, err := gatedDeploy(r, reportOf(t, []check.Finding{
		warning(check.IDLocalhost, "A development URL is hard-coded."),
	}), 0)

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

	packed, err := gatedDeploy(r, reportOf(t, []check.Finding{
		warning(check.IDLocalhost, "A development URL is hard-coded."),
	}), 0)

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

	packed, err := gatedDeploy(r, reportOf(t, nil), 0)

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
	report := reportOf(t, []check.Finding{
		warning(check.IDPagesDir, "Couldn't find src/pages."),
	}, environmental(check.IDLockfile, "couldn't read package.json"))

	err := RenderPreflight(r, report, 0)
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

	err := RenderPreflight(r, reportOf(t, []check.Finding{{
		CheckID:  check.IDBuildFormat,
		Severity: check.SeverityNote,
		Message:  "couldn't confirm build.format from this config",
	}}), 0)

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

	err := RenderPreflight(r, reportOf(t, []check.Finding{{
		CheckID:  check.IDLocalhost,
		Severity: check.SeverityWarning,
		Message:  "A development URL is hard-coded.",
		Paths:    []string{"src/pages/index.astro", "src/lib/api.ts"},
	}}), 0)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}

	for _, want := range []string{"src/pages/index.astro", "src/lib/api.ts"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("output does not name %q:\n%s", want, r.out.String())
		}
	}
}

// TestPreflightRenderMeasuresThePathsWhenTheFindingMeasuredThem is the
// sized branch of the same list.
//
// A check with measurements to report used to lay out its own table in
// its copy and ALSO set Paths, so the offenders arrived twice in the one
// hard stop a reader most needs — once measured, once bare. The facts now
// travel as facts and this is the single place that turns them into a
// column, which is also the only place that knows how wide the column
// should be.
//
// It formats through the same byte formatter the copy uses, because a
// message that restates a limit in one unit and its offenders in another
// is a message that teaches its reader the check is wrong.
//
// REQUIRED MUTATIONS, BOTH RUN — and BOTH NEED RESHAPING FIRST, which is
// worth saying because the naive spelling of either does not compile:
// dropping the only call to units.Bytes orphans the import, and a build
// failure is not a red row, it is a mutation nobody ran.
//
//   - render the path alone, ignoring Sizes, with `_ = units.Bytes` to
//     keep the import alive: reds on both measurements and on the
//     beside-its-own-path assertion;
//   - keep units.Bytes alive the same way and format with %10d instead:
//     reds on all of those AND on the raw-byte-count assertion, which is
//     the half a plain integer would otherwise pass.
//
// THE UNMEASURED CASE IS THE POSITIVE CONTROL, and it is the row above:
// a finding with paths and no sizes still renders its paths, so this one
// cannot pass against a renderer that has quietly started requiring a
// measurement it does not have.
func TestPreflightRenderMeasuresThePathsWhenTheFindingMeasuredThem(t *testing.T) {
	r := &recorder{answer: true}

	err := RenderPreflight(r, reportOf(t, []check.Finding{{
		CheckID:  check.IDLimitFileSize,
		Severity: check.SeverityHardStop,
		Message:  "2 files are larger than 5.0 MB.",
		// It carries its own copy, which is what routes it through
		// ownCopy — the branch that renders a finding's paths. A hard
		// stop with no copy is synthesised from summaries instead, and
		// that branch has never named paths.
		What:  "2 files are larger than 5.0 MB, which is the most any single file may be.",
		Next:  "Shrink or remove each one, then run `curious deploy` again.",
		Paths: []string{"public/hero.png", "public/reel.mov"},
		Sizes: []int64{5_200_000, 41_000_000},
	}}), 0)
	if err == nil {
		t.Fatal("a hard stop returned nil, so nothing below is about a rendered stop")
	}

	out := rendered(err)
	for _, want := range []string{
		"public/hero.png", "public/reel.mov", "5.2 MB", "41.0 MB",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the rendered stop does not carry %q:\n%s", want, out)
		}
	}

	// The measurement stands BESIDE its own path, not somewhere else in
	// the output: two paths and two sizes in one list is exactly the
	// arrangement an off-by-one renders wrongly while still printing
	// every string this row looks for.
	if !strings.Contains(out, "5.2 MB  public/hero.png") {
		t.Errorf("the size is not beside its own path:\n%s", out)
	}
	if strings.Contains(out, "5200000") {
		t.Errorf("the rendered stop carries a raw byte count:\n%s", out)
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
			if err := RenderPreflight(r, reportOf(t, nil), tc.elapsed); err != nil {
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
	// The declines sit on ids that carry NO finding, which the gate now
	// requires: a check that found something answered, so a finding and
	// a decline about one id is a report contradicting itself. The
	// fixture used to decline pages-dir while carrying a note under it,
	// and the fifth enforcement refuses exactly that.
	report := reportOf(t, findings,
		environmental(check.IDLockfile, "couldn't read package.json"),
		environmental(check.IDSymlinks, "couldn't read the project directory"))

	run := func() string {
		r := &recorder{answer: true}
		if err := RenderPreflight(r, report, 5*time.Second); err != nil {
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
			err := RenderPreflight(r, reportOf(t, tc.findings), 0)
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
	code := ui.New(os.Stdin, os.Stdout, os.Stderr).ExitCode(err)
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

	err := RenderPreflight(r, reportOf(t, []check.Finding{lockfileFinding()}), 0)

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

	err := RenderPreflight(r, reportOf(t, []check.Finding{
		hardStop(check.IDAstroDep, "This doesn't look like an Astro project."),
	}), 0)

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
// WHAT THE TABLE VARIES IS WHICH FINDING CARRIES THE COPY, and that is a
// correction. It used to vary their POSITION, because a renderer
// preferring "the first finding" keys on position — but a caller can no
// longer choose position at all: the report arrives through the gate,
// which orders findings by the declared universe. So the earlier
// fixture's second case pinned an order no producer can now emit, and
// the property underneath it is better stated as the one below: the
// output must not depend on which of the two worked its words out.
//
// Both cases therefore share one golden, and that shared file IS the
// assertion — two renderings that differ only in who carries copy must
// come out byte-identical.
func TestPreflightRenderTwoHardFindingsAlwaysSynthesise(t *testing.T) {
	// astro-dep sorts first and lockfile second, by declared order.
	firstBare := hardStop(check.IDAstroDep, "This doesn't look like an Astro project.")
	secondBare := hardStop(check.IDLockfile, "No lockfile found.")

	firstWithCopy := firstBare
	firstWithCopy.What = "This doesn't look like an Astro project."
	firstWithCopy.Why = "package.json here doesn't list astro as a dependency."
	firstWithCopy.Next = "If this is the wrong folder, pass the right one."

	secondWithCopy := lockfileFinding()
	secondWithCopy.Message = "No lockfile found."

	cases := []struct {
		name     string
		findings []check.Finding
	}{
		{"copy on the one that sorts first", []check.Finding{firstWithCopy, secondBare}},
		{"copy on the one that sorts second", []check.Finding{firstBare, secondWithCopy}},
		{"copy on both", []check.Finding{firstWithCopy, secondWithCopy}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{}
			err := RenderPreflight(r, reportOf(t, tc.findings), 0)

			got, code := renderedBytes(t, err)
			if want := readGolden(t, "two-hard-findings.golden"); got != want {
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
			for _, borrowed := range []string{
				"curious installs your dependencies", "package.json here doesn't list astro",
			} {
				if strings.Contains(got, borrowed) {
					t.Errorf("a finding's own copy was preferred over the summary of both:\n%s", got)
				}
			}
		})
	}
}

// TestPreflightRenderFillsAMissingHeadlineFromTheSummary, and the
// action too. Copy is optional part by part, and a check that wrote only
// a reason has still worked its copy out — so the headline falls back to
// the required one-line summary rather than the whole message falling
// back to synthesis and throwing that reason away.
//
// THE GOLDEN WAS WRONG AND IS CORRECTED HERE. It pinned a hard stop that
// ended after its reason with no action at all, as though that were
// right — while a row in this same file calls an actionless hard stop an
// error, on the grounds that it leaves a first-timer guessing. One row
// asserted the property and a golden two files away pinned its
// violation; supplying one part of the copy REMOVED the action line, so
// adding copy made the message worse than leaving it off.
//
// MUTATION: leave What empty rather than filling it. The golden reds on
// the missing headline.
func TestPreflightRenderFillsAMissingHeadlineFromTheSummary(t *testing.T) {
	r := &recorder{}

	err := RenderPreflight(r, reportOf(t, []check.Finding{{
		CheckID:  check.IDLockfile,
		Severity: check.SeverityHardStop,
		Message:  "No lockfile found.",
		Why: "curious installs your dependencies from a lockfile, so it builds the\n" +
			"exact versions you tested.",
	}}), 0)

	got, _ := renderedBytes(t, err)
	if want := readGolden(t, "lone-hard-finding-partial-copy.golden"); got != want {
		t.Errorf("rendering:\n%s\nwant:\n%s", got, want)
	}
}

// TestPreflightRenderCopyKeepsItsWordsAndStillNamesTheFiles pins what
// "verbatim" means here, because the word can be read two ways and only
// one of them is safe.
//
// VERBATIM IS A PROMISE NOT TO EDIT AN AUTHOR'S WORDS. It is not a
// promise to withhold data the finding is carrying. So the three parts
// render exactly as written and the file list follows the reason — which
// means a check that works out beautiful copy and forgets to name its
// file does not silently lose it in the terminal. The alternative put an
// obligation on every check author who has not been hired yet, and the
// failure mode was silence.
func TestPreflightRenderCopyKeepsItsWordsAndStillNamesTheFiles(t *testing.T) {
	withCopy := lockfileFinding()
	withCopy.Paths = []string{"package.json"}

	err := RenderPreflight(&recorder{}, reportOf(t, []check.Finding{withCopy}), 0)

	got, _ := renderedBytes(t, err)
	if want := readGolden(t, "lone-hard-finding-with-copy-and-paths.golden"); got != want {
		t.Errorf("carried copy did not render as written, or lost its file:\n%s\nwant:\n%s", got, want)
	}
	for _, part := range []string{withCopy.What, withCopy.Why, withCopy.Next} {
		if !strings.Contains(got, part) {
			t.Errorf("a part of the author's copy was edited or dropped:\nmissing: %q\nin:\n%s", part, got)
		}
	}

	bare := check.Finding{
		CheckID:  check.IDLockfile,
		Severity: check.SeverityHardStop,
		Message:  "No lockfile found.",
		Paths:    []string{"package.json"},
	}
	synthesised, _ := renderedBytes(t, RenderPreflight(&recorder{}, reportOf(t, []check.Finding{bare}), 0))
	if !strings.Contains(synthesised, "package.json") {
		t.Errorf("a finding with no copy of its own lost the file it is about:\n%s", synthesised)
	}
}

// TestPreflightRenderRefusesAResultThatNeverPassedTheGate. The zero
// Report is the one value a caller outside the result package can still
// name, so it is the one hole the type cannot close by construction —
// and it is closed here instead.
//
// THE COMPILE CASCADE IS THE REST OF THE ASSERTION. Every other way of
// reaching this function with unvalidated content stopped compiling when
// the parameter became a Report; there is no row for those because there
// is no longer any code that could express them.
//
// It renders as a fault in this program rather than as a problem with
// the user's project, because that is what it is: nobody deploying a
// site can cause it or fix it.
//
// MUTATION: accept an invalid report. Reds here.
// MUST NOT MOVE: every row that builds its report through the gate.
func TestPreflightRenderRefusesAResultThatNeverPassedTheGate(t *testing.T) {
	r := &recorder{answer: true}

	err := RenderPreflight(r, check.Report{}, 0)
	if err == nil {
		t.Fatal("the zero report was rendered as though it had been validated")
	}
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %v, want none", r.prompts)
	}

	var failure *ui.Failure
	if errors.As(err, &failure) {
		t.Errorf("error = %#v, want a fault rather than product copy: nobody deploying a "+
			"site can cause this or fix it", failure)
	}
	if code := exitCodeFor(t, err); code == 0 {
		t.Errorf("exit code = %d, want non-zero", code)
	}
}

// ---------------------------------------------------------------------
// A check that did not run is a decision, not a line
// ---------------------------------------------------------------------

// TestPreflightRenderAsksAboutAChecksThatDidNotRun. The manifest exists
// because a skipped check rendered as a tick is a lie the reader will
// act on — and the renderer read it, printed a line, and then decided
// exactly as it would have for a tick. A project whose package.json
// could not be read reached the packer with nobody having confirmed it
// has a lockfile, and the user was told so in one line that changed
// nothing.
//
// MUTATION: leave not-run out of the decision. Reds here and on the
// non-interactive row.
// MUST NOT MOVE: the clean-project row, whose manifest is full.
func TestPreflightRenderAsksAboutChecksThatDidNotRun(t *testing.T) {
	r := &recorder{answer: true}

	packed, err := gatedDeploy(r, reportOf(t, nil,
		environmental(check.IDLockfile, "couldn't read package.json")), 0)

	if err != nil {
		t.Fatalf("error = %v, want nil after the user agreed", err)
	}
	if !packed {
		t.Error("the packer did not run after the user agreed")
	}
	if len(r.prompts) != 1 {
		t.Fatalf("prompts = %d %v, want one — a check nobody could run is a decision",
			len(r.prompts), r.prompts)
	}
}

// TestPreflightRenderDeclinedOnANotRunCheckStopsTheDeploy is the half
// that matters most: the recorder in the reproduction was set to
// decline and was never asked.
func TestPreflightRenderDeclinedOnANotRunCheckStopsTheDeploy(t *testing.T) {
	r := &recorder{answer: false}

	packed, err := gatedDeploy(r, reportOf(t, nil,
		environmental(check.IDLockfile, "couldn't read package.json")), 0)

	if packed {
		t.Error("the packer ran after the user declined")
	}
	if !errors.Is(err, ui.ErrAborted) {
		t.Errorf("error = %#v, want the cancellation sentinel", err)
	}
	if code := exitCodeFor(t, err); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// TestPreflightRenderNonInteractiveRefusesOnANotRunCheck. Same rule as a
// pending warning: with nobody to ask, neither continue nor abort. A
// check that could not look is exactly the state where proceeding
// silently is worst.
func TestPreflightRenderNonInteractiveRefusesOnANotRunCheck(t *testing.T) {
	r := &recorder{answerTo: ui.ErrNotInteractive}

	packed, err := gatedDeploy(r, reportOf(t, nil,
		environmental(check.IDLockfile, "couldn't read package.json")), 0)

	if packed {
		t.Error("the packer ran with a check nobody could be asked about")
	}
	if !errors.Is(err, ui.ErrNotInteractive) {
		t.Errorf("error = %#v, want the no-terminal sentinel", err)
	}
}

// TestPreflightRenderNotRunPromptMatrix walks the combinations, because
// a not-run check has to compose with the other two states rather than
// have a rule of its own.
func TestPreflightRenderNotRunPromptMatrix(t *testing.T) {
	skipped := environmental(check.IDLockfile, "couldn't read package.json")
	warn := warning(check.IDLocalhost, "A development URL is hard-coded.")
	stop := hardStop(check.IDAstroDep, "This doesn't look like an Astro project.")

	cases := []struct {
		name     string
		findings []check.Finding
		prompts  int
	}{
		{"not-run alone", nil, 1},
		{"not-run with a warning", []check.Finding{warn}, 1},
		{"not-run with a hard stop", []check.Finding{stop}, 0},
		{"not-run with a hard stop and a warning", []check.Finding{stop, warn}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{answer: true}
			RenderPreflight(r, reportOf(t, tc.findings, skipped), 0)
			if len(r.prompts) != tc.prompts {
				t.Errorf("prompts = %d %v, want %d", len(r.prompts), r.prompts, tc.prompts)
			}
		})
	}

	for _, tc := range cases {
		t.Run(tc.name+", non-interactive", func(t *testing.T) {
			r := &recorder{answerTo: ui.ErrNotInteractive}
			err := RenderPreflight(r, reportOf(t, tc.findings, skipped), 0)
			if err == nil {
				t.Fatal("the deploy proceeded with nobody to ask")
			}
			wantSentinel := tc.prompts > 0
			if got := errors.Is(err, ui.ErrNotInteractive); got != wantSentinel {
				t.Errorf("no-terminal sentinel = %v, want %v (error %#v)", got, wantSentinel, err)
			}
		})
	}
}

// TestPreflightRenderNotRunWithNoReasonSaysSomething. A row carrying no
// reason rendered as "Skipped astro-dep: " — a trailing colon with
// nothing after it, which reads as truncated output rather than as a
// fact about the project.
func TestPreflightRenderNotRunWithNoReasonSaysSomething(t *testing.T) {
	r := &recorder{answer: true}

	RenderPreflight(r, reportOf(t, nil, check.Status{CheckID: check.IDAstroDep, Outcome: check.Declined}), 0)

	out := r.out.String()
	if strings.Contains(out, ": \n") || strings.HasSuffix(strings.TrimRight(out, "\n"), ":") {
		t.Errorf("a reasonless row rendered as a bare colon:\n%q", out)
	}
	if !strings.Contains(out, check.IDAstroDep) {
		t.Errorf("output does not name the skipped check:\n%s", out)
	}
}

// ---------------------------------------------------------------------
// A hard stop must not hide the warnings
// ---------------------------------------------------------------------

// TestPreflightRenderShowsWarningsBesideAHardStop. The ruling was that a
// hard stop renders NO PROMPT. The code took that as licence to drop the
// findings, so warnings were collected and never rendered on that path —
// and the user fixes the hard stop, re-runs, and only then meets them.
// That is the round-trip this engine's own documentation says it exists
// to prevent: learn every fact in one run.
//
// MUTATION: return the failure before rendering the warnings. Reds here.
// MUST NOT MOVE: the no-prompt assertion, which this row also carries.
func TestPreflightRenderShowsWarningsBesideAHardStop(t *testing.T) {
	r := &recorder{answer: true}

	err := RenderPreflight(r, reportOf(t, []check.Finding{
		hardStop(check.IDAstroDep, "This doesn't look like an Astro project."),
		warning(check.IDPagesDir, "Couldn't find src/pages."),
		warning(check.IDLocalhost, "A development URL is hard-coded."),
	}), 0)

	got, _ := renderedBytes(t, err)
	everything := r.out.String() + got

	for _, want := range []string{
		"This doesn't look like an Astro project.",
		"Couldn't find src/pages.",
		"A development URL is hard-coded.",
	} {
		if !strings.Contains(everything, want) {
			t.Errorf("nothing rendered %q — a warning a user never sees is one they meet "+
				"on the next run:\n%s", want, everything)
		}
	}
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %v, want none beside a hard stop", r.prompts)
	}
}

// ---------------------------------------------------------------------
// Copy
// ---------------------------------------------------------------------

// TestPreflightRenderAlwaysNamesAnAction is the row the tree used to
// contradict. A bare finding got the synthesised next step; one that
// supplied a HEADLINE lost it — so adding a part of the copy REMOVED the
// action, and a golden two files away pinned that as correct while a row
// in this one calls an actionless hard stop an error.
//
// Copy is optional part by part; the action is not optional at all.
//
// MUTATION: drop the Next fallback. All four cases red.
// MUST NOT MOVE: the with-copy golden, whose finding supplies its own
// Next.
func TestPreflightRenderAlwaysNamesAnAction(t *testing.T) {
	base := hardStop(check.IDAstroDep, "This doesn't look like an Astro project.")

	withWhat := base
	withWhat.What = "This doesn't look like an Astro project."
	withWhy := base
	withWhy.Why = "package.json here doesn't list astro as a dependency."
	withNext := base
	withNext.Next = "Pass the right folder: `curious deploy ./my-site`."

	cases := []struct {
		name    string
		finding check.Finding
	}{
		{"no copy at all", base},
		{"headline only", withWhat},
		{"reason only", withWhy},
		{"action only", withNext},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RenderPreflight(&recorder{}, reportOf(t, []check.Finding{tc.finding}), 0)

			var failure *ui.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("error = %#v, want a failure", err)
			}
			if failure.What == "" {
				t.Error("no headline: a failure with no What renders as a bare paragraph")
			}
			if failure.Next == "" {
				t.Error("no action: a hard stop that does not say what to do next leaves " +
					"a first-timer guessing, which is what the row beside this one calls " +
					"an error")
			}
		})
	}
}

// TestPreflightRenderKeepsASuppliedAction. The fallback must not
// overwrite a check that wrote its own.
func TestPreflightRenderKeepsASuppliedAction(t *testing.T) {
	f := hardStop(check.IDAstroDep, "This doesn't look like an Astro project.")
	f.Next = "Pass the right folder: `curious deploy ./my-site`."

	err := RenderPreflight(&recorder{}, reportOf(t, []check.Finding{f}), 0)

	var failure *ui.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("error = %#v, want a failure", err)
	}
	if failure.NextText != f.Next {
		t.Errorf("Next = %q, want the check's own %q", failure.Next, f.Next)
	}
}

// TestPreflightRenderShowsCopyOnWarnings. The warning path rendered
// Message and Paths and nothing else, so a check that worked out why its
// warning matters and what to do about it had both dropped — while the
// machine-readable result would carry them. The model documents copy
// with no severity qualifier, and the question "does this finding have
// copy" is asked in one place precisely so the two surfaces cannot
// disagree. For warnings they disagreed every time.
//
// MUTATION: render Message alone in the warnings loop. Reds here.
// MUST NOT MOVE: the single-prompt count, asserted here too.
func TestPreflightRenderShowsCopyOnWarnings(t *testing.T) {
	r := &recorder{answer: true}
	warn := warning(check.IDLocalhost, "A development URL is hard-coded.")
	warn.Paths = []string{"src/lib/api.ts"}
	warn.Why = "It will not resolve once the site is published."
	warn.Next = "Read the URL from import.meta.env.PUBLIC_API_URL instead."

	if err := RenderPreflight(r, reportOf(t, []check.Finding{warn}), 0); err != nil {
		t.Fatalf("error = %v", err)
	}

	out := r.out.String()
	for _, want := range []string{
		"A development URL is hard-coded.",
		"src/lib/api.ts",
		"It will not resolve once the site is published.",
		"Read the URL from import.meta.env.PUBLIC_API_URL instead.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not carry %q:\n%s", want, out)
		}
	}
	if len(r.prompts) != 1 {
		t.Errorf("prompts = %d, want exactly one", len(r.prompts))
	}
}

// TestPreflightRenderWarningsWithoutCopyAreUnchanged is the floor beside
// the row above: a warning that carries no copy must not grow blank
// paragraphs now that copy is rendered.
func TestPreflightRenderWarningsWithoutCopyAreUnchanged(t *testing.T) {
	r := &recorder{answer: true}
	warn := warning(check.IDLocalhost, "A development URL is hard-coded.")
	warn.Paths = []string{"src/lib/api.ts"}

	if err := RenderPreflight(r, reportOf(t, []check.Finding{warn}), 0); err != nil {
		t.Fatalf("error = %v", err)
	}

	want := "A development URL is hard-coded.\n  src/lib/api.ts\n"
	if got := r.out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestPreflightRenderPrefersAnAuthoredHeadline. A check that wrote a
// What has written the sentence it wants leading its finding, and the
// list shows that rather than the summary underneath it.
//
// ADDED BECAUSE A MUTATION WENT GREEN: replacing the headline with the
// summary changed no row, so the line was unguarded. Guarding it rather
// than deleting it is the deliberate half — silently dropping a field an
// author set is the defect this round exists to close, and doing it one
// field over would be the same mistake wearing a different name.
//
// MUTATION: use Message and ignore What. Reds here.
// MUST NOT MOVE: the row for a warning with no copy, whose Message is
// the only headline there is.
func TestPreflightRenderPrefersAnAuthoredHeadline(t *testing.T) {
	r := &recorder{answer: true}
	warn := warning(check.IDLocalhost, "a development URL is hard-coded")
	warn.What = "A development URL is hard-coded in 2 files."

	if err := RenderPreflight(r, reportOf(t, []check.Finding{warn}), 0); err != nil {
		t.Fatalf("error = %v", err)
	}

	out := r.out.String()
	if !strings.Contains(out, warn.What) {
		t.Errorf("output does not carry the authored headline %q:\n%s", warn.What, out)
	}
}

// ---------------------------------------------------------------------
// The two kinds of decline cost different things
// ---------------------------------------------------------------------

// TestPreflightRenderKeepsAByDesignDeclineOutOfSight. A check that
// looked and chose not to guess is naming nothing the user did and
// nothing they can change, so it renders below advisory: shown by no
// surface by default, asked about by nothing, and still present in the
// structured result for a caller that wants it.
//
// That is the standing criterion holding through the manifest — an
// advisory names something the user can act on or observe, or it does
// not fire — and the alternative is a deploy that stops to ask about a
// config containing a template literal.
//
// MUTATION (required): make ByDesign behave as Environmental. This row
// reds on the prompt count and on the output. MUST NOT MOVE: every
// environmental row below and above.
func TestPreflightRenderKeepsAByDesignDeclineOutOfSight(t *testing.T) {
	r := &recorder{answer: true}
	report := reportOf(t, nil,
		byDesign(check.IDBuildFormat, "the config builds this value at run time"))

	packed, err := gatedDeploy(r, report, 0)

	if err != nil || !packed {
		t.Fatalf("packed = %v, err = %v, want the deploy to carry on", packed, err)
	}
	if len(r.prompts) != 0 {
		t.Errorf("prompts = %v, want none — nothing here is anybody's decision", r.prompts)
	}
	if r.out.Len() != 0 {
		t.Errorf("output = %q, want silence", r.out.String())
	}

	// Kept, not discarded: a caller that asks gets it.
	found := false
	for _, row := range report.Manifest() {
		if row.CheckID == check.IDBuildFormat {
			found = true
			if row.Outcome != check.Declined || row.Kind != check.ByDesign {
				t.Errorf("row = %+v, want a by-design decline", row)
			}
			if row.Reason == "" {
				t.Error("the reason was dropped; the whole point of keeping the row is the reason")
			}
		}
	}
	if !found {
		t.Error("the by-design row is not in the report at all")
	}
}

// TestPreflightRenderNonInteractiveProceedsOnAByDesignDecline. The other
// half of "no prompt": with nobody to ask, there is still nothing to
// ask, so the deploy is not refused. An environmental decline in the
// same position refuses, and the row below asserts the pair together.
func TestPreflightRenderNonInteractiveProceedsOnAByDesignDecline(t *testing.T) {
	r := &recorder{answerTo: ui.ErrNotInteractive}

	packed, err := gatedDeploy(r, reportOf(t, nil,
		byDesign(check.IDBuildFormat, "the config builds this value at run time")), 0)

	if err != nil {
		t.Errorf("error = %#v, want nil — there was no question to fail to ask", err)
	}
	if !packed {
		t.Error("the deploy was refused over something nobody can act on")
	}
}

// TestPreflightRenderChargesTheTwoKindsDifferently is the pair in one
// report, which is the only place the distinction can be seen working
// rather than asserted twice. One question, about the environmental one
// only; the by-design one appears nowhere.
func TestPreflightRenderChargesTheTwoKindsDifferently(t *testing.T) {
	r := &recorder{answer: true}

	err := RenderPreflight(r, reportOf(t, nil,
		environmental(check.IDLockfile, "couldn't read package.json"),
		byDesign(check.IDBuildFormat, "the config builds this value at run time")), 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	if len(r.prompts) != 1 {
		t.Fatalf("prompts = %d %v, want one, covering the environmental decline",
			len(r.prompts), r.prompts)
	}
	out := r.out.String()
	if !strings.Contains(out, check.IDLockfile) {
		t.Errorf("output does not name the environmental decline:\n%s", out)
	}
	for _, hidden := range []string{check.IDBuildFormat, "builds this value at run time"} {
		if strings.Contains(out, hidden) {
			t.Errorf("output carries the by-design decline (%q):\n%s", hidden, out)
		}
	}
}

// TestPreflightRenderMeasuresPathsUnderSeveralHardStops is the other half
// of the sized list, and it exists because the row above cannot reach it.
//
// THE RENDERER HAS TWO BRANCHES THAT PRINT PATHS. A lone hard finding
// carries its own copy and goes through ownCopy; several are synthesised
// into one failure, and that path prints each finding through summary.
// Sizes was added for the first and not wired into the second, so a
// project breaking the per-file limit AND the total limit — the ordinary
// shape of a too-big project — lost every measurement exactly when it had
// more of them.
//
// The single-finding row above is by construction unable to see this: one
// finding never reaches summary. That is a row whose INPUTS cannot reach
// the property it names, and the fix for that shape is inputs, not
// assertions — which is what this row is.
//
// REQUIRED MUTATION: make summary ignore Sizes and print the bare path,
// keeping the formatter referenced so the package still builds. Reds on
// both measurements.
func TestPreflightRenderMeasuresPathsUnderSeveralHardStops(t *testing.T) {
	r := &recorder{answer: true}

	err := RenderPreflight(r, reportOf(t, []check.Finding{
		{
			CheckID:  check.IDLimitFileSize,
			Severity: check.SeverityHardStop,
			Message:  "1 file is larger than 5.0 MB.",
			Paths:    []string{"public/reel.mov"},
			Sizes:    []int64{41_000_000},
		},
		{
			CheckID:  check.IDLimitTotal,
			Severity: check.SeverityHardStop,
			Message:  "This project is 62.0 MB of source.",
			Paths:    []string{"public/archive.zip"},
			Sizes:    []int64{21_000_000},
		},
	}), 0)
	if err == nil {
		t.Fatal("two hard stops returned nil, so nothing below is about a rendered stop")
	}

	out := rendered(err)
	for _, want := range []string{
		"public/reel.mov", "41.0 MB",
		"public/archive.zip", "21.0 MB",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the synthesised stop does not carry %q:\n%s", want, out)
		}
	}

	// Beside its own path, not merely somewhere in the output: two
	// findings with one measurement each is exactly the arrangement a
	// renderer pairing them wrongly still prints every string above.
	for _, want := range []string{
		"41.0 MB  public/reel.mov",
		"21.0 MB  public/archive.zip",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the measurement is not beside its own path (%q):\n%s", want, out)
		}
	}
}
