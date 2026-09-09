package ui

import (
	"errors"
	"fmt"
)

// The ways a run can end that are not a fault in this program. Each is a
// distinct sentinel because the caller's response to each is different,
// and a caller that cannot tell them apart writes the wrong message: a
// cancellation is not a fault, a missing terminal is a fault with an
// obvious fix, giving up after repeated asking is neither, and a door
// the server has closed is not about this run at all.
var (
	// ErrNotInteractive means there was nobody to ask. The caller
	// renders a message naming what it needed a terminal FOR, because
	// only the caller knows — this package would have to guess.
	ErrNotInteractive = errors.New("no terminal to ask on")

	// ErrAborted means the person cancelled: end-of-input, which is
	// what Ctrl-D sends. It is a deliberate act rather than a failure,
	// so the program says "cancelled" and exits 0.
	ErrAborted = errors.New("cancelled")

	// ErrNoAnswer means the prompt was asked its bounded number of
	// times and never got an answer it could use.
	ErrNoAnswer = errors.New("no usable answer")

	// ErrInterrupted means the run was stopped from OUTSIDE it — a
	// cancelled context rather than a decision this program made.
	//
	// IT IS NOT ErrAborted, and the difference is who stopped it and
	// what that costs. ErrAborted is a person answering a prompt with
	// Ctrl-D: a deliberate act, nothing went wrong, exit 0. This is the
	// run being cut off mid-step — by a wrapper's deadline, by a parent
	// process, by a signal that reached the context before the handler.
	// The program says the same short word, and costs 130, because that
	// is the number every wrapping script already reads as "stopped
	// rather than finished".
	//
	// WHAT IT MUST NOT DO IS EXPLAIN THE SERVER. Whoever returns this
	// knows only that the wait ended; a message about what the far end
	// was doing would be invented.
	ErrInterrupted = errors.New("the run was interrupted")

	// ErrServerClosed means THE SERVER IS CLOSED TO YOU RIGHT NOW — the
	// scope behind ExitServerClosed, stated once in this package's own
	// doc comment and carried here so a caller marks a stop rather than
	// choosing a number.
	//
	// It says nothing about what went wrong and nothing about when to
	// come back. It says only that the door is shut, so a script can act
	// on that without parsing a message. Deciding which conditions mean
	// it belongs to whoever makes the call, and every one of them marks
	// its stop with ServerClosed below.
	ErrServerClosed = errors.New("the server is closed to this run right now")
)

// ExitServerClosed is what the process reports for a run that ended
// because the door was shut. It is a NAMED constant rather than a
// literal at each call site for the reason the scope is written down at
// all: a bare number is something every later surface has to guess the
// meaning of, and a number nobody can grep for is one that drifts.
const ExitServerClosed = 3

// ServerClosed marks err as a closed-door stop, so that ExitCode reports
// ExitServerClosed for it while err keeps rendering its own copy.
//
// THE SENTINEL DECIDES THE COST AND THE WRAPPED ERROR DECIDES WHAT IS
// SAID, and the split is the point. The condition that closed the door
// is known where the call was made; the words a person reads are often
// owned somewhere else entirely — a hand-off, a later offer — and
// neither should have to know the other's answer. Wrapping keeps both:
// errors.Is finds the sentinel, errors.As still finds the Failure.
//
// A nil error yields the bare sentinel rather than a wrapper around
// nothing, so a caller that has no copy to offer still gets the right
// cost and the standing copy below.
func ServerClosed(err error) error {
	if err == nil {
		return ErrServerClosed
	}
	return fmt.Errorf("%w: %w", ErrServerClosed, err)
}
