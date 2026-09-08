package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// serveDeadline bounds every row in this package.
//
// A BLOCKING SERVER IS THE FAILURE MODE THIS WHOLE PACKAGE IS ABOUT, and
// a test that blocks does not fail — it hangs until the suite's own
// timeout kills the process, which reports as "panic: test timed out"
// with no row named. So every row that runs the server runs it under a
// deadline, and the deadline is what turns a hang into a named failure.
// It matters most for the rows about prompting, where the correct
// behaviour and the broken one differ by whether anything comes back at
// all.
const serveDeadline = 10 * time.Second

// mustWithin runs fn and fails the test if it has not returned within d.
func mustWithin(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %s — it is blocked", what, d)
	}
}

// serve runs the server over the given input and returns both streams
// and whatever Serve reported.
func serve(t *testing.T, s *Server, input string) (stdout, stderr string, err error) {
	t.Helper()
	var out, logw bytes.Buffer
	mustWithin(t, serveDeadline, "Serve", func() {
		err = s.Serve(strings.NewReader(input), &out, &logw)
	})
	return out.String(), logw.String(), err
}

// drive feeds the server one message per line and returns what it wrote
// to the protocol stream and to the log. It fails the test on a
// transport error, so a row that expects one calls serve directly.
func drive(t *testing.T, s *Server, messages ...string) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, err := serve(t, s, strings.Join(messages, "\n")+"\n")
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return stdout, stderr
}

// reply is one decoded outbound message. The id and the result stay raw
// so that rows can assert on the exact bytes the server chose.
type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// protocolLines splits the protocol stream into its messages, insisting
// that it ends in a newline and contains no blank ones. The framing is
// part of what these rows check, so it is checked here once rather than
// assumed everywhere.
func protocolLines(t *testing.T, stdout string) []string {
	t.Helper()
	if stdout == "" {
		return nil
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("the protocol stream does not end in a newline, so its last message is unterminated: %q", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Fatalf("message %d of the protocol stream is blank: %q", i+1, stdout)
		}
	}
	return lines
}

// decodeReplies decodes every message on the protocol stream.
func decodeReplies(t *testing.T, stdout string) []reply {
	t.Helper()
	var replies []reply
	for i, line := range protocolLines(t, stdout) {
		var r reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("message %d is not valid JSON (%v): %q", i+1, err, line)
		}
		if r.JSONRPC != jsonrpcVersion {
			t.Errorf("message %d declares jsonrpc %q, want %q", i+1, r.JSONRPC, jsonrpcVersion)
		}
		replies = append(replies, r)
	}
	return replies
}

// onlyReply decodes the protocol stream and insists there is exactly
// one message on it.
func onlyReply(t *testing.T, stdout string) reply {
	t.Helper()
	replies := decodeReplies(t, stdout)
	if len(replies) != 1 {
		t.Fatalf("got %d messages on the protocol stream, want exactly 1: %q", len(replies), stdout)
	}
	return replies[0]
}

