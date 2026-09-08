package flow

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The real HTTP client must satisfy the seam this gate asks for.
var _ CapacityAPI = (*api.Client)(nil)

// gateRun is one configured run: the terminal double, the scripted
// server, and the dependencies the gate is called with.
type gateRun struct {
	prompt *scriptedPrompt
	script *capacityScript
	deps   CapacityDeps
	srv    *httptest.Server
}

// newGateRun wires a run against a real server and a real client, with
// the door open and plenty of room — so a row states only the thing it
// is about.
func newGateRun(t *testing.T) *gateRun {
	t.Helper()

	script := &capacityScript{
		body: wire.CapacityResponse{Open: true, AccountsLeft: 200},
	}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)
	watchForStrays(t, script)

	client, err := api.New(srv.URL)
	if err != nil {
		t.Fatalf("building the client against %s: %v", srv.URL, err)
	}

	run := &gateRun{prompt: &scriptedPrompt{}, script: script, srv: srv}
	run.deps = CapacityDeps{
		Prompt: run.prompt,
		API:    client,
		Now:    func() time.Time { return fixedNowLocal },
	}
	return run
}

// closed shuts the door, with a reset the given distance away.
func (r *gateRun) closed(in time.Duration) *gateRun {
	r.script.body = wire.CapacityResponse{Open: false, ResetsAt: fixedNowLocal.Add(in)}
	return r
}

func (r *gateRun) run(t *testing.T) error {
	t.Helper()
	return CapacityGate(t.Context(), r.deps)
}

// output is everything the run put in front of a person.
func (r *gateRun) output() string { return r.prompt.out.String() }

// seen is the narration and the stop together — everything the run put
// on screen, in the order a person met it.
func (r *gateRun) seen(err error) string { return r.output() + "\n" + rendered(err) }

// host is the authority the client was pointed at, for the row that
// asserts an unreachable server is named.
func (r *gateRun) host(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(r.srv.URL)
	if err != nil {
		t.Fatalf("parsing the double's own URL: %v", err)
	}
	return u.Host
}

// -------------------------------------------------------------------
// When the gate runs at all.
// -------------------------------------------------------------------

// TestTheGateIsSkippedWhenARunAlreadyHoldsAToken is the first row
// because it is the one an obvious reading gets wrong.
//
// The daily cap counts new ACCOUNTS, and an account is spent at the
// login step. A run holding a usable token spends none of it, so gating
// that run would be refusing work that can land — and it would spend an
// unauthenticated request to reach the wrong answer.
//
// REQUIRED MUTATION, RUN: delete the `if deps.HaveToken { return nil }`
// block at the top of CapacityGate.
func TestTheGateIsSkippedWhenARunAlreadyHoldsAToken(t *testing.T) {
	// The positive control comes first. Without it, "no request was
	// made" is satisfied by a gate that never calls anything at all.
	t.Run("positive control: with no token stored the check is made", func(t *testing.T) {
		run := newGateRun(t)
		run.deps.HaveToken = false

		if err := run.run(t); err != nil {
			t.Fatalf("an open door ended the run with %v, want a continue", err)
		}
		if got := run.script.checks(); got != 1 {
			t.Fatalf("the check ran %d times with no token stored, want exactly 1 — "+
				"the row below proves nothing if the gate never asks anything", got)
		}
	})

	run := newGateRun(t)
	run.deps.HaveToken = true

	if err := run.run(t); err != nil {
		t.Fatalf("a run holding a token was stopped with %v, want a continue", err)
	}
	if got := run.script.checks(); got != 0 {
		t.Errorf("the check ran %d times for a run that spends no account capacity, "+
			"want 0", got)
	}
	if got := run.output(); got != "" {
		t.Errorf("a skipped gate said %q, want nothing at all", got)
	}
	if got := len(run.prompt.confirmAsks); got != 0 {
		t.Errorf("a skipped gate asked %d questions, want 0", got)
	}
}

// -------------------------------------------------------------------
// The open door.
// -------------------------------------------------------------------

