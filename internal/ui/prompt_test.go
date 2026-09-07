package ui

import (
	"errors"
	"strings"
	"testing"
)

// TestConfirmDefaults pins the two things a caller cannot get wrong by
// accident because Confirm derives both from one argument: the hint it
// renders, and the answer an empty line produces.
//
// Pairing the wrong hint with the wrong default is the defect this
// signature exists to make impossible — a user who reads "[Y/n]" and
// presses return has been told what that means, and a caller that then
// treats the blank as "no" has lied to them.
func TestConfirmDefaults(t *testing.T) {
	cases := []struct {
		name       string
		defaultYes bool
		input      string
		wantHint   string
		wantAnswer bool
	}{
		{"yes-default renders the capital Y hint", true, "\n", "[Y/n]", true},
		{"no-default renders the capital N hint", false, "\n", "[y/N]", false},
		{"empty line takes the yes default", true, "\n", "[Y/n]", true},
		{"empty line takes the no default", false, "\n", "[y/N]", false},
		{"whitespace-only line is still empty", true, "   \n", "[Y/n]", true},
		{"whitespace-only line is still empty, no-default", false, "  \t \n", "[y/N]", false},
		{"an explicit answer overrides the yes default", true, "n\n", "[Y/n]", false},
		{"an explicit answer overrides the no default", false, "y\n", "[y/N]", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _, errOut := testUI(tc.input, true, nil)

			got, err := u.Confirm("Continue?", tc.defaultYes)
			if err != nil {
				t.Fatalf("Confirm returned an unexpected error: %v", err)
			}

			// REQUIRED MUTATION: swap the two return values in
			// confirmHint, so a yes-default renders "[y/N]". Every hint
			// row here goes red.
			if !strings.Contains(errOut.String(), tc.wantHint) {
				t.Errorf("prompt %q does not contain the hint %q", errOut.String(), tc.wantHint)
			}

			// REQUIRED MUTATION: in Confirm's empty-answer branch,
			// return false instead of defaultYes. The four default rows
			// go red and the two explicit-answer rows stay green, which
			// is what says the assertion is about the default rather
			// than about parsing.
			if got != tc.wantAnswer {
				t.Errorf("Confirm(defaultYes=%v) with input %q = %v, want %v",
					tc.defaultYes, tc.input, got, tc.wantAnswer)
			}
		})
	}
}

// TestConfirmReask covers the answers Confirm understands, the ones it
// does not, and the bound on how long it will keep asking.
//
// The bound is the load-bearing half. An unbounded re-ask loop against a
// misbehaving pipe — one that yields a line the parser rejects, forever
// — is a hang, and a hang is the hardest failure for a user to report
// because there is nothing on screen to quote.
func TestConfirmReask(t *testing.T) {
	accepted := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"Yes\n", true},
		{" y \n", true},
		{"n\n", false},
		{"N\n", false},
		{"no\n", false},
		{"NO\n", false},
		{"No\n", false},
	}
	for _, tc := range accepted {
		t.Run("accepts "+strings.TrimSpace(tc.input), func(t *testing.T) {
			u, _, errOut := testUI(tc.input, true, nil)
			got, err := u.Confirm("Continue?", !tc.want)
			if err != nil {
				t.Fatalf("Confirm(%q) returned an unexpected error: %v", tc.input, err)
			}
			// REQUIRED MUTATION: drop strings.ToLower from
			// parseConfirmAnswer, or remove "yes" from the affirmative
			// set. The mixed-case and long-form rows go red.
			//
			// The default is deliberately the OPPOSITE of the expected
			// answer in every row, so a parser that silently fell
			// through to the default could not produce these results.
			if got != tc.want {
				t.Errorf("Confirm(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if asked := strings.Count(errOut.String(), "Continue?"); asked != 1 {
				t.Errorf("Confirm(%q) asked %d times, want exactly 1 — a recognised "+
					"answer must not be re-asked", tc.input, asked)
			}
		})
	}

	t.Run("an unrecognised answer is re-asked, not guessed", func(t *testing.T) {
		u, _, errOut := testUI("maybe\nmaybe\nmaybe\ny\n", true, nil)
		got, err := u.Confirm("Continue?", false)
		if err != nil {
			t.Fatalf("Confirm returned an unexpected error: %v", err)
		}
		if !got {
			t.Errorf("Confirm = false, want true — the answer after three bad ones was y")
		}
		// REQUIRED MUTATION: in Confirm's unrecognised-answer branch,
		// return the default instead of continuing the loop. This row
		// reds on the ask count (1, not 4).
		if asked := strings.Count(errOut.String(), "Continue?"); asked != 4 {
			t.Errorf("the question was asked %d times, want 4 — three bad answers "+
				"must each produce another ask", asked)
		}
	})

	t.Run("the fourth bad answer is an error, not a fifth ask", func(t *testing.T) {
		// Five bad answers are supplied and only four may be read: a
		// bound that was off by one would consume the fifth and this
		// row would not notice from the error alone.
		u, _, errOut := testUI("maybe\nmaybe\nmaybe\nmaybe\nmaybe\n", true, nil)
		got, err := u.Confirm("Continue?", true)

		// REQUIRED MUTATION: raise maxPromptAttempts from 4 to 5. The
		// error goes nil, and the ask count becomes 5.
		if !errors.Is(err, ErrNoAnswer) {
			t.Fatalf("Confirm after four bad answers returned err=%v, want ErrNoAnswer", err)
		}
		// REQUIRED MUTATION: return defaultYes alongside the error. A
		// default nobody chose must not ride out on a failed prompt.
		if got {
			t.Errorf("Confirm returned true alongside an error, want the zero value — " +
				"the default (true here) must not be applied to a prompt that failed")
		}
		if asked := strings.Count(errOut.String(), "Continue?"); asked != 4 {
			t.Errorf("the question was asked %d times, want 4 — the bound is one ask "+
				"and three re-asks", asked)
		}
	})
}

