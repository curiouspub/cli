package flow

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/internal/units"
)

// Prompter is the slice of the terminal this renderer needs: a line of
// narration, and one question.
//
// It is declared HERE, by the consumer, rather than exported by the
// package that implements it. The real terminal type satisfies it
// without knowing this package exists, and a test can state what it
// means — three warnings produce exactly one prompt — without arranging
// a terminal, which is not a thing a test can portably do on all three
// platforms this ships to.
type Prompter interface {
	Step(format string, args ...any)
	Confirm(question string, defaultYes bool) (bool, error)
}

// slowPreflight is the point past which the run's own duration is worth
// a line. A silent pause reads as a hang, and scanning a large tree is
// the one plausible slow spot — but a duration printed on every deploy
// is noise, and noise is what people stop reading.
const slowPreflight = 2 * time.Second

// RenderPreflight shows a pre-flight result to a person and returns what
// the deploy should do about it.
//
// THE ENGINE PRODUCES FINDINGS; THIS DECIDES WHAT BLOCKING MEANS. The
// same warning is a question here and a non-blocking note on the result
// an agent reads, so the decision cannot live inside a check — and with
// the split in the right place the agent-facing surface is a second
// renderer rather than a second set of checks.
//
// The returns are the program's existing vocabulary rather than a new
// one, so the exit code is decided in the single place that decides
// every other exit code:
//
//   - nil — nothing found, or the user agreed to continue.
//   - a failure carrying the copy — hard stops. Non-zero, no prompt.
//   - the cancellation sentinel — the user declined. Exit 0: they made a
//     choice and the tool obeyed, and a non-zero code would make every
//     wrapper script treat that as a fault.
//   - the no-terminal sentinel — warnings pending with nobody to ask.
//     Neither continue nor abort: both would be the program deciding
//     something it was not asked to decide.
func RenderPreflight(p Prompter, report check.Report, elapsed time.Duration) error {
	// THE ONLY DOOR. A Report cannot be built outside the result
	// package, so reaching this function means the gate ran: coverage,
	// duplicates, claimed ids and declared severities were all checked
	// by a type rather than by a caller remembering to ask. The zero
	// value is the one thing a caller can still name, and it is a
	// programming error rather than anything a user did.
	if !report.Valid() {
		return errors.New("pre-flight was handed a result that never passed the gate")
	}
	findings, manifest := report.Findings(), report.Manifest()

	if elapsed > slowPreflight {
		p.Step("Pre-flight took %s.", elapsed.Round(100*time.Millisecond))
	}

	// A DECLINE IS READ FROM THE MANIFEST, never from the absence of a
	// finding. A check emits nothing when it finds nothing and nothing
	// when it could not look, so from out here those two are the same
	// silence — and a skipped check rendered as a tick is a lie the user
	// will act on.
	//
	// ONLY THE ENVIRONMENTAL ONES REACH A PERSON, and that split is the
	// whole of what the kind buys. Something outside the check stopped
	// it looking: the user can see that and may be able to fix it, so it
	// is shown and it is asked about. A check that LOOKED and chose not
	// to guess is naming nothing anybody did and nothing anybody can
	// change — the standing criterion says an advisory names something
	// the user can act on or observe, or it does not fire, and that
	// holds through the manifest exactly as it holds through a finding.
	// The by-design rows stay in the report for a caller that asks.
	//
	// The alternative is a deploy that stops to ask a person about a
	// config containing a template literal.
	environmental := manifest.DeclinesOfKind(check.Environmental)
	for _, row := range environmental {
		p.Step("%s", skipped(row))
	}

	var hardStops, warnings []check.Finding
	for _, f := range check.Advisories(findings) {
		switch f.Severity {
		case check.SeverityHardStop:
			hardStops = append(hardStops, f)
		default:
			warnings = append(warnings, f)
		}
	}

	// WARNINGS ARE RENDERED WHETHER OR NOT A HARD STOP IS PRESENT, and
	// this block's position is the whole of that fix. The rule was that
	// a hard stop asks NO QUESTION; it was read as licence to drop the
	// findings, so warnings were collected and never shown — and the
	// user fixes the hard stop, re-runs, and only then meets them. That
	// is the round-trip this program exists to prevent.
	for _, f := range warnings {
		p.Step("%s", ui.Prose(advisoryLines(f)))
	}

	if len(hardStops) > 0 {
		// NO PROMPT, even with warnings also pending. There is nothing
		// to decide, and a question whose only answer is "no" teaches
		// people to press Enter without reading the one that matters.
		return blockedFailure(hardStops)
	}

	// A CHECK THAT DID NOT RUN IS PART OF THE DECISION, not a line that
	// changes nothing. The manifest exists because a skipped check
	// rendered as a tick is a lie the reader will act on — and printing
	// it and then deciding exactly as if it were a tick is the same lie
	// with a sentence in front of it. A project whose package.json could
	// not be read would otherwise reach the packer with nobody having
	// confirmed it has a lockfile.
	if len(warnings) == 0 && len(environmental) == 0 {
		return nil
	}

	// ONE QUESTION FOR ALL OF THEM. Three hard-coded development URLs in
	// three files is one decision; three prompts is how a warning turns
	// into noise and a broken deploy turns into the user's fault.
	proceed, err := p.Confirm("Continue anyway?", true)
	if err != nil {
		return err
	}
	if !proceed {
		return ui.ErrAborted
	}
	return nil
}

