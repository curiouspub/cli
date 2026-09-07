//go:build unix

package ui

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// terminateInterrupted ends a run that was interrupted, the way every
// Unix shell understands: by letting the signal kill the process.
//
// A shell decides whether Ctrl-C aborts a loop from whether the child was
// KILLED by a signal — WIFSIGNALED — not from its exit status. A child
// that exits normally has, by that definition, handled the interrupt, and
// the loop carries on to the next iteration whatever number it exited
// with. Measured against bash: exiting 130 here let the next iteration
// run, and run successfully.
//
// So the default disposition is restored and the signal re-raised. The
// process dies of SIGINT, the shell sees it, the loop stops, and the
// status the shell reports is still 130 — the same number, arriving the
// way the shell actually reads it.
//
// THE EXIT AFTERWARDS IS A BACKSTOP, AND IT HAS TO WAIT FIRST. The first
// version of this function called it immediately, on the reasoning that
// POSIX delivers an unblocked signal before kill returns and so the line
// could never be reached. Measured: it is reached, and it wins. Go does
// not restore SIG_DFL directly — signal.Reset hands the signal back to
// the runtime, whose own handler then arranges the death — and that
// indirection is enough for a plain os.Exit on the next line to get
// there first. The probe in this package's tests reported a process
// that exited normally with 130, which is precisely the bug this
// function exists to fix, reintroduced by its own fallback.
//
// So the backstop sleeps. If the signal lands, this goroutine never
// wakes and the process dies of SIGINT as intended. If something has
// blocked or ignored it, the run still ends rather than hanging on a
// terminal nobody can use — a short pause is the cost of not choosing
// between those two failures.
func terminateInterrupted(u *UI) {
	signal.Reset(os.Interrupt)
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	time.Sleep(interruptGrace)
	u.exit(interruptExitCode)
}

// interruptGrace is how long the backstop waits for the re-raised signal
// to do its work. It is long enough that a loaded machine still gets
// there and short enough that nobody waits on it: the process is
// normally gone within microseconds, and this value is only ever spent
// on a run where the signal did not arrive at all.
const interruptGrace = 250 * time.Millisecond