// TestAnOpenDoorContinuesWithoutSayingAnything. A step that succeeds
// silently is a step nobody has to read.
//
// REQUIRED MUTATION, RUN: print lowCapacityLine unconditionally on the
// open path.
func TestAnOpenDoorContinuesWithoutSayingAnything(t *testing.T) {
	run := newGateRun(t)

	if err := run.run(t); err != nil {
		t.Fatalf("an open door ended the run with %v, want a continue", err)
	}
	if got := run.script.checks(); got != 1 {
		t.Errorf("the check ran %d times, want exactly 1", got)
	}
	if got := run.output(); got != "" {
		t.Errorf("an open door printed %q, want nothing", got)
	}
	if got := len(run.prompt.confirmAsks) + len(run.prompt.emailAsks); got != 0 {
		t.Errorf("an open door asked %d questions, want 0", got)
	}
	if got := run.script.joined(); len(got) != 0 {
		t.Errorf("an open door signed the person up to the waitlist: %v", got)
	}
}

// TestOnlyALowCountIsWorthALine pins the threshold as a NUMBER rather
// than as a feeling, and it pins both sides of it: a count above the
// line says nothing, a count on it says something. One direction alone
// passes for a gate that never speaks, and the other for one that always
// does.
//
// The zero row is the contradiction case — a door reported open with
// nothing left. Rendering that count would put a number in front of
// somebody that the very next step disproves.
//
// REQUIRED MUTATION, RUN, two of them, because the boundary and the
// contradiction are different edges of one condition. Change lowCapacity
// from 10 to 11: the "11 left" row reds. Change the open path's
// `resp.AccountsLeft > 0` to `>= 0`: the "0 left" row reds.
func TestOnlyALowCountIsWorthALine(t *testing.T) {
	for _, row := range []struct {
		name string
		left int
		want string
	}{
		{"a contradiction says nothing", 0, ""},
		{"the last one", 1, "only 1 trial slot left"},
		{"on the line", 10, "only 10 trial slots left"},
		{"one above the line", 11, ""},
		{"plenty", 200, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			run := newGateRun(t)
			run.script.body = wire.CapacityResponse{Open: true, AccountsLeft: row.left}

			if err := run.run(t); err != nil {
				t.Fatalf("an open door ended the run with %v, want a continue", err)
			}
			got := run.output()
			if row.want == "" {
				if got != "" {
					t.Errorf("%d left printed %q, want nothing", row.left, got)
				}
				return
			}
			if !strings.Contains(got, row.want) {
				t.Errorf("%d left printed %q, want it to contain %q",
					row.left, got, row.want)
			}
		})
	}
}

// -------------------------------------------------------------------
// The shut door, end to end.
// -------------------------------------------------------------------

// TestAShutDoorAnnouncesItselfBeforeItAsksAnything. A question printed
// without its reason is a question nobody could answer, and the order is
// the whole of that: what happened, when it changes, then the offer.
//
// THE ORDER IS THE SUBJECT, so it is read off the terminal's own
// transcript rather than off its output buffer. The first version of
// this row compared the buffer, and the mutation it was written for —
// the announcement moved to AFTER the question — came back green: the
// line is still printed, the buffer still contains it, and a question
// recorded in a slice leaves no mark in a buffer to be after.
//
// REQUIRED MUTATION, RUN: move the announceClosed line in offerWaitlist
// to after the Confirm.
func TestAShutDoorAnnouncesItselfBeforeItAsksAnything(t *testing.T) {
	run := newGateRun(t).closed(4 * time.Hour)
	run.prompt.confirms = []answer{no()}

	err := run.run(t)
	if err == nil {
		t.Fatal("a shut door let the run continue")
	}

	said := firstMentioning(run.prompt.transcript, closedHeadline)
	asked := firstMentioning(run.prompt.transcript, waitlistQuestion)
	if said < 0 {
		t.Fatalf("the door was never announced at all:\n%v", run.prompt.transcript)
	}
	if asked < 0 {
		t.Fatalf("the offer was never put:\n%v", run.prompt.transcript)
	}
	if said > asked {
		t.Errorf("the question was put before the reason for it:\n%v",
			run.prompt.transcript)
	}

	narration := run.output()
	for _, want := range []string{"17:00 CET (+01:00)", "in about 4 hours"} {
		if !strings.Contains(narration, want) {
			t.Errorf("the announcement never said %q:\n%s", want, narration)
		}
	}
	if strings.Contains(narration, "16:00") {
		t.Errorf("the reset was rendered in UTC rather than in the zone the clock "+
			"carries:\n%s", narration)
	}
}

// firstMentioning reports where needle first appears in a transcript,
// or -1. A caller that treats -1 as "before everything" would turn a
// missing line into a passing order, so every caller here checks it.
func firstMentioning(transcript []string, needle string) int {
	for i, entry := range transcript {
		if strings.Contains(entry, needle) {
			return i
		}
	}
	return -1
}