// TestPromptsRefuseWhenNotInteractive is the rule that a prompt is never
// silently auto-answered. Taking the "[Y/n]" default on a warning nobody
// saw is how a broken deploy becomes the user's fault.
func TestPromptsRefuseWhenNotInteractive(t *testing.T) {
	t.Run("Confirm", func(t *testing.T) {
		// The input would answer "yes" if it were ever read, and the
		// default is also yes: both routes to a true are open, so a
		// false here can only mean the refusal happened first.
		u, out, errOut := testUI("y\n", false, nil)
		got, err := u.Confirm("Continue?", true)

		// REQUIRED MUTATION: delete the !u.interactive guard at the top
		// of Confirm.
		if !errors.Is(err, ErrNotInteractive) {
			t.Fatalf("Confirm without a terminal returned err=%v, want ErrNotInteractive", err)
		}
		// REQUIRED MUTATION: return defaultYes instead of false from
		// that guard. This is the row the brief asks for by name: the
		// caller must see an ERROR, not a true.
		if got {
			t.Errorf("Confirm without a terminal returned true — the default was applied " +
				"to a question nobody was shown")
		}
		if errOut.Len() != 0 {
			t.Errorf("a question was written to stderr with nobody to answer it: %q", errOut.String())
		}
		if out.Len() != 0 {
			t.Errorf("something was written to stdout: %q", out.String())
		}
	})

	t.Run("Line", func(t *testing.T) {
		u, _, errOut := testUI("someone@example.test\n", false, nil)
		got, err := u.Line("Email address:")
		if !errors.Is(err, ErrNotInteractive) {
			t.Fatalf("Line without a terminal returned err=%v, want ErrNotInteractive", err)
		}
		if got != "" {
			t.Errorf("Line without a terminal returned %q, want the empty string", got)
		}
		if errOut.Len() != 0 {
			t.Errorf("a prompt was written to stderr with nobody to answer it: %q", errOut.String())
		}
	})

	t.Run("Email", func(t *testing.T) {
		u, _, _ := testUI("someone@example.test\n", false, nil)
		if _, err := u.Email("Email address:"); !errors.Is(err, ErrNotInteractive) {
			t.Fatalf("Email without a terminal returned err=%v, want ErrNotInteractive", err)
		}
	})
}

