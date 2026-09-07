package ui

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
)

// testUI builds a UI over string input and two buffers, with
// interactivity and the environment stated outright. Arranging a real
// terminal is not something a test can portably do — Windows has no pty
// at all — so the answer newUI would have computed is supplied instead,
// and interactiveStreams is tested separately against the streams a test
// CAN produce.
func testUI(input string, interactive bool, env map[string]string) (*UI, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	lookup := func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
	u := newUI(strings.NewReader(input), &out, &errOut, lookup, interactive)
	return u, &out, &errOut
}

// TestStreamSplit holds the rule the whole type is arranged around:
// prose to stderr, machine-consumable output to stdout. Both directions
// are asserted, because "narration never reaches stdout" is also true of
// a UI that writes nothing at all.
func TestStreamSplit(t *testing.T) {
	t.Run("narration goes to stderr", func(t *testing.T) {
		u, out, errOut := testUI("", false, nil)
		u.Step("packing %d files", 12)

		// REQUIRED MUTATION: write to u.out in Step.
		if !strings.Contains(errOut.String(), "packing 12 files") {
			t.Errorf("narration did not reach stderr; stderr was %q", errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("narration reached stdout: %q — somebody piping stdout into a "+
				"program would be feeding it prose", out.String())
		}
	})

	t.Run("results go to stdout", func(t *testing.T) {
		u, out, errOut := testUI("", false, nil)
		u.Result("%s", "brave-otter-9c1d")

		// REQUIRED MUTATION: write to u.err in Result.
		if !strings.Contains(out.String(), "brave-otter-9c1d") {
			t.Errorf("the result did not reach stdout; stdout was %q", out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("the result reached stderr: %q — a caller redirecting stdout "+
				"would capture nothing", errOut.String())
		}
	})
}

// renderSample writes one of everything this package can put on a
// terminal, so a comparison of two runs compares the whole surface
// rather than whichever call the author happened to pick.
func renderSample(u *UI) {
	u.Step("packing 12 files")
	u.Fail(NoLockfile)
	u.Cancelled()
}

// TestColourOffIsByteIdenticalToNoTerminal is the row that keeps colour
// honest: with styling off, output is not merely "similar" to the
// non-terminal rendering, it is the same bytes. Anything less and an
// escape sequence eventually survives into a log, a bug report or a
// screen reader.
//
// The fourth case is the positive control and the test is worthless
// without it. Three renderings that match each other would also match if
// this package emitted no styling at all under any condition, and then
// the assertion would be measuring nothing forever.
func TestColourOffIsByteIdenticalToNoTerminal(t *testing.T) {
	render := func(interactive bool, env map[string]string) string {
		u, _, errOut := testUI("", interactive, env)
		renderSample(u)
		return errOut.String()
	}

	noTerminal := render(false, nil)

	styled := render(true, nil)
	// REQUIRED MUTATION: make styled() return the emphasised string
	// unconditionally, ignoring u.colour. This control stays green and
	// every comparison below goes red — which is the pairing that says
	// the comparisons are about the switch rather than about a renderer
	// that never styles anything.
	if styled == noTerminal {
		t.Fatalf("an interactive run with no colour variables set produced the same "+
			"bytes as a non-terminal run, so this test cannot observe styling at "+
			"all:\n%q", styled)
	}
	if !strings.Contains(styled, "\x1b") {
		t.Fatalf("an interactive run emitted no escape sequence: %q", styled)
	}

	cases := []struct {
		name string
		env  map[string]string
	}{
		{"NO_COLOR set to 1", map[string]string{noColourEnvVar: "1"}},
		{"NO_COLOR set to the empty string", map[string]string{noColourEnvVar: ""}},
		{"NO_COLOR set to 0", map[string]string{noColourEnvVar: "0"}},
		{"TERM is dumb", map[string]string{termEnvVar: "dumb"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render(true, tc.env)
			if got != noTerminal {
				t.Errorf("output differs from a non-terminal run\n--- got ---\n%q\n"+
					"--- want ---\n%q", got, noTerminal)
			}
			if strings.Contains(got, "\x1b") {
				t.Errorf("an escape sequence survived: %q", got)
			}
		})
	}

	if strings.Contains(noTerminal, "\x1b") {
		t.Errorf("a non-terminal run emitted an escape sequence: %q", noTerminal)
	}
}

// TestColourEnabled pins the decision itself, one condition at a time,
// including the two that a rendering comparison cannot separate: a
// TERM the terminal is happy with, and the ordering that makes
// interactivity the first question.
func TestColourEnabled(t *testing.T) {
	cases := []struct {
		name        string
		interactive bool
		env         map[string]string
		want        bool
	}{
		{"interactive with an ordinary TERM", true, map[string]string{termEnvVar: "xterm-256color"}, true},
		{"interactive with no variables at all", true, nil, true},
		// REQUIRED MUTATION: delete the !interactive branch in
		// colourEnabled. This row goes red on its own.
		{"not interactive", false, nil, false},
		{"not interactive, ordinary TERM", false, map[string]string{termEnvVar: "xterm-256color"}, false},
		// REQUIRED MUTATION: test NO_COLOR's VALUE rather than its
		// presence — the empty and the zero rows go red.
		{"NO_COLOR present with a value", true, map[string]string{noColourEnvVar: "1"}, false},
		{"NO_COLOR present and empty", true, map[string]string{noColourEnvVar: ""}, false},
		{"NO_COLOR present and zero", true, map[string]string{noColourEnvVar: "0"}, false},
		// REQUIRED MUTATION: compare TERM against something other than
		// "dumb" — the dumb row goes red and the not-dumb row stays
		// green.
		{"TERM is dumb", true, map[string]string{termEnvVar: "dumb"}, false},
		{"TERM merely starts with dumb", true, map[string]string{termEnvVar: "dumb-but-not"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				v, ok := tc.env[name]
				return v, ok
			}
			if got := colourEnabled(tc.interactive, lookup); got != tc.want {
				t.Errorf("colourEnabled(%v, %v) = %v, want %v",
					tc.interactive, tc.env, got, tc.want)
			}
		})
	}
}

// TestStyledChangesNoWords is the contract that lets colour be pure
// decoration: emphasis wraps the text, it never rewrites it. Stripping
// the escapes must give the original back, because stripped is how the
// message arrives in a bug report.
func TestStyledChangesNoWords(t *testing.T) {
	const text = "No lockfile found."

	plain, _, _ := testUI("", false, nil)
	if got := plain.styled(text); got != text {
		t.Errorf("with styling off, styled(%q) = %q, want it unchanged", text, got)
	}

	coloured, _, _ := testUI("", true, nil)
	got := coloured.styled(text)
	if got == text {
		t.Fatalf("with styling on, styled(%q) returned it unchanged — this test "+
			"cannot observe styling", text)
	}
	stripped := strings.NewReplacer(boldSequence, "", resetSequence, "").Replace(got)
	if stripped != text {
		t.Errorf("stripping the escapes from %q gave %q, want %q — styling must not "+
			"change a single word", got, stripped, text)
	}
}

// hasCursorControl reports whether s contains a carriage return or an
// escape sequence that moves or erases rather than styles.
func hasCursorControl(s string) bool {
	if strings.Contains(s, "\r") {
		return true
	}
	for _, seq := range []string{"\x1b[A", "\x1b[B", "\x1b[C", "\x1b[D", "\x1b[K", "\x1b[2J", "\x1b[?25l"} {
		if strings.Contains(s, seq) {
			return true
		}
	}
	return false
}

// TestProgressOutputIsPlainLines holds the no-spinner decision. Spinners
// are why CI logs read as line noise, they are meaningless to the log
// file and the screen reader that will actually carry this output, and
// nothing in this program's flow is slow enough to need one.
//
// The detector is exercised against fabricated input in the same test.
// A scan for absence that cannot be shown to find anything is the same
// thing as no scan at all, and it passes forever.
func TestProgressOutputIsPlainLines(t *testing.T) {
	u, _, errOut := testUI("", true, nil)
	u.Step("packing 12 files")
	u.Step("uploading")
	u.Fail(NoLockfile)

	if hasCursorControl(errOut.String()) {
		t.Errorf("output carries a carriage return or a cursor sequence: %q", errOut.String())
	}

	for _, sample := range []string{
		"packing... \r",
		"one line\x1b[A",
		"clear me\x1b[K",
		"hide the cursor\x1b[?25l",
	} {
		if !hasCursorControl(sample) {
			t.Errorf("the cursor-control scan does not match %q, so its silence "+
				"above is not evidence of anything", sample)
		}
	}

	// The lines are also complete lines: a progress line that has been
	// printed stays printed, so each one ends.
	if !strings.HasSuffix(errOut.String(), "\n") {
		t.Errorf("output does not end in a newline: %q", errOut.String())
	}
}

// TestIsTerminalSaysNoToEverythingThatIsNotOne covers the streams a test
// can actually produce. None of them is a terminal, and the check must
// reach that answer without asking the operating system anything it
// cannot answer.
func TestIsTerminalSaysNoToEverythingThatIsNotOne(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating a pipe: %v", err)
	}
	defer read.Close()
	defer write.Close()

	var nilFile *os.File
	cases := []struct {
		name   string
		stream any
	}{
		{"the read end of a pipe", read},
		{"the write end of a pipe", write},
		{"a buffer", &bytes.Buffer{}},
		{"a string reader", strings.NewReader("y\n")},
		{"a nil interface", nil},
		{"a typed nil file", nilFile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isTerminal(tc.stream) {
				t.Errorf("isTerminal reported %s as a terminal", tc.name)
			}
		})
	}
}