// initializeMessage is the handshake a client opens with, asking for
// whatever revision it is given.
func initializeMessage(id, revision string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%s,"method":"initialize","params":{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"a-test-client","version":"0.0.1"}}}`,
		id, revision)
}

// testServer is a server identified the way the rows below expect.
func testServer() *Server { return New("curious", "0.0.0-test") }

// ---------------------------------------------------------------------
// The handshake, over a real pipe pair.
// ---------------------------------------------------------------------

// TestHandshakeCompletesOverAPipePair drives the server the way a client
// does: two operating-system pipes, the server reading one and writing
// the other, with the request written and the reply read while it is
// still running.
//
// IT USES REAL PIPES RATHER THAN A STRING AND A BUFFER, and the
// difference is not ceremony. A string reader hands the whole input over
// at once and never blocks; a pipe delivers whatever has been written so
// far and then waits, which is the only arrangement in which "the server
// answered the first message before the second was sent" is a thing that
// can be observed at all. Every other row here uses the cheaper harness,
// because they are about what the answer says rather than about when it
// arrives.
//
// THE REVISION IS ASSERTED AS A LITERAL, and that is the pin. Comparing
// the reply against the constant would be circular — editing the
// constant would move both sides and the row would stay green — so the
// wire value is written out here, where a change to the pinned revision
// has to be made twice and the second time is a decision.
//
// REQUIRED MUTATION, run 2026-09-08: change ProtocolVersion to any other
// date. Reds here, naming both values.
func TestHandshakeCompletesOverAPipePair(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the client-to-server pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the server-to-client pipe: %v", err)
	}

	var logw bytes.Buffer
	served := make(chan error, 1)
	go func() {
		err := testServer().Serve(inR, outW, &logw)
		outW.Close()
		served <- err
	}()

	if _, err := fmt.Fprintf(inW, "%s\n", initializeMessage("1", ProtocolVersion)); err != nil {
		t.Fatalf("writing the handshake: %v", err)
	}

	var line string
	mustWithin(t, serveDeadline, "reading the handshake reply", func() {
		var readErr error
		line, readErr = bufio.NewReader(outR).ReadString('\n')
		if readErr != nil {
			t.Errorf("reading the reply: %v", readErr)
		}
	})

	inW.Close()
	mustWithin(t, serveDeadline, "Serve returning at end of input", func() {
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	inR.Close()
	outR.Close()

	var got struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools *struct{} `json:"tools"`
			} `json:"capabilities"`
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the handshake reply is not valid JSON (%v): %q", err, line)
	}

	if got.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want %q", got.JSONRPC, "2.0")
	}
	if string(got.ID) != "1" {
		t.Errorf("id = %s, want 1", got.ID)
	}
	if got.Error != nil {
		t.Errorf("the handshake was answered with an error: %s", got.Error)
	}
	if got.Result.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q, want %q — the revision is pinned by name and this is the pin",
			got.Result.ProtocolVersion, "2025-06-18")
	}
	if ProtocolVersion != "2025-06-18" {
		t.Errorf("ProtocolVersion = %q, want %q", ProtocolVersion, "2025-06-18")
	}
	// THE TOOLS CAPABILITY IS DECLARED EVEN WITH NO TOOLS REGISTERED. A
	// client that never sees it is entitled to skip tools/list entirely,
	// which would make this server's whole surface invisible the moment
	// a wiring change registered the tools somewhere the handshake could
	// not see.
	//
	// REQUIRED MUTATION, run 2026-09-08: delete the Tools field from the
	// capabilities struct. Reds here. (An omitempty tag would NOT do it —
	// encoding/json never treats a struct as empty — which is itself
	// worth knowing before somebody "tidies" the field away.)
	if got.Result.Capabilities.Tools == nil {
		t.Error("the handshake declared no tools capability, so a client may never ask for the tool list")
	}
	if got.Result.ServerInfo.Name != "curious" || got.Result.ServerInfo.Version != "0.0.0-test" {
		t.Errorf("serverInfo = %+v, want the name and version the server was built with", got.Result.ServerInfo)
	}
}

// TestAClientAskingForAnotherRevisionGetsThisServersOwn drives the
// negotiation rule: the answer is this server's revision, never the
// client's echoed back, so the client can see the mismatch and decide.
//
// REQUIRED MUTATION, run 2026-09-08: in initialize, return
// p.ProtocolVersion instead of ProtocolVersion. Reds here.
func TestAClientAskingForAnotherRevisionGetsThisServersOwn(t *testing.T) {
	const asked = "1999-01-01"

	stdout, stderr := drive(t, testServer(), initializeMessage("7", asked))

	var got struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(onlyReply(t, stdout).Result, &got); err != nil {
		t.Fatalf("decoding the handshake result: %v", err)
	}
	if got.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocolVersion = %q, want this server's own %q — echoing the client's back is the "+
			"silent mismatch the pinning exists to prevent", got.ProtocolVersion, ProtocolVersion)
	}

	// THE MISMATCH IS REPORTED, and to the log rather than to the
	// client, because the person who has to explain why an old client
	// stopped working is reading stderr and not the protocol stream.
	//
	// REQUIRED MUTATION, run 2026-09-08: delete the mismatch Fprintf from
	// initialize. Reds here.
	if !strings.Contains(stderr, asked) || !strings.Contains(stderr, ProtocolVersion) {
		t.Errorf("the log does not record the revision mismatch (asked %q, answered %q): %q",
			asked, ProtocolVersion, stderr)
	}
	if !strings.Contains(stderr, "a-test-client") {
		t.Errorf("the log does not name the client that asked: %q", stderr)
	}
}

// TestAMatchingRevisionIsNotReportedAsAMismatch is the acceptance half
// of the row above. A log line on every handshake would be noise, and a
// mismatch warning that fires when there is no mismatch is one nobody
// reads by the third run.
func TestAMatchingRevisionIsNotReportedAsAMismatch(t *testing.T) {
	_, stderr := drive(t, testServer(), initializeMessage("1", ProtocolVersion))
	if stderr != "" {
		t.Errorf("a matching handshake wrote to the log: %q", stderr)
	}
}

// ---------------------------------------------------------------------
// Ids, notifications and malformed input.
// ---------------------------------------------------------------------

// TestIDsAreEchoedByteForByte pins the one property a hand-rolled
// JSON-RPC implementation is most likely to lose.
//
// An id may be a string or a number, and a client is entitled to choose
// either. Decoding a number through Go's any makes it a float64, so an
// id past the range a float64 represents exactly comes back CHANGED —
// the client is answered about a request it never sent, and the symptom
// is a call that appears to hang while its reply sits unmatched.
//
// REQUIRED MUTATION, run 2026-09-08: change request.ID and response.ID
// from json.RawMessage to any. The large-integer row reds.
func TestIDsAreEchoedByteForByte(t *testing.T) {
	for _, id := range []string{
		`1`,
		`"a-string-id"`,
		`9007199254740993`,
		`0`,
		`-4`,
	} {
		t.Run(id, func(t *testing.T) {
			stdout, _ := drive(t, testServer(),
				fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"ping"}`, id))
			if got := string(onlyReply(t, stdout).ID); got != id {
				t.Errorf("id came back as %s, want the bytes that were sent, %s", got, id)
			}
		})
	}
}

