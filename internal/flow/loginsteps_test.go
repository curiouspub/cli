package flow

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// TestTheTwoLoginStepsAreCallableWithoutATerminal is the row the whole
// lift exists for: a caller with no way to ask a question can still get
// a token stored, using the same two calls the machine makes.
//
// IT DRIVES THEM WITH NO PROMPTER AT ALL — LoginDeps.Prompt left nil —
// which is the honest shape of the caller this is for and is also the
// strongest available assertion that nothing on this path asks anybody
// anything. A prompter that recorded and refused would prove the calls
// do not USE the answers; a nil one proves they never reach a prompt.
func TestTheTwoLoginStepsAreCallableWithoutATerminal(t *testing.T) {
	run := newLoginRun(t)
	run.deps.Prompt = nil

	if refusal := LoginStart(t.Context(), run.deps, "someone@example.com"); refusal != nil {
		t.Fatalf("LoginStart refused: %+v", refusal)
	}
	if refusal := LoginVerify(t.Context(), run.deps, VerifyRequest{
		Email: "someone@example.com",
		Code:  "123456",
	}); refusal != nil {
		t.Fatalf("LoginVerify refused: %+v", refusal)
	}

	if got := len(run.script.starts); got != 1 {
		t.Errorf("the server was asked to send %d codes, want 1", got)
	}
	if got := len(run.script.verifies); got != 1 {
		t.Fatalf("the server saw %d verifies, want 1", got)
	}
	if got := run.script.verifies[0].Email; got != "someone@example.com" {
		t.Errorf("the verify carried %q as the address", got)
	}

	// THE TOKEN IS STORED BY THE VERIFY, not by the caller. A step that
	// handed it back and left the writing to whoever called it would
	// work perfectly for the caller that remembered.
	if run.savedToken != ui.Secret(run.script.token) {
		t.Errorf("the stored token is not the one the server issued")
	}
	if run.savedEndpoint != run.deps.Endpoint {
		t.Errorf("the token was stored against %q, want the endpoint that issued it (%q)",
			run.savedEndpoint, run.deps.Endpoint)
	}
}

// TestTheConsentDefaultsToFalseAndTravelsAsGiven.
//
// Nothing about a deploy depends on this field, which is exactly why it
// needs a row: a value nothing reads is one a caller can set wrongly for
// ever without anything going visibly wrong, and what it records is a
// person's answer to a question about email.
//
// THE ZERO VALUE IS ASSERTED AS WELL AS THE SET ONE. A request built
// without naming the field must reach the wire as false — "we did not
// ask, so no" — and only the pair can tell that from a field the server
// never sees.
func TestTheConsentDefaultsToFalseAndTravelsAsGiven(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  VerifyRequest
		want bool
	}{
		{
			name: "not named at all",
			req:  VerifyRequest{Email: "someone@example.com", Code: "123456"},
			want: false,
		},
		{
			name: "asked and answered yes",
			req: VerifyRequest{
				Email: "someone@example.com", Code: "123456", MarketingOptIn: true,
			},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newLoginRun(t)
			if refusal := LoginVerify(t.Context(), run.deps, tc.req); refusal != nil {
				t.Fatalf("LoginVerify refused: %+v", refusal)
			}
			if len(run.script.verifies) != 1 {
				t.Fatalf("the server saw %d verifies, want 1", len(run.script.verifies))
			}
			if got := run.script.verifies[0].MarketingOptIn; got != tc.want {
				t.Errorf("marketing_opt_in reached the wire as %v, want %v", got, tc.want)
			}
		})
	}
}

