package flow

import (
	"fmt"
	"strings"
	"time"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/ui"
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
func RenderPreflight(p Prompter, findings []check.Finding, manifest check.Manifest, elapsed time.Duration) error {
	if elapsed > slowPreflight {
		p.Step("Pre-flight took %s.", elapsed.Round(100*time.Millisecond))
	}

	// NOT-RUN IS READ FROM THE MANIFEST, never from the absence of a
	// finding. A check emits nothing when it finds nothing and nothing
	// when it could not look, so from out here those two are the same
	// silence — and a skipped check rendered as a tick is a lie the user
	// will act on.
	for _, row := range manifest.NotRun() {
		p.Step("Skipped %s: %s", row.CheckID, row.Reason)
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

	if len(hardStops) > 0 {
		// NO PROMPT, even with warnings also pending. There is nothing
		// to decide, and a question whose only answer is "no" teaches
		// people to press Enter without reading the one that matters.
		return blockedFailure(hardStops)
	}

	if len(warnings) == 0 {
		return nil
	}

	for _, f := range warnings {
		p.Step("%s", describe(f))
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
// EVERY ONE OF THEM IS REPORTED, in one run. Someone whose project is
// missing astro and a lockfile should learn both facts now rather than
// discover the second only after fixing the first and running again.
func blockedFailure(hardStops []check.Finding) *ui.Failure {
	things := "1 thing"
	if len(hardStops) != 1 {
		things = fmt.Sprintf("%d things", len(hardStops))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "There %s %s to fix before this project will deploy:\n",
		map[bool]string{true: "is", false: "are"}[len(hardStops) == 1], things)
	for _, f := range hardStops {
		fmt.Fprintf(&b, "\n%s\n", indent(describe(f), "  "))
	}

	return ui.NewFailure(
		"curious can't deploy this project yet.",
		strings.TrimRight(b.String(), "\n"),
		"Fix what's listed above and run `curious deploy` again. Nothing has\n"+
			"been uploaded.",
	)
}

// describe renders one finding: its message, then the files it is about.
//
// The paths are listed rather than folded into the sentence because that
// is the entire reason they are a field. A check that named three files
// in prose would leave the reader scanning a paragraph for the one they
// have to open.
func describe(f check.Finding) string {
	if len(f.Paths) == 0 {
		return f.Message
	}
	var b strings.Builder
	b.WriteString(f.Message)
	for _, path := range f.Paths {
		fmt.Fprintf(&b, "\n  %s", path)
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
