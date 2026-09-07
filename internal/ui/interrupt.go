package ui

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
)

// interruptExitCode is what a program killed by SIGINT reports: 128 plus
// the signal number, the convention every shell reads.
const interruptExitCode = 130

// Interrupts installs a handler for the interrupt signal — Ctrl-C — and
// returns a function that removes it again.
//
// The handler exists for the TIDY-UP: the terminal is put back the way
// it was found, because a program that takes over a terminal and is then
// killed leaves the shell behind it unusable, and a short "cancelled"
// is printed so the person can see the program noticed.
//
// WHAT IT MUST NOT DO IS SWALLOW THE SIGNAL, and the first version of
// this file did exactly that. It ended with an exit status of 130, on
// the reasoning that 130 is "the only thing that tells a wrapping script
// the run was interrupted rather than broken". That reasoning is wrong
// on Unix in both halves. The default already yields 130 there — and
// more importantly a shell does not decide by status at all: bash keys
// on whether the child was KILLED by a signal, so a child that exits
// normally, even with 130, says "I handled it, carry on."
//
// Measured under a pty against a real shell, one Ctrl-C into
// `for i in 1 2 3; do prog; done`:
//
//	handler that exits 130   -> iteration 2 runs, and reports success
//	no handler at all        -> the loop stops
//	restore, print, re-raise -> the loop stops
//
// So `for d in a b c; do curious deploy $d; done` would have deployed
// the second directory after the operator interrupted the first. That is
// the opposite of what an interrupt means, produced by the code written
// to honour it.
//
// The terminator is therefore platform-split — see terminateInterrupted
// in its two files. Unix restores the default disposition and re-raises,
// so the process really is killed by the signal and every shell sees it.
// Windows exits 130, because there the default is 0xC000013A and 130 is
// the value scripts there are written to read.
//
// The handler is not what makes Ctrl-C work — the default behaviour
// already ends the process — so what it buys is the tidy-up alone.
// Installing it means the default is no longer in force, which is why
// the returned stop function exists: a caller that has finished its
// interactive stretch gives the signal back to the runtime rather than
// leaving a goroutine holding it.
//
// Calling stop more than once is safe.
func (u *UI) Interrupts() (stop func()) {
	// Buffered, because signal.Notify never blocks: an unbuffered
	// channel with a slow receiver drops the signal silently, which is
	// the one failure this handler cannot report.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)

	done := make(chan struct{})
	go u.watchInterrupts(signals, done)

	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
}

// watchInterrupts is the goroutine body, separated from Interrupts so
// its behaviour can be exercised by sending on an ordinary channel.
// Raising a real interrupt at a test process is destructive rather than
// merely awkward: on Unix the terminator re-raises SIGINT at this
// process, so a test that triggered it for real would kill the test
// binary instead of failing a row — observed, not predicted. (An earlier
// version of this comment justified the seam by saying Windows cannot
// deliver an interrupt to itself. It can: Go's own signal tests do it
// with a console control event. The reason above is the true one, and it
// holds on every platform.) A rule about what happens on Ctrl-C should
// hold where this program is hardest to get right, not only where the
// test is easiest to write.
func (u *UI) watchInterrupts(signals <-chan os.Signal, done <-chan struct{}) {
	select {
	case <-signals:
		u.interrupted()
	case <-done:
	}
}

// interrupted is the tidy-up, in the order the order matters in: put the
// terminal back BEFORE writing anything, since writing to a terminal
// still in a modified state is what produces the mangled last line
// people screenshot.
func (u *UI) interrupted() {
	u.restore()
	// A newline first: the terminal has just echoed the interrupt
	// character with no line ending of its own, so without this the
	// message lands on the same line as it.
	fmt.Fprintln(u.err)
	u.Cancelled()
	u.endInterrupted()
}
