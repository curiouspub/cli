package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// echoTool is a tool registered for these rows alone. It answers with
// the arguments it was handed, so a row can tell "the handler ran" from
// "something answered on its behalf".
func echoTool(name string) Tool {
	return Tool{
		Name:        name,
		Title:       "Echo",
		Description: "Return the arguments it was given.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"say":{"type":"string"}}}`),
		Handler: func(arguments json.RawMessage) Result {
			return TextResult("echo: %s", string(arguments))
		},
	}
}

// callMessage is a tools/call request for the named tool.
func callMessage(id, name, arguments string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		id, name, arguments)
}

// resultOf decodes a tool result out of a reply, failing if the reply
// carried a protocol error instead.
func resultOf(t *testing.T, r reply) Result {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("the call was refused as a protocol error rather than answered: %+v", r.Error)
	}
	var got Result
	if err := json.Unmarshal(r.Result, &got); err != nil {
		t.Fatalf("decoding the tool result: %v", err)
	}
	return got
}

// TestAnUnknownToolIsAStructuredErrorAndAKnownOneIsNot pairs the refusal
// with its acceptance, in one row, on one server.
//
// THE ACCEPTANCE HALF IS NOT DECORATION. A refusal row on its own passes
// against a server that refuses EVERYTHING — a broken registry, a
// dispatcher that never reaches the lookup, a harness whose server was
// never given its tools. Those are indistinguishable from a working
// refusal until something valid is asked for in the same breath.
//
// REQUIRED MUTATION, run 2026-09-08: in callTool, return the rpcError
// unconditionally instead of only when the lookup misses. The acceptance
// half reds and the refusal half stays green, which is exactly the
// asymmetry this pairing exists to expose.
func TestAnUnknownToolIsAStructuredErrorAndAKnownOneIsNot(t *testing.T) {
	s := testServer()
	s.Register(echoTool("echo"))

	stdout, _ := drive(t, s,
		callMessage("1", "no-such-tool", `{}`),
		callMessage("2", "echo", `{"say":"hello"}`),
	)
	replies := decodeReplies(t, stdout)
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2: %q", len(replies), stdout)
	}

	refused := replies[0]
	if refused.Error == nil {
		t.Fatalf("an unknown tool was not refused: %s", refused.Result)
	}
	if refused.Error.Code != codeInvalidParams {
		t.Errorf("error code = %d, want %d", refused.Error.Code, codeInvalidParams)
	}
	// THE NAME IS QUOTED BACK. An agent that has just invented a tool
	// name needs to see which name was refused; a bare "unknown tool"
	// sends it round the same loop.
	if !strings.Contains(refused.Error.Message, "no-such-tool") {
		t.Errorf("the refusal does not name the tool that was asked for: %q", refused.Error.Message)
	}

	accepted := resultOf(t, replies[1])
	if accepted.IsError {
		t.Errorf("a registered tool's call came back marked as an error: %+v", accepted)
	}
	if len(accepted.Content) != 1 || !strings.Contains(accepted.Content[0].Text, `"say":"hello"`) {
		t.Errorf("the handler did not receive its arguments: %+v", accepted.Content)
	}
}

// TestAToolThatFailsIsAResultAndNotAProtocolError pins the split the
// Handler doc argues for: a deploy that did not work is a successful
// call whose result says so, not a JSON-RPC error saying this server is
// broken.
//
// REQUIRED MUTATION, run 2026-09-08: in ErrorResult, drop the
// `r.IsError = true` line. Reds here.
func TestAToolThatFailsIsAResultAndNotAProtocolError(t *testing.T) {
	s := testServer()
	s.Register(Tool{
		Name:    "always-fails",
		Handler: func(json.RawMessage) Result { return ErrorResult("the build did not compile") },
	})

	stdout, _ := drive(t, s, callMessage("1", "always-fails", `{}`))
	r := onlyReply(t, stdout)
	if r.Error != nil {
		t.Fatalf("a tool failure was reported as a protocol error: %+v", r.Error)
	}
	got := resultOf(t, r)
	if !got.IsError {
		t.Error("a failed call came back unmarked, so a client cannot tell it from an answer")
	}
	if len(got.Content) != 1 || got.Content[0].Type != contentTypeText {
		t.Fatalf("content = %+v, want one text block", got.Content)
	}
	if got.Content[0].Text != "the build did not compile" {
		t.Errorf("text = %q, want the tool's own words", got.Content[0].Text)
	}
}