// TestARefusalSaysWhetherAnotherCodeWouldHelp is the reason these calls
// return a refusal rather than an error.
//
// The distinction is the whole product of the routing table: one failure
// is answered by entering a different code and every other one is
// answered by stopping. A caller that could not tell them apart would
// either loop on a shut door or give up on a typo — and the only thing
// in the answer that carries the difference is this field, because the
// server's refusals are deliberately indistinguishable as prose.
//
// THE TWO HALVES ARE ASSERTED TOGETHER, per row: what a caller should do
// AND what it has to say. A recoverable refusal with no words is a
// caller re-prompting with nothing to show; a stop with no failure is a
// run that ends silently.
//
// REQUIRED MUTATION, run 2026-09-11: route wire.CodeUnauthorized to
// routeStop in errorRouting. Reds the wrong-code row here — recoverable
// false — and reds the machine's own no-restart row next door, which is
// the pair that establishes both surfaces read one table.
func TestARefusalSaysWhetherAnotherCodeWouldHelp(t *testing.T) {
	for _, tc := range []struct {
		name            string
		outcome         outcome
		wantRecoverable bool
		wantCode        wire.ErrorCode
	}{
		{
			// The wrong or expired code, and the one failure another
			// attempt can fix.
			name:            "the code was refused",
			outcome:         fails(http.StatusUnauthorized, wire.CodeUnauthorized, "that code is not valid"),
			wantRecoverable: true,
			wantCode:        wire.CodeUnauthorized,
		},
		{
			// The server malfunctioned, and may not have consumed the
			// code — recoverable in the sense a dropped connection is.
			name:            "the server malfunctioned",
			outcome:         fails(http.StatusInternalServerError, wire.CodeInternal, "something went wrong"),
			wantRecoverable: true,
			wantCode:        wire.CodeInternal,
		},
		{
			// Pace rather than access, and the server is naming a time to
			// come back at — so looping on it is the one thing a client
			// must not do.
			name:            "the pace was refused",
			outcome:         fails(http.StatusTooManyRequests, wire.CodeRateLimited, "slow down").after("60"),
			wantRecoverable: false,
			wantCode:        wire.CodeRateLimited,
		},
		{
			name:            "the door is shut",
			outcome:         fails(http.StatusServiceUnavailable, wire.CodeMaintenance, "back shortly"),
			wantRecoverable: false,
			wantCode:        wire.CodeMaintenance,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newLoginRun(t)
			run.deps.Prompt = nil
			run.deps.Offer = nil
			run.script.verifyOutcomes = []outcome{tc.outcome}

			refusal := LoginVerify(t.Context(), run.deps, VerifyRequest{
				Email: "someone@example.com", Code: "123456",
			})
			if refusal == nil {
				t.Fatal("the call succeeded against a server that refused it")
			}
			if refusal.Recoverable != tc.wantRecoverable {
				t.Errorf("recoverable = %v, want %v", refusal.Recoverable, tc.wantRecoverable)
			}
			if refusal.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", refusal.Code, tc.wantCode)
			}
			if tc.wantRecoverable {
				if refusal.Said != tc.outcome.message {
					t.Errorf("said %q, want the server's own words (%q)",
						refusal.Said, tc.outcome.message)
				}
				if refusal.Failure != nil {
					t.Errorf("a recoverable refusal also carried a failure: %v", refusal.Failure)
				}
				return
			}
			if refusal.Failure == nil {
				t.Fatal("a refusal that ends the run carried no copy to render")
			}
			// THE SERVER'S OWN SENTENCE REACHES THE READER, through the
			// shipped rendering rather than a reconstruction of it. A
			// stop that dropped it would leave an agent with a headline
			// and nothing that knows what happened.
			if !strings.Contains(rendered(refusal.Failure), tc.outcome.message) {
				t.Errorf("the rendered failure does not carry what the server said (%q):\n%s",
					tc.outcome.message, rendered(refusal.Failure))
			}
			if refusal.Said != "" {
				t.Errorf("a stop also carried a message to re-prompt with: %q", refusal.Said)
			}
		})
	}
}

// TestAVerifyThatCouldNotBeStoredIsNotAWrongCode.
//
// A token that arrived and could not be written is the one failure on
// this path that has nothing to do with the code, and routing it as
// recoverable would send a caller back to collect another one — which
// cannot help, and which spends an attempt the server allows.
func TestAVerifyThatCouldNotBeStoredIsNotAWrongCode(t *testing.T) {
	run := newLoginRun(t)
	run.deps.Prompt = nil
	run.saveErr = errors.New("the config directory is read-only")

	refusal := LoginVerify(t.Context(), run.deps, VerifyRequest{
		Email: "someone@example.com", Code: "123456",
	})
	if refusal == nil {
		t.Fatal("a verify whose token could not be stored reported success")
	}
	if refusal.Recoverable {
		t.Error("a token that could not be written was reported as a wrong code")
	}
	if !strings.Contains(rendered(refusal.Failure), "read-only") {
		t.Errorf("the failure does not say why the login could not be saved:\n%s",
			rendered(refusal.Failure))
	}
}

