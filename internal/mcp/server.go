package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/curiouspub/cli/internal/ui"
)

// ProtocolVersion is the revision of the Model Context Protocol this
// server speaks, and it is PINNED BY NAME on purpose.
//
// A wire contract left unnamed is one whoever implements it picks at the
// keyboard, and this one has a client on the other end. The server
// answers the handshake with this value; a client asking for a different
// revision is answered with this one rather than with silence or with
// its own guess echoed back, which is what the protocol's negotiation
// rule says to do and lets the client decide whether it can proceed.
//
// Changing it is a one-line change here and a deliberate one everywhere
// else: the shape of the messages below is the shape THIS revision
// defines, so the value and the code that implements it move together.
//
// WHY THIS ONE, AND NOT THE CURRENT ONE. The protocol's current revision
// is 2026-07-28, and it is not a newer spelling of what is below — it
// replaces the handshake entirely. The version travels per-request in a
// _meta key rather than being agreed once, there is a mandatory discovery
// call, and an unsupported version comes back as a typed error. This
// server implements the HANDSHAKE-BASED family, whose last revision is
// the one pinned here, and the current specification documents backward
// compatibility with exactly that family. So this is a deliberate
// position rather than a lag: the newest revision of the shape this
// server actually speaks.
//
// THE TRIGGER FOR MOVING IS EVIDENCE, not a date. Two things reopen it,
// and both are observable rather than a matter of taste:
//
//   - a real client that REFUSES this revision — the compatibility the
//     current specification documents is a claim, and the first refusal
//     is the measurement that tests it;
//   - a tool this server must expose that the handshake-based family
//     cannot carry.
//
// Either one reopens the decision to hand-roll rather than take a
// library, because that decision was made on a version negotiated once,
// which is exactly what the newer shape stops being.
const ProtocolVersion = "2025-11-25"

// The method names this server implements. They are constants because
// each is matched in one place and named in another, and a method
// dispatched under a name nothing else spells is how a server quietly
// stops answering something.
const (
	methodInitialize  = "initialize"
	methodInitialized = "notifications/initialized"
	methodToolsList   = "tools/list"
	methodToolsCall   = "tools/call"
	methodPing        = "ping"
)

// maxMessageBytes bounds one inbound message.
//
// THE BOUND IS THE POINT, not the number — the same argument the
// terminal prompt makes about its attempt limit. This process is started
// by a client and reads until that client closes the pipe, so an
// unbounded read is a peer able to make this program consume memory
// until the machine notices. Four mebibytes is far more than any message
// this protocol carries and far less than a problem.
//
// A message over the limit ENDS THE RUN rather than being skipped,
// because the reader cannot know where the oversized message stopped:
// resuming would mean parsing the tail of one message as the whole of
// the next, and answering a request nobody sent is worse than stopping
// with a reason on the log.
const maxMessageBytes = 4 << 20

// initialMessageBytes is the buffer the scanner starts with, so that the
// ordinary message — a few hundred bytes — costs one small allocation
// rather than the maximum above.
const initialMessageBytes = 4 << 10

// Server dispatches the protocol. It is built once, has its tools
// registered, and is then handed a pipe pair to serve until the client
// goes away.
type Server struct {
	// name and version identify this implementation to the client. They
	// are the binary's own, passed in rather than read here, so that a
	// development build cannot report itself as a release.
	name    string
	version string

	// tools is the registry, by the name a client calls. order keeps
	// registration order so that the listing is stable between runs —
	// a map's iteration order is not, and a tool list that reshuffles
	// itself is one nobody can diff.
	tools map[string]Tool
	order []string
}

// New returns a server that identifies itself with the given name and
// version and has no tools yet.
func New(name, version string) *Server {
	return &Server{name: name, version: version, tools: map[string]Tool{}}
}