// TestPromptsAbortOnEOF covers ^D, and the shape of what happens next:
// ErrAborted is a deliberate cancellation, distinguishable from every
// other failure, so the program can say "cancelled" and exit 0 rather
// than printing a fault the user did not cause.
func TestPromptsAbortOnEOF(t *testing.T) {
	t.Run("Confirm", func(t *testing.T) {
		// Input is empty, so the first read is an immediate EOF. The
		// default is true, which is what makes this row meaningful: an
		// implementation that treated EOF as "empty line" would return
		// true and no error.
		u, _, _ := testUI("", true, nil)
		got, err := u.Confirm("Continue?", true)

		// REQUIRED MUTATION: in readLine, return the empty string and a
		// nil error on io.EOF instead of ErrAborted.
		if !errors.Is(err, ErrAborted) {
			t.Fatalf("Confirm at EOF returned err=%v, want ErrAborted", err)
		}
		if got {
			t.Errorf("Confirm at EOF returned true — EOF was read as an empty line " +
				"and the default was applied")
		}
	})

	t.Run("Line", func(t *testing.T) {
		u, _, _ := testUI("", true, nil)
		if _, err := u.Line("Email address:"); !errors.Is(err, ErrAborted) {
			t.Fatalf("Line at EOF returned err=%v, want ErrAborted", err)
		}
	})

	t.Run("mid-stream EOF aborts rather than re-asking forever", func(t *testing.T) {
		// One unrecognised answer, then the stream ends. A loop that
		// treated EOF as "try again" would spin.
		u, _, _ := testUI("maybe\n", true, nil)
		if _, err := u.Confirm("Continue?", true); !errors.Is(err, ErrAborted) {
			t.Fatalf("Confirm returned err=%v, want ErrAborted", err)
		}
	})

	t.Run("a final line with no newline is an answer, not an abort", func(t *testing.T) {
		// This is the positive control for the two rows above. A pipe
		// whose last line has no trailing newline yields the data AND
		// io.EOF in one read, so an implementation that keyed on the
		// error alone would discard a perfectly good answer — and the
		// EOF rows above would still be green.
		u, _, _ := testUI("y", true, nil)
		got, err := u.Confirm("Continue?", false)
		if err != nil {
			t.Fatalf("Confirm on an unterminated final line returned err=%v, want none", err)
		}
		if !got {
			t.Errorf("Confirm on an unterminated final line = false, want true")
		}
	})
}

