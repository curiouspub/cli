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

	// Detail is what SOMEBODY ELSE said — a server's own sentence, an
	// error's own text — and it is a field rather than something a
	// caller joins into Why because of what the renderer can then do
	// with it.
	//
	// Prose has layout: a Why is paragraphs, and its line breaks are
	// this program's. A quotation has none: every byte of it is content,
	// including a line break, which is why Detail is escaped WHOLE and
	// Why is escaped per line. Joined into one string by the caller the
	// two are indistinguishable, and a server sentence carrying a
	// newline could add a line that reads as ours — a second paragraph
	// in this program's voice, written by the far end.
	//
	// It renders FIRST, after the headline, because that is where every
	// call site already put it: the server says what happened and this
	// program says what it means.
	Detail string
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

// Quoted is a failure whose middle paragraph is somebody else's sentence
// and nothing of ours — the commonest shape by far, because where the
// server knows something this client does not, its words are the only
// thing that carries it.
func Quoted(what, detail, next string) *Failure {
	return &Failure{What: what, Detail: detail, Next: next}
}

// Quoting returns the failure with somebody else's sentence attached.
//
// It is a method rather than a fourth parameter so that the ordinary
// case — three paragraphs this program wrote — stays a three-argument
// call, and so the quotation is visible as a quotation at the call site
// rather than as one more string among four.
func (f *Failure) Quoting(detail string) *Failure {
	f.Detail = detail
	return f
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

// publishedFailures is every standing Failure this package can put in
// front of a person, keyed by the name it is declared under.
//
// IT EXISTS BECAUSE A HAND-TYPED LIST IS NOT A SET. The row that asserts
// every published failure names an action used to carry its own literal
// of three names under a comment claiming it checked them "as a SET". It
// was true when it was written and stopped being true the day
// serverClosedFailure was added — nobody edits a test in another package
// while adding copy, and nothing made them. That is a closed list built
// by a pattern, unable to contain the members that arrive after the
// pattern was chosen.
//
// So the set has one home, here, beside the values — and
// TestPublishedFailureSetIsComplete reads this package's own source to
// prove it holds every one of them. Adding a Failure without adding it
// here reds. That is what makes serverClosedFailure the last member a
// list ever silently drops.
var publishedFailures = map[string]*Failure{
	"notInteractiveFailure": notInteractiveFailure,
	"noAnswerFailure":       noAnswerFailure,
	"serverClosedFailure":   serverClosedFailure,
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
	// EVERY PART GOES THROUGH THE TABLE, and it happens here rather than
	// at any call site. A Failure's Why routinely carries the server's
	// own sentence, and a caller that has to remember to escape it is a
	// caller who will one day not: the sanitised deploy id and the
	// unsanitised message sitting in ONE string, three tokens apart, is
	// what this used to look like. Styling is applied after, so the two
	// escape sequences this program emits on purpose are the only ones
	// that reach the stream.
	rendered := f.Escaped()
	if len(rendered) == 0 {
		return ""
	}
	// THE STYLING IS APPLIED AFTER THE ESCAPING, so the two escape
	// sequences this program emits on purpose are the only ones that
	// reach the stream. Escaped hands back a fresh slice, so writing
	// into it cannot reach the failure.
	if f.What != "" {
		rendered[0] = u.styled(rendered[0])
	}
	return strings.Join(rendered, "\n\n") + "\n"
}

// Escaped is the failure's parts, in the order they are shown, with
// every one of them through the escape table.
//
// IT IS EXPORTED BECAUSE A TERMINAL IS NOT THE ONLY PLACE A FAILURE IS
// SHOWN. The agent-facing surface renders the same copy to a client that
// decodes it and a person who reads it, and a caller that joined
// Paragraphs itself would be handing a reader bytes that have been
// through nothing — which is the escaping boundary skipped at the one
// surface whose output is also kept in a model's context.
//
// THE QUOTATION IS ESCAPED WHOLE AND EVERYTHING ELSE PER LINE, which is
// the rule that cannot be moved outside this package: a Why is
// paragraphs and its line breaks are this program's, and a quotation has
// none — every byte of it is content, including a newline. Escaped per
// line, a server sentence carrying a blank line adds a paragraph that
// reads as ours, written by the far end. A caller holding only
// Paragraphs cannot apply that rule at all, because the slice does not
// say which part is the quotation.
//
// BY POSITION, NOT BY VALUE. Asking whether a paragraph EQUALS the
// quotation gets the right answer for the quotation and the wrong one
// for anything that happens to read the same: a Why identical to a
// Detail was escaped whole and lost its layout. Paragraphs drops
// empties, so the quotation's index is computed the same way.
func (f *Failure) Escaped() []string {
	parts := f.Paragraphs()
	if len(parts) == 0 {
		return nil
	}
	quoted := -1
	if f.Detail != "" {
		if f.What != "" {
			quoted = 1
		} else {
			quoted = 0
		}
	}
	rendered := make([]string, 0, len(parts))
	for i, part := range parts {
		if i == quoted {
			// WHOLE, newline included. See the field.
			rendered = append(rendered, Sanitize(part))
			continue
		}
		rendered = append(rendered, SanitizeLines(part))
	}
	return rendered
}

// Paragraphs is the failure's parts in the order they are shown, empties
// dropped. It is EXPORTED because it is the only honest way for anything
// outside this package to know what a failure says.
//
// A test that joins What, Why and Next has reconstructed the rendering
// rather than read it, and a reconstruction drifts: this one did, the
// day a fourth part arrived, and it went on reporting that a server's
// message was missing from output the shipped renderer was putting it
// in. That is the same reason renderFailure exists at all — an assertion
// about an artefact should come from the artefact — arriving one package
// over. There is now one place that decides the order, and both the
// renderer and anybody asking use it.
func (f *Failure) Paragraphs() []string {
	if f == nil {
		return nil
	}
	parts := make([]string, 0, 4)
	for _, part := range []string{f.What, f.Detail, f.Why, f.Next} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
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

	case errors.Is(err, ErrInterrupted):
		// The same word as a cancellation and a different number: the
		// run did not finish, and a wrapper that reads the status has
		// to be able to tell.
		u.Cancelled()
		return interruptExitCode

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
