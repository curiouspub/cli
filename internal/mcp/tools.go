package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/ui"
)

// The names a client calls these tools by. They are constants because
// each is written in three places — the registration, the schema, and
// the sentence another tool's description uses to point at it — and a
// name spelled differently in one of them is a pointer to nothing.
const (
	toolLoginStart   = "login_start"
	toolLoginVerify  = "login_verify"
	toolDeploySite   = "deploy_site"
	toolDeployStatus = "deploy_status"
)

// RegisterTools adds the four callables this binary exposes to an agent,
// in the order a client will list them.
//
// # Four, and the fifth is named here because its absence is a decision
//
// There is no whoami. It would have to wrap a route the API does not
// serve, and a tool that wrapped a call that does not exist would answer
// an agent with a transport failure for every invocation — so it appears
// in no list, no description and no usage text until that route exists.
// Saying so here is cheaper than the next reader re-deriving it from an
// endpoint table.
//
// # They take no dependencies, and that is the point
//
// Every one of these resolves its endpoint with the resolver the command
// uses, reads the configuration the command reads, and drives the
// sequence the command drives. There is no seam to point somewhere else,
// because a seam a test supplies is a seam production can differ from:
// the failure this split exists to prevent is a second implementation of
// the deploy path, and the cheapest way not to have one is to have
// nowhere to put it.
func RegisterTools(s *Server) {
	s.Register(loginStartTool())
	s.Register(loginVerifyTool())
	s.Register(deploySiteTool())
	s.Register(deployStatusTool())
}

// endpoint is the API address this run talks to.
//
// IT GOES THROUGH THE SAME RESOLVER THE COMMAND USES, which is the whole
// of why this exists rather than each tool reaching for the default. The
// resolution answers one question — where is this run talking to — and a
// second answer to it would let an agent and a person on one machine
// reach different servers, or read a token stored against one and spend
// it at the other.
func endpoint() string { return api.ResolveBaseURL("") }

// decodeArguments reads a call's arguments into the shape a tool
// declared, and returns the result to send back when they will not.
//
// UNKNOWN FIELDS ARE REFUSED, and that is the opposite of the rule this
// project keeps on the WIRE. There, a client tolerates fields it does not
// know so a newer server can add them; here the sender is a model
// choosing a field name, and the likeliest reason for one this tool does
// not know is that it invented a spelling. Ignored silently, that is an
// argument the agent believes it passed and a tool that did something
// else — and it will keep passing it, because nothing said no.
//
// The name is quoted back by the decoder's own message, which is what
// makes the refusal actionable rather than a shrug.
func decodeArguments(raw json.RawMessage, into any) *Result {
	if len(bytes.TrimSpace(raw)) == 0 {
		// A call with no arguments object at all. Every field then takes
		// its zero value, which each tool checks for itself — a required
		// one missing is its own message, in its own words.
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		r := ErrorResult("The arguments could not be read: %s\n\n"+
			"Check the tool's input schema and call it again.", ui.Sanitize(err.Error()))
		return &r
	}
	return nil
}

// missingArgument is the refusal for a required field nobody sent. It
// names the field, because a tool that says only "missing argument"
// sends an agent round the same loop.
func missingArgument(field, what string) Result {
	return ErrorResult("%s is required: %s", field, what)
}

// jsonResult is a successful call whose answer is FACTS rather than
// prose.
//
// AN AGENT-FACING RESULT IS NOT A RENDERING OF THE HUMAN ONE. The
// terminal's answer to a deploy is a sentence and an address on two
// streams; this one is an object, so a caller reads a field instead of
// parsing English out of a paragraph that exists to be read by a person
// and is the part most likely to be reworded.
//
// It is indented, which costs a few bytes on a pipe and buys a
// transcript somebody can read when a call did something surprising.
func jsonResult(v any) Result {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Unreachable for the shapes below — strings, numbers and
		// slices of those — and reported rather than swallowed, because
		// the alternative is a successful call answering with nothing.
		return ErrorResult("The answer could not be encoded: %s", ui.Sanitize(err.Error()))
	}
	return TextResult("%s", string(encoded))
}