// TestJoiningSendsExactlyOneSignupCarryingTheAddressTheGateCollected.
//
// The gate has no address of its own — it runs before any login — so it
// prompts, and it prompts only once and only after the person has said
// yes. Asking for an address before saying why is how a program collects
// one from somebody who would not have given it.
//
// Exactly one signup is assertable here and only here: the call is
// documented idempotent, so a failing one is retried, and a row about a
// FAILED signup can only count attempts.
//
// The ORDER — nothing is asked for until the offer has been accepted —
// is not asserted here, because this row's terminal answers yes and so
// cannot tell the two orders apart. The row below is where that lives.
//
// REQUIRED MUTATION, RUN: send wire.WaitlistRequest{} rather than the
// address the run collected.
func TestJoiningSendsExactlyOneSignupCarryingTheAddressTheGateCollected(t *testing.T) {
	run := newGateRun(t).closed(4 * time.Hour)
	run.prompt.confirms = []answer{yes()}
	run.prompt.emails = []answer{says("someone@example.com")}

	err := run.run(t)
	if err == nil {
		t.Fatal("a shut door let the run continue")
	}

	joined := run.script.joined()
	if len(joined) != 1 {
		t.Fatalf("the run made %d signups, want exactly 1: %v", len(joined), joined)
	}
	if joined[0].Email != "someone@example.com" {
		t.Errorf("the signup carried %q, want the address the run collected",
			joined[0].Email)
	}
	if got := len(run.prompt.emailAsks); got != 1 {
		t.Errorf("the address was asked for %d times, want exactly 1", got)
	}

	if got := run.prompt.confirmAsks; len(got) != 1 || got[0].question != waitlistQuestion {
		t.Errorf("the offer was put as %v, want %q exactly once", got, waitlistQuestion)
	}
}

// TestDecliningTheOfferAsksForNoAddressAtAll. A person who has just said
// they do not want an email is not then asked for an email address.
//
// IT IS ALSO WHERE THE ORDER IS PINNED, which is why it is a row of its
// own rather than three lines inside the one above. Asking for an
// address before saying what it is for is how a program collects one
// from somebody who would not have given it — and a terminal that
// answers yes cannot tell the two orders apart, because both end with
// one address collected and one signup sent. Only a decline can.
//
// REQUIRED MUTATION, RUN: collect the address before the Confirm in
// offerWaitlist.
func TestDecliningTheOfferAsksForNoAddressAtAll(t *testing.T) {
	run := newGateRun(t).closed(4 * time.Hour)
	run.prompt.confirms = []answer{no()}

	if err := run.run(t); err == nil {
		t.Fatal("a shut door let the run continue")
	}
	if got := len(run.prompt.emailAsks); got != 0 {
		t.Errorf("a declined offer still asked for an address %d times", got)
	}
	if got := run.script.joined(); len(got) != 0 {
		t.Errorf("a declined offer still signed the person up: %v", got)
	}
}

// -------------------------------------------------------------------
// What a shut door COSTS.
// -------------------------------------------------------------------

// TestJoiningCostsExactlyWhatDecliningCosts. Whether or not somebody
// joins a waitlist, the deploy did not happen — and a command that exits
// 0 having done nothing lies to whatever script ran it. Declining the
// OFFER is not declining the deploy; the deploy was declined by the
// service.
//
// The two runs are compared on the number AND on the copy, so a mutation
// that keeps the cost and loses the words is still visible.
//
// REQUIRED MUTATION, RUN, two of them. Return offerWaitlist's error from
// CapacityGate without the ui.ServerClosed mark: both halves red at the
// exit code. Return nil rather than joinedStop after a successful
// signup: the joined half reds at the headline.
func TestJoiningCostsExactlyWhatDecliningCosts(t *testing.T) {
	joinedRun := newGateRun(t).closed(4 * time.Hour)
	joinedRun.prompt.confirms = []answer{yes()}
	joinedRun.prompt.emails = []answer{says("someone@example.com")}
	joinedErr := joinedRun.run(t)

	declinedRun := newGateRun(t).closed(4 * time.Hour)
	declinedRun.prompt.confirms = []answer{no()}
	declinedErr := declinedRun.run(t)

	for _, row := range []struct {
		name string
		err  error
		what string
	}{
		{"joined", joinedErr, "You're on the list."},
		{"declined", declinedErr, nothingDeployed},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.err == nil {
				t.Fatal("the run continued past a shut door")
			}
			if !errors.Is(row.err, ui.ErrServerClosed) {
				t.Error("the stop carries no closed-door mark, so it costs the same " +
					"as a broken project")
			}
			if code := exitCodeFor(t, row.err); code != ui.ExitServerClosed {
				t.Errorf("exit code %d, want %d", code, ui.ExitServerClosed)
			}
			var f *ui.Failure
			if !errors.As(row.err, &f) {
				t.Fatalf("the stop is a %T, which the program renders as an internal "+
					"fault", row.err)
			}
			if f.What != row.what {
				t.Errorf("the stop is headed %q, want %q", f.What, row.what)
			}
		})
	}

	// The reset time is what makes the decline actionable: a stop that
	// says "come back later" without saying when is a stop nobody can
	// plan around.
	if !strings.Contains(rendered(declinedErr), "17:00 CET (+01:00)") {
		t.Errorf("the decline never named the reset time:\n%s", rendered(declinedErr))
	}
}