// blockedFailure turns the hard stops into the copy a person reads.
//
// A LONE FINDING CARRYING ITS OWN COPY RENDERS THAT COPY, and the
// synthesis below is for everything else. This is the commonest case by
// some distance — most projects that cannot deploy have exactly one
// reason — and it is the one the synthesis was worst at: a check author
// who worked out what happened, why, and what to do about it had their
// three paragraphs wrapped in a summary saying there is 1 thing to fix.
//
// TWO OR MORE ALWAYS SYNTHESISE, whether or not either carries copy.
// Preferring the first finding's own words would show one problem to
// somebody who has two, and the second would be discovered only after
// the first was fixed — the round-trip the engine exists to prevent.
func blockedFailure(hardStops []check.Finding) *ui.Failure {
	if len(hardStops) == 1 && hardStops[0].HasCopy() {
		return ownCopy(hardStops[0])
	}
	return synthesised(hardStops)
}

// ownCopy renders a finding's three parts as written, and then names the
// files it is about.
//
// VERBATIM IS A PROMISE NOT TO EDIT AN AUTHOR'S WORDS. It is not a
// promise to withhold what the finding is carrying. The three parts go
// out exactly as the check wrote them and the path list follows the
// reason, so a check that works out good copy and forgets to name its
// file does not lose it silently in the terminal.
//
// The alternative — copy suppresses the paths — was tried and ruled
// against, because it put an obligation on every check author who has
// not been hired yet, and its failure mode was silence. A renderer that
// drops data on the author's behalf is making an editorial decision it
// cannot see the consequences of.
//
// The headline falls back to the required one-line summary when the
// check supplied only a reason or only an action. Copy is optional part
// by part, and a check that wrote one part has worked its copy out as
// far as it needed to — dropping back to the synthesis there would
// throw that part away.
func ownCopy(f check.Finding) *ui.Failure {
	what := f.What
	if what == "" {
		what = f.Message
	}

	why := f.Why
	if len(f.Paths) > 0 {
		var b strings.Builder
		b.WriteString(why)
		if why != "" {
			b.WriteString("\n")
		}
		// THE MEASURED FORM IS THE SAME LIST WITH ITS NUMBERS, rendered
		// here rather than by each check that has some.
		//
		// A check with sizes to report used to lay out its own table in
		// its copy and ALSO set Paths, so the offenders arrived twice —
		// once measured, once bare — in the one hard stop a user most
		// needs to read. The facts now travel as facts and this is the
		// single place that turns them into a column, which is also the
		// only place that knows how wide the column should be.
		//
		// Both branches that print paths go through one renderer, so a
		// measurement cannot appear on one and not the other.
		b.WriteString(pathList(f))
		why = strings.TrimPrefix(b.String(), "\n")
	}

	next := f.Next
	if next == "" {
		next = standingAction
	}
	return ui.NewFailure(what, why, ui.NextFreshDeploy, next)
}

