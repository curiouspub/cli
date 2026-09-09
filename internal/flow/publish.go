package flow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// # The numbers this file chooses, and what each of them bounds HERE
//
// Both are chosen at this site for what they bound at this site. Neither
// is carried from elsewhere in this package because it looked similar:
// the stream's windows bound a silence on a connection held open across
// a whole build, and these bound a small request being asked again while
// the server finishes something the build log has already announced.
const (
	// publishRetryInterval is how long this step waits before asking
	// again when the server says the deploy is not ready yet.
	//
	// ONE SECOND, and what it bounds is the gap between two asks about a
	// TRANSITION rather than about a build. The build log has already
	// ended with the builder's own terminal event; what is outstanding
	// is the check that runs on what the build produced and the record
	// write that follows it. Shorter would put several requests a second
	// on an endpoint that cannot answer sooner for being asked more
	// often. Longer would spend the window below waiting rather than
	// asking, which is the wrong half to spend it on when the thing
	// being waited for usually resolves between two asks.
	publishRetryInterval = time.Second

	// publishConfirmWindow bounds the WHOLE of that asking. It is a
	// window rather than a count of attempts because the count is a
	// consequence of the interval and the window is the fact a person
	// experiences — and because a later change to the interval would
	// silently change how long a run waits if the bound were the count.
	//
	// THIRTY SECONDS, and the two directions cost differently, which is
	// why it is not shorter. Stopping too early ends a run whose build
	// was fine, and the only way back is a fresh deploy: the pack, the
	// upload and the build again. Waiting too long costs half a minute
	// and a message saying what happened. So the number sits on the
	// generous side of what a record write should need.
	//
	// IT IS SPENT ONLY ON THE RACE. Nothing waits here on the ordinary
	// path — a deploy the server considers built is answered on the
	// first ask — so this is not half a minute added to every run.
	publishConfirmWindow = 30 * time.Second
)

// siteBaseDomain is the domain a published site answers under.
//
// IT IS A BARE HOST WITH NO SCHEME, and that is the whole of why it is
// spelled this way. This module allows a small, pinned number of NAMED
// URL constants and refuses every other compiled-in http(s) string; a
// domain with no scheme is not a URL literal, so it consumes none of
// that budget and never reaches it. What it does reach is the separate
// rule about bare hostnames, which this client is entitled to name
// because it is one of the handful this program legitimately knows.
//
// The scheme is added at the point of use, by the composition below.
const siteBaseDomain = "curiously.dev"

// The copy this step owns, held as named values so a row asserting one
// of them cannot drift into asserting a second copy of it.
const (
	// publishedHeadline is the whole of this client's claim, and what it
	// does NOT say is the point of it. The server said the deploy is
	// published; it did not say the site answers yet, and neither does
	// this. One word, in the past tense, about the one thing that was
	// actually observed.
	publishedHeadline = "Published."

	// propagationCaveat is the sentence that stands between a person and
	// a wrong conclusion. An address begins answering a little after the
	// deploy is published — measured once at about half a minute, which
	// is an OBSERVATION and not a bound, so the copy says "up to about a
	// minute" in the softer form rather than promising a figure nobody
	// measured twice.
	//
	// IT NAMES A NEXT STEP, because a caveat with none leaves somebody
	// refreshing. A reader who opens the address straight away and finds
	// a placeholder has been told, one line earlier, both that it can
	// happen and what to do about it.
	propagationCaveat = "It can take up to about a minute before the address starts\n" +
		"answering everywhere, so opening it straight away may show a\n" +
		"placeholder page instead of your site. If you see that page, wait a\n" +
		"moment and reload."

	// expiresPrefix opens the expiry line. The window is the SERVER's and
	// nothing this client holds determines it, so this renders what it
	// was told and computes nothing.
	expiresPrefix = "This deploy expires at "

	// buildStillBeingChecked is the one line a race puts on stderr. It is
	// narration and not a failure: the server has said the deploy is not
	// ready, which is a thing that resolves on its own, and a client that
	// announced it as a problem would be describing an ordinary moment as
	// a fault.
	buildStillBeingChecked = "The build is still being checked; asking again."
)