// TestAShutDoorWithNobodyToAskExitsThreeAndNamesTheTerminal is the row
// the whole non-interactive path exists for, and the trap it avoids is
// already wired.
//
// ui.ExitCode matches ui.ErrNotInteractive BEFORE it reaches the
// closed-door branch, so a stop carrying that sentinel anywhere in its
// chain costs 1 and prints a generic message about needing a terminal —
// not the third code, and not a word about capacity. The run stopped
// because the door is shut; there being nobody to ask is a fact about
// the message rather than the reason for the stop.
//
// TWO POSITIVE CONTROLS, because the number 3 alone proves little. The
// same shut run WITH a terminal also exits 3, so this row cannot pass on
// a code that arrives for a different reason; and a run with no terminal
// against an OPEN door continues, so it cannot pass against a gate that
// refuses everything the moment stdin is a pipe.
//
// REQUIRED MUTATION, RUN: in offerWaitlist, return the confirm's own
// error rather than unaskableStop(err, ...).
func TestAShutDoorWithNobodyToAskExitsThreeAndNamesTheTerminal(t *testing.T) {
	t.Run("positive control: with a terminal, the same shut door exits three", func(t *testing.T) {
		run := newGateRun(t).closed(4 * time.Hour)
		run.prompt.confirms = []answer{no()}

		err := run.run(t)
		if code := exitCodeFor(t, err); code != ui.ExitServerClosed {
			t.Fatalf("exit code %d, want %d — the row below proves nothing if the "+
				"shut door does not cost this on the ordinary path",
				code, ui.ExitServerClosed)
		}
	})

	t.Run("positive control: with no terminal, an open door still continues", func(t *testing.T) {
		run := newGateRun(t)
		run.prompt.notInteractive = true

		if err := run.run(t); err != nil {
			t.Fatalf("an open door with nobody to ask ended the run with %v, want a "+
				"continue — the row below proves nothing if a pipe stops everything", err)
		}
	})

	run := newGateRun(t).closed(4 * time.Hour)
	run.prompt.notInteractive = true

	err := run.run(t)
	if err == nil {
		t.Fatal("a shut door with nobody to ask let the run continue")
	}
	if errors.Is(err, ui.ErrNotInteractive) {
		t.Fatal("the stop carries the no-terminal sentinel, which ui.ExitCode " +
			"matches before the closed-door branch — so this run costs 1 and says " +
			"nothing about capacity")
	}
	if !errors.Is(err, ui.ErrServerClosed) {
		t.Error("the stop carries no closed-door mark")
	}
	if code := exitCodeFor(t, err); code != ui.ExitServerClosed {
		t.Errorf("exit code %d, want %d", code, ui.ExitServerClosed)
	}
	if got := len(run.script.joined()); got != 0 {
		t.Errorf("a run with nobody to ask signed the person up %d times — an "+
			"unanswered offer is not a yes", got)
	}

	seen := run.seen(err)
	for _, want := range []string{"terminal", "join the waitlist", "17:00 CET (+01:00)"} {
		if !strings.Contains(seen, want) {
			t.Errorf("the stop never said %q:\n%s", want, seen)
		}
	}
	// The generic copy is what a leaked sentinel produces. Naming it
	// here means the row fails with the reason rather than with a
	// missing substring.
	if strings.Contains(seen, "curious needs a terminal for that.") {
		t.Errorf("the run rendered the program's generic no-terminal copy, which "+
			"means the sentinel reached ui.ExitCode:\n%s", seen)
	}
}