// synthesised builds one failure out of several summaries.
//
// EVERY ONE OF THEM IS REPORTED, in one run. Someone whose project is
// missing astro and a lockfile should learn both facts now rather than
// discover the second only after fixing the first and running again.
func synthesised(hardStops []check.Finding) *ui.Failure {
	things := "1 thing"
	if len(hardStops) != 1 {
		things = fmt.Sprintf("%d things", len(hardStops))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "There %s %s to fix before this project will deploy:\n",
		map[bool]string{true: "is", false: "are"}[len(hardStops) == 1], things)
	for _, f := range hardStops {
		fmt.Fprintf(&b, "\n%s\n", indent(summary(f), "  "))
	}

	return ui.NewFailure(
		"curious can't deploy this project yet.",
		strings.TrimRight(b.String(), "\n"),
		ui.NextFreshDeploy, standingAction,
	)
}

// standingAction is the next step for a finding whose author did not
// write one. THE ACTION IS NEVER ABSENT: copy is optional part by part,
// and the part a reader can act on is the one a hard stop cannot do
// without. Supplying a headline used to REMOVE this line, so adding copy
// made the message worse than leaving it off.
const standingAction = "Fix what's listed above and run `curious deploy` again. Nothing has" +
	"\nbeen uploaded."

// skipped renders one manifest row for a check that did not run.
//
// A row with no reason is a producer's omission rather than a fact about
// the project, and it used to render as a bare trailing colon — which
// reads as truncated output rather than as information.
func skipped(row check.Status) string {
	if row.Reason == "" {
		return fmt.Sprintf("Skipped %s: no reason was recorded.", row.CheckID)
	}
	return fmt.Sprintf("Skipped %s: %s", row.CheckID, row.Reason)
}

// summary renders one finding for a LIST of them: its headline, then the
// files it is about, and nothing else.
//
// The paths are listed rather than folded into the sentence because that
// is the entire reason they are a field. A check that named three files
// in prose would leave the reader scanning a paragraph for the one they
// have to open.
func summary(f check.Finding) string {
	headline := f.What
	if headline == "" {
		headline = f.Message
	}
	if len(f.Paths) == 0 {
		return headline
	}
	var b strings.Builder
	b.WriteString(headline)
	b.WriteString(pathList(f))
	return b.String()
}

// pathList renders a finding's paths, measured when it measured them.
//
// IT IS SHARED BY BOTH BRANCHES THAT PRINT PATHS, which is the whole
// reason it exists. ownCopy renders a lone hard finding; summary renders
// each of several, and warnings. Sizes was wired into the first and not
// the second, so a project with two reasons to be refused lost the
// measurements exactly when it had more of them — the feature working
// until there was more than one occasion to use it.
//
// The gate guarantees the two slices line up, so this indexes without
// checking: that is what an enforcement buys the code downstream of it.
func pathList(f check.Finding) string {
	var b strings.Builder
	for i, path := range f.Paths {
		if len(f.Sizes) > 0 {
			fmt.Fprintf(&b, "\n  %10s  %s", units.Bytes(f.Sizes[i]), path)
			continue
		}
		fmt.Fprintf(&b, "\n  %s", path)
	}
	return b.String()
}

// advisoryLines renders one warning in full: the summary, then whatever
// copy its author wrote.
//
// WARNINGS RENDER COPY, and that they did not was two surfaces
// disagreeing about one finding. This path showed the message and the
// paths and dropped What, Why and Next — while the machine-readable
// result carries them — even though the model documents copy with no
// severity qualifier, and the question "does this finding have copy" is
// asked in one place precisely so the two cannot diverge. For warnings
// they diverged every time.
//
// No standing action is appended here. A warning is something a user may
// knowingly proceed past and the question below IS the action; a hard
// stop is a dead end, which is why the action is mandatory there and
// offered here only when the author wrote one.
func advisoryLines(f check.Finding) string {
	var b strings.Builder
	b.WriteString(summary(f))
	for _, part := range []string{f.Why, f.Next} {
		if part != "" {
			fmt.Fprintf(&b, "\n%s", part)
		}
	}
	return b.String()
}

// indent prefixes every line, including the ones inside a message its
// author wrapped by hand. Wrapping is done at author time in this
// program, so a message arrives with newlines already in it and
// indenting only the first line would produce a ragged block.
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}
