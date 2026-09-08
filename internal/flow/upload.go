package flow

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/curiouspub/cli/internal/ui"
)

// uploadTimeout bounds the PUT end to end.
//
// IT IS SET HERE RATHER THAN INHERITED, and that is the whole reason
// this constant exists. The API client's own timeout and its redirect
// refusal live on a private http.Client; what it exposes is the
// TRANSPORT alone. So an upload built on that transport starts with no
// deadline and net/http's default follow-redirects behaviour, and both
// have to be stated again rather than assumed to have come along.
const uploadTimeout = 30 * time.Second

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
func uploadFailed(host string, status int, why, next string) error {
	return &storeRefused{
		Status: status,
		failure: ui.NewFailure(
			"curious couldn't upload the archive to "+host+".",
			why+uploadLeftBehind,
			next),
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
	Transport *http.Transport

	// Now is the clock the window is compared against.
	Now func() time.Time
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
		return uploadFailed(host, 0,
			"The archive curious packed could not be opened to send it:\n\n  "+err.Error(),
			"Check that the temporary directory is readable, then run\n"+
				"`curious deploy` again.")
	}
	defer func() { _ = file.Close() }()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, deps.URL, file)
	if err != nil {
		// THE UNDERLYING ERROR IS NOT WRAPPED, because net/http builds
		// this one out of the URL it was handed.
		return uploadFailed(host, 0,
			"curious could not build the upload request for that address.",
			"Run `curious deploy` again. If it keeps happening, please report it.")
	}
	// The EXACT length the create declared. The link was signed with it,
	// so anything else — including the chunked encoding net/http would
	// otherwise choose for a file body — is refused by the store.
	req.ContentLength = deps.Bytes

	client := &http.Client{
		Transport: deps.Transport,
		Timeout:   uploadTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		// THE TRANSPORT ERROR IS NEVER RETURNED. It is a *url.Error
		// carrying the whole signed link, and returning it — or
		// wrapping it — puts a credential into every log line, bug
		// report and terminal that error reaches.
		return uploadFailed(host, 0,
			"curious could not reach "+host+" to send the archive. That usually\n"+
				"means the connection dropped, or something between here and there\n"+
				"is blocking it.",
			"Check your connection and run `curious deploy` again.")
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil

	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return uploadFailed(host, resp.StatusCode,
			fmt.Sprintf("It answered %d, redirecting the upload somewhere else. curious\n"+
				"does not follow a redirect when it is sending your project, because\n"+
				"the address it was given is the only one the server signed.",
				resp.StatusCode),
			"Check that no proxy is rewriting requests, then run\n"+
				"`curious deploy` again.")

	case resp.StatusCode == http.StatusForbidden:
		why, next := refusalCopy(host, deps.ExpiresAt, deps.Now())
		return uploadFailed(host, resp.StatusCode, why, next)
	}

	return uploadFailed(host, resp.StatusCode,
		fmt.Sprintf("It answered %d, and that is not an answer this client can\n"+
			"explain.", resp.StatusCode),
		"Run `curious deploy` again. If it keeps happening, please report it\n"+
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
func refusalCopy(host string, expiresAt, now time.Time) (why, next string) {
	switch {
	case expiresAt.IsZero():
		return mayHaveExpired + ", or something about the archive did not\n" +
				"match what the server signed. This server does not yet say when an\n" +
				"upload link stops working, so curious cannot tell you which it was.",
			"Run `curious deploy` again. If it fails the same way twice, please\n" +
				"report it."

	case !now.Before(expiresAt):
		return linkExpired + " Links are short-lived on\n" +
				"purpose, and a slow connection or a large project can outlast one.",
			"Run `curious deploy` again — a fresh link is issued every time."
	}

	return "The link had not run out yet, so " + refusedInsideWindow + ". Something\n" +
			"about the archive did not match what the server signed for it, which\n" +
			"is a fault in curious rather than anything about your project.",
		"Please report this, and say which version you are on — `curious version`\n" +
			"prints it."
}
