package flow

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// A REFUSAL WITH NO CODE is a body that was not the wire envelope at all.
// The service always writes the envelope, so one without it was written by
// something else: a proxy in front of the service while it restarts, or a
// router answering for a route it does not serve. Which of those it was is
// in the status, and that decides whether waiting can help.
//
// These rows used to be answered as a code this build does not know, which
// said "Updating curious may help" and gave up. A proxy's 502 passes on its
// own, and updating changes nothing about it.

const (
	idErrorUnexplained = ui.FailureID("server-error-unexplained")
	idRequestUnserved  = ui.FailureID("server-request-unserved")

	// codelessMessage is what the client itself puts in an answer it could
	// not read. It is not the server's, so it must never be quoted as if it
	// were.
	codelessMessage = "the server returned an error this client could not parse"

	unexplainedNext = "Try again in a moment. If it keeps happening, check anything between\n" +
		"this machine and the service, such as a proxy."
	unservedNext = "Check CURIOUS_API_URL, or unset it to use the default, and anything\n" +
		"between this machine and the service, such as a proxy. If both are\n" +
		"right, please report this with the output of `curious version`."

	wantLoginMayHaveActed = "The server may have acted before this answer arrived, so a code already\n" +
		"entered may be spent. Start the login again for a new code; sending this\n" +
		"code again may count as a wrong attempt."
	wantBuildLogCannotTell = "curious cannot tell from this answer whether the build is still running."
	wantBuildNotAffected   = "The build itself is not affected: it runs on the server, and this was\n" +
		"only the window onto it."
	wantPublishCannotTell = "The deploy may or may not have got an address; curious cannot tell\n" +
		"from this answer.\n\nThe deploy is dpl-abc."
)

func codeless(status int) *api.APIError {
	return &api.APIError{Status: status, Message: codelessMessage}
}

func unexplainedWhy(status string) string {
	return "The server answered HTTP " + status + ", in a form curious could not read as an\n" +
		"answer. Answers like this usually pass on their own."
}

func unservedWhy(status string) string {
	return "The server answered HTTP " + status + ", in a form curious could not read as an\n" +
		"answer, and sending the same request again will not change that. The\n" +
		"address may not be the curious.pub service, something in between may be\n" +
		"answering for it, or the service may not serve this request. The address\n" +
		"comes from CURIOUS_API_URL."
}

// codelessSite is one of the six handlers, met with a given answer.
type codelessSite struct {
	name     string
	stop     func(*api.APIError) error
	stage    ui.Stage
	wantWhat string
}

func codelessSites() []codelessSite {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	return []codelessSite{
		{"at the capacity check", func(e *api.APIError) error { return capacityCheckFailure(e) },
			ui.StageDeploys, "curious couldn't start the deploy."},
		{"at the create", func(e *api.APIError) error { return createStopFailure(e, now) },
			ui.StageDeploys, "curious couldn't start the deploy."},
		{"at the login", func(e *api.APIError) error {
			return stopFailure(context.Background(), e, LoginDeps{Endpoint: "https://api.example"},
				"someone@example.com", now)
		}, ui.StageLogins, "curious couldn't finish logging you in."},
		{"at the publish", func(e *api.APIError) error { return publishStopFailure(e, "dpl-abc", now) },
			ui.StageAddresses, "The server wouldn't give this deploy an address."},
		{"at the start", func(e *api.APIError) error { return startFailure(e, now) },
			ui.StageBuilding, "The server wouldn't start the build."},
		{"at the build log", func(e *api.APIError) error { return streamRefusedFailure(e) },
			ui.StageBuildLog, "The server wouldn't send the build log."},
	}
}

// assertCodeless holds every part of a code-less answer that does not
// depend on the site.
func assertCodeless(t *testing.T, err error, id ui.FailureID, next ui.NextAction, nextText string) *ui.Failure {
	t.Helper()
	var f *ui.Failure
	if !errors.As(err, &f) {
		t.Fatalf("the stop is a %T", err)
	}
	if f.ID != id {
		t.Errorf("id = %q, want %q", f.ID, id)
	}
	if f.Next != next {
		t.Errorf("action = %q, want %q", f.Next, next)
	}
	if f.NextText != nextText {
		t.Errorf("next-step copy = %q, want %q", f.NextText, nextText)
	}
	// NOTHING IS QUOTED. The only sentence an answer like this carries is
	// the client's own, and quoting it would put it in the server's mouth.
	if f.Detail != "" {
		t.Errorf("the failure quotes %q as the server's words; the server sent none", f.Detail)
	}
	out := rendered(err)
	for _, never := range []string{"older than the server", `the code "`, codelessMessage} {
		if strings.Contains(out, never) {
			t.Errorf("a refusal with no code says %q:\n%s", never, out)
		}
	}
	if code := exitCodeFor(t, err); code != 1 {
		t.Errorf("exit code %d, want 1: this is a failure of the run, not a closed door", code)
	}
	return f
}

