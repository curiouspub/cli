package ui

import (
	"errors"
	"fmt"
	"strings"
)

// Failure is a hard stop written for a person: WHAT happened, WHY it
// happened, and WHAT TO DO NEXT.
//
// The third part is the product. A message that stops a run without
// naming an action leaves the reader to guess, and the reader is usually
// somebody deploying their first site who has no model of what this
// program does. "No lockfile found" is a fact; "run npm install, commit
// the lockfile, try again" is a fix.
//
// Why and Next are stored ALREADY WRAPPED, and are printed exactly as
// written. Re-wrapping to the terminal's width would mean measuring the
// terminal and moving the cursor, which is what this package
// deliberately does not do — and it would make a golden file a test of
// the terminal rather than of the copy. Wrapping at author time is one
// decision made once by whoever wrote the sentence.
//
// A Failure is also an error, so a check deep in a flow can return the
// copy it owns and let the top of the program render it. Error returns
// What alone: it is a whole sentence, capitalised and stopped, which is
// unlike an ordinary Go error string on purpose — this is product copy
// that happens to travel as an error, not an error string that happens
// to be shown.
type Failure struct {
	What string
	Why  string
	Next string
}

// Error makes a Failure travel as an error, so a check deep in a flow
// can return the copy it owns and let the top of the program render it.
// It returns What alone: a whole sentence, capitalised and stopped,
// unlike an ordinary Go error string on purpose — this is product copy
// that happens to travel as an error, not an error string that happens
// to be shown.
//
// THE RECEIVER IS A POINTER, and that is a correction rather than a
// preference. With a value receiver both Failure and *Failure satisfied
// error, so `return &ui.Failure{...}` — the more natural of the two
// spellings — compiled perfectly and then failed to match the
// errors.As target in ExitCode, which looks for the value type. The
// caller's own copy was silently replaced by the internal-fault copy: a
// wrong message, with nothing anywhere reporting that a substitution had
// happened. One spelling is now the only spelling, and the compiler is
// what enforces it.
func (f *Failure) Error() string { return f.What }

// NewFailure builds one. It exists so the pointer is produced by this
// package rather than by every caller remembering an ampersand, and so
// the three parts are named at the call site — a positional
// Failure{a, b, c} reads as three interchangeable strings, and they are
// not: the third is the only one the reader can act on.
func NewFailure(what, why, next string) *Failure {
	return &Failure{What: what, Why: why, Next: next}
}

// THE TWO WORKED EXAMPLES USED TO LIVE HERE, and where they went is
// worth a paragraph because this is the file a reader looks in for them.
//
// They were three-part copy about a missing lockfile and a project that
// declares no Astro dependency, sitting in this package with nothing but
// its own tests reading it — because a pre-flight finding could carry
// only a one-line summary, and there was no way to deliver three
// paragraphs from the check that met the condition to the person
// standing in front of it. A finding now carries optional What, Why and
// Next, and a lone hard finding renders its own copy, so the words went
// to the checks that own those conditions.
//
// The centralising instinct was right and it picked the wrong package.
// Copy in two places diverges, and the divergence shows up as two halves
// of one program telling a user different stories — but the second place
// was never this one. It was the CONDITION, which lives with the check,
// and words and condition are what had to stop being two things.
//
// What stays here is copy this package itself is the author of: the
// failures below belong to situations this package meets on its own —
// a prompt with no terminal, a prompt nobody answered, a door held shut
// — and there is no check anywhere that owns them.

// notInteractiveFailure is the fallback rendering for a prompt reached
// with no terminal. It is deliberately generic: only the caller knows
// what it needed to ask, so a caller with a specific message handles
// ErrNotInteractive itself and this is what is left for one that has
// none.
var notInteractiveFailure = &Failure{
	What: "curious needs a terminal for that.",
	Why: "It had a question to ask you and no way to ask it. That happens when\n" +
		"curious runs through a pipe, from a script, or inside a tool that\n" +
		"captures its output.",
	Next: "Run curious directly in a terminal and answer the question there.",
}

// noAnswerFailure is what a prompt renders when it has asked its bounded
// number of times and never got an answer it could use.
//
// It exists because the alternative was the internal-fault copy. That
// path told a person "this is a fault in curious rather than a problem
// with your project, so there is nothing here for you to fix" and invited
// a bug report — after they had typed four answers the prompt could not
// read. The claim was false, the advice was useless, and it appeared in
// the one surface whose entire job is telling people what happened.
//
// So this copy does the opposite of blaming the program: it says plainly
// that the answers were not understood, and it names the two words that
// work. Nothing about it suggests anything is broken, because nothing is.
var noAnswerFailure = &Failure{
	What: "Didn't catch that.",
	Why: "curious asked the same question a few times and couldn't read any of\n" +
		"the answers, so it stopped rather than keep asking.",
	Next: "Run the command again and answer with y or n — or press Ctrl-C to\n" +
		"stop here.",
}

// serverClosedFailure is the standing copy for a closed-door stop whose
// caller offered none of its own.
//
// It exists for the same reason noAnswerFailure does: without it, a
// sentinel this package exports falls through to the internal-fault
// copy, which tells a person that something is broken and invites a bug
// report — when what actually happened is that a server said no for a
// while. Nothing is broken, and nothing here is theirs to fix.
//
// It names no time to come back, because this sentinel carries none. A
// caller that knows one says so in the Failure it wraps.
var serverClosedFailure = &Failure{
	What: "curious.pub isn't taking this right now.",
	Why: "The server is closed to this run — not because of anything wrong with\n" +
		"your project, and not because of anything you did.",
	Next: "Try again a little later. Nothing has been uploaded.",
}