// expiresLayout renders an instant DAYS out: a date, a time of day, and
// the numeric offset.
//
// It is deliberately not the time-of-day layout this package already has
// for a capacity reset, and the difference is the DISTANCE rather than a
// preference. That one names a door reopening within the day, where a
// date would be noise. This one names a window measured in days, where a
// bare time of day is a number a reader attaches to today and is wrong
// about by two.
//
// The offset is carried for the reason the other layout carries it: a
// zone abbreviation alone is ambiguous across the world, and absent
// entirely from a zone that has no name.
const expiresLayout = "Mon 2 Jan 2006, 15:04 MST (-07:00)"

// publishedURL composes the address a published deploy answers at, out
// of the LABEL the server sent and the domain compiled in above.
//
// IT IS BUILT THROUGH net/url RATHER THAN BY CONCATENATION, and that is
// not a style choice. Written as a "https://" literal joined to the
// label, the scheme and its separator are a compiled-in URL fragment
// sitting in a source file — which this module refuses, and refuses for
// a reason that applies here as much as anywhere: a host a reader cannot
// find by grepping for one declaration is the shape a phone-home takes,
// and a scheme fragment beside a domain constant is that host written in
// two pieces. Assembling the value in the type the standard library has
// for the job produces the identical string with no such fragment
// anywhere.
//
// MEASURED RATHER THAN ASSUMED, because a claim about a guard made
// without running the guard is how this decision was got wrong before it
// was got right. Written as the concatenation, the URL guard reds twice
// over one line — once for a compiled-in URL that is not a named
// constant, and once for a host being assembled out of scheme fragments.
// Written this way it is silent, and the value in the binary is the
// same.
//
// IT ALSO ESCAPES, which the concatenation would not. The label is the
// SERVER's and this string is printed for somebody to click; url.URL
// encodes the host on its way out, so a label carrying anything a host
// may not carry arrives visibly escaped rather than as something else.
// That is why nothing downstream escapes it a second time: there is no
// byte left for a terminal to obey, and a call that cannot fire is an
// assertion with no reachable path to failure.
func publishedURL(subdomain string) string {
	u := url.URL{Scheme: "https", Host: subdomain + "." + siteBaseDomain}
	return u.String()
}

// publishRoute is what one refused publish does to the run.
type publishRoute int

const (
	// publishAskAgain waits and asks the same question again, bounded by
	// publishConfirmWindow.
	publishAskAgain publishRoute = iota + 1

	// publishStop ends the run.
	publishStop
)

// publishRouting states, for every code the contract defines, whether
// this step waits or stops. It is keyed to the contract's own
// enumeration rather than to a list typed beside it, so a code added
// later arrives already needing an answer here.
//
// EXACTLY ONE CODE WAITS, and the asymmetry was decided at the server
// rather than defaulted here. A deploy in a state nobody can name
// answers the TERMINAL code, not the waitable one — a client told to
// wait for a state nobody can name waits for ever, and one told to stop
// loses a retry it might have won and is left with something a person
// can act on. Only the second is recoverable, so this client needs no
// ambiguous third branch of its own: inventing one would be a second
// decision about a fact the contract has already decided.
var publishRouting = map[wire.ErrorCode]publishRoute{
	// The race this step exists to resolve: the build log ended, and the
	// record has not caught up. Not terminal, and the only code here
	// that is worth asking about twice.
	wire.CodeDeployNotReady: publishAskAgain,

	// Terminal. The build did not produce something the server would
	// take, and it never will.
	wire.CodeDeployFailed: publishStop,

	// An older or otherwise different server saying the same thing with
	// the one code it had. It shares the copy below rather than growing
	// a second wording for one event.
	wire.CodeBadRequest: publishStop,

	// The deploy id is unknown, or is not this token's.
	wire.CodeNotFound: publishStop,

	// The kill switch. The service declined; the build is fine.
	wire.CodeMaintenance: publishStop,

	// The server could not complete it — including the at-capacity case,
	// whose own message says it is theirs to fix and not to retry.
	wire.CodeInternal: publishStop,

	// DECLARED, NOT REACHABLE from this endpoint by anything this client
	// can produce. Every one of them is stated because the table is
	// exhaustive over the contract, and a code with no stated routing is
	// a gap rather than an answer.
	wire.CodeUnauthorized:   publishStop,
	wire.CodeForbidden:      publishStop,
	wire.CodeRateLimited:    publishStop,
	wire.CodeCapacityClosed: publishStop,
}