// Register adds a tool to the surface this server exposes.
//
// It PANICS on an empty or duplicate name, and that is deliberate. This
// runs at wiring time, before a client is connected and outside every
// containment in this package: a tool registered twice, or under no
// name, is a programming error in this binary that has no correct
// runtime behaviour — the second registration would silently replace the
// first, and the client would call a tool that is not the one anybody
// meant.
func (s *Server) Register(t Tool) {
	if t.Name == "" {
		panic("mcp: a tool was registered with no name")
	}
	if _, exists := s.tools[t.Name]; exists {
		panic("mcp: two tools registered as " + t.Name)
	}
	if t.Handler == nil {
		panic("mcp: the " + t.Name + " tool was registered with no handler")
	}
	s.tools[t.Name] = t
	s.order = append(s.order, t.Name)
}

// Serve reads messages from in, writes replies to out, and writes
// everything else to logw. It returns when in reaches its end, which is
// how a client shuts this server down.
//
// THE THREE STREAMS ARE SEPARATE ARGUMENTS BECAUSE THE SPLIT IS THE
// WHOLE RULE. out is the protocol and carries nothing else; logw is
// where every diagnostic goes. A function handed both cannot reach for
// the process's own streams and so cannot write a log line into the
// protocol by forgetting which one it was holding.
//
// THE CONTEXT IS TAKEN HERE AND HANDED TO EVERY TOOL CALL, rather than
// being minted further down where it is needed. A context created inside
// the dispatcher is one nothing outside this package can ever cancel,
// and handing a handler that value while calling it "your call's
// context" is a parameter that lies about what it is. Taken as an
// argument it is whatever the entry point decided — today the process's
// own, and the day a run learns to stop on a signal or on the client's
// cancellation, a real one, with nothing in this file to change.
//
// IT DOES NOT END THE READ LOOP. The loop ends when the client closes
// the pipe, which is the protocol's own shutdown and the only one this
// server has ever had. Making a cancelled context stop the reader as
// well would be a second way out with different tidy-up, and there is no
// caller asking for one.
// notes is the diagnostic half of the connection.
//
// EVERYTHING THIS SERVER SAYS ABOUT A CLIENT IS ABOUT SOMETHING THE
// CLIENT SENT — a method name it chose, a protocol revision it asked
// for, the name it gave itself. Written straight to the stream those
// were terminal control sequences on an operator's screen: a cold
// review turned an MCP notification called
// "notifications/\x1b]0;pwned\a" into a retitled window, through a raw
// io.Writer this package was handed and trusted.
//
// It goes through the same boundary as everything else now. The format
// is this server's own words and an argument is the client's, which is
// the rule the rest of the program keeps — and go vet holds it here too,
// because say forwards its own format and variadic to a renderer it
// already knows about.
type notes struct{ render *ui.UI }

func newNotes(w io.Writer) notes {
	// io.Discard for the machine half: this connection's stdout is the
	// protocol and carries nothing else.
	return notes{render: ui.Writing(io.Discard, w)}
}

func (n notes) say(format string, args ...any) { n.render.Step(format, args...) }

func (s *Server) Serve(ctx context.Context, in io.Reader, out, logw io.Writer) error {
	diagnostics := newNotes(logw)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, initialMessageBytes), maxMessageBytes)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		// A blank line is not a message. Clients do not send them, but
		// a person driving this by hand in a terminal sends one every
		// time they press return, and answering it with a parse error
		// is a confusing way to greet somebody who is exploring.
		if len(line) == 0 {
			continue
		}

		reply, answer := s.handle(ctx, line, diagnostics)
		if !answer {
			continue
		}
		if err := writeMessage(out, reply); err != nil {
			return fmt.Errorf("writing a reply to the client: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return fmt.Errorf("the client sent a message longer than the %d-byte limit; "+
				"the stream cannot be resynchronised after one, so this run stops here", maxMessageBytes)
		}
		return fmt.Errorf("reading from the client: %w", err)
	}
	return nil
}

