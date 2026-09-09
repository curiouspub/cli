package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// UI owns every byte this program shows a person, and the one place it
// reads a person's answer from. It exists so the stream split, the
// prompt defaults, the colour decision and the failure shape are decided
// ONCE — a program whose every command rolls its own prompt drifts into
// several programs wearing one name.
//
// THE STREAM SPLIT, which is the rule the rest of the type is arranged
// around: narration, prompts and failures go to STDERR; only
// machine-consumable output goes to STDOUT. Somebody piping stdout into
// a file or another program still sees the prompt they are being asked
// to answer, and still sees why the run stopped. The alternative —
// prompting on stdout — produces the worst possible bug report: "it
// hangs", because the question went into the pipe.
type UI struct {
	// out is machine-consumable output only; err is everything a person
	// reads. See the type comment: the split is the point.
	out io.Writer
	err io.Writer

	// reader is built ONCE and reused by every prompt. A bufio.Reader
	// reads ahead, so a fresh one per prompt discards whatever the
	// previous read buffered past its newline — which is invisible
	// against a terminal (a person types one line at a time and the
	// buffer is empty when the next prompt starts) and eats the second
	// answer of every scripted run. The bug would be found by a user,
	// not by us.
	reader *bufio.Reader

	// interactive is the whole basis of "may I ask a question": stdin
	// AND stderr are both terminals. See interactiveStreams.
	interactive bool

	// colour reports whether escape sequences may be written. Every
	// message must read correctly without them — see styled.
	colour bool

	// debug reports whether CURIOUS_DEBUG is set. It is the ONLY thing
	// that adds detail to an unexpected internal failure, so that the
	// default rendering can stay short without the detail being lost.
	debug bool

	// restore puts the terminal back the way it was found. It is a no-op
	// unless stdin is a real terminal whose state could be captured, and
	// it exists for the interrupt handler: a program killed mid-prompt
	// must not leave a shell with echo off.
	restore func()

	// endInterrupted ends a run that was interrupted, in the way this
	// platform's shells understand — see terminateInterrupted, which has
	// one file per platform because the right answer differs. It is a
	// field so a test can observe that it was reached without the test
	// process being killed by its own assertion.
	endInterrupted func()

	// exit ends the process. It is a field so the interrupt path can be
	// tested for the code it produces rather than for the fact that it
	// terminated the test binary.
	exit func(int)
}

// Writing returns a UI that renders to the given streams and asks
// nothing: not interactive, no colour, no environment.
//
// IT EXISTS SO A TEST DOUBLE NEED NOT BE A SECOND IMPLEMENTATION. A
// double that formats a message the way this package formats one is a
// second copy of the rendering, and the two drift — this repository has
// the instance: the flow suite's terminal double called Sprintf and
// wrote the result, so it escaped nothing, and the day the boundary
// began escaping arguments it went on reporting clean output for a
// shipped path that was mangling every multi-paragraph narration. A
// double that DELEGATES cannot drift.
//
// It is not a testing-only door in the ordinary sense: what it removes
// is a second renderer, which is a liability wherever it lives.
func Writing(out, err io.Writer) *UI {
	return newUI(strings.NewReader(""), out, err,
		func(string) (string, bool) { return "", false }, false,
		func() bool { return false })
}

// Help writes this program's own prose to STDOUT, keeping its layout.
//
// IT IS THE ONE EXCEPTION to "stdout is one record per line", and it has
// exactly one caller: the usage text, which is prose a person reads and
// which goes to stdout so that `curious -h | less` shows something. A
// Result would escape its line breaks, because on that stream a line
// break inside one record is a second record. This does not, and takes
// a Prose so that the exception can only be reached deliberately.
func (u *UI) Help(text Prose) {
	fmt.Fprint(u.out, sanitizeLines(string(text)))
}

// New returns a UI wired to the given streams and this process's
// environment.
//
// IT TAKES ITS STREAMS RATHER THAN READING THEM, so that the process's
// own stdout and stderr are named in exactly one place — main — and
// every byte a person sees has passed through here. It used to read
// os.Stdout and os.Stderr itself, which made this package a second
// place the real streams were obtained and left the entry point free to
// keep its own copies: the flag parser wrote errors straight to stderr
// before this ever existed, and the MCP server was handed a raw one.
//
// It is still the only constructor that touches the operating system,
// for the environment and for the terminal questions: newUI takes the
// answers as arguments, and this function is where they are obtained.
func New(in io.Reader, out, errw io.Writer) *UI {
	u := newUI(in, out, errw, os.LookupEnv,
		interactiveStreams(in, errw),
		func() bool { return terminalUnderstandsEscapes(errw) })

	// Capture the terminal's state now, while it is certainly untouched,
	// so the interrupt handler has something to put back. GetState fails
	// on anything that is not a terminal, and that failure is not an
	// error condition — it means there is nothing to restore.
	if fd, ok := terminalFd(in); ok {
		if state, err := term.GetState(fd); err == nil {
			u.restore = func() { _ = term.Restore(fd, state) }
		}
	}
	return u
}

