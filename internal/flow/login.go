package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// LoginPrompter is the slice of the terminal this flow needs: narration,
// a line of text, an address, and a yes-or-no question.
//
// It is declared HERE, by the consumer, rather than exported by the
// package that implements it. The real terminal type satisfies it
// without knowing this package exists, and a test can state what this
// flow does — one address prompt across a run with three wrong codes —
// without arranging a terminal, which is not a thing a test can portably
// do on all three platforms this ships to.
//
// A PROMPT ERROR IS RETURNED UNCHANGED, and that leaves one thing this
// seam's to keep: ui.ServerClosed is the flow's to mint, never an
// implementation's. The third exit code means a door that is shut, and
// the routing below only marks a stop with it holding a capacity or
// maintenance response. A prompter that wrapped its own error in it
// would exit 3 with nothing behind it, and nothing here would notice.
type LoginPrompter interface {
	Step(format string, args ...any)
	Line(prompt string) (string, error)
	Email(prompt string) (string, error)
	Confirm(question string, defaultYes bool) (bool, error)
}

// Authenticator is the slice of the API client this flow needs. Neither
// call takes a bearer token: the second one RETURNS the token every
// later call will spend.
type Authenticator interface {
	AuthStart(ctx context.Context, req wire.AuthStartRequest) (*wire.AuthStartResponse, error)
	AuthVerify(ctx context.Context, req wire.AuthVerifyRequest) (*wire.AuthVerifyResponse, error)
}

// WaitlistOffer is the hand-off for a run that met a closed account cap.
//
// Account capacity is spent at the verify step, so it can close between
// whatever checked it and this call — a genuine race rather than a
// mistake. The reset time is passed IN because the response that carried
// it is in hand here: an offer that had to ask a second time would be
// making a call to learn something it was already told.
//
// It takes the run's context because an offer joins a waitlist, and
// joining one is a call. Giving it the context now costs a parameter;
// giving it later means changing a signature inside the change that
// wires the real offer up, which is the change least able to afford an
// unrelated edit.
//
// It returns the error that ends the run. What the offer SAYS is not
// this flow's business; that it stops is.
//
// WHAT AN IMPLEMENTATION OWES THE RUN, said here because nothing
// enforces it: it must RETURN. A blocking offer blocks the login; the
// context handed in is the only cancellation there is, and it works only
// if the callback honours it; a panic is not contained here and ends the
// process. Deliberately no machinery — a recover() would swallow a
// programming error in code this package does not own, and a deadline
// would be this flow inventing one for a call whose cost it cannot know.
type WaitlistOffer func(ctx context.Context, email string, resetsAt time.Time) error

// TokenWriter stores the token together with the endpoint it was issued
// against. The pair is validated together by whoever implements this, so
// a token with no endpoint or an endpoint with no token cannot be
// written at all.
//
// ITS ERROR TEXT REACHES THE USER VERBATIM — writeFailure prints it. So
// an error from this seam must never carry the token. The shipped writer
// does not, and that is why this is a sentence rather than a defect: the
// type accepts any error and the renderer trusts it.
type TokenWriter func(token ui.Secret, issuedAgainst string) error

// LoginDeps is everything Login needs from outside itself.
type LoginDeps struct {
	// Prompt is the terminal. Required.
	Prompt LoginPrompter

	// Auth is the API client. Required.
	Auth Authenticator

	// Endpoint is the base URL THIS FLOW CALLED — the value the client
	// was constructed with, never one read back from a config file and
	// never a second resolution of the same question.
	//
	// It is an argument rather than something this flow looks up because
	// the store cannot check it: a writer can prove a URL is
	// canonicalisable, and it cannot prove a token came from that URL,
	// because it has no issuer identity to compare against. The only
	// code that knows which endpoint issued this token is this flow, at
	// the moment it makes the call.
	Endpoint string

	// Save stores the pair. Optional: the default loads the existing
	// configuration first and saves onto the value it returned.
	Save TokenWriter

	// Offer is the waitlist hand-off. Optional; without one a closed
	// capacity stops the run with this flow's own copy.
	Offer WaitlistOffer

	// Now is the clock a retry time is rendered against. Optional.
	Now func() time.Time
}

