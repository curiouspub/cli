package flow

import (
	"fmt"
	"net/http"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
)

// A REFUSAL WITH NO CODE is a body that was not the wire envelope at all,
// or an envelope with an empty code. The service's own handlers always
// write the envelope, so an answer without one came from somewhere else: a
// proxy in front of the service while the service restarts or is down, a
// router answering for a route or a method it does not serve, a captive
// portal, or a CURIOUS_API_URL that points somewhere else.
//
// It is not a code this build has no copy for, so it does not get that
// answer. Updating curious is the remedy for few of the causes above, and
// nothing in the answer says which one it was. The client's own sentence
// about an answer it could not read is not the server's, so nothing is
// quoted.
//
// WHAT CAN BE TOLD IS THE STATUS, and it decides the remedy. An answer that
// passes on its own says to wait. One that does not says what to check,
// and claims neither that this build is older than the server nor newer.
// Each handler meets the empty code in a `case "":` of its own switch,
// passes what it can honestly say about what had already happened, and a
// guard holds every handler to it.

// clearsOnItsOwn reports whether a code-less answer is one that passes
// without anything outside the run changing: any 5xx, and the three 4xx
// statuses that say "not now" rather than "not this".
//
// Everything else does not pass by waiting. A 404 or a 405 is a router
// that does not serve the request, and a 1xx or 3xx arrives here only
// because curious never follows a redirect: something else is answering
// for the address.
func clearsOnItsOwn(apiErr *api.APIError) bool {
	switch apiErr.Status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return apiErr.Status >= http.StatusInternalServerError
}

const (
	// whyUnexplained and whyUnserved are the fixed half of each answer's
	// Why, with the status in it. They are constants at the construction
	// so that a setting either one names is visible there.
	whyUnexplained = "The server answered HTTP %d, in a form curious could not read as an\n" +
		"answer. Answers like this usually pass on their own."
	whyUnserved = "The server answered HTTP %d, in a form curious could not read as an\n" +
		"answer, and sending the same request again will not change that. The\n" +
		"address may not be the curious.pub service, something in between may be\n" +
		"answering for it, or the service may not serve this request. The address\n" +
		"comes from CURIOUS_API_URL."

	tryAgainUnexplained = "Try again in a moment. If it keeps happening, check anything between\n" +
		"this machine and the service, such as a proxy."
	checkTheAddress = "Check CURIOUS_API_URL, or unset it to use the default, and anything\n" +
		"between this machine and the service, such as a proxy. If both are\n" +
		"right, please report this with the output of `curious version`."
)

// errorUnexplained is the answer to a code-less refusal that passes on
// its own. The caller says where the person was, what did not happen, and
// what it can and cannot tell about what had already happened.
func errorUnexplained(stage ui.Stage, what string, apiErr *api.APIError, situation string) *ui.Failure {
	return ui.NewFailure(
		ui.IDServerErrorUnexplained,
		stage,
		what,
		fmt.Sprintf(whyUnexplained, apiErr.Status)+thenSituation(situation), ui.NextWait,
		tryAgainUnexplained)
}

// requestUnserved is the answer to a code-less refusal that does not pass
// by waiting. It names what can be checked from this machine.
func requestUnserved(stage ui.Stage, what string, apiErr *api.APIError, situation string) *ui.Failure {
	return ui.NewFailure(
		ui.IDServerRequestUnserved,
		stage,
		what,
		fmt.Sprintf(whyUnserved, apiErr.Status)+thenSituation(situation), ui.NextGiveUp,
		checkTheAddress)
}

// thenSituation is the caller's paragraph about what had already
// happened, or nothing when it has none.
func thenSituation(situation string) string {
	if situation == "" {
		return ""
	}
	return "\n\n" + situation
}
