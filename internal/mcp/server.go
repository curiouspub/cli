package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
const ProtocolVersion = "2025-06-18"

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
func (s *Server) Serve(in io.Reader, out, logw io.Writer) error {
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

		reply, answer := s.handle(line, logw)
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
func (s *Server) handle(line []byte, logw io.Writer) (response, bool) {
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
		result, rpcErr := s.callTool(req.Params, logw)
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
func (s *Server) handleNotification(req request, logw io.Writer) {
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
		fmt.Fprintf(logw, "curious mcp: ignoring the %s notification, which this server does not implement\n", req.Method)
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
func (s *Server) initialize(params json.RawMessage, logw io.Writer) initializeResult {
	var p initializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			fmt.Fprintf(logw, "curious mcp: the handshake parameters would not decode (%v); "+
				"answering with revision %s anyway\n", err, ProtocolVersion)
		}
	}
	if p.ProtocolVersion != "" && p.ProtocolVersion != ProtocolVersion {
		client := "an unnamed client"
		if p.ClientInfo != nil && p.ClientInfo.Name != "" {
			client = p.ClientInfo.Name
		}
		fmt.Fprintf(logw, "curious mcp: %s asked for protocol revision %s; this server speaks %s "+
			"and answered with that\n", client, p.ProtocolVersion, ProtocolVersion)
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
func (s *Server) callTool(params json.RawMessage, logw io.Writer) (Result, *rpcError) {
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

	return invoke(tool, p.Arguments, logw), nil
}