// handle turns one inbound line into the reply that should go back, and
// reports whether there is a reply at all.
func (s *Server) handle(ctx context.Context, line []byte, logw notes) (response, bool) {
	// THE TWO WAYS AN INBOUND MESSAGE CAN BE UNREADABLE ARE DIFFERENT
	// FAULTS AND GET DIFFERENT CODES, which is worth the extra call
	// because the codes are the only thing a client can act on. Bytes
	// that are not JSON mean the framing has gone wrong — a message
	// split across lines, a stray write from something sharing the
	// stream. Valid JSON that is not a request object means the client
	// sent something well-formed and wrong, which is a bug in the
	// client rather than in the pipe.
	//
	// BOTH ARE ANSWERED WITH A NULL ID, because in both cases the id is
	// exactly what could not be read.
	if !json.Valid(line) {
		return errorReply(nil, codeParseError, "the message was not valid JSON"), true
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return errorReply(nil, codeInvalidRequest,
			"the message was valid JSON but not a JSON-RPC request object"), true
	}

	// NOTIFICATIONS ARE DISPATCHED HERE, ahead of everything that
	// produces a reply, because "a notification is never answered" is
	// the rule and this is the one place it can be enforced rather than
	// remembered. Doing it the other way — dispatch first, drop the
	// reply afterwards — means a tools/call arriving without an id would
	// RUN, with nowhere to report what it did.
	if req.isNotification() {
		s.handleNotification(req, logw)
		return response{}, false
	}

	// AN EXPLICIT NULL ID IS ANSWERED, NOT DISPATCHED. The protocol
	// discourages null as a request id and this server declines to guess
	// what a client meant by it — but declining to guess is not the same
	// as declining to reply. Answered here rather than run and answered
	// afterwards, so a tools/call spelled this way cannot do work whose
	// result nobody can match to a request.
	if req.isNullID() {
		return errorReply(req.ID, codeInvalidRequest,
			"a request id of null cannot be matched to a reply; send a string or a number"), true
	}

	if req.JSONRPC != jsonrpcVersion {
		return errorReply(req.ID, codeInvalidRequest,
			fmt.Sprintf("this server speaks JSON-RPC %s; the message declared %q", jsonrpcVersion, req.JSONRPC)), true
	}

	switch req.Method {
	case methodInitialize:
		return resultReply(req.ID, s.initialize(req.Params, logw)), true

	case methodToolsList:
		return resultReply(req.ID, s.listTools()), true

	case methodToolsCall:
		result, rpcErr := s.callTool(ctx, req.Params, logw)
		if rpcErr != nil {
			return errorReply(req.ID, rpcErr.Code, rpcErr.Message), true
		}
		return resultReply(req.ID, result), true

	case methodPing:
		// An empty result is the whole of it: the question is whether
		// this process is alive and reading, and the answer is that it
		// replied. Implemented because the protocol requires a prompt
		// reply from both sides, and a client that pings a server which
		// answers "no such method" is entitled to conclude it is
		// talking to something broken.
		return resultReply(req.ID, struct{}{}), true

	default:
		return errorReply(req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method)), true
	}
}

// handleNotification acts on a message that expects no reply.
func (s *Server) handleNotification(req request, logw notes) {
	switch req.Method {
	case methodInitialized:
		// The client saying it is ready. There is nothing to do: this
		// server sends nothing unprompted, so no queue is waiting on
		// the handshake to complete. It is named here rather than left
		// to fall through so that a reader can see the method set is
		// complete, and so that the first server-initiated message has
		// an obvious place to be released from.

	default:
		// IGNORED TOWARD THE CLIENT, REPORTED TOWARD THE OPERATOR. The
		// protocol grows notifications — cancellation, progress, roots
		// changing — and a client sending one this server does not
		// implement has done nothing wrong; a reply would be a protocol
		// violation and an error reply would be a lie. The log line is
		// what turns "the agent's cancel does nothing" from a mystery
		// into a sentence somebody can read.
		logw.say("curious mcp: ignoring the %s notification, which this server does not implement", req.Method)
	}
}

// implementation names one side of the connection.
type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// toolsCapability is the tools capability object. It is empty: this
// server's tool list is fixed at wiring time, so it does not announce
// listChanged, which would be a promise to send notifications it has no
// way to produce.
type toolsCapability struct{}

// capabilities is what this server can do. Only tools, because tools are
// the entire point of this surface — there are no resources to read, no
// prompts to offer and no sampling to ask for.
type capabilities struct {
	Tools toolsCapability `json:"tools"`
}