// ErrEndpointUnusable is what Login returns when the endpoint it was
// handed cannot be compared with the one recorded beside a stored token.
//
// It is a sentinel because the caller's response to it differs from
// every other ending here: it is a wiring mistake in this program or a
// mis-set variable in the environment, not something the person at the
// terminal did. It deliberately carries no copy of the offending value —
// a base URL can carry a password, and a refusal is not the place to
// find that out.
var ErrEndpointUnusable = errors.New(
	"the login flow needs the API endpoint this run is talking to, in a form " +
		"that can be compared with the endpoint stored beside a saved token")

// The copy this flow owns. Every string a person reads is here rather
// than inline, so the wording can be read in one place and so the rows
// that assert it cannot drift into asserting a second copy of it.
const (
	emailPrompt = "Email address:"
	codePrompt  = "Enter the 6-digit code:"

	// consentQuestion is asked once per run and nowhere else. No other
	// prompt in this flow mentions marketing, and nothing anywhere
	// implies that opting in helps a deploy.
	consentQuestion = "Product news a few times a year?"

	resendQuestion = "Resend code to the same address?"

	// addressLine says where a code WOULD go. It does not say one was
	// sent, because this client cannot know: the start endpoint answers
	// identically whether it sent a code, declined, was over a send
	// budget, or was in a cooldown.
	addressLine = "Login codes for %s go to that address."

	// spamFolderLine is the other half of that honesty — what to do when
	// nothing arrives, which is the only actionable thing there is to
	// say about a state the client cannot observe.
	spamFolderLine = "Nothing arriving? Check your spam folder, or try again later."

	// escapeHatchLine is the honest fix for this flow's one hole. A user
	// who typo'd their address will never receive a code, and resending
	// to the same wrong address for ever cannot help them. The client
	// cannot detect that case — the server will not say whether an
	// address is real — so the way out is copy rather than logic.
	escapeHatchLine = "Wrong address? Press Ctrl-C and run `curious deploy` again."

	// misshapenCodeLine is the local re-prompt. It never reaches the
	// network, which is the whole point of it.
	misshapenCodeLine = "That should be 6 digits — just the code from the email."

	// cooldownHint is widened copy rather than a claim about this user's
	// state, because the client genuinely cannot know: a cooldown, an
	// expired code and a typo arrive as one byte-identical refusal.
	cooldownHint = "Still not working? After several wrong codes there's a short cooldown —\n" +
		"wait about 15 minutes and try again."
)

// wallClockLayout renders a retry time as a time of day. "Try again in
// 900 seconds" is arithmetic somebody has to do while annoyed.
const wallClockLayout = "15:04 MST"

// unauthorizedFailuresBeforeHint is how many consecutive refusals earn
// the widened copy. It is a threshold for a HINT and never a budget: the
// server owns the limits, and a second, different limit invented here
// would produce a client that gives up while the server would still have
// answered.
const unauthorizedFailuresBeforeHint = 3

// codeDigits is the shape this client checks locally, and the only shape
// it checks. Anything else is re-asked without a request, so a
// five-digit typo does not spend one of the attempts the server allows
// before its own cooldown.
const codeDigits = 6

// maxCodeAttempts bounds how many mis-shaped entries one code prompt
// accepts. The BOUND is the point rather than the number: against a
// terminal an unbounded loop is fine, but against a pipe that keeps
// yielding something the parser rejects it is a program that hangs, and
// a hang is the hardest thing a user can report.
const maxCodeAttempts = 4

// verifyRoute is what one failure does to the run: back into the retry
// loop, or a stop.
type verifyRoute int

const (
	routeRecover verifyRoute = iota + 1
	routeStop
)

