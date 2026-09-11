package mcp

import (
	"context"
	"encoding/json"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/ui"
)

// loginDeps builds what the two login calls need: the endpoint this run
// talks to, and an unauthenticated client for it.
//
// THE ENDPOINT COMES FROM THE SAME RESOLVER THE COMMAND USES, so an
// agent and a person on one machine cannot end up logging in to
// different places — and a token is stored beside the endpoint that
// issued it, so they would not even share the result.
//
// THE CLIENT IS BUILT THROUGH THE SEQUENCE PACKAGE rather than directly,
// and that is not indirection for its own sake. An API address this
// client refuses has a refusal written for it over there — one that
// names the variable to look at and echoes NEITHER the value NOR the
// underlying error, because a base URL can carry a username and a
// password and the parse failure quotes the whole string it was handed.
// Returned raw, that error reaches a tool's output, and a tool's output
// is the one this program produces that is also kept in a model's
// context.
//
// There is no prompter and no waitlist offer. Both are terminal things:
// the offer asks a question and then asks for an address, and there is
// nobody here to ask — so a closed door ends with the copy written for
// exactly that case rather than with a question nothing can answer.
//
// The client is handed back BESIDE the dependencies rather than fished
// back out of them: the capacity gate asks for a wider slice of it than
// the login does, and reaching through an interface field with a type
// assertion to get the wider one is a cast that compiles today and
// panics the day the seam is satisfied by something else.
func loginDeps() (flow.LoginDeps, *api.Client, error) {
	base := endpoint()
	client, err := flow.UnauthenticatedClient(base)
	if err != nil {
		return flow.LoginDeps{}, nil, err
	}
	return flow.LoginDeps{Auth: client, Endpoint: base}, client, nil
}

// loginStartArgs is what login_start is called with.
type loginStartArgs struct {
	Email string `json:"email"`
}

// loginStartDescription is what a model reads to decide whether this is
// the tool it wants.
//
// NO NUMBER APPEARS IN IT, and that is the rule rather than an
// oversight. A description may carry a figure only when the figure is a
// constant of the wire contract, and there is no send budget, no
// cooldown and no code length in that contract — so a number here would
// be this client stating a limit the server does not take from it, which
// is worse than stating none. What is true and useful is said in words.
const loginStartDescription = "Ask curious.pub to email a one-time login code to an address. " +
	"Call this first on a machine that has never logged in, and again if a code " +
	"has expired. Follow it with " + toolLoginVerify + ", which exchanges the code " +
	"for the credential every other tool here spends.\n\n" +
	"A success means the request was accepted and NOTHING MORE. The server answers " +
	"identically whether or not it actually sent a code — it will not reveal whether " +
	"an address is known to it — so do not tell the user a code is on its way; tell " +
	"them to check the address they gave you, spam folder included.\n\n" +
	"The trial has a daily cap on new accounts. When the cap is shut this refuses " +
	"rather than sending a code that could not be used, and says when it reopens."

func loginStartTool() Tool {
	return Tool{
		Name:        toolLoginStart,
		Title:       "Start a login",
		Description: loginStartDescription,
		InputSchema: objectSchema(
			`"email":{"type":"string","description":"The address to email the login code to. `+
				`Ask the user for it; never guess one, and never reuse an address from another project."}`,
			"email"),
		Handler: func(ctx context.Context, arguments json.RawMessage, _ Progress) Result {
			var args loginStartArgs
			if bad := decodeArguments(arguments, &args); bad != nil {
				return *bad
			}
			if args.Email == "" {
				return missingArgument("email", "the address to email the login code to")
			}

			deps, client, err := loginDeps()
			if err != nil {
				return refusal(err)
			}

			// THE CAPACITY GATE RUNS HERE FOR THE REASON IT RUNS BEFORE
			// THE COMMAND'S OWN LOGIN: the daily cap counts accounts and
			// an account is spent at the verify, so the gate belongs
			// immediately before the login it gates. Skipping it would
			// cost an email, a person reading it, and a code typed back
			// to an agent, before anybody learned the door was shut.
			//
			// AND IT IS TOLD WHETHER A LOGIN IS ACTUALLY GOING TO SPEND
			// ONE. The cap counts NEW accounts, and a repeat verify for
			// an identity that already holds a token reissues rather than
			// spending a second slot — so a run that already holds a
			// usable credential costs the day nothing, and refusing it
			// would be refusing free work. The command enforces that by
			// only reaching its login when the stored token is missing or
			// refused; this tool has no such precondition, because an
			// agent may call it at any time, so it asks the same question
			// through the same door instead.
			_, noLogin := flow.OpenStoredLogin(deps.Endpoint)
			if err := flow.CapacityGate(ctx, flow.CapacityDeps{
				Prompt:    quietPrompter{},
				API:       client,
				HaveToken: noLogin == nil,
			}); err != nil {
				return refusal(err)
			}

			if r := flow.LoginStart(ctx, deps, args.Email); r != nil {
				return loginRefusal(r)
			}
			return TextResult("A login code was requested for %s.\n\n"+
				"curious cannot tell whether one was actually sent — the server answers the "+
				"same either way — so ask the user to check that address, spam folder "+
				"included, and call %s with the code that arrives.",
				ui.Sanitize(args.Email), toolLoginVerify)
		},
	}
}

