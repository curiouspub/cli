package ui

import "errors"

// The three ways a prompt can end without an answer. Each is a distinct
// sentinel because the caller's response to each is different, and a
// caller that cannot tell them apart writes the wrong message: a
// cancellation is not a fault, a missing terminal is a fault with an
// obvious fix, and giving up after repeated asking is neither.
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
)