// errorRouting states, for every code the contract defines, whether the
// run recovers or stops. Two properties of this table are asserted
// against the contract itself rather than against a list typed beside
// it, so a ninth code arrives already needing an answer here:
//
//   - No code that carries a retry time may enter the retry loop. A code
//     the contract guarantees a Retry-After for is the server saying
//     "wait this long", and looping on it is the one thing this client
//     must not do.
//   - Every code that carries one stops, with the time USED rather than
//     dropped — rendered as a time of day, or handed to the waitlist
//     offer so it can name a return time without asking again.
//
// What cannot be derived from the contract is the choice below, and the
// row that matters most is the first: the generic auth failure IS the
// wrong-or-expired-code case. The server returns one byte-identical
// refusal for a wrong code, an expired one, a lost race, a cooldown, an
// unknown identity and identity-scoped rate limiting. Routing the
// malformed-input code into the retry loop instead — the obvious reading
// — produces a client that gives up on the one recoverable failure and
// loops on the one that cannot recover.
var errorRouting = map[wire.ErrorCode]verifyRoute{
	// The wrong or expired code. Recoverable, and the reason this flow
	// is a machine rather than a function that returns an error.
	wire.CodeUnauthorized: routeRecover,

	// The server malfunctioned. The verify may not have consumed the
	// code, so this is recoverable in the same sense a dropped
	// connection is.
	wire.CodeInternal: routeRecover,

	// Malformed INPUT, not a bad code: the shape is checked locally
	// before anything is sent, so this means the request itself is
	// wrong, and resending the same malformed thing cannot help.
	wire.CodeBadRequest: routeStop,

	// Network-scoped throttling. The identity-scoped kind is
	// indistinguishable from a wrong code and hides inside the
	// unauthorized row above.
	//
	// It stops WITHOUT the closed-door mark, and that is a decision
	// rather than an omission: this is about pace, not access. The
	// server is telling the caller when to come back, so a script that
	// treated it as a shut door would sleep through a limit it was being
	// invited to wait out.
	wire.CodeRateLimited: routeStop,

	// The account cap closed between whatever checked it and here.
	wire.CodeCapacityClosed: routeStop,

	// The kill switch. Worth retrying later, and carrying no reset the
	// server can honestly name — which is exactly why the contract's own
	// predicate is about a header rather than about retryability.
	wire.CodeMaintenance: routeStop,

	// No handler returns it today. It is in the contract's enumeration,
	// and a code with no stated routing fails by this flow's own rule,
	// so it has one.
	wire.CodeForbidden: routeStop,

	// The route does not exist — almost always a mis-set endpoint
	// variable rather than a server fault, so the stop names the base
	// URL it called, because that is the thing that is wrong.
	wire.CodeNotFound: routeStop,
}

// routeFor reports the stated routing for code, and whether there is
// one. A code with none stops, which is the safe direction; that it has
// none is what a test notices.
func routeFor(code wire.ErrorCode) (verifyRoute, bool) {
	route, stated := errorRouting[code]
	return route, stated
}

// loginState is one position in the machine. The states are named
// because the rule this flow exists to keep is a statement about EDGES —
// "wrong or expired code never restarts the flow" — and an edge cannot
// be forbidden in code that has no names for its ends.
type loginState int

const (
	stateAskEmail loginState = iota
	stateStart
	stateAskCode
	stateAskConsent
	stateVerify
	stateRecover
	stateSave
)

