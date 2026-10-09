package flow

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The rows beside the shared answer for a code a step has no copy for.
// The answer at each of the six sites that give it is asserted in full by
// TestAnAnswerThisBuildCannotReadGivesUp; these are the cases that row does
// not reach.

// TestADefinedCodeAStepHasNoCopyForIsNotCalledUnrecognised. A code the
// contract defines can still reach a step that has no copy of its own for
// it, and calling it a code this build does not recognise would be false:
// this build declares it. The sentence says what is true instead, and the
// rest of the answer is the same.
func TestADefinedCodeAStepHasNoCopyForIsNotCalledUnrecognised(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	defined := &api.APIError{Status: http.StatusNotFound, Code: wire.CodeNotFound, Message: "no such deploy"}

	var f *ui.Failure
	if err := startFailure(defined, now); !errors.As(err, &f) {
		t.Fatalf("the stop is a %T", err)
	}
	const want = "The server answered with the code \"not_found\",\n" +
		"which curious has no answer for at this step."
	if f.Why != want {
		t.Errorf("why = %q, want %q", f.Why, want)
	}
	if f.ID != ui.IDServerAnswerUnrecognised || f.Next != ui.NextGiveUp {
		t.Errorf("id and action = %q, %q; want %q, %q", f.ID, f.Next,
			ui.IDServerAnswerUnrecognised, ui.NextGiveUp)
	}
	if f.Detail != defined.Message {
		t.Errorf("the server's message is %q here, want it verbatim: %q", f.Detail, defined.Message)
	}
}

// TestAnAnswerWithNoCodeIsNotSaidToHaveOne. A refusal whose body is not the
// wire envelope at all — a proxy's own page, an empty body — arrives with
// no code. Saying the server answered with the code "" would put a
// quotation in the server's mouth that it never said.
func TestAnAnswerWithNoCodeIsNotSaidToHaveOne(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	codeless := &api.APIError{Status: http.StatusBadGateway,
		Message: "the server returned an error this client could not parse"}

	var f *ui.Failure
	if err := startFailure(codeless, now); !errors.As(err, &f) {
		t.Fatalf("the stop is a %T", err)
	}
	const want = "The server's answer carried no code this build could read."
	if f.Why != want {
		t.Errorf("why = %q, want %q", f.Why, want)
	}
	if f.Detail != codeless.Message {
		t.Errorf("the message is %q here, want it verbatim: %q", f.Detail, codeless.Message)
	}
}

// TestACodeCarryingALineBreakCannotWriteALineOfOurs. The code is the
// server's, and it is shown as sent — except a line break, which is shown
// escaped. The sentence it sits in is this program's prose, laid out line
// by line, so a raw line break in the code would start a line that reads as
// ours. The one below would read as advice to retry.
func TestACodeCarryingALineBreakCannotWriteALineOfOurs(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	sneaky := &api.APIError{Status: http.StatusTeapot,
		Code: wire.ErrorCode("teapot\nTry again in a moment."), Message: "I am a teapot"}

	err := startFailure(sneaky, now)
	var f *ui.Failure
	if !errors.As(err, &f) {
		t.Fatalf("the stop is a %T", err)
	}
	const want = `The server answered with the code "teapot\nTry again in a moment.",` + "\n" +
		"which this build of curious does not recognise."
	if f.Why != want {
		t.Errorf("why = %q, want %q", f.Why, want)
	}
	for _, line := range strings.Split(rendered(err), "\n") {
		if strings.HasPrefix(line, "Try again") {
			t.Errorf("the code's line break started a line of its own: %q", line)
		}
	}
}

