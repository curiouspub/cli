package flow

import (
	"context"
	"errors"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The copy this step owns, held as named values so the rows that assert
// it cannot drift into asserting a second copy of it.
const (
	// authenticationFailed is what the server's one refusal is allowed
	// to be called, and the wording is a ruling rather than a
	// preference. FIVE distinct causes reach one byte-identical
	// response — no header, a non-bearer scheme, an empty value, a
	// token nobody recognises, and a token that exists and has been
	// withdrawn. They are identical on purpose: answering one of them
	// differently from another is an oracle. So the sentence a person
	// reads names none of them.
	authenticationFailed = "Authentication failed."

	// deployMayExist is the honest half of a create that never
	// answered. A failure after the request left this process is
	// indistinguishable from one before it, so a client that said
	// "nothing happened" would be guessing — and guessing in the
	// direction that reads as reassurance.
	deployMayExist = "A deploy may already have been created"
)

// createRoute is what one refused create does to the run.
type createRoute int

const (
	// createReauthenticate re-enters at the capacity check, then the
	// login, then tries the create ONE more time.
	createReauthenticate createRoute = iota + 1

	// createStop ends the run.
	createStop
)

// createRouting states, for every code the contract defines, whether the
// run recovers or stops. It is keyed to the contract's own enumeration
// rather than to a list typed beside it, so a ninth code arrives already
// needing an answer here.
//
// EXHAUSTIVE IS NOT THE SAME AS REACHABLE, and the difference is
// annotated rather than hidden: capacity_closed keeps its row because
// the table is a statement about the vocabulary, and it gets no row of
// its own in the suite because the chain in front of this endpoint has
// no capacity gate. A test asserting behaviour for an input the real
// server cannot send is one that can never fail in production while
// reading as though the mapping were verified.
var createRouting = map[wire.ErrorCode]createRoute{
	// The one recoverable refusal. Every cause behind it is fixed by
	// holding a token the server accepts, and getting one means going
	// back to the capacity check and the login — in that order, because
	// the daily cap counts accounts and an account is spent at the
	// verify step.
	wire.CodeUnauthorized: createReauthenticate,

	// The kill switch. The service declined; the project is fine.
	wire.CodeMaintenance: createStop,

	// DECLARED, NOT REACHABLE from this endpoint: the chain in front of
	// it checks maintenance and then authentication, with no capacity
	// gate. It is routed because the table is exhaustive over the
	// contract, and a code with no stated routing is a gap rather than
	// an answer.
	wire.CodeCapacityClosed: createStop,

	// Pace, not access. The server says when to come back, so the run
	// stops WITHOUT the closed-door mark — a script told to wait must
	// not be told the door is shut.
	wire.CodeRateLimited: createStop,

	// The declared size disagrees with a limit this server keeps.
	// Sending the same number again cannot help.
	wire.CodeBadRequest: createStop,

	// No handler returns these from this endpoint today. They are in
	// the contract's enumeration, and a code with no stated routing
	// fails this package's own rule, so each has one.
	wire.CodeForbidden: createStop,
	wire.CodeNotFound:  createStop,
	wire.CodeInternal:  createStop,
	// THE PUBLISH STEP'S TWO CODES, declared here and not reachable from
	// this call — the same shape the closed-capacity code already has.
	// The create cannot receive them: they say a deploy is not in a
	// publishable state, and this call is what brings a deploy into
	// existence. Stated anyway, because the contract's list is what this
	// table is keyed to, and a code with no entry would be answered by a
	// fallback nobody chose.
	wire.CodeDeployFailed:   createStop,
	wire.CodeDeployNotReady: createStop,
}

// deployCreator is the slice of the API client this step needs. It is
// declared HERE, by the consumer, like every other seam in this package.
type deployCreator interface {
	DeployCreate(ctx context.Context, req wire.DeployCreateRequest) (*wire.DeployCreateResponse, error)
}

// createDeps is everything createDeploy needs from outside itself.
type createDeps struct {
	// Client is the authenticated client, and reauthenticate is how a
	// fresh one is obtained when the server refuses this one. The second
	// is a function rather than a flag because getting a new token means
	// running the capacity check and the whole login, which this step
	// has no business knowing the shape of.
	Client         deployCreator
	Reauthenticate func(ctx context.Context) (deployCreator, error)

	// Bytes is the PACKED archive's size, taken from the hand-off. It is
	// never measured again here: the pack already read it back off the
	// file, and a second measurement of one fact is free to disagree
	// with the first.
	Bytes int64

	// Now is the clock a retry time is rendered against.
	Now func() time.Time
}

// createDeploy makes the create, and on an authentication refusal
// re-enters at the capacity check and the login before trying ONE more
// time.
//
// # It is never retried for anything else
//
// A repeat is not idempotent: it creates a second deploy record and
// spends quota again. The client enforces that at the transport level —
// the call declares itself non-idempotent, so a connection-level failure
// or a 5xx comes straight back rather than going round again. What this
// function adds is the ONE deliberate second attempt, and only after a
// login has changed the thing that was wrong.
//
// Reauthenticate OWNS what happens to the caller's own client: this
// function holds a seam, not a *Client, so replacing the one the rest of
// the run uses is the caller's job and is done inside the function it
// supplies.
func createDeploy(ctx context.Context, deps createDeps) (*wire.DeployCreateResponse, error) {
	client := deps.Client
	reauthenticated := false

	for {
		resp, err := client.DeployCreate(ctx, wire.DeployCreateRequest{Bytes: deps.Bytes})
		if err == nil {
			return resp, nil
		}

		var apiErr *api.APIError
		if !errors.As(err, &apiErr) {
			// No envelope, no code, nothing the server said. The
			// request may well have arrived.
			return nil, createUnansweredFailure(err)
		}

		if createRouting[apiErr.Code] == createReauthenticate &&
			!reauthenticated && deps.Reauthenticate != nil {
			fresh, authErr := deps.Reauthenticate(ctx)
			if authErr != nil {
				return nil, authErr
			}
			client = fresh
			reauthenticated = true
			continue
		}

		return nil, createStopFailure(apiErr, deps.Now())
	}
}

// createUnansweredFailure is what a create with no answer ends the run
// as — a dropped connection, a deadline, a cancellation.
//
// IT DOES NOT SAY NOTHING HAPPENED. A failure after the request left
// this process is indistinguishable from one before it, so the only
// honest sentence names both possibilities. Every other stop in this
// package ends with the reassurance that nothing was uploaded; this one
// must not, and that difference is the point of it.
func createUnansweredFailure(err error) error {
	return ui.NewFailure(
		ui.IDServerUnanswered,
		"curious didn't hear back after asking for somewhere to upload.",
		deployMayExist+" — curious cannot tell whether the\n"+
			"request arrived. Nothing has been uploaded to it either way, and an\n"+
			"unused deploy is discarded by the server on its own.", ui.NextWait,
		"Run `curious deploy` again when the connection is back.").Quoting(err.Error())
}

// createStopFailure is the copy for each code that ends the run.
func createStopFailure(apiErr *api.APIError, now time.Time) error {
	switch apiErr.Code {
	case wire.CodeUnauthorized:
		// Reached on the SECOND refusal: a fresh login has already been
		// done and the server refused the result. Looping again would
		// spend another of the sends the server allows in an hour on a
		// run that is not going to end differently.
		return ui.NewFailure(
			ui.IDClientRequestRejected,
			authenticationFailed,
			"curious logged in again and the server still would not "+
				"accept the\nrequest, so it stopped rather than keep asking.", ui.NextFreshDeploy,
			"Check that this machine's clock is right, then run `curious deploy`\n"+
				"again. If it keeps happening, please get in touch. "+uploadedNothing).Quoting(apiErr.Message)

	case wire.CodeMaintenance:
		// The server's message, verbatim, and NO retry time — the kill
		// switch has no reset anybody can honestly name.
		return ui.ServerClosed(ui.Quoted(
			ui.IDServiceUnavailable,
			"curious.pub is not taking deploys right now.",
			apiErr.Message, ui.NextWait,
			"Try again a little later. "+uploadedNothing))

	case wire.CodeCapacityClosed:
		// Declared and not reachable from this endpoint. The copy and
		// the reset rendering have ONE home for every route that meets a
		// shut cap, so two of them cannot drift into telling one user
		// something the other is not told.
		return ui.ServerClosed(closedCapacityFailure(
			apiErr.Message, now.Add(apiErr.RetryAfter), now))

	case wire.CodeRateLimited:
		// Pace, not access: no closed-door mark, and the time the
		// server named is USED rather than dropped.
		return ui.Quoted(
			ui.IDRateLimited,
			"Too many requests from here.",
			apiErr.Message, ui.NextWait,
			retryAdvice(apiErr.RetryAfter, now))

	case wire.CodeBadRequest:
		// The declared size disagrees with a limit this server keeps,
		// and the server's own message is the only thing here that
		// knows which one.
		return ui.NewFailure(
			ui.IDClientRequestRejected,
			"The server wouldn't accept that archive.",
			"Sending the same thing again would not go any better, "+
				"so the run\nstopped here.", ui.NextGiveUp,
			"Check that you are running a current version — `curious version` says\n"+
				"which one — and please report this if it keeps happening. "+
				uploadedNothing).Quoting(apiErr.Message)
	}

	// Routed to a stop with no copy of its own, or a code this build
	// predates. The contract is additive-only, so the server is entitled
	// to introduce one, and the honest answer is to show what it said.
	return ui.Quoted(
		ui.IDServerAnswerUnrecognised,
		"curious couldn't start the deploy.",
		apiErr.Message, ui.NextWait,
		"Try again in a moment. If it keeps happening, updating curious may\n"+
			"help — this build may be older than the server. "+uploadedNothing)
}
