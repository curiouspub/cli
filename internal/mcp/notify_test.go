package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// watchedCall is a tools/call that asks to be told how it is going, by
// carrying a progress token in the protocol's own side-channel.
//
// The token is INTERPOLATED RAW rather than quoted, so a row can send a
// string, a number, or the null a client sends when it means nothing —
// which are three different things and only one of them is a token.
func watchedCall(id, name, arguments, token string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":%q,"arguments":%s,"_meta":{"progressToken":%s}}}`,
		id, name, arguments, token)
}

// reportingTool is a tool that says what it is doing and then answers.
// The reports are fixed so a row asserts on the rendering rather than on
// a build.
func reportingTool(reports ...report) Tool {
	return Tool{
		Name:        "reporter",
		Description: "Say a few things and then finish.",
		Handler: func(_ context.Context, _ json.RawMessage, progress Progress) Result {
			for _, r := range reports {
				progress.Report(r.phase, r.line)
			}
			return TextResult("done")
		},
	}
}

// sentProgress is one progress notification as it reached the CLIENT,
// decoded by this file rather than by the type that wrote it.
//
// THE TOKEN IS READ AS RAW BYTES, which is the whole reason this is not
// the production struct. A row here asks whether the bytes a client sent
// came back unchanged, and decoding them through the same type that
// encoded them would make any transformation invisible — the two sides
// would agree with each other by construction, which is what an
// assertion that derives its expectation from the thing it tests looks
// like. Reading the wire is the only thing that can see a token turned
// into a float and back.
type sentProgress struct {
	ProgressToken json.RawMessage `json:"progressToken"`
	Progress      int             `json:"progress"`
	Message       string          `json:"message"`
}

// progressMessages is every progress notification on a protocol stream,
// in order, decoded — and it insists that everything else on the stream
// is a reply, so a row cannot pass over a message it did not expect.
func progressMessages(t *testing.T, stdout string) []sentProgress {
	t.Helper()
	var got []sentProgress
	for i, line := range protocolLines(t, stdout) {
		var msg struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("message %d is not valid JSON (%v): %q", i+1, err, line)
		}
		if msg.Method == "" {
			continue // a reply
		}
		if msg.Method != methodProgress {
			t.Fatalf("message %d is a %q notification, which this server does not send: %q",
				i+1, msg.Method, line)
		}
		// A NOTIFICATION HAS NO id MEMBER, and an explicit null is not
		// the same thing: a client matching replies by id would be
		// handed one it never sent. The distinction is invisible to a
		// decoder that only reads the method, so it is checked here.
		if len(msg.ID) != 0 {
			t.Errorf("message %d is a notification carrying an id (%s), which makes it a reply: %q",
				i+1, msg.ID, line)
		}
		var p sentProgress
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			t.Fatalf("decoding the progress parameters of message %d: %v", i+1, err)
		}
		got = append(got, p)
	}
	return got
}