// Login runs the email-and-code login and returns nil ONLY when a token
// has been stored.
//
// # The rule, made structural
//
// No failure edge points back at stateAskEmail. Every naive
// implementation of this flow violates it: the verify call returns an
// error, the function returns that error, and the caller starts again at
// "what is your email?" — so somebody who mistypes one digit is asked to
// retype their address, and the code they were sent is still valid and
// now unreachable. Writing this as an explicit machine, with the
// transitions named, is what makes the rule survive contact with the
// error paths, because every recovery has to name the state it goes to.
//
// The address is asked for once. After that the only ways out are a
// stored token, a stop, or the person cancelling.
func Login(ctx context.Context, deps LoginDeps) error {
	// The endpoint is checked BEFORE anybody is asked anything. An
	// endpoint the writer will refuse is worth refusing now rather than
	// after somebody has typed a code and spent one of the attempts the
	// server allows on a run that cannot end in a saved token.
	//
	// The value is not echoed. It is checked with the same
	// canonicaliser the store compares with, which also refuses one
	// carrying a username or password — so everything below may name it.
	if _, err := api.CanonicalKey(deps.Endpoint); err != nil {
		return ErrEndpointUnusable
	}

	now := deps.Now
	if now == nil {
		now = time.Now
	}
	save := deps.Save
	if save == nil {
		save = defaultTokenWriter
	}

	p := deps.Prompt

	var (
		email string
		code  string
		token ui.Secret

		optIn        bool
		consentAsked bool

		// consecutiveRefusals counts refusals in a row. A resend does
		// not reset it: resending a code does not clear the server's
		// count of wrong attempts, so a hint about that count must not
		// be cleared by one either.
		consecutiveRefusals int

		// cooldownHintShown is set when the widened copy has been
		// rendered and is never cleared. ONCE PER RUN, not once per
		// three refusals: clearing the counter when the hint fires is
		// the natural-looking version, and it repeats the hint to
		// somebody who has already read it.
		cooldownHintShown bool

		// lastMessage is what the recover state renders. It is carried
		// rather than re-derived because that state is reached from two
		// different calls.
		lastMessage string
	)

	state := stateAskEmail
	for {
		switch state {
		case stateAskEmail:
			answer, err := p.Email(emailPrompt)
			if err != nil {
				return err
			}
			email = answer
			state = stateStart

		case stateStart:
			_, err := deps.Auth.AuthStart(ctx, wire.AuthStartRequest{Email: email})
			if err != nil {
				route, message, stop := classify(ctx, err, deps, email, now())
				if route == routeStop {
					return stop
				}
				lastMessage = message
				consecutiveRefusals = trackRefusal(err, consecutiveRefusals)
				state = stateRecover
				continue
			}
			// The same two lines after the first send and after every
			// resend. A resend that dropped the caveat would leave the
			// one reader who most needs it — the one whose mail is not
			// arriving — without it.
			p.Step(addressLine, email)
			p.Step("%s", spamFolderLine)
			state = stateAskCode

		case stateAskCode:
			entry, err := askCode(p)
			if err != nil {
				return err
			}
			code = entry
			if consentAsked {
				state = stateVerify
			} else {
				state = stateAskConsent
			}

		case stateAskConsent:
			// Asked once per run, after the first code entry and
			// immediately before the first verify. It has to precede a
			// verify because it is a field in that request; it follows
			// the code entry so that somebody who abandons while waiting
			// for the mail is never asked a marketing question at all.
			answer, err := p.Confirm(consentQuestion, false)
			if err != nil {
				return err
			}
			optIn = answer
			consentAsked = true
			state = stateVerify

		case stateVerify:
			resp, err := deps.Auth.AuthVerify(ctx, wire.AuthVerifyRequest{
				Email:          email,
				Code:           code,
				MarketingOptIn: optIn,
			})
			if err == nil {
				token = ui.Secret(resp.Token)
				state = stateSave
				continue
			}
			route, message, stop := classify(ctx, err, deps, email, now())
			if route == routeStop {
				return stop
			}
			lastMessage = message
			consecutiveRefusals = trackRefusal(err, consecutiveRefusals)
			state = stateRecover

		case stateRecover:
			// The server's own message, and only that. One
			// byte-identical refusal covers every reason a code can be
			// rejected, so it is the only thing either side can say
			// about which one happened — and a status code beside it is
			// noise to somebody deploying their first site.
			p.Step("%s", lastMessage)
			if consecutiveRefusals >= unauthorizedFailuresBeforeHint && !cooldownHintShown {
				p.Step("%s", cooldownHint)
				cooldownHintShown = true
			}
			p.Step("%s", escapeHatchLine)
			resend, err := p.Confirm(resendQuestion, true)
			if err != nil {
				return err
			}
			if resend {
				// The SAME address. There is no edge back to the email
				// prompt, so there is nowhere else it could come from.
				state = stateStart
			} else {
				// They may already have a valid code in another window
				// and simply fat-fingered it, so this must not spend one
				// of the sends the server allows in an hour.
				state = stateAskCode
			}

		case stateSave:
			if err := save(token, deps.Endpoint); err != nil {
				return writeFailure(err)
			}
			// One line. No token, no masked token, and no expiry claim
			// this client cannot verify.
			p.Step("You're logged in as %s.", email)
			return nil
		}
	}
}

// trackRefusal advances the consecutive-refusal count. A refusal that is
// not the generic auth failure resets it, because the hint it earns is
// about wrong codes and nothing else.
func trackRefusal(err error, consecutive int) int {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Code == wire.CodeUnauthorized {
		return consecutive + 1
	}
	return 0
}

