package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/curiouspub/cli/internal/ui"
)

// stallTimeout bounds a STALL, not a transfer. Thirty seconds is how long
// this client waits for the NEXT byte to move, and no amount of time is
// too long provided bytes keep moving.
//
// ~~const uploadTimeout = 30 * time.Second~~ — the same number bounding
// the whole PUT, and it was wrong. It was carried across from the JSON
// client, where thirty seconds bounds a small request and a small
// answer. Here it bounded a body up to the archive cap: 30 MB needs 48
// seconds at 5 Mbit/s and 24 at 10, so a legitimate large project on an
// ordinary uplink failed a deadline it could never have met, and said
// the host was unreachable while it was busy talking to it. A constant
// carried between two places carries its NUMBER; it does not carry the
// reason the number was chosen, and the two sites bound different
// things.
//
// THE ONLY HARD CEILING IS THE LINK'S OWN WINDOW, which the create
// announced and the store enforces regardless of what this client
// believes. A second ceiling of our own would be two facts about one
// window, free to disagree — so there is not one.
//
// IT IS SET HERE RATHER THAN INHERITED. The API client's own timeout and
// its redirect refusal live on a private http.Client; what it exposes is
// the TRANSPORT alone, so an upload built on that transport starts with
// no policy at all and every piece of it has to be stated again.
const stallTimeout = 30 * time.Second

// errUploadStalled is the cause the watchdog cancels with, so the branch
// that renders the stall reads WHY the request ended rather than
// guessing from the shape of net/http's error.
var errUploadStalled = errors.New("upload stalled")

// progressReader reports every byte handed to the transport. It is the
// only thing that can tell a slow upload from a stopped one: net/http
// offers no progress signal, and the difference between the two is the
// whole reason this client stopped using a total deadline.
type progressReader struct {
	r        io.Reader
	progress func(n int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.progress(n)
	}
	return n, err
}

// The copy a refused upload renders, held as named values because three
// sentences that must never be confused with one another are exactly the
// three that get confused when they are typed inline. Each is the phrase
// its own row asserts present and the other rows assert absent.
const (
	// linkExpired is for a refusal AT OR AFTER the window the server
	// announced. Running again is the fix, because a fresh link is
	// issued each time.
	linkExpired = "The upload link had expired by the time the archive got there."

	// refusedInsideWindow is for a refusal BEFORE that window closed.
	// The one cause this client can rule out is the one everybody would
	// otherwise name, and sending somebody round a loop with no exit is
	// worse than saying nothing.
	refusedInsideWindow = "running again will not fix this"

	// uploadStalled is for a request where no byte moved for the whole
	// stall window. It is deliberately NOT the unreachable sentence
	// below: the connection was made and bytes may well have crossed it,
	// so telling somebody their network is down is both wrong and the
	// least actionable thing this client could say.
	uploadStalled = "The upload stalled"

	// couldNotReach is for a failure where NOTHING was ever sent. Its
	// own row asserts it appears nowhere else — a stall after progress
	// that rendered it would be describing a connection that plainly
	// worked.
	couldNotReach = "could not reach"

	// connectionDropped is the third of that family: bytes moved, then
	// the connection failed. Neither "unreachable" nor "stalled" is true
	// of it.
	connectionDropped = "The connection dropped while curious was sending the archive"

	// mayHaveExpired is the honest ambiguous form, for a server that
	// told this client no window at all. IT IS THE BRANCH THAT RUNS
	// TODAY: the field is published in the contract and the server does
	// not populate it yet. A missing window means "not told", never
	// "already expired" — reading it as a deadline in the past would
	// render the expired copy for every failure, which is the defect
	// this whole mechanism exists to remove.
	mayHaveExpired = "The link may have expired"

	// uploadLeftBehind closes EVERY upload failure, and it says the two
	// things this client actually knows. A failed upload leaves a
	// created deploy record on the server, which is the server's to
	// discard; this step writes no state and no file of its own, so
	// copy implying it tidied something up would be inventing a
	// clean-up nobody performed.
	uploadLeftBehind = "\n\n" + nothingDeployed + " The deploy the server recorded for " +
		"this\narchive is discarded on its own when nothing is uploaded to it."
)