// deployPublisher is the slice of the API client this step needs. It is
// declared HERE, by the consumer, like every other seam in this package.
type deployPublisher interface {
	DeployPublish(ctx context.Context, deployID string) (*wire.DeployPublishResponse, error)
}

// publishRenderer is the slice of the terminal this step needs, and it
// is TWO METHODS BECAUSE THE STREAM SPLIT IS TWO STREAMS. The address is
// content a script may consume and goes to stdout; everything this
// client says about it is narration and goes to stderr.
//
// That it has the same shape as the build log's renderer is arithmetic
// rather than design — this step needs the same two halves for its own
// reasons — and sharing the name would mean one step's requirements
// quietly deciding another's.
type publishRenderer interface {
	Step(format string, args ...any)
	Result(format string, args ...any)
}

// publishDeps is everything the publish needs from outside itself.
type publishDeps struct {
	// Client is the authenticated client. There is no reauthenticate
	// seam beside it, and that is a decision rather than an omission: by
	// the time this call is made the same token has already been
	// accepted by the create, the start and the stream, so a refusal
	// here is not a run that began without a usable login.
	Client deployPublisher

	// DeployID is the record this call names, and the thing every
	// failure below prints so a person has something to ask about.
	DeployID string

	// Render is the terminal. Only the narration half is used here; the
	// address is written by the rendering below, once, at the end.
	Render publishRenderer

	// Now is the clock the confirm window is measured against — the
	// run's own, so the bound and any sentence about it are answers from
	// one clock rather than two.
	Now func() time.Time

	// ConfirmWindow is the bound on the whole of the asking. Zero means
	// publishConfirmWindow, which is what production passes.
	//
	// IT IS INJECTED FOR A REASON THE CLOCK CANNOT COVER. The window is
	// measured on Now, and the sleep between two asks is measured on the
	// real one — which is right, because in production they are the same
	// clock. A row about the BOUND therefore has to spend real time, and
	// thirty seconds of it per run is not a row anybody will keep. This
	// makes the bound small enough to measure and leaves everything else
	// exactly as it ships.
	ConfirmWindow time.Duration

	// RetryInterval is the pause between two asks. Zero means
	// publishRetryInterval, which is what production passes; it is
	// injected for the reason the clock is, so a row can drive the race
	// in milliseconds instead of in seconds. It changes the PACE and
	// never the bound: the window above is what decides when the asking
	// stops.
	RetryInterval time.Duration
}

// publishDeploy asks the server to give this deploy an address, and
// resolves the one race the last step of a deploy can meet.
//
// # The race, and why waiting is this client's job
//
// The build log's terminal event carries the BUILDER's claim, posted
// before the check that runs on what the build produced. So a run whose
// log has just said the build finished can reach this call while the
// record still says the deploy is queued or building — the server has
// not caught up with a thing it has already told this client about. That
// is a race rather than a failure, it resolves on its own, and the
// server sends no figure for how long it takes because it has no honest
// one to send. The wait is therefore chosen here, at its own site,
// bounded by the window above on the run's own clock.
//
// EVERY OTHER REFUSAL STOPS. A deploy the server will not take is a
// build outcome arriving late; a deploy it has never heard of will not
// appear; a closed door does not open by being knocked on again.
func publishDeploy(ctx context.Context, deps publishDeps) (*wire.DeployPublishResponse, error) {
	window := deps.ConfirmWindow
	if window <= 0 {
		window = publishConfirmWindow
	}
	deadline := deps.Now().Add(window)
	interval := deps.RetryInterval
	if interval <= 0 {
		interval = publishRetryInterval
	}
	announced := false

	for {
		resp, err := deps.Client.DeployPublish(ctx, deps.DeployID)
		if err == nil {
			return resp, nil
		}

		if errors.Is(err, api.ErrUnusableResponse) {
			// The answer ARRIVED and cannot be used. Reporting it as a
			// transport failure would tell a person the request might
			// not have got there, which is the one thing this branch
			// knows to be false.
			return nil, publishUnusableAnswerFailure(deps.DeployID)
		}

		var apiErr *api.APIError
		if !errors.As(err, &apiErr) {
			// No envelope, no code, nothing the server said. The request
			// may well have arrived.
			return nil, publishUnansweredFailure(err, deps.DeployID)
		}

		if publishRouting[apiErr.Code] != publishAskAgain {
			return nil, publishStopFailure(apiErr, deps.DeployID, deps.Now())
		}

		now := deps.Now()
		if !now.Before(deadline) {
			return nil, publishNotConfirmedFailure(deps.DeployID)
		}
		if !announced {
			// ONCE, not once per ask. The reader learns that the run is
			// waiting on something; repeating it every second would turn
			// a resolved race into a wall of text about a wait that ended
			// fine.
			deps.Render.Step("%s", buildStillBeingChecked)
			announced = true
		}
		// THE SLEEP IS CLAMPED TO WHAT IS LEFT OF THE WINDOW, because
		// the window says it bounds the whole of the asking and the
		// interval says it changes the pace and never the bound. Left
		// unclamped, the last sleep runs past the deadline it was
		// checked against and the run takes window + interval — which is
		// a second on the shipped numbers and the whole of the bound if
		// a caller injects a longer pace through the seam above.
		wait := interval
		if remaining := deadline.Sub(now); remaining < wait {
			wait = remaining
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, publishStoppedFromOutside(ctx.Err(), deps.DeployID)
		}
	}
}