// TestACharacterDeviceIsNotATerminal is the empirical half of this
// module's dependency argument, and the reason it is a test rather than
// a paragraph.
//
// The standard-library way to answer "is this a terminal" without a
// dependency is os.Stat plus ModeCharDevice. The null device is a
// character device on every platform this ships to and is a terminal on
// none of them, so that shortcut answers YES for a stream nobody can
// type into — and the failure would be a prompt written into a void,
// waiting forever for an answer that cannot come.
//
// If the null device is not reported as a character device on some
// platform, the comparison this row makes does not exist there and it
// SKIPS rather than passing: a skip says the question was not asked,
// which is true, where a pass would claim something was measured.
func TestACharacterDeviceIsNotATerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening the null device: %v", err)
	}
	defer devNull.Close()

	info, err := devNull.Stat()
	if err != nil {
		t.Fatalf("stat on the null device: %v", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Skipf("the null device is not reported as a character device on %s, so the "+
			"standard-library comparison this row makes cannot be made here",
			runtime.GOOS)
	}

	if isTerminal(devNull) {
		t.Error("the null device was reported as a terminal")
	}
}

// TestInteractivityNeedsBothStreams pins the AND. A terminal on stdin
// alone means the question is written somewhere the user is not looking;
// a terminal on stderr alone means the program waits for a person who is
// not typing. Either way there is no conversation, and either way the
// right answer is to refuse rather than to prompt.
func TestInteractivityNeedsBothStreams(t *testing.T) {
	in := strings.NewReader("")
	errw := &bytes.Buffer{}

	cases := []struct {
		name              string
		inIsTerm, errTerm bool
		want              bool
	}{
		{"both", true, true, true},
		// REQUIRED MUTATION: change the && in bothTerminals to ||. The
		// two middle rows go red and the outer two stay green.
		{"stdin only", true, false, false},
		{"stderr only", false, true, false},
		{"neither", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := func(stream any) bool {
				if stream == any(in) {
					return tc.inIsTerm
				}
				return tc.errTerm
			}
			if got := bothTerminals(in, errw, check); got != tc.want {
				t.Errorf("bothTerminals(stdin=%v, stderr=%v) = %v, want %v",
					tc.inIsTerm, tc.errTerm, got, tc.want)
			}
		})
	}
}

