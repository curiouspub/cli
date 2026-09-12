package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// CapacityPrompter is the slice of the terminal the closed-door path
// needs: narration, an address, and a yes-or-no question. Both consumers
// of it are on that path — the offer below, and the gate that hands off
// to it.
//
// It is declared HERE, by the consumer, rather than exported by the
// package that implements it — the same arrangement Prompter and
// LoginPrompter already make. It is deliberately NOT LoginPrompter with
// one method dropped: nothing here asks for a line of free text, and a
// seam that asked for one would be describing a terminal rather than
// this flow's needs.
type CapacityPrompter interface {
	Step(format string, args ...any)
	Email(prompt string) (string, error)
	Confirm(question string, defaultYes bool) (bool, error)
}

// WaitlistJoiner is the slice of the API client the waitlist offer
// needs. It is separate from CapacityAPI below because the offer is
// reachable from a path that never checks capacity at all — the account
// cap can close at the login step, after this gate said yes — and a seam
// asking for a call its caller cannot make is a seam nobody can wire.
type WaitlistJoiner interface {
	Waitlist(ctx context.Context, req wire.WaitlistRequest) (*wire.WaitlistResponse, error)
}

// The copy the offer owns, gathered here for the same reason the login
// flow gathers its own: so the wording can be read in one place, and so
// the rows that assert it cannot drift into asserting a second copy.
const (
	// waitlistQuestion is the offer. The bracketed hint and the default
	// come from Confirm, so this is a question and nothing else.
	waitlistQuestion = "Want an email when it reopens?"

	// nothingDeployed is the headline for every ending of this path that
	// is not a completed signup. It says what happened to the user's run
	// rather than repeating the announcement they have just read three
	// lines above.
	nothingDeployed = "Nothing has been deployed."
)

// supportAddress is the CLI's support contact, and it has ONE named home
// for the reason every other piece of copy here does: an address typed
// into a string is an address that exists in two places the next time
// anybody needs it, and the two spellings are then one typo apart.
const supportAddress = "hello@curious.pub"

// NewWaitlistOffer builds the hand-off the login flow calls when the
// account cap closes at the moment an account would be spent.
//
// It satisfies WaitlistOffer, whose doc states what an implementation
// owes the run: it RETURNS, it honours the context it is handed, and it
// does not panic. Everything below is a prompt, one HTTP call made with
// that context, and string building — there is no loop without a bound
// and nothing that blocks on anything else.
//
// THE OFFER TAKES THE ADDRESS RATHER THAN ASKING FOR ONE. The login path
// already has one — the person typed it to log in — and asking again
// would be this program forgetting something it was told a moment ago.
// The gate above has none, so it supplies a prompt instead; that is the
// only difference between the two callers, and it is one argument.
func NewWaitlistOffer(p CapacityPrompter, join WaitlistJoiner, now func() time.Time) WaitlistOffer {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, email string, resetsAt time.Time) error {
		return offerWaitlist(ctx, waitlistDeps{prompt: p, join: join, now: now},
			knownAddress(email), resetsAt)
	}
}

// waitlistDeps is what the shared offer body needs. It is unexported
// because both ways in are exported functions above; nothing outside
// this package assembles one.
type waitlistDeps struct {
	prompt CapacityPrompter
	join   WaitlistJoiner
	now    func() time.Time
}

// addressSource yields the address a signup would use.
//
// It is a function rather than a string so the OFFER can decide when the
// address is needed, which is the whole of the difference between the
// two callers: the gate must not ask for an address before it has said
// why it wants one, and the login path must not be asked to produce a
// prompt for something it already knows.
type addressSource func() (string, error)

// knownAddress is the login path's source: an answer already given.
func knownAddress(email string) addressSource {
	return func() (string, error) { return email, nil }
}

// promptedAddress is the gate's source. It asks with the login flow's
// own prompt wording, because a person meeting the address prompt twice
// in one program should meet the same question both times.
func promptedAddress(p CapacityPrompter) addressSource {
	return func() (string, error) { return p.Email(emailPrompt) }
}