// renderPublished writes the end of the whole command: the narration
// first, then the address on its own line.
//
// THE ORDER IS THE PRODUCT. The address goes last so that the caveat is
// the sentence directly above it — somebody who opens the link
// immediately and meets a placeholder page has just read why, and
// somebody who waits has lost nothing. Printed the other way round, the
// warning arrives after the thing it is a warning about.
//
// THE ADDRESS IS THE ONLY THING ON STDOUT, by the origin rule the build
// log established: it is content a script may consume, so
// `curious deploy > url.txt` yields the address and nothing else, while
// the claim, the caveat and the expiry are read by a person on stderr.
// There is deliberately no structured success object — the
// machine-readable surface of a successful run is that one line and the
// exit code.
func renderPublished(render publishRenderer, resp *wire.DeployPublishResponse, now time.Time) {
	closing := publishedHeadline + "\n\n" + propagationCaveat
	if line, known := expiresLine(resp.ExpiresAt, now); known {
		closing += "\n\n" + line
	}
	render.Step("%s", ui.Prose(closing))
	render.Result("%s", publishedURL(resp.Subdomain))
}

// expiresLine renders the expiry BOTH WAYS and reports whether there was
// anything to render.
//
// Both, because a bare timestamp in a terminal is a thing people misread
// by a day, and a bare relative phrase cannot be put in a calendar. The
// relative half goes through the same renderer every other duration in
// this package uses, so two places cannot start describing one kind of
// fact differently.
//
// NOTHING IS COMPUTED. The window is the server's and nothing this
// client can see determines it, so an expiry that is absent or already
// behind the clock renders no line at all rather than a sentence
// invented to fill the space.
func expiresLine(expiresAt, now time.Time) (string, bool) {
	if expiresAt.IsZero() || !expiresAt.After(now) {
		return "", false
	}
	return expiresPrefix + expiresAt.In(now.Location()).Format(expiresLayout) +
		" — " + relativePhrase(expiresAt.Sub(now)) + ".", true
}

// publishStoppedFromOutside is what a wait that was cut off ends the run
// as, and it asks WHY before it says anything.
//
// A DEADLINE AND A CANCELLATION ARE NOT THE SAME EVENT and they used to
// render the same sentence: "the server was still working on it 30s
// later. curious stopped asking rather than wait indefinitely." Both
// clauses are false for a cancellation — seconds may have passed, and
// it was not curious that stopped. Somebody who interrupted their own
// run was told a story about the far end.
//
// So a deadline keeps that copy, which is exactly what it describes, and
// a cancellation gets the short word and the interrupted cost. It claims
// nothing about the server, because nothing here knows anything about
// the server. The cause is wrapped rather than dropped, so a caller can
// still tell the two apart.
func publishStoppedFromOutside(cause error, deployID string) error {
	if errors.Is(cause, context.DeadlineExceeded) {
		return publishNotConfirmedFailure(deployID)
	}
	return fmt.Errorf("%w: the publish of %s was still being confirmed (%w)",
		ui.ErrInterrupted, deployID, cause)
}