// refusalText renders what went wrong for an agent, through the shipped
// copy rather than a second set of messages.
//
// # It goes through the failure's own rendering
//
// A failure carries its parts and decides their order; a caller that
// joined What, Why and Next has rebuilt that order and will drift from it
// the day a fourth part arrives. So the parts come from the type that
// owns them, and what this adds is the paragraph separator — the terminal
// adds colour and a leading blank line, which a client does not want.
//
// # The sentinels are mapped HERE because the ACTION is this surface's
//
// The fact behind each is shared with the command — nobody to ask, the
// run was stopped, the door is shut — and what to DO about it is not: a
// person is told to run this in a terminal, and an agent has two tools to
// call instead. A sentinel left to fall through would render its own Go
// text, which is written for a log rather than for a reader.
//
// EVERY SENTINEL THE TERMINAL PACKAGE EXPORTS HAS A CASE, including the
// two that cannot arrive here. Nothing on these paths asks a question it
// would wait for an answer to, so a cancelled prompt and an unusable
// answer are unreachable — and a case that says so is the difference
// between a decision and a gap, which is exactly the shape the missing
// one took last time: the only unmapped member was the only one missing
// from the table, and a person who mistyped four times was told the
// program was broken.
//
// # The copy is preferred over the sentinel, and that is the opposite order to the exit code's
//
// The terminal's exit-code decision reads the sentinels FIRST, correctly:
// it is deciding what a run COSTS, and the sentinel is the thing that
// says so — a closed door costs its own number whatever copy came with
// it. This is deciding what a reader is TOLD, and there the most
// specific words win: a closed door met with nobody to ask carries a
// failure that names both facts and what to do, and reading the sentinel
// first would replace it with the generic sentence for either half.
func refusalText(err error) string {
	var failure *ui.Failure
	if errors.As(err, &failure) {
		// THE FAILURE'S OWN ESCAPED RENDERING, not a join of its raw
		// parts. The quotation inside a failure is a sentence the SERVER
		// wrote, and it is escaped WHOLE — newline included — precisely
		// so it cannot add a blank line and a paragraph that reads as
		// this program's. Joined raw, a server message ending in two
		// newlines and a sentence would arrive in a model's context as
		// something curious said. Only the package that owns the parts
		// knows which one is the quotation.
		return strings.Join(failure.Escaped(), "\n\n")
	}

	switch {
	case errors.Is(err, ui.ErrNotInteractive):
		return "curious is not logged in on this machine, and it cannot ask for an " +
			"address and a code here.\n\nCall " + toolLoginStart + " with the email " +
			"address to use, then " + toolLoginVerify + " with the code that arrives. " +
			"The login is stored, so this is a one-off."

	case errors.Is(err, ui.ErrInterrupted):
		return "The call was stopped before it finished, so what it was doing did not " +
			"complete.\n\nNothing here can say how far it got, and a deploy that was " +
			"already under way is the server's to finish or expire. Deploying again makes " +
			"a NEW deploy rather than resuming this one."

	case errors.Is(err, ui.ErrAborted), errors.Is(err, ui.ErrNoAnswer):
		// UNREACHABLE AND MAPPED ANYWAY. Both come from a prompt — one
		// from somebody cancelling it, one from it being answered
		// unusably several times — and nothing on these paths waits for
		// an answer: the terminal handed to the sequence refuses to ask
		// rather than asking badly. If one ever arrives it is a defect
		// in this package, and the reader should be told that rather
		// than handed a sentinel's own words.
		return "A step expected an answer from a person, which this surface cannot " +
			"provide.\n\nThat is a fault in curious rather than anything you did. " +
			"Please report it."

	case errors.Is(err, ui.ErrServerClosed):
		// REACHED ONLY BARE, which is the whole reason it has a case.
		// Every place this program marks a stop as a closed door wraps
		// copy that says which door and when it reopens, and that copy
		// is rendered above — so arriving here means the marker was used
		// with nothing behind it, and the honest answer is the one thing
		// the marker itself asserts.
		return "curious.pub is not taking this right now.\n\n" +
			"Nothing was deployed. Try again a little later."
	}
	// SOMEBODY ELSE'S TEXT, WHOLE. An error with no copy of its own is
	// usually a transport failure or a library's message, and a newline
	// in one is content rather than layout.
	return ui.Sanitize(err.Error())
}

// refusal is a call that ran and failed, carrying the shipped copy.
func refusal(err error) Result {
	return ErrorResult("%s", refusalText(err))
}

// noLoginText is what a tool that needs a stored login says when there
// is not one. The FACT comes from the sequence package, which owns
// whether a token is usable; the ACTION is this surface's, because the
// way out of it is two tool calls rather than a terminal.
func noLoginText(err error) string {
	said := ""
	// THE REASON IS READ OFF A FIELD rather than trimmed out of a
	// sentence. It is quoted and never re-derived — the configuration
	// package states in terms that its explanation is for a person to
	// read and that nothing should branch on its identity — and it is
	// escaped, because it names a file path and an endpoint that came
	// off this machine rather than out of this program.
	var missing *flow.NoLogin
	if errors.As(err, &missing) && missing.Reason != "" {
		said = "\n\n" + ui.SanitizeLines(missing.Reason)
	}
	return "curious is not logged in on this machine." + said +
		"\n\nCall " + toolLoginStart + " with the email address to use, then " +
		toolLoginVerify + " with the code that arrives."
}

// objectSchema renders a tool's input schema.
//
// IT IS WRITTEN OUT RATHER THAN REFLECTED off a Go struct, because the
// schema is PROSE as much as it is validation: the descriptions in it
// are what a model reads to decide how to call the tool, and a generated
// schema cannot carry the sentence that says what a field is for.
//
// additionalProperties is false on every one of them, which says out
// loud what the decoder already enforces — a model reading the schema
// learns the field set is closed before it invents a name, rather than
// after.
func objectSchema(properties string, required ...string) json.RawMessage {
	quoted := make([]string, 0, len(required))
	for _, name := range required {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return json.RawMessage(fmt.Sprintf(
		`{"type":"object","properties":{%s},"required":[%s],"additionalProperties":false}`,
		properties, strings.Join(quoted, ",")))
}
