package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"

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

// New returns a UI wired to this process's own streams and environment.
//
// It is the only constructor that touches the operating system, which is
// what keeps every decision below testable: newUI takes the answers as
// arguments, and this function is where they are actually obtained.
func New() *UI {
	u := newUI(os.Stdin, os.Stdout, os.Stderr, os.LookupEnv,
		interactiveStreams(os.Stdin, os.Stderr))

	// Capture the terminal's state now, while it is certainly untouched,
	// so the interrupt handler has something to put back. GetState fails
	// on anything that is not a terminal, and that failure is not an
	// error condition — it means there is nothing to restore.
	if fd, ok := terminalFd(os.Stdin); ok {
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
func newUI(in io.Reader, out, errw io.Writer, lookupEnv func(string) (string, bool), interactive bool) *UI {
	_, debug := lookupEnv(debugEnvVar)
	u := &UI{
		out:         out,
		err:         errw,
		reader:      bufio.NewReader(in),
		interactive: interactive,
		colour:      colourEnabled(interactive, lookupEnv),
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
func colourEnabled(interactive bool, lookupEnv func(string) (string, bool)) bool {
	if !interactive {
		return false
	}
	if _, set := lookupEnv(noColourEnvVar); set {
		return false
	}
	if value, _ := lookupEnv(termEnvVar); value == "dumb" {
		return false
	}
	return true
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

// Step writes one line of narration to stderr.
//
// PLAIN SEQUENTIAL LINES, and that is a decision rather than a stage on
// the way to something prettier. No spinner, no carriage return, no
// cursor movement: those are why CI logs look like line noise, they are
// meaningless to the screen reader and the log file that will actually
// carry this output, and nothing in the CLI's flow is slow enough to
// need one. A line that has been printed stays printed.
func (u *UI) Step(format string, args ...any) {
	fmt.Fprintf(u.err, format+"\n", args...)
}

// Result writes machine-consumable output to STDOUT — the other half of
// the stream split, and the only thing that belongs there. If a person
// would read it as prose, it is a Step.
func (u *UI) Result(format string, args ...any) {
	fmt.Fprintf(u.out, format+"\n", args...)
}