// TestNotificationsAreNeverAnswered covers the rule that a message with
// no id gets no reply — including the ones this server does not
// implement, where a reply would be a protocol violation and an error
// reply would additionally be wrong.
//
// REQUIRED MUTATION, run 2026-09-08: delete the isNotification branch
// from handle. Every row here reds.
func TestNotificationsAreNeverAnswered(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		logged  bool
	}{
		{"initialized", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, false},
		{"a notification this server does not implement", `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`, true},
		{"an unknown method with no id", `{"jsonrpc":"2.0","method":"frobnicate"}`, true},
		{"a request-shaped method with no id", `{"jsonrpc":"2.0","method":"tools/list"}`, true},
		{"an explicit null id", `{"jsonrpc":"2.0","id":null,"method":"frobnicate"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := drive(t, testServer(), tc.message)
			if stdout != "" {
				t.Errorf("a notification was answered: %q", stdout)
			}
			// AN IGNORED NOTIFICATION IS STILL REPORTED SOMEWHERE. The
			// client is told nothing because the protocol says so; the
			// operator is told, because "the agent's cancel does
			// nothing" is otherwise a mystery with no evidence.
			//
			// REQUIRED MUTATION, run 2026-09-08: delete the default
			// branch's Fprintf from handleNotification. Reds here.
			if logged := strings.Contains(stderr, req(tc.message)); logged != tc.logged {
				t.Errorf("the log names the method = %v, want %v: %q", logged, tc.logged, stderr)
			}
		})
	}
}

// req pulls the method name back out of a message, so the row above
// states the method once instead of twice.
func req(message string) string {
	var m struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(message), &m); err != nil {
		return ""
	}
	return m.Method
}

// TestMalformedInputIsAnsweredAndTheServerKeepsReading pairs each
// refusal with proof that the server was still there afterwards.
//
// THE SECOND HALF IS THE POINT. A row asserting only that a bad message
// produces an error passes just as well against a server that answered
// and then died, which is the failure that actually costs a user their
// session.
//
// REQUIRED MUTATION, run 2026-09-08: in Serve, return nil immediately
// after writing a reply, so the loop serves one message and stops. The
// follow-up ping is never answered and every row here reds.
func TestMalformedInputIsAnsweredAndTheServerKeepsReading(t *testing.T) {
	for _, tc := range []struct {
		name     string
		message  string
		wantCode int
		wantID   string
		wantSays string
	}{
		{
			name:     "not JSON at all",
			message:  `this is not JSON`,
			wantCode: codeParseError,
			wantID:   "null",
			wantSays: "valid JSON",
		},
		{
			name:     "valid JSON that is not an object",
			message:  `["a","list"]`,
			wantCode: codeInvalidRequest,
			wantID:   "null",
			wantSays: "request object",
		},
		{
			name:     "another JSON-RPC version",
			message:  `{"jsonrpc":"1.0","id":2,"method":"ping"}`,
			wantCode: codeInvalidRequest,
			wantID:   "2",
			wantSays: "1.0",
		},
		{
			name:     "no JSON-RPC version at all",
			message:  `{"id":3,"method":"ping"}`,
			wantCode: codeInvalidRequest,
			wantID:   "3",
			wantSays: "JSON-RPC",
		},
		{
			name:     "an unknown method",
			message:  `{"jsonrpc":"2.0","id":4,"method":"frobnicate"}`,
			wantCode: codeMethodNotFound,
			wantID:   "4",
			wantSays: "frobnicate",
		},
		{
			name:     "tool call parameters that will not decode",
			message:  `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":"not an object"}`,
			wantCode: codeInvalidParams,
			wantID:   "5",
			wantSays: "parameters",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := drive(t, testServer(), tc.message,
				`{"jsonrpc":"2.0","id":"still-here","method":"ping"}`)

			replies := decodeReplies(t, stdout)
			if len(replies) != 2 {
				t.Fatalf("got %d replies, want the refusal and the answer that proves the server "+
					"is still reading: %q", len(replies), stdout)
			}

			refusal := replies[0]
			if refusal.Error == nil {
				t.Fatalf("the message was accepted rather than refused: %q", stdout)
			}
			if refusal.Error.Code != tc.wantCode {
				t.Errorf("error code = %d, want %d", refusal.Error.Code, tc.wantCode)
			}
			if string(refusal.ID) != tc.wantID {
				t.Errorf("error id = %s, want %s", refusal.ID, tc.wantID)
			}
			// THE REASON NAMES THE THING THAT WAS WRONG. A bare code is
			// a number the reader has to go and look up, and this
			// program's standing rule is that every stop names what
			// happened.
			if !strings.Contains(refusal.Error.Message, tc.wantSays) {
				t.Errorf("the reason does not name %q: %q", tc.wantSays, refusal.Error.Message)
			}

			survivor := replies[1]
			if survivor.Error != nil || string(survivor.ID) != `"still-here"` {
				t.Errorf("the server did not answer the next message normally: %q", stdout)
			}
		})
	}
}