// offerWaitlist is the whole closed-door interaction, and it is ONE BODY
// on purpose. Two copies of "announce, ask, sign up, say what happened"
// is two copies of a promise the company would have to keep.
//
// EVERY ENDING IS THIS FUNCTION'S OWN COPY, and no error from a prompt
// is ever returned. That is not tidiness: ui.ExitCode matches
// ui.ErrNotInteractive BEFORE it reaches the closed-door branch, so a
// stop carrying that sentinel anywhere in its chain costs 1 and prints
// a generic message about needing a terminal — not the third exit code,
// and not a word about capacity. The run stopped because the door is
// shut; there being nobody to ask is a fact about the message rather
// than the reason for the stop.
func offerWaitlist(ctx context.Context, w waitlistDeps, address addressSource, resetsAt time.Time) error {
	// One clock read for the whole interaction. Read twice, the
	// announcement and the advice underneath it could name different
	// relative times for one reset.
	now := w.now()

	w.prompt.Step("%s", announceClosed(resetsAt, now))

	// Default YES: the offer is the only useful thing left on this path,
	// and a bare return from somebody who has just been told the door is
	// shut means "yes, tell me when it opens" far more often than it
	// means "no".
	join, err := w.prompt.Confirm(waitlistQuestion, true)
	if err != nil {
		return unaskableStop(err, resetsAt, now)
	}
	if !join {
		return declinedStop(resetsAt, now)
	}

	email, err := address()
	if err != nil {
		return unaskableStop(err, resetsAt, now)
	}

	if _, err := w.join.Waitlist(ctx, wire.WaitlistRequest{Email: email}); err != nil {
		return signupFailedStop(err, resetsAt, now)
	}
	return joinedStop(email, resetsAt, now)
}

// declinedStop ends a run whose user was offered the waitlist and said
// no — and it is also what a prompt nobody could answer ends as.
//
// THE EXIT CODE IS THE CLOSED-DOOR ONE EITHER WAY, and the reason is
// worth stating where the copy is: declining the waitlist is not
// declining the deploy. The deploy was declined by the service, and a
// command that exits zero having deployed nothing is a command that lies
// to whatever script ran it.
func declinedStop(resetsAt, now time.Time) *ui.Failure {
	return ui.NewFailure(
		ui.IDWaitlistDeclined,
		nothingDeployed,
		capacityUsedUp+" "+uploadedNothing, ui.NextWait,
		"Run `curious deploy` again "+afterTheReset(resetsAt, now)+".")
}

// noTerminalStop is the closed door met by a run with nobody to ask.
//
// It carries its OWN copy rather than the sentinel a prompt returns, and
// its Next names the terminal — the one thing a person reading this in a
// CI log can act on.
func noTerminalStop(resetsAt, now time.Time) *ui.Failure {
	return ui.NewFailure(
		ui.IDWaitlistNeedsTerminal,
		nothingDeployed,
		capacityUsedUp+" There is a waitlist for the moment it reopens, and "+
			"curious had no terminal to offer it on — that happens through a "+
			"pipe, from a script, or inside a tool that captures output.\n\n"+
			uploadedNothing, ui.NextWait,
		"Run this in a terminal to join the waitlist, or run `curious deploy` "+
			"again "+afterTheReset(resetsAt, now)+".")
}

// unaskableStop is what a question that got no usable answer ends as.
//
// The two cases are told apart because only one of them has a fix the
// reader can act on: with no terminal, the action is to run this in one.
// A cancellation or four unreadable answers leave nothing to say beyond
// what the closed door already says, so they end as a decline — the
// person stopped, and the reason the RUN stopped is still the shut door.
func unaskableStop(err error, resetsAt, now time.Time) *ui.Failure {
	if errors.Is(err, ui.ErrNotInteractive) {
		return noTerminalStop(resetsAt, now)
	}
	return declinedStop(resetsAt, now)
}

// joinedStop ends a run that got onto the list.
//
// WHAT IT PROMISES IS WHAT THE SERVER WILL DO AND NOTHING MORE. Capacity
// reopens and it is first-come; nothing is held for anybody. The
// tempting sentence — that a place is waiting — would be a promise this
// company would then have to keep, and the CLI is where it would be
// read.
func joinedStop(email string, resetsAt, now time.Time) *ui.Closed {
	return &ui.Closed{
		What: "You're on the list.",
		Why: fmt.Sprintf("curious will email %s when trial capacity reopens. It is "+
			"first-come from that moment and nothing is held back for anyone on "+
			"the list, so the sooner you run it after that, the better.\n\n%s",
			email, uploadedNothing),
		NextText: "Run `curious deploy` again " + afterTheReset(resetsAt, now) + ".",
	}
}

// signupFailedStop is the honest ending for a signup that did not
// happen: it says so, and it names somewhere a person can go instead.
//
// Saying it worked would be worse than the failure. Somebody who
// believes they are on a list stops checking, and finds out days later
// that nothing was ever recorded.
func signupFailedStop(err error, resetsAt, now time.Time) *ui.Failure {
	return ui.NewFailure(
		ui.IDWaitlistSignupFailed,
		"That didn't get you onto the list.",
		uploadedNothing, ui.NextWait,
		"Email "+supportAddress+" and we'll add you by hand.\nRun `curious deploy` "+
			"again "+afterTheReset(resetsAt, now)+".").
		Quoting(serverSaid(err))
}

// serverSaid prefers the server's own message and falls back to the
// error's text. A message written by the service is the only thing in
// reach that knows what actually happened.
func serverSaid(err error) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return err.Error()
}