// TestLineTrimsSurroundingWhitespace pins the trim, including the
// carriage return a Windows-authored input file carries. Trailing \r is
// invisible in every failure message it would ever appear in, so a value
// carrying one produces a wrong answer that reads as correct.
func TestLineTrimsSurroundingWhitespace(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"leading and trailing spaces", "  hello  \n", "hello"},
		{"tabs", "\thello\t\n", "hello"},
		{"a carriage return before the newline", "hello\r\n", "hello"},
		{"nothing to trim", "hello\n", "hello"},
		{"interior spaces survive", "  two words  \n", "two words"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _, _ := testUI(tc.input, true, nil)
			got, err := u.Line("Say something:")
			if err != nil {
				t.Fatalf("Line returned an unexpected error: %v", err)
			}
			// REQUIRED MUTATION: drop the strings.TrimSpace in readLine.
			// Every row but the last two goes red.
			if got != tc.want {
				t.Errorf("Line(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestLineWritesThePromptToStderr is the stream rule at the prompt: a
// question goes where the person is, never into the pipe they redirected
// stdout into.
func TestLineWritesThePromptToStderr(t *testing.T) {
	u, out, errOut := testUI("hello\n", true, nil)
	if _, err := u.Line("Say something:"); err != nil {
		t.Fatalf("Line returned an unexpected error: %v", err)
	}
	// REQUIRED MUTATION: write the prompt to u.out in writePrompt.
	if !strings.Contains(errOut.String(), "Say something:") {
		t.Errorf("the prompt did not reach stderr; stderr was %q", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("the prompt reached stdout: %q — a user piping stdout would not "+
			"see the question", out.String())
	}
}

// TestEmailShapeOnly covers the deliberately shallow validation: enough
// to catch a typo before a network round trip, and nothing that pretends
// to know whether an address exists.
//
// It is NOT a real address validator and NEVER a disposable-domain
// blocklist. That list is maintained on the server, it changes, and a
// copy compiled into a public binary is both a disclosure and stale the
// week after it ships. A server-side refusal renders from the server's
// own message.
func TestEmailShapeOnly(t *testing.T) {
	valid := []string{
		"someone@example.test",
		"a@b",
		"first.last+tag@sub.example.test",
		"UPPER@EXAMPLE.TEST",
		// Shape-only means shape-only: this is not a deliverable
		// address, and refusing it here would be this client claiming
		// knowledge it does not have.
		"weird!chars#ok@example.test",
	}
	for _, address := range valid {
		t.Run("accepts "+address, func(t *testing.T) {
			u, _, _ := testUI(address+"\n", true, nil)
			got, err := u.Email("Email address:")
			if err != nil {
				t.Fatalf("Email(%q) returned an unexpected error: %v", address, err)
			}
			if got != address {
				t.Errorf("Email returned %q, want %q — the answer must come back "+
					"unaltered, not normalised", got, address)
			}
		})
	}

	invalid := []struct {
		name  string
		entry string
	}{
		// REQUIRED MUTATION, for this whole block: change
		// validEmailShape to `return entry != ""`. Every row here goes
		// red and every row above stays green.
		{"empty", ""},
		{"no at sign", "someone.example.test"},
		{"two at signs", "someone@@example.test"},
		{"two at signs, separated", "someone@example@test"},
		{"nothing before the at sign", "@example.test"},
		{"nothing after the at sign", "someone@"},
		{"an interior space", "some one@example.test"},
		{"an interior tab", "someone@exam\tple.test"},
	}
	for _, tc := range invalid {
		t.Run("re-asks on "+tc.name, func(t *testing.T) {
			u, _, errOut := testUI(tc.entry+"\nsomeone@example.test\n", true, nil)
			got, err := u.Email("Email address:")
			if err != nil {
				t.Fatalf("Email returned an unexpected error: %v", err)
			}
			if got != "someone@example.test" {
				t.Errorf("Email = %q, want the second, well-formed entry", got)
			}
			if asked := strings.Count(errOut.String(), "Email address:"); asked != 2 {
				t.Errorf("the prompt was written %d times, want 2 — a malformed entry "+
					"must be re-asked locally rather than sent to the server", asked)
			}
		})
	}

	t.Run("the fourth malformed entry is an error, not a fifth ask", func(t *testing.T) {
		u, _, errOut := testUI("nope\nnope\nnope\nnope\nnope\n", true, nil)
		got, err := u.Email("Email address:")
		if !errors.Is(err, ErrNoAnswer) {
			t.Fatalf("Email after four malformed entries returned err=%v, want ErrNoAnswer", err)
		}
		if got != "" {
			t.Errorf("Email returned %q alongside an error, want the empty string", got)
		}
		if asked := strings.Count(errOut.String(), "Email address:"); asked != 4 {
			t.Errorf("the prompt was written %d times, want 4 — Email is bound the "+
				"same way Confirm is", asked)
		}
	})
}

// TestTheCodePromptEchoesLikeAnyOtherLine states the rule this package
// does NOT have, as a test, so it is a decision rather than an omission:
// the six-digit login code is ordinary echoed input, not a password.
//
// People mistype a code they cannot see, and a code is single-use and
// expires in minutes — hiding it costs a retry loop and buys nothing.
// The token the code is exchanged FOR never passes through a prompt at
// all; it is held as a Secret, which is where the redaction rule lives.
func TestTheCodePromptEchoesLikeAnyOtherLine(t *testing.T) {
	u, _, errOut := testUI("123456\n", true, nil)
	got, err := u.Line("Enter the 6-digit code:")
	if err != nil {
		t.Fatalf("Line returned an unexpected error: %v", err)
	}
	if got != "123456" {
		t.Errorf("Line = %q, want the code back unaltered", got)
	}
	// A no-echo implementation would have to take over the terminal;
	// nothing here does, so there is no raw-mode state to leak. The
	// assertion available to a test is that the prompt is written like
	// any other and the answer comes back through the ordinary path.
	if !strings.Contains(errOut.String(), "Enter the 6-digit code:") {
		t.Errorf("the code prompt did not reach stderr: %q", errOut.String())
	}
}
