package ui

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// maxPromptAttempts bounds how many times one question is put: one ask
// and three re-asks.
//
// THE BOUND IS THE POINT, not the number. An unbounded loop against a
// terminal is fine — a person gets bored and presses Ctrl-D — but
// against a pipe that keeps yielding a line the parser rejects it is an
// infinite loop, and a program that hangs is the hardest thing a user
// can report, because there is nothing on screen to quote. Four is
// chosen as more chances than a person needs and fewer than a wedged
// pipe would take to notice.
const maxPromptAttempts = 4

// Confirm asks a yes-or-no question and returns the answer.
//
// The hint and the default come from ONE argument, and that is the whole
// reason this function exists rather than each caller writing its own
// Fprintf: rendering "[Y/n]" while treating a blank line as "no" is a
// lie told to somebody who did exactly what they were told, and it is a
// lie no caller can tell through this signature.
//
// Empty input takes the default. "y", "yes", "n" and "no" are accepted
// in any case. Anything else is re-asked rather than guessed at, up to
// maxPromptAttempts in total; after that the answer is an error, never a
// default. Taking the "[Y/n]" default on a warning nobody saw is how a
// broken deploy becomes the user's fault.
func (u *UI) Confirm(question string, defaultYes bool) (bool, error) {
	if !u.interactive {
		// Nothing is written: a question posted where nobody can answer
		// it is noise in a log, and the caller is about to say
		// something specific about what it needed instead.
		return false, ErrNotInteractive
	}

	for attempt := 0; attempt < maxPromptAttempts; attempt++ {
		u.writePrompt(question + " " + confirmHint(defaultYes))

		answer, err := u.readLine()
		if err != nil {
			return false, err
		}
		if answer == "" {
			return defaultYes, nil
		}
		if value, understood := parseConfirmAnswer(answer); understood {
			return value, nil
		}
		if attempt < maxPromptAttempts-1 {
			u.Step("Please answer y or n.")
		}
	}

	// The zero value, not the default. A default is what somebody gets
	// for pressing return; nobody pressed return here.
	return false, ErrNoAnswer
}

// confirmHint renders the bracketed hint, with the default in capitals —
// the convention every shell tool shares, and the only thing telling the
// user what a bare return will do.
func confirmHint(defaultYes bool) string {
	if defaultYes {
		return "[Y/n]"
	}
	return "[y/N]"
}

// parseConfirmAnswer maps an answer to a yes or a no, reporting whether
// it understood it at all. The second return value is what keeps "not
// understood" from collapsing into "no" — a distinction the caller of
// Confirm can never recover once it is lost.
func parseConfirmAnswer(answer string) (value, understood bool) {
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, true
	case "n", "no":
		return false, true
	}
	return false, false
}

// Line asks for a line of text and returns it with surrounding
// whitespace removed.
//
// It does not hide what is typed, and the six-digit login code is the
// case that decides it: a code is single-use, expires in minutes, and is
// mistyped constantly. Hiding it buys nothing and costs a retry the user
// cannot diagnose. The value that IS secret — the token the code is
// exchanged for — never passes through a prompt.
func (u *UI) Line(prompt string) (string, error) {
	if !u.interactive {
		return "", ErrNotInteractive
	}
	u.writePrompt(prompt)
	return u.readLine()
}

// Email asks for an email address and re-asks locally on one that cannot
// possibly be one, bounded like every other prompt.
//
// It lives here rather than beside either of its callers because there
// are two of them and neither should have to depend on the other for it.
func (u *UI) Email(prompt string) (string, error) {
	if !u.interactive {
		return "", ErrNotInteractive
	}

	for attempt := 0; attempt < maxPromptAttempts; attempt++ {
		entry, err := u.Line(prompt)
		if err != nil {
			return "", err
		}
		if validEmailShape(entry) {
			return entry, nil
		}
		if attempt < maxPromptAttempts-1 {
			u.Step("That doesn't look like an email address — it needs an @ with " +
				"something on both sides and no spaces.")
		}
	}
	return "", ErrNoAnswer
}

// validEmailShape reports whether an entry could be an email address at
// all: non-empty, exactly one "@", something either side of it, no
// whitespace anywhere.
//
// SHAPE ONLY, and deliberately less than a real validator. The local
// part of an address may contain almost anything, so a stricter rule
// here refuses addresses that work — and this check exists to catch a
// typo before a network round trip, not to adjudicate deliverability.
// Whether an address can receive mail is the server's answer, and a
// refusal from the server renders from the server's own message.
//
// It is emphatically NOT a disposable-domain blocklist. That list is
// maintained server-side and changes; a copy compiled into a public
// binary would be both a disclosure of the list and stale the week after
// it shipped.
func validEmailShape(entry string) bool {
	at := strings.Index(entry, "@")
	if at <= 0 || at != strings.LastIndex(entry, "@") || at == len(entry)-1 {
		return false
	}
	return strings.IndexFunc(entry, unicode.IsSpace) < 0
}

// writePrompt puts the question on stderr, with a trailing space so the
// answer is typed after a gap rather than against the bracket. Stderr,
// not stdout: somebody piping stdout still has to see what they are
// being asked.
func (u *UI) writePrompt(text string) {
	fmt.Fprintf(u.err, "%s ", text)
}

// readLine reads one answer, trimmed.
//
// The EOF handling is the part worth reading. ReadString returns
// whatever it managed to read ALONGSIDE io.EOF when the input ends
// without a final newline, so the error alone cannot say what happened:
//
//   - No bytes at all before the EOF is Ctrl-D, which is a cancellation.
//   - Bytes before the EOF is an ordinary last line that happens to be
//     unterminated, which every pipe produces sooner or later, and
//     discarding it would throw away an answer the user gave.
//
// The test for the second case is the positive control for the first:
// without it, "EOF means abort" would look right while quietly eating
// the last line of every scripted run.
//
// The trim takes the carriage return of a CRLF line with it, which is
// not a nicety — a trailing \r is invisible in every message it would
// ever appear in, so a value carrying one is wrong in a way that reads
// as correct.
func (u *UI) readLine() (string, error) {
	line, err := u.reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			if line == "" {
				return "", ErrAborted
			}
			return strings.TrimSpace(line), nil
		}
		return "", fmt.Errorf("reading your answer: %w", err)
	}
	return strings.TrimSpace(line), nil
}