// classify decides what one failed call does to the run.
//
// It returns the route, the message to RENDER inside the retry loop, and
// — for a stop — the failure that ends the run. Only one of the last two
// is ever meaningful, and which one is decided by the first.
func classify(ctx context.Context, err error, deps LoginDeps, email string, now time.Time) (verifyRoute, string, error) {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		// A transport failure: no envelope, no code, nothing the server
		// said. A retry may simply work, and a dropped connection says
		// nothing about whether the code was any good.
		return routeRecover, err.Error(), nil
	}

	route, stated := routeFor(apiErr.Code)
	if !stated {
		// A code this binary predates. The contract is additive-only, so
		// the server is entitled to introduce one — and the honest
		// answer is to stop and show what the server said, because that
		// message is the only thing here that knows what happened.
		return routeStop, "", unknownCodeFailure(apiErr)
	}
	if route == routeRecover {
		return routeRecover, apiErr.Message, nil
	}
	return routeStop, "", stopFailure(ctx, apiErr, deps, email, now)
}

// stopFailure is the copy for each code that ends the run.
func stopFailure(ctx context.Context, apiErr *api.APIError, deps LoginDeps, email string, now time.Time) error {
	switch apiErr.Code {
	case wire.CodeBadRequest:
		return ui.NewFailure(
			"curious sent something this server wouldn't accept.",
			apiErr.Message+"\n\nSending it again would not go any better, so the run "+
				"stopped here rather than spending another of your attempts.",
			"Check that you are running a current version — `curious version` says\n"+
				"which one — and please report this if it keeps happening.")

	case wire.CodeRateLimited:
		return ui.NewFailure(
			"Too many requests from here.",
			apiErr.Message,
			retryAdvice(apiErr.RetryAfter, now))

	case wire.CodeCapacityClosed:
		resetsAt := now.Add(apiErr.RetryAfter)
		if deps.Offer != nil {
			if err := deps.Offer(ctx, email, resetsAt); err != nil {
				// The offer owns the WORDS; this flow owns the COST. A
				// hand-off should not have to decide an exit code as a
				// side effect of writing a message, and marking it here
				// is what spares it from having to.
				return ui.ServerClosed(err)
			}
		}
		// Reached when no offer is wired, and when a wired one reports
		// nothing. This function's callers return nil only for a stored
		// token, so an offer that hands back no error must not turn a
		// run with no credential into a success.
		//
		// THE COPY IS NOT WRITTEN HERE. A shut account cap reaches a
		// person by two routes — met before a login is attempted, or met
		// at the moment an account would be spent — and the words and
		// the reset rendering have one home for both, so the two cannot
		// drift into telling one user something the other is not told.
		// See closedCapacityFailure.
		return ui.ServerClosed(closedCapacityFailure(apiErr.Message, resetsAt, now))

	case wire.CodeMaintenance:
		// The server's message, verbatim, and NO retry time — the kill
		// switch has no reset anybody can honestly name, which is why
		// the contract does not promise one for it.
		return ui.ServerClosed(ui.NewFailure(
			"curious.pub is not taking logins right now.",
			apiErr.Message,
			"Try again a little later. Nothing has been uploaded."))

	case wire.CodeForbidden:
		return ui.NewFailure(
			"That login was refused.",
			apiErr.Message,
			"If you think this is wrong, please get in touch — there is nothing\n"+
				"to retry here.")

	case wire.CodeNotFound:
		// The message names the base URL, because a missing route is
		// almost always a mis-set endpoint rather than a server fault,
		// and the endpoint is the thing that can be changed.
		return ui.NewFailure(
			"That server does not answer the login endpoint.",
			fmt.Sprintf("curious called %s and the route was not there. That usually "+
				"means\nthe address is wrong rather than that the server is broken.",
				deps.Endpoint),
			"Check CURIOUS_API_URL, or unset it to use the default.")
	}

	// Routed to a stop by the table above without copy of its own. The
	// server's message is the only thing that knows what happened.
	return unknownCodeFailure(apiErr)
}

// unknownCodeFailure shows what the server said and stops. It is what a
// code this binary predates gets, and it is deliberately the same shape
// as every other stop rather than the program's unexpected-failure copy:
// nothing is broken here, the server simply said something newer than
// this build.
func unknownCodeFailure(apiErr *api.APIError) error {
	return ui.NewFailure(
		"curious couldn't finish logging you in.",
		apiErr.Message,
		"Try again in a moment. If it keeps happening, updating curious may\n"+
			"help — this build may be older than the server.")
}