// runCall serves one message and hands back what reached the protocol
// stream.
func runCall(t *testing.T, s *Server, message string) string {
	t.Helper()
	var out, logw bytes.Buffer
	mustWithin(t, serveDeadline, "Serve", func() {
		if err := s.Serve(context.Background(), strings.NewReader(message+"\n"), &out, &logw); err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return out.String()
}

// TestAWatchedCallIsToldAboutAndAnUnwatchedOneIsNot is ONE ROW FOR TWO
// HALVES, because neither establishes anything alone.
//
// The silent half is the claim a client actually relies on: a call
// nobody asked about puts nothing on the protocol stream but its reply.
// On its own that passes against a server that has no notifications at
// all, a sink that is never handed to anything, and a tool whose handler
// never ran — every one of which is the same one message.
//
// The watched half is what makes the silent one mean something. It is the
// same tool, on the same server, through the same dispatcher, differing
// only in whether the client sent a token.
//
// REQUIRED MUTATION, run 2026-09-11: build the reporting sink in
// progressFor whether or not a token arrived, falling back to one of its
// own. The silent half reds — three messages where one was wanted — and
// the watched half stays green, which is the asymmetry the pairing
// exists to expose. Two further rows red with it and the list is
// recorded rather than trimmed to the one predicted: the null-token row
// next door, and the dispatcher's own context row in the file beside
// this one, whose tool reports and which therefore counts replies that
// are now outnumbered by notifications.
func TestAWatchedCallIsToldAboutAndAnUnwatchedOneIsNot(t *testing.T) {
	reports := []report{
		{phase: wire.PhaseInstalling, line: "added 41 packages"},
		{phase: wire.PhaseBuilding, line: "building for production"},
	}

	t.Run("the client asked to be told", func(t *testing.T) {
		s := testServer()
		s.Register(reportingTool(reports...))

		stdout := runCall(t, s, watchedCall("1", "reporter", `{}`, `"watch-me"`))

		lines := protocolLines(t, stdout)
		if len(lines) != 3 {
			t.Fatalf("got %d messages, want two notifications and a reply: %q", len(lines), stdout)
		}
		// THE REPORTS COME BEFORE THE REPLY, which is the whole point of
		// them: a notification that arrived after the call finished
		// would be a progress report about something that is over.
		if got := progressMessages(t, strings.Join(lines[:2], "\n")+"\n"); len(got) != 2 {
			t.Fatalf("the first two messages are not both progress notifications: %q", stdout)
		}

		got := progressMessages(t, stdout)
		want := []sentProgress{
			{ProgressToken: json.RawMessage(`"watch-me"`), Progress: 1, Message: "installing: added 41 packages"},
			{ProgressToken: json.RawMessage(`"watch-me"`), Progress: 2, Message: "building: building for production"},
		}
		if len(got) != len(want) {
			t.Fatalf("got %d notifications, want %d: %+v", len(got), len(want), got)
		}
		for i := range want {
			if string(got[i].ProgressToken) != string(want[i].ProgressToken) {
				t.Errorf("notification %d quotes token %s, want %s — a client matches a "+
					"notification to a call by this and nothing else",
					i, got[i].ProgressToken, want[i].ProgressToken)
			}
			if got[i].Progress != want[i].Progress {
				t.Errorf("notification %d carries progress %d, want %d — the protocol "+
					"requires it to increase", i, got[i].Progress, want[i].Progress)
			}
			if got[i].Message != want[i].Message {
				t.Errorf("notification %d says %q, want %q", i, got[i].Message, want[i].Message)
			}
		}

		if result := resultOf(t, decodeReplies(t, lines[2]+"\n")[0]); result.IsError {
			t.Errorf("the watched call itself failed: %+v", result)
		}
	})

	t.Run("the client did not", func(t *testing.T) {
		s := testServer()
		s.Register(reportingTool(reports...))

		stdout := runCall(t, s, callMessage("1", "reporter", `{}`))

		if lines := protocolLines(t, stdout); len(lines) != 1 {
			t.Fatalf("a call nobody is watching put %d messages on the stream, want just "+
				"its reply: %q", len(lines), stdout)
		}
		if result := resultOf(t, onlyReply(t, stdout)); result.IsError {
			t.Errorf("the unwatched call failed: %+v", result)
		}
	})
}

// TestAProgressTokenIsQuotedBackExactly.
//
// The protocol lets a token be a string or a number, and a number
// decoded through an untyped value becomes a float64 — so a token past
// what a float can hold exactly comes back as a DIFFERENT number and the
// client cannot match the notification to the call it sent. It is the
// same defect the request id is held as raw bytes to avoid, arriving on
// the one other value this server quotes back.
//
// REQUIRED MUTATION, run 2026-09-11: decode ProgressToken as `any`
// instead of json.RawMessage, on the inbound side and the outbound one
// together. Reds here, and only on the large-integer case: "the token
// came back as 9007199254740992, want 9007199254740993 exactly". The
// string and the small number survive the round trip, which is what
// makes the large one the case worth having.
func TestAProgressTokenIsQuotedBackExactly(t *testing.T) {
	for _, token := range []string{`"a-string"`, `9007199254740993`, `0`} {
		t.Run(token, func(t *testing.T) {
			s := testServer()
			s.Register(reportingTool(report{phase: wire.PhaseQueued}))

			stdout := runCall(t, s, watchedCall("1", "reporter", `{}`, token))

			got := progressMessages(t, stdout)
			if len(got) != 1 {
				t.Fatalf("got %d notifications, want 1: %q", len(got), stdout)
			}
			if string(got[0].ProgressToken) != token {
				t.Errorf("the token came back as %s, want %s exactly",
					got[0].ProgressToken, token)
			}
		})
	}
}

// TestANullProgressTokenIsNotAToken.
//
// A client that sends null has named no handle, and quoting null back on
// a notification would be this server inventing one — the notifications
// would arrive and the client would have nothing to match them to, which
// is a stream of messages about a call it cannot identify.
func TestANullProgressTokenIsNotAToken(t *testing.T) {
	s := testServer()
	s.Register(reportingTool(report{phase: wire.PhaseQueued, line: "here"}))

	stdout := runCall(t, s, watchedCall("1", "reporter", `{}`, `null`))

	if lines := protocolLines(t, stdout); len(lines) != 1 {
		t.Fatalf("a call with a null token put %d messages on the stream, want just "+
			"its reply: %q", len(lines), stdout)
	}
}

// TestTheBuildsOwnOutputIsEscapedOnItsWayToTheClient.
//
// A build log is arbitrary program output — whatever the package manager
// and the site builder printed — and a terminal obeys some of those
// bytes. The client decodes this JSON and a person reads the result, so
// the escaping the terminal path does has to happen here too: a local
// safety property must not rest on a remote guarantee this client cannot
// verify, watch regress or version-check.
//
// THE PHASE IS DELIBERATELY NOT ESCAPED and this row does not ask it to
// be. It is the contract's own vocabulary and this program's to render.
//
// REQUIRED MUTATION, run 2026-09-11: drop the Sanitize call from
// progressMessage. Reds here, with the raw escape byte in the message.
func TestTheBuildsOwnOutputIsEscapedOnItsWayToTheClient(t *testing.T) {
	s := testServer()
	s.Register(reportingTool(report{
		phase: wire.PhaseBuilding,
		line:  "\x1b]0;retitled\a and then some ordinary output",
	}))

	stdout := runCall(t, s, watchedCall("1", "reporter", `{}`, `"watch-me"`))

	got := progressMessages(t, stdout)
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1: %q", len(got), stdout)
	}
	if strings.ContainsRune(got[0].Message, 0x1b) {
		t.Errorf("the notification carries a raw escape byte: %q", got[0].Message)
	}
	// THE OUTPUT IS STILL THERE. An escaping that solved the problem by
	// dropping the line would pass the assertion above and tell the
	// reader nothing.
	if !strings.Contains(got[0].Message, "and then some ordinary output") {
		t.Errorf("the notification lost the line it was reporting: %q", got[0].Message)
	}
}
