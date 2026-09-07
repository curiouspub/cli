package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
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
	// The terminator itself is replaced, not just the exit underneath
	// it. On Unix the real one re-raises SIGINT at this process, which
	// kills the whole test binary — observed, before this line existed.
	// Recording the code here keeps every assertion below meaning what
	// it meant, while the platform behaviour is asserted separately by
	// the rows that can actually see it.
	u.endInterrupted = func() { codes = append(codes, interruptExitCode) }

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

	// REQUIRED MUTATION: change interruptExitCode from 130 to 1. NOTE
	// WHICH ASSERTION REDS: not this one, which compares against the
	// constant and therefore moves with it, but the literal check two
	// statements down. The test reds either way; the attribution in the
	// first draft of this comment was off by one assertion, which is a
	// claim about coverage and so is worth correcting rather than
	// leaving to be rediscovered.
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

// interruptHelperEnv selects the helper-process mode. A test binary
// re-executed with it set runs one of the two terminators for real
// instead of asserting about a copy of them.
const interruptHelperEnv = "UI_TEST_INTERRUPT_HELPER"

// TestInterruptHelperProcess is not a test. It is the child half of
// TestTheTerminatorReallyDiesOfTheSignal, and it exists so that row
// exercises the SHIPPED terminator rather than an inlined imitation of
// it.
//
// The first version of that row built a small program containing the
// same three lines terminateInterrupted runs. It asserted the property
// correctly and proved nothing about this package: deleting the wait
// from the real function left the row green, because the row was never
// running the real function. A copy of the code under test is a
// restatement of the author's intention, which is the thing a test is
// supposed to be independent of.
func TestInterruptHelperProcess(t *testing.T) {
	mode := os.Getenv(interruptHelperEnv)
	if mode == "" {
		t.Skip("child half of the terminator row; runs only when re-executed")
	}

	u := New()
	if mode == "exit" {
		// The control: the behaviour this package shipped with, so the
		// two are produced by the same harness and differ only where
		// they are meant to.
		u.endInterrupted = func() { os.Exit(interruptExitCode) }
	}
	stop := u.Interrupts()
	defer stop()

	fmt.Println("ready")
	time.Sleep(10 * time.Second)
}

// TestTheTerminatorReallyDiesOfTheSignal is the row the in-process seam
// cannot cover, run out of process because covering it in process means
// killing the test binary.
//
// THE PROPERTY IS NOT THE EXIT STATUS. It is that the process is KILLED
// BY THE SIGNAL, because that — WIFSIGNALED — and not the status is what
// a shell reads when deciding whether Ctrl-C stops a loop. A child that
// exits normally with 130 and a child killed by SIGINT report the same
// number to `$?` and are not the same event: measured under a real bash,
// the first let `for i in 1 2 3; do prog; done` run its next iteration
// and the second stopped it. For this tool that difference is deploying
// the next directory after the operator interrupted the previous one.
//
// A shell is not needed to assert it, and using one made this row slow
// and flaky before it made it correct. Go's ProcessState answers the
// same question the shell asks. The control confirms the two outcomes
// are INDISTINGUISHABLE by exit status, which is why reasoning from the
// status is what produced the defect in the first place.
//
// REQUIRED MUTATION: delete the time.Sleep from terminateInterrupted
// (interrupt_unix.go). The backstop exit then races the re-raised signal
// and wins, Signaled() goes false, and this row reds — while the exit
// code stays 130 throughout.
func TestTheTerminatorReallyDiesOfTheSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dying of a signal is the property under test; Windows has no " +
			"equivalent and terminateInterrupted exits 130 there by design")
	}

	run := func(t *testing.T, mode string) (signaled bool, code int) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=TestInterruptHelperProcess", "-test.v")
		cmd.Env = append(os.Environ(), interruptHelperEnv+"="+mode)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// Wait for the child to say it has installed the handler, rather
		// than sleeping: a timing guess is how a row like this becomes a
		// flake on a loaded machine.
		scanner := bufio.NewScanner(stdout)
		ready := false
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "ready" {
				ready = true
				break
			}
		}
		if !ready {
			_ = cmd.Process.Kill()
			t.Fatal("the helper never reported that its handler was installed")
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("the helper did not exit after being interrupted")
		}
		status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if !ok {
			t.Skip("this platform does not expose a WaitStatus, so the property cannot be read")
		}
		if status.Signaled() {
			return true, 128 + int(status.Signal())
		}
		return false, cmd.ProcessState.ExitCode()
	}

	t.Run("the shipped terminator is killed by the signal", func(t *testing.T) {
		signaled, code := run(t, "real")
		if !signaled {
			t.Errorf("the process exited normally instead of dying of the signal — a " +
				"shell reads that as 'handled, carry on' and the next loop iteration runs")
		}
		if code != interruptExitCode {
			t.Errorf("reported %d, want %d", code, interruptExitCode)
		}
	})

	t.Run("control: exiting 130 is a different event with the same number", func(t *testing.T) {
		signaled, code := run(t, "exit")
		if signaled {
			t.Fatal("the control died of the signal, so it is not a control")
		}
		if code != interruptExitCode {
			t.Errorf("the control reported %d, want %d — the two cases must be "+
				"indistinguishable by exit status, or this row proves nothing about why "+
				"the status was the wrong thing to reason from", code, interruptExitCode)
		}
	})
}