// publishUnusableAnswerFailure is a 200 this client cannot act on: the
// server gave the deploy an address and the label in it is not one.
//
// IT DOES NOT SAY NOTHING WAS DEPLOYED, because something may well have
// been — the server answered success. What it says is that the address
// cannot be printed, which is the only honest thing a client holding a
// label like "" can say, and it is a great deal better than printing
// https://.curiously.dev and exiting zero.
func publishUnusableAnswerFailure(deployID string) error {
	return ui.NewFailure(
		"The server gave this deploy an address that cannot be one.",
		"The publish itself was accepted, so the deploy may well be live — but\n"+
			"the label the server sent back is not a label, so curious has no\n"+
			"address to show you and will not guess at one.\n\n"+
			"The deploy is "+deployID+".",
		"Please report this. Running `curious deploy` again would make a second\n"+
			"deploy rather than answer the question about this one.")
}

// publishStopFailure is the copy for each code that ends the run.
//
// THE SERVER'S OWN MESSAGE IS RENDERED FOR SOME OF THESE AND NOT FOR THE
// BUILD ONES, and the split is deliberate. Where the server knows
// something this client does not — which limit, which capacity, which
// door — its sentence is the only thing that carries it. Where the
// server's sentence says a build cannot be given an address, it is
// describing THIS step, and this step is not what went wrong: repeating
// it would put the reader's attention on the last call of the run
// instead of on the build it is really about, and it names a state the
// reader can do nothing with, because the action is identical whichever
// state it was.
func publishStopFailure(apiErr *api.APIError, deployID string, now time.Time) error {
	switch apiErr.Code {
	case wire.CodeRateLimited:
		// THE CONTRACT SAYS THESE CARRY A TIME, so this renders it.
		// wire.CarriesRetryAfter names the set and the row that drives
		// this is keyed to that function rather than to a list typed
		// beside it — so a code that joins the set arrives here already
		// needing an answer, and one that leaves it cannot be left
		// behind. Whether this endpoint emits them today is the
		// server's business; a client that threw away a time it was
		// promised would be wrong either way.
		return ui.NewFailure(
			"The server is asking for a pause.",
			nothingDeployed+"\n\nThe deploy is "+deployID+".",
			"Try again "+afterTheReset(now.Add(apiErr.RetryAfter), now)+".").Quoting(apiErr.Message)

	case wire.CodeCapacityClosed:
		// The door, which costs ExitServerClosed rather than 1 — the
		// scoped code exists so a script can tell "come back later" from
		// "this went wrong".
		return ui.ServerClosed(ui.NewFailure(
			closedHeadline,
			nothingDeployed+"\n\nThe deploy is "+deployID+".",
			"Try again "+afterTheReset(now.Add(apiErr.RetryAfter), now)+".").Quoting(apiErr.Message))

	case wire.CodeDeployFailed, wire.CodeBadRequest:
		return buildRefusedFailure(deployID)

	case wire.CodeNotFound:
		return ui.NewFailure(
			"The server doesn't know that deploy.",
			"The deploy is "+deployID+". That usually "+
				"means it has already\nexpired, or that it belongs to a different login "+
				"from the one this run\nis using. "+nothingDeployed,
			"Run `curious deploy` again to make a fresh one.").Quoting(apiErr.Message)

	case wire.CodeMaintenance:
		// The kill switch. The build is fine and the archive got there;
		// the service simply is not taking this step right now.
		return ui.ServerClosed(ui.NewFailure(
			"curious.pub isn't giving out addresses right now.",
			nothingDeployed,
			"Try again a little later.").Quoting(apiErr.Message))

	case wire.CodeInternal:
		// NO RETRY ADVICE. The server's own message for the case this
		// code most often carries says in terms that it is theirs to fix
		// and not something to retry, and copy telling somebody to try
		// again anyway would be this client contradicting the sentence
		// printed directly above it.
		return ui.NewFailure(
			"The server couldn't finish the deploy.",
			nothingDeployed+"\n\nThe deploy is "+
				deployID+".",
			"There is nothing to fix at this end and nothing here worth retrying.\n"+
				"If it keeps happening, please get in touch.").Quoting(apiErr.Message)
	}

	// Routed to a stop with no copy of its own, or a code this build
	// predates. The contract is additive-only, so the server is entitled
	// to introduce one, and the honest answer is to show what it said.
	return ui.NewFailure(
		"The server wouldn't give this deploy an address.",
		nothingDeployed+"\n\nThe deploy is "+
			deployID+".",
		"Run `curious deploy` again. If it keeps happening, updating curious may\n"+
			"help — this build may be older than the server.").Quoting(apiErr.Message)
}