// storeRefused is what a failed upload travels as. It carries the STATUS
// as a number beside the sentence, because a caller that wants to act on
// the answer should not have to read prose to find it — and because the
// alternative, reading the store's own error document, is rejected: that
// response belongs to the substrate rather than to this project's
// contract, and a public client that switches on one substrate's error
// vocabulary has made it part of the contract by accident.
//
// Status is 0 when no answer ever arrived.
//
// It UNWRAPS to the Failure it was built with, so the copy renders
// through the same path every other stop in this package does.
type storeRefused struct {
	Status  int
	failure *ui.Failure
}

func (r *storeRefused) Error() string { return r.failure.Error() }

func (r *storeRefused) Unwrap() error { return r.failure }

// uploadFailed is the ONE constructor for every error the upload can
// return, and that is the mechanism rather than a tidiness preference.
//
// THE SIGNED URL IS A BEARER CREDENTIAL IN A QUERY STRING, and no
// redacting type can keep it out of a transport failure: the request is
// built from a plain string and net/http composes the error itself, so
// every refused connection, timeout and TLS failure comes back as a
// *url.Error carrying the whole signed URL through %v and %+v alike.
// Measured, not assumed. What keeps it out of a message is therefore
// call-site discipline — the transport error is never returned — and a
// single constructor is what makes that discipline checkable from
// outside, because a row can enumerate this function's returns and
// require every one of them to come through here.
//
// It names the operation and the REDACTED HOST: enough for a person to
// tell a wrong address from a dead connection, and nothing that is a
// credential.
// detail is somebody else's sentence — an error's own text — carried as
// a quotation rather than joined into this program's prose, so a line
// break inside it cannot become a paragraph in ours. Empty where there
// is nothing to quote.
func uploadFailed(id ui.FailureID, host string, status int, detail, why string,
	action ui.NextAction, next string) error {
	return &storeRefused{
		Status: status,
		failure: ui.NewFailure(
			id,
			"curious couldn't upload the archive to "+host+".",
			why+uploadLeftBehind, action,
			next).Quoting(detail),
	}
}

// redactedHost is the only part of an upload link that may be shown. The
// signature lives in the query string, the path names an object nobody
// outside this run can act on, and the host is the half that tells a
// person whether they are looking at a wrong address or a dead
// connection.
//
// A LINK THIS CANNOT PARSE RENDERS A PLACEHOLDER RATHER THAN ITSELF.
// Redaction needs a parsed URL, so the branch where parsing failed is
// exactly the branch that must not fall back to echoing the raw value.
func redactedHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "the upload address"
	}
	// u.Host is host:port with any "user:pass@" already removed, and it
	// carries neither the path nor the query the signature sits in.
	return u.Host
}

// uploadDeps is everything the upload needs from outside itself.
type uploadDeps struct {
	// URL is the presigned target. It is never logged, never printed and
	// never put in an error.
	URL string

	// ArchivePath is the packed project on this machine, and Bytes is
	// the size the create DECLARED. The two are one fact measured once:
	// the server signed the presigned link with Bytes as an exact
	// Content-Length, so this is the number that has to go on the wire.
	ArchivePath string
	Bytes       int64

	// ExpiresAt is the window the create announced, and the zero value
	// means the server did not say. It decides which SENTENCE a refusal
	// renders and nothing else — every value leads to the identical
	// action, which is why a success body selecting copy is not the same
	// as a client branching on one.
	ExpiresAt time.Time

	// Transport is the API client's own transport, so a proxy or extra
	// trust material configured for this run applies here too.
	Transport http.RoundTripper

	// Now is the clock the window is compared against.
	Now func() time.Time

	// StallTimeout is how long the upload waits for the NEXT byte before
	// giving up. Zero means stallTimeout, which is what production
	// passes; it is injected for the same reason Now is, so a row can
	// prove the behaviour in milliseconds instead of half a minute. It
	// is the same single constant arriving by argument, not a second
	// one.
	StallTimeout time.Duration
}