// TestAnUnknownCodeAtTheLoginGivesUpAndShowsTheCode drives the whole login
// over HTTP. The login decides recover-or-stop by looking the code up in its
// routing table, and a lookup is invisible to the guard that holds every
// switch over a code to the shared answer — so this row is what proves an
// unknown code at the login ends there too.
func TestAnUnknownCodeAtTheLoginGivesUpAndShowsTheCode(t *testing.T) {
	const invented = wire.ErrorCode("teapot_closed")
	const serverMessage = "This build predates whatever just happened."
	for _, known := range wire.AllErrorCodes {
		if known == invented {
			t.Fatalf("%q is now part of the contract, so this row no longer tests "+
				"an unknown code", invented)
		}
	}

	res := runVerifyCode(t, invented, http.StatusBadRequest, serverMessage, "")
	var f *ui.Failure
	if !errors.As(res.err, &f) {
		t.Fatalf("the login ended with %T (%v), want a failure", res.err, res.err)
	}
	if f.ID != ui.IDServerAnswerUnrecognised {
		t.Errorf("id = %q, want %q", f.ID, ui.IDServerAnswerUnrecognised)
	}
	if f.Next == ui.NextWait {
		t.Errorf("action = %q: the login stopped on a code it cannot read, so telling "+
			"the reader it will work later contradicts it", f.Next)
	}
	out := rendered(res.err)
	if !strings.Contains(out, `"teapot_closed"`) {
		t.Errorf("the code the server sent is nowhere in what a person reads:\n%s", out)
	}
	if !strings.Contains(out, serverMessage) {
		t.Errorf("the server's own message was replaced rather than shown:\n%s", out)
	}
}

// TestAnUnknownCodeOnTheBuildLogIsNamedAsOne. The bracket after a
// diagnostic is the only place the build log shows a code, and a code this
// build does not define is shown as sent, said to be one, and given no
// advice. A defined code the bracket has no words for is shown as sent,
// unchanged.
//
// REQUIRED MUTATION, run: give errorSide's default a retry phrase. The
// exact comparison here reds, and so does the error-line row.
func TestAnUnknownCodeOnTheBuildLogIsNamedAsOne(t *testing.T) {
	const want = "teapot_overflow, a code this build of curious does not recognise"
	if got := errorSide(wire.ErrorCode("teapot_overflow")); got != want {
		t.Errorf("errorSide = %q, want %q", got, want)
	}
	if got := errorSide(wire.CodeNotFound); got != string(wire.CodeNotFound) {
		t.Errorf("a defined code with no words of its own reads %q, want it as sent: %q",
			got, wire.CodeNotFound)
	}
	line := errorLine(wire.Error{Code: wire.ErrorCode("teapot_overflow"), Message: "the build stopped"})
	if !strings.HasSuffix(line, "the build stopped ("+want+")") {
		t.Errorf("the log line is %q, want it to end with the message and %q", line, want)
	}
}

// TestTheCapacityCheckKeepsTheCopyForWhatItIsSent. The capacity endpoint
// answers with two codes. The kill switch is a closed door and costs the
// closed-door exit; a server fault is the server's, and its advice — try
// again in a moment — is right. Everything else is the shared answer, and
// the shut daily cap still costs the closed-door exit wherever it is met.
func TestTheCapacityCheckKeepsTheCopyForWhatItIsSent(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	var fault *ui.Failure
	faultErr := capacityCheckFailure(&api.APIError{Status: http.StatusInternalServerError,
		Code: wire.CodeInternal, Message: "Something went wrong here."})
	if !errors.As(faultErr, &fault) {
		t.Fatalf("a server fault is a %T", faultErr)
	}
	if fault.ID != ui.IDCapacityCheckFailed || fault.Next != ui.NextWait ||
		fault.NextText != "Try again in a moment. "+uploadedNothing {
		t.Errorf("a server fault at the capacity check reads %q / %q / %q; its copy is not "+
			"this change's to make", fault.ID, fault.Next, fault.NextText)
	}
	if code := exitCodeFor(t, faultErr); code != 1 {
		t.Errorf("a server fault exits %d, want 1", code)
	}

	closed := capacityCheckFailure(&api.APIError{Status: http.StatusServiceUnavailable,
		Code: wire.CodeMaintenance, Message: "Down for maintenance."})
	if code := exitCodeFor(t, closed); code != ui.ExitServerClosed {
		t.Errorf("the kill switch at the capacity check exits %d, want %d", code, ui.ExitServerClosed)
	}

	// THE POSITIVE CONTROL for every "exit 1" row: a shut daily cap is
	// still the closed door.
	shut := createStopFailure(&api.APIError{Status: http.StatusTooManyRequests,
		Code: wire.CodeCapacityClosed, Message: "Full for today.", RetryAfter: time.Hour}, now)
	if code := exitCodeFor(t, shut); code != ui.ExitServerClosed {
		t.Errorf("a shut daily cap exits %d, want %d", code, ui.ExitServerClosed)
	}
}