// TestACodelessAnswerThatCanClearSaysWait. A 502 is what the proxy in front
// of the service sends while the service restarts, and it passes. Each site
// says what it can and cannot tell about what happened before it.
//
// REQUIRED MUTATIONS, run:
//   - delete the six `case "":` clauses, so a code-less answer falls to the
//     shared answer for a code this build has no copy for. Every site reds.
//   - make the status predicate never answer "clears". Every site reds.
func TestACodelessAnswerThatCanClearSaysWait(t *testing.T) {
	situation := map[string]string{
		"at the capacity check": uploadedNothing,
		"at the create":         uploadedNothing,
		"at the login":          wantLoginMayHaveActed,
		"at the publish":        wantPublishCannotTell,
		"at the start": "The build may be running anyway — an answer like this can arrive after\n" +
			"the server acted.",
		"at the build log": wantBuildLogCannotTell,
	}
	for _, site := range codelessSites() {
		t.Run(site.name, func(t *testing.T) {
			err := site.stop(codeless(http.StatusBadGateway))
			f := assertCodeless(t, err, idErrorUnexplained, ui.NextWait, unexplainedNext)
			if f.Stage != site.stage || f.What != site.wantWhat {
				t.Errorf("stage and headline = %q, %q; want %q, %q", f.Stage, f.What, site.stage, site.wantWhat)
			}
			if want := unexplainedWhy("502") + "\n\n" + situation[site.name]; f.Why != want {
				t.Errorf("why = %q,\nwant %q", f.Why, want)
			}
		})
	}
}

// TestACodelessAnswerThatCannotClearGivesUp. A 404 or a 405 is a router
// answering for a route or a method it does not serve, and a redirect is
// something else answering for the address. Neither passes by waiting.
//
// REQUIRED MUTATION, run: make the status predicate always answer "clears".
// Every site reds.
func TestACodelessAnswerThatCannotClearGivesUp(t *testing.T) {
	situation := map[string]string{
		"at the capacity check": uploadedNothing,
		"at the create":         uploadedNothing,
		"at the publish":        wantPublishCannotTell,
		"at the build log":      wantBuildNotAffected,
	}
	for _, site := range codelessSites() {
		t.Run(site.name, func(t *testing.T) {
			err := site.stop(codeless(http.StatusNotFound))
			f := assertCodeless(t, err, idRequestUnserved, ui.NextGiveUp, unservedNext)
			if f.Stage != site.stage || f.What != site.wantWhat {
				t.Errorf("stage and headline = %q, %q; want %q, %q", f.Stage, f.What, site.stage, site.wantWhat)
			}
			want := unservedWhy("404")
			if s := situation[site.name]; s != "" {
				want += "\n\n" + s
			}
			if f.Why != want {
				t.Errorf("why = %q,\nwant %q", f.Why, want)
			}
		})
	}
	// A REDIRECT. curious never follows one, so a captive portal's 307
	// arrives here with no code.
	t.Run("a redirect, at the capacity check", func(t *testing.T) {
		err := capacityCheckFailure(codeless(http.StatusTemporaryRedirect))
		f := assertCodeless(t, err, idRequestUnserved, ui.NextGiveUp, unservedNext)
		if want := unservedWhy("307") + "\n\n" + uploadedNothing; f.Why != want {
			t.Errorf("why = %q,\nwant %q", f.Why, want)
		}
	})
}

// TestTheStatusDecidesWhetherACodelessAnswerCanClear pins the edges of the
// split at one site, the create. A 408, a 425 and a 429 pass on their own
// like any 5xx; every other status does not.
//
// REQUIRED MUTATIONS, run: make the status predicate always answer
// "clears" (the GiveUp rows red), and never (the Wait rows red).
func TestTheStatusDecidesWhetherACodelessAnswerCanClear(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status int
		want   ui.FailureID
	}{
		{http.StatusRequestTimeout, idErrorUnexplained},
		{http.StatusTooEarly, idErrorUnexplained},
		{http.StatusTooManyRequests, idErrorUnexplained},
		{http.StatusInternalServerError, idErrorUnexplained},
		{http.StatusFound, idRequestUnserved},
		{http.StatusMethodNotAllowed, idRequestUnserved},
		{http.StatusConflict, idRequestUnserved},
		{499, idRequestUnserved},
	} {
		t.Run(http.StatusText(tc.status)+" "+strconv.Itoa(tc.status), func(t *testing.T) {
			var f *ui.Failure
			if err := createStopFailure(codeless(tc.status), now); !errors.As(err, &f) {
				t.Fatalf("the stop is a %T", err)
			}
			if f.ID != tc.want {
				t.Errorf("HTTP %d at the create: id = %q, want %q", tc.status, f.ID, tc.want)
			}
		})
	}
}