// TestBlankLinesAreNotMessages covers somebody driving this by hand: a
// bare return is not a parse error, it is a person pressing return.
func TestBlankLinesAreNotMessages(t *testing.T) {
	stdout, stderr := drive(t, testServer(), "", "   ", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "")
	if len(decodeReplies(t, stdout)) != 1 {
		t.Errorf("blank lines produced replies of their own: %q", stdout)
	}
	if stderr != "" {
		t.Errorf("blank lines produced diagnostics: %q", stderr)
	}
}

// TestAMessageOverTheLimitStopsTheRunWithAReason covers the bound on an
// inbound message, and the two things that have to be true when it is
// hit: the run stops rather than resynchronising onto the tail of a
// message it could not read, and it says why.
//
// REQUIRED MUTATION, run 2026-09-08: delete the scanner.Buffer call, so
// the limit falls back to the scanner's own default of 64 KiB. The
// smaller-than-the-limit row reds, which is the half that shows the
// number is this package's choice rather than a library's.
func TestAMessageOverTheLimitStopsTheRunWithAReason(t *testing.T) {
	// A LEGAL MESSAGE FIRST, and it is deliberately larger than the
	// scanner's own default so that this pair measures the configured
	// bound rather than an accident.
	big := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope","arguments":{"pad":%q}}}`,
		strings.Repeat("x", 256<<10))
	stdout, _, err := serve(t, testServer(), big+"\n")
	if err != nil {
		t.Fatalf("a message well inside the limit was refused: %v", err)
	}
	if len(decodeReplies(t, stdout)) != 1 {
		t.Fatalf("a message well inside the limit was not answered: %q", stdout)
	}

	oversized := strings.Repeat("x", maxMessageBytes+1)
	stdout, _, err = serve(t, testServer(), oversized+"\n"+`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	if err == nil {
		t.Fatal("a message over the limit was accepted")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(maxMessageBytes)) {
		t.Errorf("the reason does not name the limit: %v", err)
	}
	if stdout != "" {
		t.Errorf("the run answered something after an unreadable message: %q", stdout)
	}
}

// TestServeReturnsWhenTheClientClosesTheStream is how a client shuts
// this server down: it closes the pipe and the process ends. An end of
// input is not an error.
func TestServeReturnsWhenTheClientClosesTheStream(t *testing.T) {
	stdout, stderr, err := serve(t, testServer(), "")
	if err != nil {
		t.Errorf("Serve on an immediately-closed stream = %v, want nil", err)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("a server that was never spoken to wrote something: stdout %q, log %q", stdout, stderr)
	}
}

// TestPingIsAnswered covers the utility both sides of the protocol are
// required to answer promptly. A client that pings a server which
// replies "no such method" is entitled to conclude it is talking to
// something broken and disconnect.
func TestPingIsAnswered(t *testing.T) {
	stdout, _ := drive(t, testServer(), `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	got := onlyReply(t, stdout)
	if got.Error != nil {
		t.Fatalf("ping was refused: %+v", got.Error)
	}
	if string(got.Result) != "{}" {
		t.Errorf("ping result = %s, want an empty object", got.Result)
	}
}