// loginVerifyArgs is what login_verify is called with.
//
// MarketingOptIn is a POINTER so that "not sent" and "sent as false" are
// different values here, even though they mean the same thing on the
// wire. They do not mean the same thing to a reviewer of this code: the
// default has to be false, and a plain bool cannot tell a caller that
// chose it from a caller that never thought about it.
type loginVerifyArgs struct {
	Email          string `json:"email"`
	Code           string `json:"code"`
	MarketingOptIn *bool  `json:"marketing_opt_in"`
}

// loginVerifyDescription. No number appears in it, for the reason
// login_start's carries none — and one is conspicuous by its absence:
// the code's length is not stated, because nothing in the wire contract
// fixes it and a figure typed here would be this client promising a
// shape the server never agreed to. Send what the email says.
const loginVerifyDescription = "Finish a login: submit the code from the email and store the " +
	"credential this machine will use from then on. Call " + toolLoginStart + " first.\n\n" +
	"The credential is written to the configuration file the curious CLI reads. It is " +
	"never returned to you, never appears in any tool's output, and does not need to: " +
	"every other tool here finds it on its own.\n\n" +
	"If the code is refused, the reason the server gives is the only thing that knows " +
	"which of several causes it was — a wrong code, an expired one, or a short cooldown " +
	"after several wrong ones all read the same from here. Ask the user for the code " +
	"again, or call " + toolLoginStart + " to have a fresh one sent."

func loginVerifyTool() Tool {
	return Tool{
		Name:        toolLoginVerify,
		Title:       "Finish a login",
		Description: loginVerifyDescription,
		InputSchema: objectSchema(
			`"email":{"type":"string","description":"The same address `+toolLoginStart+
				` was called with."},`+
				`"code":{"type":"string","description":"The code from the email, as a string. `+
				`Send it exactly as written; a leading zero matters and is lost if it is sent as a number."},`+
				`"marketing_opt_in":{"type":"boolean","default":false,"description":`+
				`"Whether the person at the keyboard wants occasional product news by email. `+
				`Defaults to false. It changes nothing about deploying, and you must not set `+
				`it to true unless you have asked them and they said yes."}`,
			"email", "code"),
		Handler: func(ctx context.Context, arguments json.RawMessage, _ Progress) Result {
			var args loginVerifyArgs
			if bad := decodeArguments(arguments, &args); bad != nil {
				return *bad
			}
			switch {
			case args.Email == "":
				return missingArgument("email", "the address the code was sent to")
			case args.Code == "":
				return missingArgument("code", "the code from the login email")
			}

			deps, _, err := loginDeps()
			if err != nil {
				return refusal(err)
			}

			// CONSENT DEFAULTS TO NO, and the default is applied here
			// rather than left to the zero value of a bool somewhere
			// downstream. A field nobody sent is a question nobody was
			// asked, and the answer to a question nobody was asked is no.
			optIn := args.MarketingOptIn != nil && *args.MarketingOptIn

			if r := flow.LoginVerify(ctx, deps, flow.VerifyRequest{
				Email:          args.Email,
				Code:           args.Code,
				MarketingOptIn: optIn,
			}); r != nil {
				return loginRefusal(r)
			}
			return TextResult("Logged in as %s. The credential is stored on this machine, "+
				"so %s can be called from now on without logging in again.",
				ui.Sanitize(args.Email), toolDeploySite)
		},
	}
}

// loginRefusal renders what a login step refused with, and says which of
// the two things a caller should do about it.
//
// THE DISTINCTION IS THE WHOLE PRODUCT OF THE ROUTING TABLE, and it is
// not one an agent could reach on its own: the server's refusals are
// deliberately indistinguishable as prose, so "another code would fix
// this" and "nothing you send will fix this" read identically. A tool
// that handed both back the same way would have an agent looping on a
// shut door or giving up on a typo.
func loginRefusal(r *flow.LoginRefusal) Result {
	if !r.Recoverable {
		return refusal(r.Failure)
	}
	// The server's own words, which are the only thing that knows what
	// happened, and then what to do — which this surface knows and the
	// server does not.
	// THE SERVER'S SENTENCE IS ESCAPED WHOLE, newline included. It is
	// somebody else's text in its entirety, so a line break in it is
	// content rather than layout — and left alone it could add a
	// paragraph below in this program's voice, which is exactly what the
	// two blank lines under it would make it look like.
	said := ui.Sanitize(r.Said)
	if said == "" {
		// A transport failure: no envelope, nothing the server said.
		said = "The request did not get an answer."
	}
	return ErrorResult("%s\n\nThat can be recovered from: ask the user for the code again "+
		"and call %s, or call %s to have a fresh code sent.",
		said, toolLoginVerify, toolLoginStart)
}