// TestACodelessAnswerAtTheCapacityCheckIsRetriedThenSaysWait drives the
// real client, so its retry is in the row: the capacity check is the one
// call of the six that retries, and it retries a 5xx and nothing else.
func TestACodelessAnswerAtTheCapacityCheckIsRetriedThenSaysWait(t *testing.T) {
	t.Run("an empty 502", func(t *testing.T) {
		run := newGateRun(t)
		run.script.capacityOutcome = bare(http.StatusBadGateway, "", "")
		err := run.run(t)
		assertCodeless(t, err, idErrorUnexplained, ui.NextWait, unexplainedNext)
		if got := run.script.checks(); got != 3 {
			t.Errorf("the check was made %d times, want 3: a 5xx is retried twice", got)
		}
	})
	t.Run("a text/plain 405", func(t *testing.T) {
		run := newGateRun(t)
		run.script.capacityOutcome = bare(http.StatusMethodNotAllowed,
			"text/plain; charset=utf-8", "Method Not Allowed\n")
		err := run.run(t)
		assertCodeless(t, err, idRequestUnserved, ui.NextGiveUp, unservedNext)
		if got := run.script.checks(); got != 1 {
			t.Errorf("the check was made %d times, want 1: a 4xx is not retried", got)
		}
	})
}

// TestACodelessAnswerAtTheLoginStopsAndSaysWait drives the two login calls
// through the real client and the routing they share, which the six-site
// row bypasses. Neither call is repeated by the client, and a verify the
// server acted on has spent its code, so the copy says to start again.
func TestACodelessAnswerAtTheLoginStopsAndSaysWait(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(run *loginRun) *LoginRefusal
	}{
		{"asking for a code", func(run *loginRun) *LoginRefusal {
			run.script.startOutcomes = []outcome{bare(http.StatusBadGateway, "", "")}
			return LoginStart(t.Context(), run.deps, "someone@example.com")
		}},
		{"sending the code", func(run *loginRun) *LoginRefusal {
			run.script.verifyOutcomes = []outcome{bare(http.StatusBadGateway, "", "")}
			return LoginVerify(t.Context(), run.deps, VerifyRequest{
				Email: "someone@example.com", Code: "123456",
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newLoginRun(t)
			run.deps.Prompt = nil
			run.deps.Offer = nil
			refusal := tc.call(run)
			if refusal == nil {
				t.Fatal("the call succeeded against a server that refused it")
			}
			if refusal.Recoverable {
				t.Errorf("a refusal with no code was offered as recoverable, said %q", refusal.Said)
			}
			f := assertCodeless(t, refusal.Failure, idErrorUnexplained, ui.NextWait, unexplainedNext)
			if !strings.Contains(f.Why, wantLoginMayHaveActed) {
				t.Errorf("the login's failure does not say a code may be spent:\n%s", f.Why)
			}
		})
	}
}

// TestTheBuildLogsInternalSaysWait. The events route answers `internal`
// when the server fails while reading the deploy, and its own message says
// to try again. It is the start's answer to the same code, at the build log.
//
// REQUIRED MUTATION, run: delete streamRefusedFailure's internal case. The
// refusal falls to the shared answer and reds.
func TestTheBuildLogsInternalSaysWait(t *testing.T) {
	const serverSaid = "internal error"
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{{
		refuse: fails(http.StatusInternalServerError, wire.CodeInternal, serverSaid),
	}}

	handoff, err := run.run()
	defer handoff.Release()
	var f *ui.Failure
	if !errors.As(err, &f) {
		t.Fatalf("the stop is a %T: %v", err, err)
	}
	if f.ID != ui.IDServerFault || f.Next != ui.NextWait {
		t.Errorf("id and action = %q, %q; want %q, %q", f.ID, f.Next, ui.IDServerFault, ui.NextWait)
	}
	if f.Stage != ui.StageBuildLog || f.What != "The server hit a problem sending the build log." {
		t.Errorf("stage and headline = %q, %q", f.Stage, f.What)
	}
	if f.Detail != serverSaid {
		t.Errorf("the server's message is %q here, want it verbatim: %q", f.Detail, serverSaid)
	}
	if want := "Try again in a moment: run `curious deploy` again.\n" + wantBuildNotAffected; f.NextText != want {
		t.Errorf("next-step copy = %q,\nwant %q", f.NextText, want)
	}
}

// TestTheBuildLogsRateLimitStaysWithTheSharedAnswer is a control: the
// events route is not rate limited, so a rate limit there is a code this
// step has no copy for, and the build log's new case is for internal only.
func TestTheBuildLogsRateLimitStaysWithTheSharedAnswer(t *testing.T) {
	var f *ui.Failure
	err := streamRefusedFailure(&api.APIError{Status: http.StatusTooManyRequests,
		Code: wire.CodeRateLimited, Message: "slow down"})
	if !errors.As(err, &f) {
		t.Fatalf("the stop is a %T", err)
	}
	if f.ID != ui.IDServerAnswerUnrecognised {
		t.Errorf("id = %q, want %q", f.ID, ui.IDServerAnswerUnrecognised)
	}
}