// uploadArchive PUTs the packed archive at the presigned link.
//
// # Three things this request is not
//
// It does not carry the bearer token. The presigned link IS the
// credential, and adding an Authorization header sends a second one to
// an origin that never asked for it.
//
// It does not answer in this project's error envelope. The response
// comes from an object store, so a failure is an HTTP status plus a body
// this client must not attempt to parse — see storeRefused.
//
// It does not retry, and it does not follow redirects. The refusal is
// spelled so that a 3xx arrives as a RESPONSE rather than an error,
// which is the trap: read as a success because it is neither a 4xx nor a
// 5xx, it would report a deploy that never uploaded.
func uploadArchive(ctx context.Context, deps uploadDeps) error {
	host := redactedHost(deps.URL)

	file, err := os.Open(deps.ArchivePath)
	if err != nil {
		return uploadFailed(ui.IDArchiveUnreadable, host, 0, err.Error(),
			"The archive curious packed could not be opened to send it.",
			ui.NextFreshDeploy, "Check that the temporary directory is readable, then run\n"+
				"`curious deploy` again.")
	}
	defer func() { _ = file.Close() }()

	// THE CEILING IS THE SERVER'S FACT. The window the create announced
	// becomes this request's deadline, so a link that has already run out
	// refuses before a single byte leaves this machine, and one that runs
	// out mid-transfer ends the moment the store would have started
	// refusing anyway. No ceiling of our own sits beside it.
	//
	// ONE CLOCK GOVERNS BOTH the ceiling and the copy. The window is
	// announced as an instant, and it becomes a DURATION measured against
	// deps.Now before it reaches the context — because context.WithDeadline
	// would compare that instant against the wall clock while every
	// sentence this file renders compares it against deps.Now, and two
	// clocks deciding one window is the shape this round exists to
	// remove. A window already closed gives a non-positive duration, and
	// a context that is expired on arrival refuses before a byte leaves
	// this machine.
	reqCtx := ctx
	if !deps.ExpiresAt.IsZero() {
		var cancelWindow context.CancelFunc
		reqCtx, cancelWindow = context.WithTimeout(reqCtx, deps.ExpiresAt.Sub(deps.Now()))
		defer cancelWindow()
	}
	reqCtx, cancelStall := context.WithCancelCause(reqCtx)
	defer cancelStall(nil)

	stall := deps.StallTimeout
	if stall <= 0 {
		stall = stallTimeout
	}
	// The watchdog fires when the next byte has not moved for the whole
	// window; every byte that does move pushes it back. A Reset that
	// races a firing timer is harmless — by then the request is already
	// cancelled and nothing reads the timer again.
	watchdog := time.AfterFunc(stall, func() { cancelStall(errUploadStalled) })
	defer watchdog.Stop()

	var sent atomic.Int64
	body := &progressReader{r: file, progress: func(n int) {
		sent.Add(int64(n))
		watchdog.Reset(stall)
	}}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPut, deps.URL, body)
	if err != nil {
		// THE UNDERLYING ERROR IS NOT WRAPPED, because net/http builds
		// this one out of the URL it was handed.
		return uploadFailed(ui.IDUploadAddressUnusable, host, 0, "",
			"curious could not build the upload request for that address.",
			ui.NextFreshDeploy, "Run `curious deploy` again. If it keeps happening, please report it.")
	}
	// The EXACT length the create declared. The link was signed with it,
	// so anything else — including the chunked encoding net/http would
	// otherwise choose for a file body — is refused by the store.
	req.ContentLength = deps.Bytes

	// NO Timeout FIELD. It is a total deadline by construction, which is
	// the thing this round removed; the stall watchdog and the link's own
	// window are the two limits, and both are on the context.
	client := &http.Client{
		Transport: deps.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		// THE TRANSPORT ERROR IS NEVER RETURNED. It is a *url.Error
		// carrying the whole signed link, and returning it — or
		// wrapping it — puts a credential into every log line, bug
		// report and terminal that error reaches. Every branch below
		// builds its message from scratch and none of them touches err.
		switch cause := context.Cause(reqCtx); {
		case errors.Is(cause, errUploadStalled):
			return uploadFailed(ui.IDUploadStalled, host, 0, "",
				fmt.Sprintf("%s: no data was sent for %s. The connection is open but\n"+
					"nothing is moving across it, so curious stopped rather than wait\n"+
					"indefinitely.", uploadStalled, stall),
				ui.NextFreshDeploy, "Check your connection and run `curious deploy` again.")

		case errors.Is(cause, context.DeadlineExceeded) && !deps.ExpiresAt.IsZero():
			// The deadline firing IS the window closing. Asking
			// deps.Now to confirm it would be a second fact about one
			// window — exactly what having no ceiling of our own
			// avoids — and the two could disagree.
			why, next := expiredCopy()
			return uploadFailed(ui.IDUploadLinkExpired, host, 0, "", why, ui.NextFreshDeploy, next)

		case sent.Load() == 0:
			return uploadFailed(ui.IDUploadHostUnreachable, host, 0, "",
				"curious "+couldNotReach+" "+host+" to send the archive. That usually\n"+
					"means the connection dropped, or something between here and there\n"+
					"is blocking it.",
				ui.NextFreshDeploy, "Check your connection and run `curious deploy` again.")
		}

		return uploadFailed(ui.IDUploadConnectionLost, host, 0, "",
			connectionDropped+". It had reached "+host+" and\n"+
				"was part way through when the connection failed.",
			ui.NextFreshDeploy, "Check your connection and run `curious deploy` again.")
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil

	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return uploadFailed(ui.IDUploadRedirected, host, resp.StatusCode, "",
			fmt.Sprintf("It answered %d, redirecting the upload somewhere else. curious\n"+
				"does not follow a redirect when it is sending your project, because\n"+
				"the address it was given is the only one the server signed.",
				resp.StatusCode),
			ui.NextFreshDeploy, "Check that no proxy is rewriting requests, then run\n"+
				"`curious deploy` again.")

	case resp.StatusCode == http.StatusForbidden:
		id, action, why, next := refusalCopy(host, deps.ExpiresAt, deps.Now())
		return uploadFailed(id, host, resp.StatusCode, "", why, action, next)
	}

	return uploadFailed(ui.IDUploadAnswerUnrecognised, host, resp.StatusCode, "",
		fmt.Sprintf("It answered %d, and that is not an answer this client can\n"+
			"explain.", resp.StatusCode),
		ui.NextFreshDeploy, "Run `curious deploy` again. If it keeps happening, please report it\n"+
			"with the number above.")
}

