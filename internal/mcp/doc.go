// Package mcp is the Model Context Protocol server this binary speaks
// over stdin and stdout, so that an agent deploys the same way a person
// does.
//
// # STDOUT IS THE PROTOCOL
//
// This is the rule the whole package is arranged around, and it is the
// single defect most likely to be written here. The transport is
// line-delimited JSON on the process's own stdout; one stray byte on
// that stream is a message the client cannot parse, and what the user
// sees is a client that disconnected for no stated reason. So every log
// line, every diagnostic and every prompt goes to a separate writer,
// which the command wires to stderr — and Serve takes the two streams as
// SEPARATE ARGUMENTS rather than reaching for the process's own, because
// a function that cannot name the wrong stream cannot write to it by
// accident.
//
// # WHY THIS IS HAND-ROLLED
//
// The protocol over stdio is JSON-RPC 2.0 with a small method set —
// initialize, notifications/initialized, tools/list, tools/call, ping —
// over newline-delimited JSON on a pipe pair, which encoding/json and a
// buffered scanner already do. An SDK would be this module's first
// genuine third-party dependency, in a public tool whose near-empty
// dependency list is the trust argument it makes to anyone reading it,
// and it would buy conformance that is bought better elsewhere: a real
// client speaking to this server end to end exercises the transport that
// actually ships rather than one delegated to a library.
//
// That is a decision with an exit rather than a position, and both
// triggers for revisiting it are observable rather than matters of
// taste. The first is a tool surface that outgrows this method set —
// anything needing resources, prompts, sampling, or messages this server
// starts itself. The second is a protocol revision this implementation
// would have to TRACK, rather than one version pinned and negotiated
// once.
//
// # NO PROMPTS, AND NO SECOND SWITCH TO DECIDE THAT
//
// A tool call has nobody to ask: under a client, stdin and stdout are
// pipes. The terminal package already decides interactivity from stdin
// and stderr both being terminals, so under a client every prompt
// already returns its no-terminal sentinel without this package doing
// anything. That is asserted here rather than re-implemented — two
// answers to "may I ask a question" is two things that can disagree, and
// the day they disagree the symptom is a server that hangs.
//
// # WHERE THE PANIC CONTAINMENT LIVES, AND WHERE IT DOES NOT
//
// A tool handler is code this package does not own, and this is the one
// host where the process OUTLIVES the call: in a terminal run a panic
// costs one run, here it costs the server and every later call the
// client was going to make. So a handler that panics is turned into a
// structured error and the server keeps serving.
//
// The containment is around the handler and NOWHERE ELSE — not the
// transport loop, not the dispatcher. A panic in the protocol code is a
// real fault in this program, and converting it into a tidy error
// message would hide a defect behind the very mechanism built to survive
// somebody else's. There is a guard asserting exactly that, because the
// tempting repair for a flaky server is to widen the recover by one
// function.
package mcp