// retryAdvice renders a time of day to come back at.
//
// A zero duration means the header was absent or unparseable. The
// contract guarantees it for the codes that reach here, so its absence
// is a server breaking its own promise — and adding nothing to the
// current time would tell the reader to come back immediately, which is
// the one answer that is certainly wrong.
func retryAdvice(retryAfter time.Duration, now time.Time) string {
	if retryAfter <= 0 {
		return "Try again a little later. Nothing has been uploaded."
	}
	return fmt.Sprintf("Try again after %s. Nothing has been uploaded.",
		now.Add(retryAfter).Format(wallClockLayout))
}

// writeFailure is what a login that worked and could not be recorded
// looks like.
//
// It NEVER prints the token, not even as a "here, save it yourself"
// fallback: a token pasted into scrollback is worse than logging in
// again. And logging in again really is free — a repeat verify for an
// identity that already holds a token reissues rather than spending a
// second slot of the day's account capacity — so the last line is an
// instruction rather than an apology.
//
// THAT IS A CLAIM ABOUT THE SERVER, and the one sentence here this
// repository cannot prove. It holds because verify skips the capacity
// spend when the identity already holds an active token, and the
// server's own code carries the other half of this pointer, at the step
// that makes it true. Nothing tests the pair across that boundary: if
// the step ever starts spending, this copy becomes a lie and no suite on
// either side goes red. Change one end, come to the other.
func writeFailure(err error) error {
	return ui.NewFailure(
		"Logged in, but the login could not be saved.",
		err.Error(),
		"Check that the folder above exists, is writable and has space, then\n"+
			"run the command again. It is safe to log in again — it costs you\n"+
			"nothing and replaces the token that could not be stored.")
}

// defaultTokenWriter is the real store: LOAD FIRST, THEN SAVE.
//
// The save is a method on the value the load returned, and that is the
// whole reason this function exists rather than a fresh value being
// built here. The map of fields this build does not recognise lives on
// the loaded value, so a configuration built fresh carries an empty one
// and writing it DROPS every field a newer release wrote — an older
// binary run once would silently delete a newer one's state.
//
// The hazard pointing the other way is the endpoint: reusing the one
// read back out of the file would record a token against wherever the
// PREVIOUS token came from, and every later run would then decide the
// stored token belongs to somebody else. That is why the endpoint is an
// argument and the value is not. This loads, sets nothing, and saves the
// pair.
func defaultTokenWriter(token ui.Secret, issuedAgainst string) error {
	cfg, err := config.Load(issuedAgainst)
	if err != nil {
		return err
	}
	return cfg.Save(token, issuedAgainst)
}

// askCode reads one code and checks its SHAPE, locally.
//
// Nothing is sent until the entry is six digits, and that is a
// protection rather than politeness: the server allows a small number of
// wrong attempts before a cooldown, and a five-digit typo that reached
// it would spend one of them.
//
// The value stays a STRING from here to the wire. Read as a number,
// 012345 becomes 12345, which fails for ever and looks like a fault on
// the server.
func askCode(p LoginPrompter) (string, error) {
	for attempt := 0; attempt < maxCodeAttempts; attempt++ {
		entry, err := p.Line(codePrompt)
		if err != nil {
			return "", err
		}
		code := normaliseCode(entry)
		if wellShapedCode(code) {
			return code, nil
		}
		if attempt < maxCodeAttempts-1 {
			p.Step("%s", misshapenCodeLine)
		}
	}
	// The bounded prompt's own sentinel rather than a made-up code: the
	// program already knows what to say about being asked something it
	// could not read.
	return "", ui.ErrNoAnswer
}

// normaliseCode strips the decorations a code picks up between an email
// and a terminal: surrounding whitespace, a leading hash, and the spaces
// a mail client puts between groups of digits.
func normaliseCode(entry string) string {
	code := strings.TrimSpace(entry)
	code = strings.TrimPrefix(code, "#")
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, code)
}

// wellShapedCode reports whether code is exactly six ASCII digits.
//
// ASCII deliberately, rather than a general "is this a digit" test:
// other scripts have digits, the server's codes are not written in them,
// and accepting one here would turn a paste this client should refuse
// into a refusal from the server that spends an attempt.
func wellShapedCode(code string) bool {
	if len(code) != codeDigits {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}