// TestToolsListNamesEveryToolInRegistrationOrder covers the listing, and
// the ordering with it: a map's iteration order is not stable, so a
// listing built straight off the registry reshuffles itself between runs
// and nobody can diff two of them.
//
// REQUIRED MUTATION, run 2026-09-08: in listTools, range over s.tools
// instead of s.order. Reds intermittently, which is itself the argument
// — run it a few times.
func TestToolsListNamesEveryToolInRegistrationOrder(t *testing.T) {
	s := testServer()
	for _, name := range []string{"zebra", "aardvark", "moose"} {
		s.Register(echoTool(name))
	}

	stdout, _ := drive(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var got struct {
		Tools []struct {
			Name        string          `json:"name"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(onlyReply(t, stdout).Result, &got); err != nil {
		t.Fatalf("decoding the tool list: %v", err)
	}

	var names []string
	for _, tool := range got.Tools {
		names = append(names, tool.Name)
	}
	if want := []string{"zebra", "aardvark", "moose"}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tools = %v, want them in registration order %v", names, want)
	}
	for _, tool := range got.Tools {
		if tool.Title == "" || tool.Description == "" {
			t.Errorf("%s was listed without its title or description: %+v", tool.Name, tool)
		}
		if len(tool.InputSchema) == 0 {
			t.Errorf("%s was listed with no input schema, which the protocol requires", tool.Name)
		}
	}
}

// TestAnEmptyToolListIsAnArrayAndNotNull covers the shape of the listing
// before any tool exists — which is the state this server ships in until
// its tools land, so it is the state a client will actually meet.
//
// A nil slice marshals to null. A client reading null where an array was
// promised either crashes or decides this server has no tools for a
// reason it cannot see; an empty array is a true statement.
//
// REQUIRED MUTATION, run 2026-09-08: in listTools, declare
// `var tools []toolDescriptor` instead of make. Reds here.
func TestAnEmptyToolListIsAnArrayAndNotNull(t *testing.T) {
	stdout, _ := drive(t, testServer(), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if got := string(onlyReply(t, stdout).Result); got != `{"tools":[]}` {
		t.Errorf("an empty tool list rendered as %s, want {\"tools\":[]}", got)
	}
}

// TestAToolWithNoSchemaIsListedWithTheEmptyObjectSchema covers the
// substitution: the protocol requires an inputSchema on every tool, and
// a tool that takes no arguments is an ordinary case rather than a
// reason to omit a required field.
//
// REQUIRED MUTATION, run 2026-09-08: in listTools, drop the empty-schema
// substitution and list t.InputSchema as it stands. Reds here — the
// field marshals as null, which is not a schema.
func TestAToolWithNoSchemaIsListedWithTheEmptyObjectSchema(t *testing.T) {
	s := testServer()
	s.Register(Tool{
		Name:    "takes-nothing",
		Handler: func(json.RawMessage) Result { return TextResult("ok") },
	})

	stdout, _ := drive(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var got struct {
		Tools []struct {
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(onlyReply(t, stdout).Result, &got); err != nil {
		t.Fatalf("decoding the tool list: %v", err)
	}
	if len(got.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(got.Tools))
	}
	if string(got.Tools[0].InputSchema) != schemaForNoArguments {
		t.Errorf("inputSchema = %s, want %s", got.Tools[0].InputSchema, schemaForNoArguments)
	}
}

// TestOneMessagePerLineEvenWhenARawSchemaIsIndented covers the framing
// against the one thing in this package that can carry a newline into an
// outbound message: a tool's input schema, which is passed through as
// raw bytes so an author can write it readably.
//
// An indented schema is the natural way to write one. If it survived
// into the outbound message it would split a single reply across as many
// lines as the schema has, and the client would see one message it can
// parse followed by a dozen parse errors — a defect whose cause is
// invisible from either end.
//
// WHAT THIS ROW ACTUALLY PINS IS A DEPENDENCY ON encoding/json, and that
// is worth saying because it is not obvious from the code it covers.
// writeMessage does no compaction of its own: the encoder compacts a raw
// message when it marshals one, which was MEASURED rather than assumed
// after an explicit compaction step turned out to be unprovable — no
// mutation could red this row with that step removed, because the
// standard library had already done the work. So this row is the only
// thing standing between the framing and a change in behaviour nobody
// here would author.
//
// REQUIRED MUTATION, run 2026-09-08: in writeMessage, add
// enc.SetIndent("", "  "). Reds here — which is what shows this row can
// see a message that is not one line.
func TestOneMessagePerLineEvenWhenARawSchemaIsIndented(t *testing.T) {
	s := testServer()
	s.Register(Tool{
		Name: "readable-schema",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "dir": { "type": "string" }
  }
}`),
		Handler: func(json.RawMessage) Result { return TextResult("ok") },
	})

	stdout, _ := drive(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if lines := protocolLines(t, stdout); len(lines) != 1 {
		t.Fatalf("the tool listing arrived as %d lines, want 1: %q", len(lines), stdout)
	}
	// And the schema survived the compaction as a schema, rather than
	// being flattened into a string or dropped.
	var got struct {
		Tools []struct {
			InputSchema struct {
				Type string `json:"type"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(onlyReply(t, stdout).Result, &got); err != nil {
		t.Fatalf("decoding the tool list: %v", err)
	}
	if len(got.Tools) != 1 || got.Tools[0].InputSchema.Type != "object" {
		t.Errorf("the schema did not survive compaction: %+v", got.Tools)
	}
}

// TestRegisteringTheSameNameTwiceIsRefusedAtWiringTime covers the one
// place this package panics on purpose. It happens before a client is
// connected, so there is no session to protect and nothing to report to;
// letting the second registration win would silently replace a tool with
// another wearing its name.
//
// REQUIRED MUTATION, run 2026-09-08: delete the duplicate check from
// Register. Reds here.
func TestRegisteringTheSameNameTwiceIsRefusedAtWiringTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool Tool
	}{
		{"no name", Tool{Handler: func(json.RawMessage) Result { return Result{} }}},
		{"no handler", Tool{Name: "handlerless"}},
		{"a duplicate name", echoTool("echo")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer()
			s.Register(echoTool("echo"))

			defer func() {
				if recover() == nil {
					t.Error("registering it was allowed")
				}
			}()
			s.Register(tc.tool)
		})
	}
}

// TestRegisteringADistinctToolIsAllowed is the acceptance control for
// the row above: a refusal row that refuses everything looks identical
// from the outside to one that discriminates.
func TestRegisteringADistinctToolIsAllowed(t *testing.T) {
	s := testServer()
	s.Register(echoTool("echo"))
	s.Register(echoTool("echo-two"))
	if len(s.order) != 2 {
		t.Errorf("two distinct tools registered as %v, want both", s.order)
	}
}