// TestAnEndpointThatCannotBeComparedIsRefusedBeforeAnythingIsSent is the
// check's third door, and it is a row because the other two are reached
// through the machine.
//
// A token has to be stored beside the endpoint that issued it, and an
// endpoint the store cannot canonicalise makes that impossible — so the
// refusal belongs before the request rather than after somebody has
// spent a code on a run that could never have ended in a saved token.
//
// REQUIRED MUTATION, run 2026-09-11: delete the checkEndpoint call from
// LoginStart. Reds here and nowhere else — the machine's own rows reach
// the check through Login, which still makes it. That narrowness is what
// establishes this row as the one covering the second door.
func TestAnEndpointThatCannotBeComparedIsRefusedBeforeAnythingIsSent(t *testing.T) {
	run := newLoginRun(t)
	run.deps.Prompt = nil
	run.deps.Endpoint = "::not a url::"

	refusal := LoginStart(t.Context(), run.deps, "someone@example.com")
	if refusal == nil {
		t.Fatal("an endpoint that cannot be compared was accepted")
	}
	if !errors.Is(refusal.Failure, ErrEndpointUnusable) {
		t.Errorf("failure = %v, want ErrEndpointUnusable", refusal.Failure)
	}
	if len(run.script.starts) != 0 {
		t.Errorf("%d code requests were sent before the endpoint was refused",
			len(run.script.starts))
	}
}

// TestAStoredLoginIsTheSameOneTheDeploySequenceWouldUse.
//
// The claim is that an agent-facing call reads the same configuration
// and accepts a token on the same terms the command does — so the two
// cannot end up disagreeing about whether this machine is logged in.
//
// THE ABSENT CASE IS THE ONE WITH TEETH. A caller told "no login" has to
// be able to act on it without reading a sentence, which is what the
// sentinel is for; and the reason, when there is one, has to survive,
// because "your token was issued against somewhere else" and "there is
// no file yet" lead a person to different places.
func TestAStoredLoginIsTheSameOneTheDeploySequenceWouldUse(t *testing.T) {
	t.Run("a token stored against this endpoint", func(t *testing.T) {
		run := newLoginRun(t)
		run.deps.Save = nil
		run.deps.Prompt = nil
		if refusal := LoginVerify(t.Context(), run.deps, VerifyRequest{
			Email: "someone@example.com", Code: "123456",
		}); refusal != nil {
			t.Fatalf("LoginVerify refused: %+v", refusal)
		}

		login, err := OpenStoredLogin(run.deps.Endpoint)
		if err != nil {
			t.Fatalf("OpenStoredLogin: %v", err)
		}
		if login.Client == nil {
			t.Fatal("a stored login came back with no client to spend it")
		}
	})

	t.Run("nothing stored at all", func(t *testing.T) {
		run := newLoginRun(t)
		_, err := OpenStoredLogin(run.deps.Endpoint)
		if !errors.Is(err, ErrNoLogin) {
			t.Fatalf("err = %v, want ErrNoLogin", err)
		}
	})

	t.Run("a token issued against somewhere else", func(t *testing.T) {
		run := newLoginRun(t)
		run.deps.Save = nil
		run.deps.Prompt = nil
		if refusal := LoginVerify(t.Context(), run.deps, VerifyRequest{
			Email: "someone@example.com", Code: "123456",
		}); refusal != nil {
			t.Fatalf("LoginVerify refused: %+v", refusal)
		}

		// A DIFFERENT ENDPOINT, and a loopback one so the client would
		// have accepted it — the refusal has to come from the token
		// belonging elsewhere rather than from the address being
		// unusable.
		_, err := OpenStoredLogin("http://127.0.0.1:1")
		if !errors.Is(err, ErrNoLogin) {
			t.Fatalf("err = %v, want ErrNoLogin", err)
		}
		// The reason survives, because where the token DOES belong is
		// the only actionable half of this.
		if !strings.Contains(err.Error(), run.deps.Endpoint) {
			t.Errorf("the refusal does not say where the stored token belongs:\n%v", err)
		}
	})
}