// refusalCopy picks which of the three sentences a refusal renders.
//
// # The clock is the discriminator, and it has to be
//
// The server signs an exact Content-Length, so the store answers the
// same refusal for a link whose window has closed and for a body whose
// length does not match the signature. One means wait and run again; the
// other is a fault that reproduces forever. Nothing in the response can
// tell them apart, and the announced window is the one thing both ends
// already share a vocabulary for.
// IT RETURNS THE FAMILY AND THE ACTION TOO, because the three branches
// are three different diagnoses and the caller cannot know which one ran.
// Passing one id and one action for all three — which is what this did
// before the catalog — filed a signature mismatch under the family for
// "we could not tell", and told a reader to deploy again about a fault
// that reproduces forever.
func refusalCopy(host string, expiresAt, now time.Time) (
	id ui.FailureID, action ui.NextAction, why, next string) {
	switch {
	case expiresAt.IsZero():
		return ui.IDUploadRefusedUnexplained, ui.NextFreshDeploy,
			mayHaveExpired + ", or something about the archive did not\n" +
				"match what the server signed. This server does not yet say when an\n" +
				"upload link stops working, so curious cannot tell you which it was.",
			"Run `curious deploy` again. If it fails the same way twice, please\n" +
				"report it."

	case !now.Before(expiresAt):
		why, next := expiredCopy()
		return ui.IDUploadLinkExpired, ui.NextFreshDeploy, why, next
	}

	// ADJUDICATED 2026-09-12, correction #1: the copy was right and the
	// ACTION was wrong. This branch is a signature mismatch inside the
	// window — a fault that reproduces forever — and its words have
	// always said so, asking only for a report and the version. The
	// action said FreshDeploy, which told a reader to do the one thing
	// the sentence above it explains will not help. The words stay
	// exactly as they were; the action is GiveUp, which is what
	// ui.NextGiveUp means.
	return ui.IDUploadSignatureMismatch, ui.NextGiveUp,
		"The link had not run out yet, so " + refusedInsideWindow + ". Something\n" +
			"about the archive did not match what the server signed for it, which\n" +
			"is a fault in curious rather than anything about your project.",
		"Please report this, and say which version you are on — `curious version`\n" +
			"prints it."
}

// expiredCopy is the ONE place the closed-window sentence is written.
// Two callers reach it — a refusal the store returned, and the request
// deadline the announced window set — and they must say the same thing,
// because they are the same event observed from two sides.
func expiredCopy() (why, next string) {
	return linkExpired + " Links are short-lived on\n" +
			"purpose, and a slow connection or a large project can outlast one.",
		"Run `curious deploy` again — a fresh link is issued every time."
}
