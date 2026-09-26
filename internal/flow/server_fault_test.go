package flow

import (
	"net/http"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// internal on the create and on the start is the server failing and saying
// to try again, and the client says the same. Before it had a case of its
// own on either call it fell to the fallback for a code this build does not
// know, which told a person their build might be older than the server:
// false for a code the contract has always had.
//
// REQUIRED MUTATION: remove the internal case from createStopFailure or from
// startFailure. That call's row reds on "older than the server".
func TestAServerFaultSaysTryAgainAndNeverBlamesTheBuild(t *testing.T) {
	const serverSaid = "Something went wrong on our end. Please try again."
	for _, tc := range []struct {
		name     string
		script   func(r *deployRun)
		wantWhat string
		uploaded bool
	}{
		{
			name: "the create",
			script: func(r *deployRun) {
				r.script.deployOutcome = outcome{status: http.StatusInternalServerError,
					code: wire.CodeInternal, message: serverSaid}
			},
			wantWhat: "The server hit a problem starting the deploy.",
		},
		{
			name: "the start",
			script: func(r *deployRun) {
				r.script.startOutcome = outcome{status: http.StatusInternalServerError,
					code: wire.CodeInternal, message: serverSaid}
			},
			wantWhat: "The server hit a problem starting the build.",
			uploaded: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			tc.script(run)

			handoff, err := run.run()
			defer handoff.Release()
			if err == nil {
				t.Fatal("a server fault was reported as a success")
			}
			text, _ := renderedBytes(t, err)
			for _, want := range []string{tc.wantWhat, serverSaid, "Try again in a moment", "server-fault"} {
				if !strings.Contains(text, want) {
					t.Errorf("the failure never said %q:\n%s", want, text)
				}
			}
			if strings.Contains(text, "older than the server") {
				t.Errorf("a code the contract has always had was answered as one this build predates:\n%s", text)
			}
			if tc.uploaded && !strings.Contains(text, "The archive was uploaded") {
				t.Errorf("the start's failure does not say the archive went up:\n%s", text)
			}
		})
	}
}

// The error line on the build log names whose side a stop is on for the
// three codes that say so, and shows any other code as it was sent.
//
// REQUIRED MUTATION: make errorSide return the bare code for build_failed.
// The first row reds, and the line reads the way the defect did.
func TestTheErrorLineNamesWhoseSideTheStopIsOn(t *testing.T) {
	for _, tc := range []struct {
		code  wire.ErrorCode
		want  string
		never string
	}{
		{wire.CodeBuildFailed, "(in the project's build)", "internal"},
		{wire.CodeLimitReached, "(a platform limit on builds)", "internal"},
		{wire.CodeInternal, "(on curious.pub's side)", "project"},
		{"a_code_this_build_predates", "(a_code_this_build_predates)", ""},
	} {
		got := errorLine(wire.Error{Code: tc.code, Message: "building the site failed"})
		if !strings.HasSuffix(got, tc.want) {
			t.Errorf("errorLine(%q) = %q, want it to end %q", tc.code, got, tc.want)
		}
		if tc.never != "" && strings.Contains(got, tc.never) {
			t.Errorf("errorLine(%q) = %q, which says %q", tc.code, got, tc.never)
		}
	}
	if got := errorLine(wire.Error{Message: "no code at all"}); got != errorNarration+"no code at all" {
		t.Errorf("an error with no code renders %q", got)
	}
}
