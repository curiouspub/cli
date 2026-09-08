package ui

import (
	"bytes"
	"errors"
	"fmt"
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
	// The escape gate is injected here for the same reason lookupEnv is:
	// the real one asks the console, and a test's stderr is a buffer.
	// Left to the real gate, EVERY styling assertion on Windows would
	// answer "no colour" for a reason none of them is about — which is
	// exactly what happened on the matrix once the gate landed, and it
	// took two positive controls with it. The gate has its own row.
	u := newUI(strings.NewReader(input), &out, &errOut, lookup, interactive, alwaysUnderstood)
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
	u.Fail(&threePartSample)
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
			// The escape gate answers yes for these rows: each is about
			// one of the OTHER reasons colour is off, and a gate that
			// said no would make every row pass for a reason none of
			// them is about. The gate itself has its own row below.
			if got := colourEnabled(tc.interactive, lookup, alwaysUnderstood); got != tc.want {
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
	u.Fail(&threePartSample)

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
// character device and is a terminal on none of the platforms this
// ships to, so that shortcut answers YES for a stream nobody can type
// into — and the failure would be a prompt written into a void, waiting
// forever for an answer that cannot come.
//
// THE ASSERTION THAT MATTERS NEEDS NO PREMISE and therefore runs
// everywhere: the null device is not a terminal, whatever any platform
// reports about its mode.
//
// The character-device fact is what makes this row a COMPARISON with
// the standard library rather than a bare check, and it is REPORTED
// rather than made a condition for running. An earlier draft skipped
// the whole row where the mode was not set, which was wrong for a
// reason worth writing down: this repository's suite runs without -v,
// so a skip prints nothing at all. "Measured" and "not asked" would
// have looked identical in the one output anybody reads, which is the
// failure a named skip exists to prevent, arriving through the skip.
func TestACharacterDeviceIsNotATerminal(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening the null device: %v", err)
	}
	defer devNull.Close()

	// REQUIRED MUTATION: make isTerminal return true for any *os.File
	// without consulting term.IsTerminal.
	if isTerminal(devNull) {
		t.Error("the null device was reported as a terminal — a prompt would be " +
			"written into a void and wait forever for an answer")
	}

	info, err := devNull.Stat()
	if err != nil {
		t.Fatalf("stat on the null device: %v", err)
	}
	charDevice := info.Mode()&os.ModeCharDevice != 0
	t.Logf("on %s the null device reports ModeCharDevice=%v, so the standard-library "+
		"shortcut would answer %v where the assertion above answers false",
		runtime.GOOS, charDevice, charDevice)
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

// alwaysUnderstood and neverUnderstood stand in for the terminal's own
// answer about escape sequences, so the table above can be about the
// reasons colour is switched off that have nothing to do with it.
func alwaysUnderstood() bool { return true }
func neverUnderstood() bool  { return false }

// TestColourIsOffWhenTheTerminalWouldNotInterpretEscapes is the row for
// the gate itself, and it exists because CI cannot reach the case it
// guards.
//
// A Windows console prints "\x1b[1m" literally unless virtual-terminal
// processing is enabled on the handle, and nothing in this program's
// dependencies enables it: x/term touches the input flag only, inside a
// function this package never calls. Windows Terminal turns it on for
// itself; cmd.exe and PowerShell under classic conhost do not. So a
// person there would have read an escape sequence where the headline
// should be.
//
// The matrix cannot see this. A test process on the Windows runner has
// no console attached, so the colour path is never reached there at all
// — the green leg is silent about it rather than evidence for it. That
// is why the real gate is behind a seam and this row asserts the wiring:
// with everything else saying colour is fine, a terminal that will not
// interpret escapes still switches it off.
//
// REQUIRED MUTATION: in colourEnabled, return true instead of calling
// escapesUnderstood. This row reds; every row in the table above stays
// green, because none of them is about this.
func TestColourIsOffWhenTheTerminalWouldNotInterpretEscapes(t *testing.T) {
	noEnv := func(string) (string, bool) { return "", false }

	if colourEnabled(true, noEnv, neverUnderstood) {
		t.Error("colour is on for a terminal that would print the escape sequences " +
			"literally — the headline would arrive as visible control characters")
	}
	// The control: everything here is identical except the gate, so the
	// row above cannot be passing because some other condition happened
	// to be false.
	if !colourEnabled(true, noEnv, alwaysUnderstood) {
		t.Error("colour is off even with every condition satisfied, so the assertion " +
			"above proves nothing about the escape gate specifically")
	}
}

// TestDebugIsReadByValueNotByPresence pins the difference between
// CURIOUS_DEBUG and NO_COLOR, which look like the same kind of switch
// and are not.
//
// Colour is decoration: switching it off costs a reader nothing, so
// treating any presence as "off" errs harmlessly and matches the
// widely-published convention. Debug is not decoration. It changes the
// copy a person sees — dropping the line that tells them there is
// nothing here for them to fix — and it prints an error's own text
// verbatim, which is the one path in this package where something a
// caller constructed reaches the terminal unedited.
//
// So "CURIOUS_DEBUG=0" must mean off. A shell that exports an unset
// variable as empty must not turn it on. And the program's own message
// says "re-run with CURIOUS_DEBUG=1", which is a statement about values
// that was not true of the code until this row existed.
//
// REQUIRED MUTATION: in debugEnabled, make the switch match nothing so
// every set value is truthy — the old presence test. The falsey rows red
// and the truthy rows stay green, which is what makes this a table about
// values rather than about whether the variable was read at all.
func TestDebugIsReadByValueNotByPresence(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"yes", true},
		{"anything at all", true},
		{"TRUE", true},

		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"off", false},
		{"  0  ", false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.value), func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				if name == debugEnvVar {
					return tc.value, true
				}
				return "", false
			}
			if got := debugEnabled(lookup); got != tc.want {
				t.Errorf("debugEnabled with %s=%q = %v, want %v", debugEnvVar, tc.value, got, tc.want)
			}
		})
	}

	t.Run("unset", func(t *testing.T) {
		if debugEnabled(func(string) (string, bool) { return "", false }) {
			t.Error("debug is on with the variable unset")
		}
	})

	// The end-to-end control: the value semantics above have to reach
	// the rendering, not just the predicate. A UI built with
	// CURIOUS_DEBUG=0 must produce the ordinary internal-failure copy,
	// detail withheld.
	t.Run("a falsey value withholds the detail", func(t *testing.T) {
		u, _, errOut := testUI("", false, map[string]string{debugEnvVar: "0"})
		u.Internal(errors.New("the-detail-nobody-asked-for"))
		if strings.Contains(errOut.String(), "the-detail-nobody-asked-for") {
			t.Errorf("CURIOUS_DEBUG=0 printed the detail anyway: %q", errOut.String())
		}
		if !strings.Contains(errOut.String(), internalWhy) {
			t.Errorf("the ordinary copy is missing, so the check above observed nothing: %q",
				errOut.String())
		}
	})
}