// -------------------------------------------------------------------
// A check that could not be made.
// -------------------------------------------------------------------

// TestTheKillSwitchIsAShutDoorAndNothingRunsAfterIt. The same condition
// must not cost different numbers depending on which step met it, and
// the login flow already treats the kill switch as a shut door.
//
// The "nothing ran after it" half is asserted through a sentinel the
// caller would have reached, rather than through the absence of a
// request — absence is also what a gate that never got started looks
// like.
//
// REQUIRED MUTATION, RUN: drop the maintenance branch from
// capacityCheckFailure so the kill switch falls through to the generic
// stop.
func TestTheKillSwitchIsAShutDoorAndNothingRunsAfterIt(t *testing.T) {
	// The positive control: a different server fault at the same status
	// costs the ordinary code, so this row cannot pass against a gate
	// that marks every failure as a shut door.
	t.Run("positive control: a server fault is not a shut door", func(t *testing.T) {
		run := newGateRun(t)
		run.script.capacityOutcome = fails(http.StatusInternalServerError,
			wire.CodeInternal, "Something went wrong here.")

		err := run.run(t)
		if err == nil {
			t.Fatal("a server fault let the run continue")
		}
		if code := exitCodeFor(t, err); code != 1 {
			t.Fatalf("a server fault exits %d, want 1 — a retry loop must not be "+
				"told to come back after a reset that has nothing to do with it", code)
		}
	})

	const serverMessage = "curious.pub is down for maintenance. Back shortly."

	run := newGateRun(t)
	run.script.capacityOutcome = fails(http.StatusServiceUnavailable,
		wire.CodeMaintenance, serverMessage)

	// The sequence this gate sits at the front of, written out: nothing
	// after it runs unless it returns nil. Asserting the absence of a
	// request instead would also be satisfied by a gate that never
	// started.
	loginRan := false
	sequence := func() error {
		if err := run.run(t); err != nil {
			return err
		}
		loginRan = true // the login, and then the pack
		return nil
	}

	err := sequence()
	if loginRan {
		t.Fatal("the kill switch let the run continue into a login and a pack")
	}
	if err == nil {
		t.Fatal("the kill switch ended the run at success")
	}
	if code := exitCodeFor(t, err); code != ui.ExitServerClosed {
		t.Errorf("the kill switch exits %d, want %d — the same condition costs "+
			"the same on both paths", code, ui.ExitServerClosed)
	}
	if got := rendered(err); !strings.Contains(got, serverMessage) {
		t.Errorf("the server's message was not rendered verbatim:\n%s", got)
	}
	if got := len(run.prompt.confirmAsks) + len(run.prompt.emailAsks); got != 0 {
		t.Errorf("the kill switch asked %d questions, want 0", got)
	}
	if got := len(run.script.joined()); got != 0 {
		t.Errorf("the kill switch signed the person up %d times", got)
	}
	// No reset time is invented. The kill switch carries none that
	// anybody can honestly name.
	if strings.Contains(rendered(err), "CET") {
		t.Errorf("the kill switch named a reopening time it was never given:\n%s",
			rendered(err))
	}
}

// TestAnUnreachableServerNamesTheHostItTried. A person who mis-set an
// endpoint and a person whose connection died read the same sentence,
// and the host is the only thing in it that tells them apart.
//
// REQUIRED MUTATION, RUN: replace err.Error() in capacityCheckFailure's
// transport branch with a fixed sentence.
func TestAnUnreachableServerNamesTheHostItTried(t *testing.T) {
	run := newGateRun(t)
	host := run.host(t)
	// Closing the double is what makes the address unreachable, and it
	// is a real refusal from the operating system rather than a
	// simulated one.
	run.srv.Close()

	err := run.run(t)
	if err == nil {
		t.Fatal("an unreachable server let the run continue — a login and a pack " +
			"would follow, which is what this step exists to prevent")
	}
	got := rendered(err)
	if !strings.Contains(got, host) {
		t.Errorf("the stop never named the host it tried (%s):\n%s", host, got)
	}
	if code := exitCodeFor(t, err); code != 1 {
		t.Errorf("an unreachable server exits %d, want 1 — nothing about it says "+
			"come back after a reset", code)
	}
	if got := len(run.prompt.confirmAsks); got != 0 {
		t.Errorf("an unreachable server asked %d questions, want 0", got)
	}
}
