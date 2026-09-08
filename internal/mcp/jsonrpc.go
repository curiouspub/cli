package mcp

import (
	"bytes"
	"encoding/json"
	"io"
)

// jsonrpcVersion is the only value this server accepts in, or writes to,
// the "jsonrpc" member. JSON-RPC 2.0 requires it verbatim on every
// message.
const jsonrpcVersion = "2.0"

// The JSON-RPC 2.0 error codes this server produces. They are the
// protocol's own reserved codes rather than anything invented here: a
// client showing a person "-32601" and a method name has told them
// something true, whereas a code from this program's own space is a
// number only this program can explain.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// request is one inbound message.
//
// THE ID IS HELD AS RAW BYTES, and that is load-bearing rather than
// lazy. JSON-RPC lets an id be a string or a number, and decoding a
// number into any makes it a float64 — so an id of 9007199254740993
// comes back as 9007199254740992 and the client is answered about a
// request it never sent. Raw bytes go back out exactly as they came in,
// which is the only behaviour that is correct for every id a client may
// choose.
//
// Params is raw for a different reason: each method decodes its own, so
// a malformed parameter object for one method cannot fail the decode of
// a message this server would otherwise dispatch fine.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification reports whether this message expects no answer.
//
// A missing id is the protocol's definition. An explicit null is
// forbidden as a request id and there is nothing useful to answer it
// with, so it is read the same way — an unanswerable message is a
// message not answered, which is the more conservative of the two
// readings and the only one that cannot produce a reply a client is
// unable to match.
func (r request) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// response is one outbound reply. Exactly one of Result and Error is
// ever set; both are omitted when empty so that a reply carrying an
// error does not also carry a null result, which some clients read as a
// successful call returning nothing.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a failure of the REQUEST — an unreadable message, an
// unknown method, an unknown tool, parameters that would not decode. A
// tool that ran and failed is not one of these; see Result.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// errorReply builds a reply carrying an error, for the id it came in on.
func errorReply(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: jsonrpcVersion, ID: id, Error: &rpcError{Code: code, Message: message}}
}

// resultReply builds a reply carrying a result, for the id it came in
// on.
func resultReply(id json.RawMessage, result any) response {
	return response{JSONRPC: jsonrpcVersion, ID: id, Result: result}
}

// writeMessage writes one message to the protocol stream, as exactly one
// line.
//
// THE ENCODER'S TRAILING NEWLINE IS THE FRAME. A message is delimited by
// a newline and may contain none of its own, and the second half of that
// is not something this function does — it is something encoding/json
// does, and this package DEPENDS on it. Strings are escaped, so a build
// log full of newlines is one line here; and a raw message passed
// through — a tool's input schema, carried as raw bytes so an author can
// write it readably — is COMPACTED by the encoder when it marshals it,
// indentation and all.
//
// That last part is measured rather than assumed, and the measurement is
// the reason this function is three lines instead of ten. An explicit
// compaction pass was written here first, on the reasoning that raw
// bytes are copied verbatim; no mutation could make the framing row red
// with it removed, because the encoder had already done the work. Rather
// than keep a step nothing could prove was load-bearing, the dependency
// is stated here and pinned by a row that feeds an indented schema
// through and requires one line out — which is the assertion that would
// notice if this ever stopped being true.
//
// HTML escaping is switched off because there is nothing here for it to
// protect. It exists so that JSON can be embedded in a script element;
// this stream goes to a pipe, and with it on, a build log containing a
// tag arrives at the agent as a wall of escape sequences.
func writeMessage(out io.Writer, msg any) error {
	var line bytes.Buffer
	enc := json.NewEncoder(&line)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(msg); err != nil {
		return err
	}
	_, err := out.Write(line.Bytes())
	return err
}
