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
// Two things have to happen on Ctrl-C and neither is automatic. The
// terminal is put back the way it was found, because a program that
// takes over a terminal and is then killed leaves the shell behind it
// unusable; and the process reports 130, which is 128 plus the signal
// number and the only thing that tells a wrapping script the run was
// interrupted rather than broken.
//
// The handler is not what makes Ctrl-C work — the default behaviour
// already ends the process — so what it buys is the tidy-up and the
// exit code. Installing it means the default is no longer in force,
// which is why the returned stop function exists: a caller that has
// finished its interactive stretch gives the signal back to the runtime
// rather than leaving a goroutine holding it.
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
// Raising a real interrupt at a test process is not portable — Windows
// has no way to deliver one to itself — and a rule about what happens on
// Ctrl-C should hold on the platform where this program is hardest to
// get right, not only where the test is easiest to write.
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
	u.exit(interruptExitCode)
}
