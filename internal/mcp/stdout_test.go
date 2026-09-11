package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// protocolOnly reports whether a captured protocol stream is EXACTLY the
// messages it was supposed to carry — same bytes, same order, nothing
// else, nothing extra.
//
// IT RETURNS THE DIFFERENCE RATHER THAN REPORTING IT, and that is what
// makes the positive control below possible. An assertion written
// straight into a test is one nobody can point at a stream it ought to
// reject; as a function it can be handed a deliberately dirty stream and
// required to notice.
func protocolOnly(stdout string, want []string) error {
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if stdout == "" {
		got = nil
	}
	if len(got) != len(want) {
		return fmt.Errorf("the stream carries %d lines, want exactly %d:\n got: %q\nwant: %q",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("line %d differs:\n got: %s\nwant: %s", i+1, got[i], want[i])
		}
	}
	if stdout != "" && !strings.HasSuffix(stdout, "\n") {
		return fmt.Errorf("the stream does not end in a newline: %q", stdout)
	}
	return nil
}

// purityScenario drives a run in which every part of this server that
// has anything to SAY says it, and returns both streams.
//
// The three logging paths are deliberately all present at once, because
// the defect this row exists for is one stray writer, and a scenario
// that exercises one path proves nothing about the other two:
//
//   - the handshake reporting a revision mismatch,
//   - a notification this server does not implement,
//   - a tool that panics, which logs the panic value and a stack.
//
// The panicking tool is also what makes the log LONG. A single line
// slipping onto the protocol stream would be caught by a shorter
// scenario; a stack trace on the wrong stream is the version of this
// defect that actually happens, because a stack is what somebody adds
// while debugging.
func purityScenario(t *testing.T) (stdout, stderr string) {
	t.Helper()
	s := testServer()
	s.Register(Tool{
		Name: "boom",
		Handler: func(context.Context, json.RawMessage, Progress) Result {
			panic("a handler that was written on a Friday")
		},
	})

	return drive(t, s,
		initializeMessage("1", "1999-01-01"),
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		callMessage("2", "boom", `{}`),
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
	)
}

// wantPurityTranscript is what the scenario above must put on stdout,
// written out rather than derived.
//
// THESE ARE LITERALS ON PURPOSE. Building the expectation by marshalling
// the same structs the server marshals would be the server agreeing with
// itself: any change to a field name, a tag, an order or an omission
// would move both sides together and this row would stay green while
// every client broke. What is written here is what goes on the wire.
var wantPurityTranscript = []string{
	`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"curious","version":"0.0.0-test"}}}`,
	`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"The boom tool failed unexpectedly and nothing it was doing was finished. The server is still running, so you can try the call again."}],"isError":true}}`,
	`{"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"boom","inputSchema":{"type":"object"}}]}}`,
}

// TestNothingButProtocolReachesStdout is the row the whole package is
// arranged around.
//
// STDOUT IS THE PROTOCOL. One stray byte on it is a message the client
// cannot parse, and the report a user files is that the client
// disconnected without saying why — there is no error text anywhere,
// because from the client's side nothing failed, something merely became
// unreadable. It is the single defect most likely to be written here and
// the hardest to see once it is.
//
// So the assertion is byte-identical rather than "contains", and the
// same run is required to have written to the log, which is what tells
// "the diagnostics went to the right stream" apart from "there were no
// diagnostics".
//
// REQUIRED MUTATION, run 2026-09-08: in Serve, add a
// fmt.Fprintln(out, "reading") inside the scan loop. Reds here.
//
// SECOND REQUIRED MUTATION, run 2026-09-08: in invoke, write the panic
// diagnostic to the result's own stream by having Serve pass out instead
// of logw. Reds here.
func TestNothingButProtocolReachesStdout(t *testing.T) {
	stdout, stderr := purityScenario(t)

	if err := protocolOnly(stdout, wantPurityTranscript); err != nil {
		t.Errorf("something other than the protocol reached stdout: %v", err)
	}

	// THE LOG MUST NOT BE EMPTY. Without this, a server that wrote
	// nothing anywhere would pass — and "no diagnostics at all" is
	// indistinguishable from "diagnostics on the right stream" when only
	// one stream is looked at. It is the same reason a refusal row needs
	// an acceptance beside it.
	for _, want := range []string{
		"1999-01-01",                      // the revision mismatch
		"notifications/cancelled",         // the notification nothing implements
		"panicked",                        // the containment
		"a handler that was written on a", // the panic value itself
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the log does not mention %q, so this run did not exercise that path: %q", want, stderr)
		}
	}
}

// TestTheStdoutPurityAssertionFiresOnAStrayWrite is the POSITIVE CONTROL
// for the row above, and it is not a nicety.
//
// A byte-identical assertion is a negative assertion: it says nothing
// else is there. Such a row passes just as well when the comparison is
// broken, when the scenario stopped running, or when the expectation was
// quietly widened — all three of which look exactly like a pass. The
// only thing that distinguishes "the assertion refused a dirty stream"
// from "nothing was ever checked" is showing it refuse one.
//
// So this takes the real transcript and dirties it in each of the ways a
// stray write actually arrives — a diagnostic printed before a reply,
// one printed after the last, one appended to a line without a newline —
// and requires the assertion to reject every one.
func TestTheStdoutPurityAssertionFiresOnAStrayWrite(t *testing.T) {
	clean, _ := purityScenario(t)

	// The instrument reads clean first. If it does not, everything below
	// is measuring a broken comparison rather than a dirty stream.
	if err := protocolOnly(clean, wantPurityTranscript); err != nil {
		t.Fatalf("the assertion rejects the real transcript, so this control measures nothing: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(clean, "\n"), "\n")
	for _, tc := range []struct {
		name  string
		dirty string
	}{
		{
			name:  "a fmt.Print before the first reply",
			dirty: "curious mcp: starting up\n" + clean,
		},
		{
			name:  "a fmt.Print after the last reply",
			dirty: clean + "curious mcp: the boom tool panicked\n",
		},
		{
			name:  "a fmt.Print between two replies",
			dirty: lines[0] + "\nignoring a notification\n" + strings.Join(lines[1:], "\n") + "\n",
		},
		{
			name:  "a fmt.Print with no newline, glued to a reply",
			dirty: "reading" + clean,
		},
		{
			name:  "a reply that went missing",
			dirty: strings.Join(lines[:len(lines)-1], "\n") + "\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := protocolOnly(tc.dirty, wantPurityTranscript); err == nil {
				t.Errorf("the assertion accepted a stream carrying %s", tc.name)
			}
		})
	}
}