// Fail writes a Failure to stderr — never stdout, because a failure is
// prose for a person and stdout is for output a program will read.
func (u *UI) Fail(f *Failure) {
	fmt.Fprint(u.err, u.renderFailure(f))
}

// renderFailure produces the exact bytes Fail writes. It is separate so
// the golden files can be compared against the shipped rendering rather
// than against a reconstruction of it — an assertion about an artefact
// should come from the artefact.
//
// Empty parts are dropped rather than rendered as a blank paragraph, so
// a half-filled Failure looks wrong to its author rather than merely
// spacious. Nothing this package exports has an empty part, and a test
// says so.
func (u *UI) renderFailure(f *Failure) string {
	if f == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if f.What != "" {
		parts = append(parts, u.styled(f.What))
	}
	if f.Why != "" {
		parts = append(parts, f.Why)
	}
	if f.Next != "" {
		parts = append(parts, f.Next)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// internalWhat, internalWhy and internalNext are the copy for a failure
// that is nobody's fault but ours. The user is told three things: it is
// not their project, there is a switch that shows more, and reporting it
// is useful.
const (
	internalWhat = "Something went wrong inside curious."

	internalWhy = "This is a fault in curious rather than a problem with your project,\n" +
		"so there is nothing here for you to fix."

	internalDebugWhy = "This is a fault in curious rather than a problem with your project.\n" +
		"The detail follows because " + debugEnvVar + " is set:"
)

// Internal renders an unexpected failure — the class of thing that has
// no user-facing explanation because nobody anticipated it.
//
// WHAT IT DOES NOT PRINT is the substance of this function. No stack
// trace, no Go type name, no wrapped-error dump: those tell a person
// deploying their first site nothing, and they make a tool look broken
// rather than sorry. CURIOUS_DEBUG is the single switch that adds
// detail, so the default can stay short without the detail being lost —
// and the message names the switch, so nobody has to know it in advance.
//
// Even under the switch, only err.Error() is printed: the error's own
// message, never a formatted struct and never a stack. Keeping a secret
// out of that message belongs to whoever CONSTRUCTS the error, which is
// what Secret is for — a Secret folded into an error's text renders as a
// placeholder here like everywhere else, and there is a test for exactly
// that.
func (u *UI) Internal(err error) {
	f := &Failure{
		What: internalWhat,
		Why:  internalWhy,
		Next: "Re-run with " + debugEnvVar + "=1 to see the detail, and please\n" +
			"report it with that output.",
	}

	if u.debug {
		detail := "no detail was recorded"
		if err != nil {
			detail = err.Error()
		}
		f.Why = internalDebugWhy + "\n\n  " + detail
		f.Next = "Please report this, with the detail above."
	}

	u.Fail(f)
}

// Cancelled is what the user sees when they cancelled: one short,
// lowercase word and nothing else. They know what they did — a
// paragraph explaining it, or a fault rendered as though something broke,
// would both be the program disagreeing with them about whose decision
// it was.
func (u *UI) Cancelled() {
	fmt.Fprintln(u.err, "cancelled")
}

// ExitCode renders err in this program's voice and returns the exit code
// the process should use. It is the single place that decides what a
// failure COSTS, so the answer cannot drift between commands.
//
// A cancellation exits 0. The user asked for the run to stop and it
// stopped; that is the program working, and a non-zero code would make
// every wrapper script treat a deliberate Ctrl-D as a fault.
func (u *UI) ExitCode(err error) int {
	switch {
	case err == nil:
		return 0

	case errors.Is(err, ErrAborted):
		u.Cancelled()
		return 0

	case errors.Is(err, ErrNotInteractive):
		u.Fail(notInteractiveFailure)
		return 1

	case errors.Is(err, ErrNoAnswer):
		// A person answering unusably is not a fault in this program,
		// and this case's absence was how they got told it was. Every
		// sentinel this package exports now has a mapping here; that is
		// the property to keep, and the test table below is written to
		// notice when it stops holding.
		u.Fail(noAnswerFailure)
		return 1
	}

	// A Failure carries its own copy, written by whoever owns the check
	// that produced it, so it renders as itself rather than as an
	// internal fault.
	var f *Failure
	hasCopy := errors.As(err, &f)

	// THE MARK DECIDES THE COST, THE COPY DECIDES THE WORDS, and this is
	// the one branch where those two answers come from different places.
	// A closed-door stop is marked by whoever met the condition and
	// worded by whoever owns the message — often not the same code — so
	// this reads the mark for the number and the wrapped Failure for the
	// text, and falls back to the standing copy only when there is none.
	//
	// It sits AFTER the errors.As above and BEFORE the ordinary Failure
	// branch, because a marked Failure satisfies both and only one of
	// them may answer.
	if errors.Is(err, ErrServerClosed) {
		if !hasCopy {
			f = serverClosedFailure
		}
		u.Fail(f)
		return ExitServerClosed
	}

	if hasCopy {
		u.Fail(f)
		return 1
	}

	u.Internal(err)
	return 1
}
