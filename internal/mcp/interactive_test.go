package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

// TestAToolThatWouldPromptReachesTheNoTerminalSentinel drives a tool
// handler that asks a question, under exactly the streams a client hands
// this process, and requires the question to come back refused rather
// than waited on.
//
// THERE IS NO SECOND INTERACTIVITY SWITCH HERE, AND THAT IS THE POINT.
// The terminal package already decides whether it may ask a question,
// from stdin AND stderr both being terminals; under a client both are
// pipes, so every prompt already returns the no-terminal sentinel
// without this package doing anything at all. Two answers to "may I ask
// a question" is two things that can disagree, and the day they disagree
// the symptom is a server that hangs — which is the hardest failure to
// report, because there is nothing on screen to quote.
//
// So this is a ROW rather than a mechanism: it asserts the existing
// switch gives the right answer in this host, and it fails if anybody
// ever makes that untrue.
//
// THE STREAMS ARE REPLACED WITH REAL PIPES rather than trusted to be
// non-terminals already. The terminal constructor reads this process's
// own stdin and stderr, and what those are under `go test` is a property
// of the test runner and the machine — a fixture's construction is a
// platform assumption, and it is the one nobody writes down. A pipe is
// never a terminal on any of the three platforms this ships to, so
// replacing them states the condition instead of inheriting it.
//
// THE WRITE END OF STDIN IS DELIBERATELY LEFT OPEN, with nothing written
// to it. That is what makes the mutation below visible: a build that
// decided it MAY prompt would block forever on a read nobody will
// answer, and the deadline turns that hang into a named failure. Closing
// it would produce an end-of-input instead, and the row would then be
// measuring a cancellation rather than a refusal.
//
// REQUIRED MUTATION, run 2026-09-08: in the terminal package, make
// interactiveStreams return true unconditionally. The prompt is written
// and the handler blocks on a read; the deadline reds this row.
//
// SECOND REQUIRED MUTATION, run 2026-09-08: in the terminal package's
// Confirm, drop the not-interactive guard and fall into the ask loop.
// Same failure, by the other route.
func TestAToolThatWouldPromptReachesTheNoTerminalSentinel(t *testing.T) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating a pipe for stdin: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating a pipe for stderr: %v", err)
	}

	originalIn, originalErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = stdinR, stderrW
	defer func() {
		os.Stdin, os.Stderr = originalIn, originalErr
		stdinW.Close()
		stdinR.Close()
		stderrW.Close()
		stderrR.Close()
	}()

	// The instrument reads first: if these streams were somehow
	// interactive, everything below would be measuring the harness
	// rather than the rule.
	if ui.New(os.Stdin, os.Stdout, os.Stderr).Interactive() {
		t.Fatal("a pipe pair was reported as interactive, so this row measures nothing")
	}

	var asked error
	s := testServer()
	s.Register(Tool{
		Name: "would-prompt",
		Handler: func(json.RawMessage) Result {
			_, asked = ui.New(os.Stdin, os.Stdout, os.Stderr).Confirm("Continue anyway?", true)
			if errors.Is(asked, ui.ErrNotInteractive) {
				return ErrorResult("There was nobody to ask, so nothing was assumed.")
			}
			return TextResult("the prompt returned %v", asked)
		},
	})

	stdout, _ := drive(t, s, callMessage("1", "would-prompt", `{}`))

	if !errors.Is(asked, ui.ErrNotInteractive) {
		t.Errorf("the prompt returned %v, want the no-terminal sentinel", asked)
	}

	// AND THE CLIENT GETS A STRUCTURED RESULT, not a hang and not a
	// protocol fault. A path that would ask a person a question in a
	// terminal has to come back as something the agent can read and act
	// on; that conversion is each tool's own job, and this row is the
	// proof that the sentinel arrives for a tool to convert.
	r := onlyReply(t, stdout)
	if r.Error != nil {
		t.Fatalf("a prompt with nobody to answer it was reported as a protocol fault: %+v", r.Error)
	}
	got := resultOf(t, r)
	if !got.IsError || len(got.Content) != 1 || !strings.Contains(got.Content[0].Text, "nobody to ask") {
		t.Errorf("the client was not told what happened: %+v", got)
	}
}