// buildRefusedFailure is what a deploy the server will not take ends the
// run as, and every word of it is chosen against one failure mode: a
// reader concluding that the last step is what broke.
//
// IT NEVER CALLS THIS A FAILURE OF THIS STEP. The build log said the
// build had finished; what runs afterwards is a check on what the build
// produced, and the server refused the deploy at that point. So the copy
// names the BUILD, says plainly that nothing has been deployed, prints
// the deploy id so there is something to ask about, and offers the one
// action there is — a fresh run. Nothing at this step is retryable on
// its own, and saying otherwise would send somebody round a loop with no
// exit.
//
// IT CLAIMS NO STATE. This client is told which of two things happened
// by a CODE; the state the server observed reaches it only inside a
// sentence written for a person, and parsing that back is exactly what
// this project refuses to do with prose. So the copy is true whatever
// state was observed — including one this build has never heard of,
// which the contract routes here on purpose.
func buildRefusedFailure(deployID string) *ui.Failure {
	return ui.NewFailure(
		"The build finished, and the server would not take the result.",
		"The build log above ended with the build reporting that it had\n"+
			"finished. What runs after that is a check on what the build actually\n"+
			"produced, and the server refused this deploy at that point — so this\n"+
			"is the second half of a build outcome arriving late rather than\n"+
			"anything going wrong at the last step. "+nothingDeployed+"\n\n"+
			"The deploy is "+deployID+".",
		"Run `curious deploy` again. What went wrong is in the build log above\n"+
			"rather than here, and there is nothing at this step to retry on its\n"+
			"own.")
}

// publishNotConfirmedFailure is what a deploy that never left the
// not-ready answer ends the run as.
//
// IT DOES NOT SAY THE BUILD FAILED, because nothing said so: the last
// thing the server reported was that it was still working. What it says
// is that the run stopped asking, how long it asked for, and that
// nothing has been deployed — which is the honest shape of a bounded
// thing giving up rather than a verdict it was never given.
func publishNotConfirmedFailure(deployID string) error {
	return ui.NewFailure(
		"The build could not be confirmed in time.",
		"The build log ended with the build reporting that it had finished, and\n"+
			"the server was still working on it "+publishConfirmWindow.String()+
			" later. curious stopped\nasking rather than wait indefinitely. "+
			nothingDeployed+"\n\nThe deploy is "+deployID+".",
		"Run `curious deploy` again.")
}

// publishUnansweredFailure is what a last step with no answer ends the
// run as — a dropped connection, a deadline, a cancellation.
//
// IT CLAIMS NEITHER OUTCOME, which is the whole of it. A failure after
// the request left this process is indistinguishable from one before it,
// so the deploy may have an address and may not, and a client that
// picked one would be guessing in front of somebody who cannot check.
//
// IT CANNOT NAME THE ADDRESS EITHER, and saying so is better than
// leaving a reader to wonder why the one useful thing is missing: the
// subdomain arrives in the answer that never came, and nothing else this
// run holds determines it. So the action offered is the only one there
// is.
func publishUnansweredFailure(err error, deployID string) error {
	return ui.NewFailure(
		"curious didn't hear back after asking for the deploy's address.",
		"The deploy may or may not have got one — curious cannot "+
			"tell whether\nthe request arrived, and it does not ask twice, because "+
			"asking again is\nnot a way of finding out. It never learned the address "+
			"either: that\narrives in the answer that did not come.\n\nThe deploy is "+
			deployID+".",
		"Run `curious deploy` again when the connection is back.").Quoting(err.Error())
}