// initializeResult is the handshake answer.
type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    capabilities   `json:"capabilities"`
	ServerInfo      implementation `json:"serverInfo"`
}

// initializeParams is the part of the client's handshake this server
// reads. The client's capabilities are deliberately not decoded: this
// server uses none of them, and a struct listing fields nothing consults
// is a claim to negotiate something.
type initializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	ClientInfo      *implementation `json:"clientInfo"`
}

// initialize answers the handshake.
//
// IT CANNOT FAIL, and that is the negotiation rule rather than
// carelessness. When the client asks for a revision this server does not
// speak, the protocol says to answer with a revision this server DOES
// speak and let the client decide whether it can proceed — so parameters
// that will not decode produce the same answer as parameters that do.
// Refusing the handshake would leave a client that could have downgraded
// with nothing to downgrade to.
//
// The mismatch is not silent, it is just not silent AT THE CLIENT: it
// goes to the log, where the person who has to explain why an old client
// stopped working can read it.
func (s *Server) initialize(params json.RawMessage, logw notes) initializeResult {
	var p initializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			logw.say("curious mcp: the handshake parameters would not decode (%s); "+
				"answering with revision %s anyway", err.Error(), ProtocolVersion)
		}
	}
	if p.ProtocolVersion != "" && p.ProtocolVersion != ProtocolVersion {
		client := "an unnamed client"
		if p.ClientInfo != nil && p.ClientInfo.Name != "" {
			client = p.ClientInfo.Name
		}
		logw.say("curious mcp: %s asked for protocol revision %s; this server speaks %s "+
			"and answered with that", client, p.ProtocolVersion, ProtocolVersion)
	}

	return initializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    capabilities{},
		ServerInfo:      implementation{Name: s.name, Version: s.version},
	}
}

// toolDescriptor is one entry of the tool listing.
type toolDescriptor struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toolsListResult is the tool listing.
type toolsListResult struct {
	Tools []toolDescriptor `json:"tools"`
}

// listTools renders the registered tools in registration order.
//
// THE SLICE IS ALLOCATED EVEN WHEN IT IS EMPTY. A nil slice marshals to
// null, and a client reading null where an array was promised either
// crashes or decides this server has no tools for a reason it cannot
// see. An empty list is a true statement; null is a broken message.
func (s *Server) listTools() toolsListResult {
	tools := make([]toolDescriptor, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		schema := t.InputSchema
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(schemaForNoArguments)
		}
		tools = append(tools, toolDescriptor{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return toolsListResult{Tools: tools}
}

// callParams is a tool invocation.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callTool runs one tool and returns either its result or the reason the
// REQUEST was wrong.
//
// The two returns are the split the Handler doc describes, arriving at
// the place it is decided: an unknown name and unreadable parameters are
// faults in the request and are answered as protocol errors, and
// everything from the tool itself — including a total failure — comes
// back as a result the client can read.
func (s *Server) callTool(ctx context.Context, params json.RawMessage, logw notes) (Result, *rpcError) {
	var p callParams
	if err := json.Unmarshal(params, &p); err != nil {
		return Result{}, &rpcError{Code: codeInvalidParams, Message: "the tool call parameters were not readable"}
	}

	tool, known := s.tools[p.Name]
	if !known {
		// The name is quoted back. An agent that has just called a tool
		// under a name it invented needs to see which name was refused,
		// and a bare "unknown tool" sends it round the same loop.
		return Result{}, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool %q", p.Name)}
	}

	// NOBODY IS LISTENING YET, AND THAT IS SAID HERE RATHER THAN LEFT TO
	// A NIL. Reporting a tool's progress to a client means a notification
	// with the token the client sent on the call it wants watched, which
	// is protocol this server does not speak today; the sink that turns
	// a report into one belongs to the change that has a tool to report
	// from. This is the line that change edits, and until it does, the
	// honest value is the sink that discards — not a field nothing ever
	// writes, which would be a seam pointing at nothing.
	return invoke(ctx, tool, p.Arguments, noProgress{}, logw), nil
}
