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
//
// # THE FOUR TOOLS, AND WHY THERE IS NO FIFTH
//
// login_start and login_verify get a credential onto this machine;
// deploy_site runs a whole deploy and answers with the address; and
// deploy_status reports what a deploy's build log said. They are
// REGISTERED BY THE COMMAND rather than by this package, so the server
// stays a protocol and a registry and a test can stand one up with
// whatever set it needs.
//
// There is no whoami. It would have to wrap a route the API does not
// serve, so every call to it would answer an agent with a transport
// failure — and a model that can see a tool keeps trying it. It appears
// in no listing, no description and no usage text until that route
// exists.
//
// # EVERY TOOL DRIVES THE SEQUENCE THE COMMAND DRIVES
//
// The tools resolve their endpoint with the resolver the command uses,
// read the configuration the command reads, and enter the deploy at the
// same door. THEY TAKE NO DEPENDENCIES AND THERE IS NO SEAM TO POINT
// SOMEWHERE ELSE, which is the point: the failure this whole split
// exists to prevent is a second implementation of the deploy path, and
// the cheapest way not to have one is to have nowhere to put it.
//
// What differs between the two surfaces is the terminal and the answer.
// The terminal handed to the sequence here refuses every question except
// the pre-flight one, which it answers yes — warnings are non-blocking
// on this surface by design, and the findings come back as fields
// instead. The answer is an OBJECT rather than a sentence and an
// address: an agent-facing result is not a rendering of the human one,
// and a caller reading a field is not parsing English out of a paragraph
// written to be read by a person.
//
// # WHAT A DESCRIPTION MAY SAY
//
// A tool description is what a model reads to decide how to call
// something, so a figure in one is a promise. The only figures these may
// make are the wire contract's own, because those are the numbers the
// server enforces; quota and expiry are server policy, which changes
// without a release and which this binary is not told about, so they are
// described in words and carry no number at all. A guard parses the
// rendered descriptions and requires every number in them to be one of
// the contract's.
//
// # PROGRESS IS WIRED, AND ONLY WHEN A CLIENT ASKS
//
// A handler is given a context and a Progress alongside its arguments. A
// client that wants to watch a call sends a progress token on it, and
// every report the tool makes then goes out as a notification quoting
// that token, before the reply. Without a token the reports go to the
// sink that discards — which is the ordinary case and costs a tool
// nothing, because it reports either way and never asks whether anybody
// is listening.
package mcp