// TestNewReadsTheRealEnvironment is the row that says New actually wires
// the process's own environment through, rather than every other test
// here checking a lookup function that only tests supply.
//
// CURIOUS_DEBUG is the variable it uses, because it is the one whose
// effect survives a non-terminal test process: colour is off in `go
// test` whatever NO_COLOR says, so setting that would prove nothing.
//
// t.Setenv restores the variable afterwards and nothing here clears a
// second spelling of it. Environment variable names are case-insensitive
// on Windows, so "tidying up" a lowercase twin there deletes the value
// just set — a harness bug that reds one platform and no other.
func TestNewReadsTheRealEnvironment(t *testing.T) {
	t.Setenv(debugEnvVar, "1")

	u := New()
	if !u.debug {
		t.Errorf("New did not see %s in the process environment", debugEnvVar)
	}
	if u.restore == nil {
		t.Error("New left restore nil — the interrupt handler would panic on Ctrl-C")
	}
	if u.exit == nil {
		t.Error("New left exit nil — the interrupt handler could not end the process")
	}
	if u.reader == nil {
		t.Error("New left the input reader nil")
	}
}

// TestNewWithoutTheDebugVariable is the other half of the row above: the
// flag must come FROM the environment rather than being on by default,
// and one test showing it true cannot tell those apart.
func TestNewWithoutTheDebugVariable(t *testing.T) {
	// Setting it empty and then clearing it keeps t.Setenv's restore
	// behaviour without this test depending on what the operator's own
	// environment happens to hold.
	t.Setenv(debugEnvVar, "")
	os.Unsetenv(debugEnvVar)

	if u := New(); u.debug {
		t.Errorf("New reported debug with %s unset", debugEnvVar)
	}
}
