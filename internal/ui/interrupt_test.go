package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// recorder is an io.Writer that keeps the bytes AND notes, in order,
// that a write happened. The order is what one assertion below is
// actually about: putting the terminal back has to happen before
// anything is written to it.
type recorder struct {
	buf    bytes.Buffer
	events *[]string
}

func (r *recorder) Write(p []byte) (int, error) {
	*r.events = append(*r.events, "write")
	return r.buf.Write(p)
}

// interruptUI builds a UI whose terminal restore and process exit are
// recorded rather than performed. Ending the test binary is not an
// observation.
func interruptUI() (u *UI, out *recorder, events *[]string, exited *[]int) {
	seq := []string{}
	codes := []int{}
	rec := &recorder{events: &seq}

	u = newUI(strings.NewReader(""), &bytes.Buffer{}, rec,
		func(string) (string, bool) { return "", false }, true)
	u.restore = func() { seq = append(seq, "restore") }
	u.exit = func(code int) { codes = append(codes, code) }

	// The closures above append to the local slices, so the UI's fields
	// and these pointers see the same growth.
	return u, rec, &seq, &codes
}

// TestInterruptRestoresTheTerminalThenExits130 covers the two things
// Ctrl-C has to do that the default behaviour does not: hand the
// terminal back, and report the conventional interrupted code so a
// wrapping script can tell an interruption from a fault.
func TestTheInterruptHandlerRestoresThenExits130(t *testing.T) {
	u, rec, events, exited := interruptUI()

	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	defer close(done)

	finished := make(chan struct{})
	go func() {
		u.watchInterrupts(signals, done)
		close(finished)
	}()

	signals <- os.Interrupt
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupt handler never ran")
	}

	// REQUIRED MUTATION: change interruptExitCode from 130 to 1.
	if len(*exited) != 1 || (*exited)[0] != interruptExitCode {
		t.Errorf("the handler exited with %v, want exactly one exit with %d",
			*exited, interruptExitCode)
	}
	if interruptExitCode != 130 {
		t.Errorf("interruptExitCode is %d, want 130 — 128 plus the signal number is "+
			"what a shell reads as an interruption", interruptExitCode)
	}

	// REQUIRED MUTATION: move u.restore() below the write in
	// interrupted. The ordering assertion goes red while the two above
	// stay green.
	if len(*events) == 0 || (*events)[0] != "restore" {
		t.Errorf("event order was %v, want the restore first — writing to a terminal "+
			"still in a modified state is what produces the mangled last line",
			*events)
	}
	if !strings.Contains(rec.buf.String(), "cancelled") {
		t.Errorf("the handler wrote %q, want it to say cancelled", rec.buf.String())
	}
	if developerText.MatchString(rec.buf.String()) {
		t.Errorf("Ctrl-C produced developer text: %q", rec.buf.String())
	}
}

// TestStoppingTheWatchDoesNotExit is the negative half, and without it
// the test above proves only that SOMETHING makes the handler fire. A
// watcher that exited on any wake-up at all would pass that one.
func TestStoppingTheWatchDoesNotExit(t *testing.T) {
	u, rec, events, exited := interruptUI()

	signals := make(chan os.Signal, 1)
	done := make(chan struct{})

	finished := make(chan struct{})
	go func() {
		u.watchInterrupts(signals, done)
		close(finished)
	}()

	close(done)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not return when it was stopped")
	}

	if len(*exited) != 0 {
		t.Errorf("stopping the watch exited the process with %v", *exited)
	}
	if len(*events) != 0 {
		t.Errorf("stopping the watch touched the terminal: %v", *events)
	}
	if rec.buf.Len() != 0 {
		t.Errorf("stopping the watch wrote %q", rec.buf.String())
	}
}

// TestInterruptsInstallsAndReleasesTheSignal exercises the real
// registration, which the two tests above deliberately bypass. It cannot
// raise an interrupt — Windows has no way for a process to send itself
// one — so what it pins is that installing and releasing the handler is
// safe, and that releasing it twice is too. A stop function that
// panicked on a second call would be a crash on the tidy-up path of an
// already-failing run.
func TestInterruptsInstallsAndReleasesTheSignal(t *testing.T) {
	u, _, _, exited := interruptUI()

	stop := u.Interrupts()
	stop()
	stop()

	if len(*exited) != 0 {
		t.Errorf("installing and releasing the handler exited the process with %v", *exited)
	}
}
