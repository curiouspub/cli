package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
)

// Handler runs one tool call and returns what the client should see.
//
// IT RETURNS A RESULT AND NEVER AN ERROR, and that is the protocol's own
// split rather than a style choice. A tool that ran and failed is a
// SUCCESSFUL call whose result is marked as an error — the agent reads
// the text, learns the deploy did not work, and tries something else. A
// JSON-RPC error means the REQUEST was wrong: an unknown tool, arguments
// that would not decode. A handler able to return an error would let an
// ordinary failure be reported as a protocol fault, which tells the
// client this server is broken when the truth is that a build did not
// compile.
//
// Arguments arrive as raw bytes so that each tool decodes into its own
// shape and reports its own bad input, in its own words.
type Handler func(arguments json.RawMessage) Result

// Tool is one callable this server exposes.
//
// InputSchema is raw JSON — a JSON Schema object — rather than a
// reflected Go type. The schema is what an agent reads to decide how to
// call the tool, so it is prose as much as it is validation, and a
// generated one cannot carry the sentence that explains what a field is
// for.
type Tool struct {
	// Name is how the client asks for this tool, and it is the key it
	// is registered under.
	Name string

	// Title is an optional human-readable name for display.
	Title string

	// Description tells the model what the tool does and when to reach
	// for it. It is the single most load-bearing string here: a tool
	// nobody can tell apart from its neighbour is a tool called at the
	// wrong moment.
	Description string

	// InputSchema is the JSON Schema for the arguments. An empty value
	// is rendered as the schema for "an object with no defined
	// properties", because the protocol requires the field on every
	// tool and a tool that takes no arguments is an ordinary case, not
	// a reason to make every author write the same four bytes.
	InputSchema json.RawMessage

	// Handler runs the call. See the Handler doc for why it cannot
	// report a protocol error.
	Handler Handler
}

// Result is what one tool call produced.
//
// IsError is written on every reply rather than omitted when false. It
// is the field a client keys on to decide whether to show the text as an
// answer or as a failure, and a field that is sometimes absent is one
// every reader has to know the default of.
type Result struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError"`
}

// Content is one block of a tool's output. Text is the only kind this
// server produces: everything these tools have to say — a URL, a build
// log, a reason a deploy stopped — is text, and a block type nothing
// emits is a shape nothing has ever checked.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// contentTypeText is the protocol's discriminator for a text block.
const contentTypeText = "text"

// TextResult is a successful call carrying one block of text.
func TextResult(format string, args ...any) Result {
	return Result{Content: []Content{{Type: contentTypeText, Text: fmt.Sprintf(format, args...)}}}
}

// ErrorResult is a call that ran and failed, carrying the reason.
//
// The reason is read by a model deciding what to do next, so it is
// written the way every other message in this program is written: name
// what happened and name something the reader can do about it.
func ErrorResult(format string, args ...any) Result {
	r := TextResult(format, args...)
	r.IsError = true
	return r
}

// schemaForNoArguments is what a tool with no declared schema is listed
// with. The protocol requires an inputSchema on every tool, so the
// choice is between this and refusing to list the tool at all.
const schemaForNoArguments = `{"type":"object"}`

// invoke calls one tool's handler, containing a panic in it.
//
// THIS IS THE ONLY recover() IN THE PACKAGE AND ITS POSITION IS THE
// RULE, not an implementation detail. Handlers are code this package
// does not own, and this is the one host where the process outlives the
// call — in a terminal run a panicking callback costs one run, here it
// would cost the server and every later call the client meant to make.
//
// It does NOT wrap the transport loop and it does NOT wrap the
// dispatcher. A panic in the protocol code is a real fault in this
// program: converting it into a structured error would hide a defect in
// this package behind the mechanism built to survive somebody else's,
// and the symptom would be a server that answers every request with the
// same polite failure. There is a guard asserting the position, because
// the tempting repair for a server that crashed once is to move this
// defer one function outwards.
//
// THE PANIC VALUE GOES TO THE LOG AND NEVER TO THE CLIENT. A panic
// carries whatever the panicking code was holding, which in this program
// can be a token or an upload URL, and the client's stream is read by a
// model and then usually by a person in a transcript. The operator gets
// the value and the stack on the diagnostic stream; the client is told
// which tool failed and that the server is still up.
func invoke(t Tool, arguments json.RawMessage, logw io.Writer) (result Result) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		fmt.Fprintf(logw, "curious mcp: the %s tool panicked: %v\n%s\n", t.Name, r, debug.Stack())
		result = ErrorResult("The %s tool failed unexpectedly and nothing it was doing was finished. "+
			"The server is still running, so you can try the call again.", t.Name)
	}()
	return t.Handler(arguments)
}