// newUI builds a UI from explicit streams, an explicit environment
// lookup and an explicit answer to "is this interactive". Every input
// this type's behaviour depends on arrives through this signature, so a
// test configures a UI by calling it rather than by arranging a
// terminal, which is not a thing a test can portably arrange.
func newUI(in io.Reader, out, errw io.Writer, lookupEnv func(string) (string, bool), interactive bool, escapesUnderstood func() bool) *UI {
	debug := debugEnabled(lookupEnv)
	u := &UI{
		out:         out,
		err:         errw,
		reader:      bufio.NewReader(in),
		interactive: interactive,
		colour:      colourEnabled(interactive, lookupEnv, escapesUnderstood),
		debug:       debug,
		restore:     func() {},
		exit:        os.Exit,
	}
	u.endInterrupted = func() { terminateInterrupted(u) }
	return u
}

// debugEnvVar names the variable that adds detail to an unexpected
// internal failure. It is the only one, and Internal's own copy names it
// to the user, so the two cannot drift.
const debugEnvVar = "CURIOUS_DEBUG"

// debugEnabled reads CURIOUS_DEBUG by VALUE, not by presence.
//
// The distinction matters here where it does not for NO_COLOR, and the
// difference is what the variable buys. Colour is decoration: switching
// it off costs a reader nothing, so treating any presence as "off" is
// generous in the harmless direction, and NO_COLOR's own convention is
// widely written that way. Debug is not decoration — it changes the copy
// a person sees, dropping the line that says there is nothing here for
// them to fix, and it prints an error's own text verbatim. Turning that
// on because a shell exported CURIOUS_DEBUG= from an unset variable, or
// because somebody wrote =0 meaning off, is the program disagreeing with
// a plain instruction.
//
// So the falsey spellings are refused, and the message the program
// prints — "re-run with CURIOUS_DEBUG=1" — is now true of the code
// rather than merely near it.
func debugEnabled(lookupEnv func(string) (string, bool)) bool {
	value, set := lookupEnv(debugEnvVar)
	if !set {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// noColourEnvVar and termEnvVar are the two environment answers that
// switch styling off. Named constants rather than inline strings because
// colourEnabled's doc comment argues about them and a reader should be
// able to find every use.
const (
	noColourEnvVar = "NO_COLOR"
	termEnvVar     = "TERM"
)

// Interactive reports whether this UI may ask a question. A caller that
// needs to CHOOSE a path — prompt, or take a documented non-interactive
// route — asks this; a caller that simply wants to prompt should just
// prompt and handle ErrNotInteractive, because a check followed by a
// call has a gap between them and the prompt is the honest place for the
// decision.
func (u *UI) Interactive() bool { return u.interactive }

// terminalFd extracts the file descriptor a terminal check needs, and
// reports whether the value even has one. A bytes.Buffer, a
// strings.Reader and an io.Pipe do not, and neither does a nil
// interface: all three are "not a terminal" rather than an error.
func terminalFd(stream any) (int, bool) {
	f, ok := stream.(*os.File)
	if !ok || f == nil {
		return 0, false
	}
	return int(f.Fd()), true
}

// interactiveStreams reports whether a person is on the other end of
// BOTH halves of a prompt: the question is written to stderr and the
// answer is read from stdin, so a terminal on only one of them is not
// enough. With stderr redirected the user answers a question they never
// saw; with stdin redirected the program waits for a person who is not
// typing.
//
// WHY golang.org/x/term RATHER THAN THE STANDARD LIBRARY, recorded here
// because this is the module's first third-party dependency and the
// dependency policy asks for the argument at the point of use.
//
// On Unix the standard library does answer: os.Stat on the stream and a
// test of ModeCharDevice. On Windows it does not, because a character
// device is a different question from a console — the answer there is
// GetConsoleMode, which lives in golang.org/x/sys/windows. Reaching for
// x/sys to hand-roll what x/term already wraps is ONE dependency either
// way and the worse of the two, since it means owning Windows console
// handling in a repository that has already been bitten by Windows twice
// (a checkout's line endings, and an environment variable whose name is
// case-insensitive there). The narrower library that does exactly this
// is the cheaper answer.
//
// The standard-library shortcut is also wrong in a way that is easy to
// miss even on Unix: the null device IS a character device and is NOT a
// terminal, so ModeCharDevice answers "yes" for a stream nobody can type
// into. There is a test for precisely that, because an argument for a
// dependency should be checkable rather than merely written down.
func interactiveStreams(in io.Reader, errw io.Writer) bool {
	return bothTerminals(in, errw, isTerminal)
}

// terminalCheck reports whether one stream is a terminal.
type terminalCheck func(stream any) bool

// bothTerminals is interactiveStreams with the terminal question handed
// in, and it exists because the AND is a rule in its own right and
// nothing else could pin it. A real terminal is not something a test can
// portably arrange — Windows has no pty to open — so with the check
// wired in directly, "requires both streams" and "requires stdin" would
// be indistinguishable from every test this repository can run on every
// platform it ships to. A seam that makes a required mutation
// expressible is worth more than the indirection costs.
func bothTerminals(in, errw any, isTerm terminalCheck) bool {
	return isTerm(in) && isTerm(errw)
}

// isTerminal reports whether a stream is a terminal a person can see or
// type into. Anything that is not an *os.File is not, without asking the
// operating system.
func isTerminal(stream any) bool {
	fd, ok := terminalFd(stream)
	return ok && term.IsTerminal(fd)
}

// colourEnabled decides whether escape sequences may be written at all.
// Three answers switch styling off, and the order below is the order
// they are cheapest to be sure about:
//
//   - Not interactive. Escapes written into a pipe, a file or a CI log
//     are noise in the one place the output is read most carefully.
//   - NO_COLOR is set. This implementation treats the variable as set
//     REGARDLESS OF ITS VALUE, which is deliberately wider than the
//     published convention's "set to a non-empty string". An empty
//     NO_COLOR in somebody's environment is far more likely to mean
//     they asked for no colour than to mean they want it; styling is
//     decoration, so the cost of reading the ambiguous case that way is
//     zero and the cost of reading it the other way is ignoring a
//     request.
//   - TERM is dumb. That is the terminal telling us so itself.
//
// The switch governs ALL escape styling rather than hue alone, which is
// how the variable is understood in practice: a reader who asked for no
// escape sequences did not mean "bold is fine".
func colourEnabled(interactive bool, lookupEnv func(string) (string, bool), escapesUnderstood func() bool) bool {
	if !interactive {
		return false
	}
	if _, set := lookupEnv(noColourEnvVar); set {
		return false
	}
	if value, _ := lookupEnv(termEnvVar); value == "dumb" {
		return false
	}
	// Last, and only about the stream this program will actually write
	// escapes to: does the terminal interpret them at all? Everywhere
	// but Windows that is yes by construction. On Windows it is a
	// question with a real answer, and asking it is the difference
	// between a bold headline and a headline reading
	// "<ESC>[1mNo lockfile found.<ESC>[0m". See
	// terminalUnderstandsEscapes.
	//
	// It arrives as a function rather than as the writer because CI
	// cannot exercise the Windows half — a test process there has no
	// console attached, so the real gate would answer "no" on every row
	// and the table would be measuring the runner rather than this
	// logic. A seam lets the table state what it means, and lets one
	// dedicated row assert that the gate is wired at all.
	return escapesUnderstood()
}

// boldSequence and resetSequence are the only escape sequences this
// program writes. One attribute, no hue: a headline needs to stand out,
// and a colour that stands out on one terminal theme is unreadable on
// another. Decoration that can make a message harder to read is not
// decoration.
const (
	boldSequence  = "\x1b[1m"
	resetSequence = "\x1b[0m"
)

// styled returns s emphasised when styling is on and s UNCHANGED when it
// is off — never a different string, never different words. That is the
// whole contract: every message must read correctly with all escapes
// stripped, because stripped is how it arrives in a bug report, in a CI
// log and in a screenshot pasted into a chat window.
func (u *UI) styled(s string) string {
	if !u.colour {
		return s
	}
	return boldSequence + s + resetSequence
}

// Written composes a paragraph of this program's prose that has values
// in it, and it is how a failure carries an identifier.
//
// A FAILURE'S PROSE KEEPS ITS LINE BREAKS, because they are the layout;
// its quotation does not, because every byte of that is content. An
// identifier sat in the first of those — "The deploy is " + id + "." —
// so it kept ITS line breaks too, and a server-supplied id carrying one
// could add a paragraph in this program's voice. That was the residue
// left when the quotation became a field, disclosed at the time.
//
// This closes it without moving the sentence: the FORMAT is the
// caller's own copy, layout and all, and every string argument is
// escaped whole before it is placed in. go vet holds the seam, because
// this forwards its own format and variadic to Sprintf.
func Written(format string, args ...any) string {
	escapeInPlace(args)
	return fmt.Sprintf(format, args...)
}

// Prose is THIS PROGRAM'S OWN WORDS, marked as such.
//
// AN ARGUMENT IS ESCAPED WHOLE, newline included, because an argument is
// where somebody else's text arrives. But some of this program's own
// copy is composed at RUN TIME and cannot be written into the format: a
// pack receipt, a pre-flight finding, the closing narration with its
// optional expiry line. Passed as a plain string its paragraphs become
// visible backslash-n; passed as the format it is a non-constant format
// string, which go vet refuses on purpose.
//
// So it is passed as a Prose. It is not a string, so the argument
// escaping leaves it alone, and the rendered line still goes through the
// escape table per line: the layout is kept and everything a terminal
// obeys is still inert.
//
// MISUSING IT IS THE ONE HOLE IT OPENS. Prose around somebody else's
// sentence would let a newline through, so a guard refuses that — and
// forgetting it produces a visibly mangled paragraph rather than a quiet
// hole, which is the right way round for a mistake to fail.
type Prose string

// Step writes one line of narration to stderr.
//
// PLAIN SEQUENTIAL LINES, and that is a decision rather than a stage on
// the way to something prettier. No spinner, no carriage return, no
// cursor movement: those are why CI logs look like line noise, they are
// meaningless to the screen reader and the log file that will actually
// carry this output, and nothing in the CLI's flow is slow enough to
// need one. A line that has been printed stays printed.
func (u *UI) Step(format string, args ...any) {
	escapeInPlace(args)
	fmt.Fprintln(u.err, sanitizeLines(fmt.Sprintf(format, args...)))
}

// Result writes machine-consumable output to STDOUT — the other half of
// the stream split, and the only thing that belongs there. If a person
// would read it as prose, it is a Step.
func (u *UI) Result(format string, args ...any) {
	// THE WHOLE LINE, newline included, because stdout is one record per
	// line and a newline inside one record is a second record somebody
	// else wrote. Step's prose may have line breaks in it; a machine-read
	// line may not.
	escapeInPlace(args)
	fmt.Fprintln(u.out, Sanitize(fmt.Sprintf(format, args...)))
}

// compose is the one place a message is assembled out of this program's
// words and somebody else's, and it treats the two differently.
//
// AN ARGUMENT IS THE VARIABLE HALF. Every call site in this program
// writes its prose as the format — a compile-time literal — and passes
// the parts it did not write as arguments: a server's status, a label,
// an address, an error's own text. So every argument goes through the
// whole escape table, newline included, before it is interpolated. A
// line the far end added to a status cannot become a line of this
// program's narration.
//
// WITH NO ARGUMENTS THERE IS NOTHING TO INTERPOLATE, and the format is
// this program's own prose: it is written out as it stands. That also
// makes `Step(text)` safe for copy containing a per-cent sign, which is
// why several call sites that would otherwise need a "%s" can simply
// pass their text.
//
// What it cannot do is separate copy from data inside a string a caller
// has already joined — a *Failure carries three assembled paragraphs, so
// its layout is preserved and a newline arriving inside one of them is
// preserved with it. That is the remaining edge, and closing it means a
// Failure that carries its data as data.
// THE SHAPE OF THE TWO METHODS ABOVE IS NOT AN ACCIDENT, and this is the
// paragraph that says why, because the obvious tidying breaks it.
//
// go vet's printf analyser finds a wrapper by looking for a function
// whose last two parameters are a format string and a variadic, and
// whose body hands BOTH OF THOSE SAME VARIABLES to a print function.
// Once it has found one, `Step(somebodyElsesSentence)` — a non-constant
// format with nothing to interpolate — is reported wherever it is
// written, in every package, by `go vet ./...` and nothing else.
//
// That is the seam the format/argument split leaves open. The split
// makes an ARGUMENT safe; a caller who puts the same text in the FORMAT
// has moved somebody else's words into this program's prose, and no
// escaping can tell the difference because by then the two are one
// string. The analyser can, and it is the only thing that can.
//
// SO THE ESCAPING IS IN PLACE. Measured, on this analyser, with a probe
// kept beside the guard: a method forwarding `format, args...` is
// detected; the same method with `args = somethingElse(args)` first is
// NOT, and neither `-printf.funcs=Step,Result` nor its qualified
// spellings put the detection back. Mutating the elements and
// forwarding the same slice keeps it.
//
// WHAT IN-PLACE COSTS, and how it is paid: a caller who spreads a slice
// — `Step(f, xs...)` — passes that very slice, so escaping its elements
// would reach back into the caller's own values. No call site does, and
// a guard says so rather than a comment hoping.
func escapeInPlace(args []any) {

	// ONLY THE STRINGS, and every other argument is handed to its verb
	// untouched. Rendering a duration, a count or a Secret through
	// fmt.Sprint first and escaping the result would make %T print
	// "string", make %x hex-encode a rendering rather than a value, and
	// — the one that matters — would put a Secret through a path other
	// than the one its own tests range over. A string is where somebody
	// else's bytes actually arrive.
	for i, arg := range args {
		if text, ok := arg.(string); ok {
			args[i] = Sanitize(text)
		}
	}
}
