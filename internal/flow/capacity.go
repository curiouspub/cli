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

// CapacityAPI is what the gate itself needs: the check, and the call the
// offer makes when the answer is no.
type CapacityAPI interface {
	WaitlistJoiner
	Capacity(ctx context.Context) (*wire.CapacityResponse, error)
}

// CapacityDeps is everything CapacityGate needs from outside itself.
type CapacityDeps struct {
	// Prompt is the terminal. Required.
	Prompt CapacityPrompter

	// API is the client. Required.
	API CapacityAPI

	// HaveToken reports whether this run already holds a usable token —
	// that is, whether a login is going to be needed at all.
	//
	// It is an ARGUMENT rather than something this gate looks up, and
	// the reason is that the answer has already been worked out by the
	// time anything gets here: the deploy sequence reads the stored
	// configuration before it makes any network call, and a second read
	// would be a second chance to disagree with the first about which
	// file was read and what was in it.
	//
	// Its zero value runs the gate, which is the safe direction: a
	// caller that forgets to set it spends one unauthenticated request
	// and gets a correct answer, where the opposite default would skip
	// the check for everybody.
	HaveToken bool

	// Now is the clock. Optional; it is also where the RENDERING ZONE
	// comes from — see reopensPhrase, which shows a reset time in the
	// location the clock's own value carries. time.Now returns a time in
	// the machine's local zone, so production renders local without this
	// package ever naming a zone.
	Now func() time.Time
}

// lowCapacity is the point at which what is left is worth a line.
//
// Ten against a cap of three hundred: the point past which somebody
// starting a deploy now could plausibly lose the race to somebody
// starting one a minute later. It is a stated number rather than a felt
// one so that it can be argued with; revisit it on a real report rather
// than by picking again.
const lowCapacity = 10

// CapacityGate is the step that runs immediately before the login it
// gates: it asks whether curious.pub has room for a new account today,
// and when the answer is no it offers the waitlist and stops the run.
//
// It returns nil to mean CONTINUE. Every other ending is an error
// carrying the copy a person reads, and the closed-door endings are
// marked with ui.ServerClosed so the run costs ui.ExitServerClosed.
//
// # Why it runs here and not earlier
//
// The daily cap counts new ACCOUNTS, and an account is spent at the
// login step. So the gate is worth running exactly when a login is going
// to be needed — with a usable token already stored, a run spends none
// of the day's capacity, and stopping it would be refusing work that can
// land. That is what HaveToken decides, and it decides it before any
// request is made.
//
// # Why it does not "assume open and continue"
//
// A check that cannot be made STOPS the run. The alternative walks
// somebody through a login and a pack that may have nowhere to go, which
// is the one thing this step exists to prevent — so a network failure is
// an ending here rather than a shrug.
func CapacityGate(ctx context.Context, deps CapacityDeps) error {
	if deps.HaveToken {
		// Not a cheaper path to the same answer — a DIFFERENT question,
		// already answered. Nothing is printed, because a step that
		// decides it has nothing to do is a step the user never has to
		// read about.
		return nil
	}

	now := deps.Now
	if now == nil {
		now = time.Now
	}

	resp, err := deps.API.Capacity(ctx)
	if err != nil {
		return capacityCheckFailure(err)
	}

	if resp.Open {
		// AT MOST ONE LINE, and only when it says something the reader
		// can act on. A count that is not low is noise, and a count of
		// zero alongside an open door is the server contradicting
		// itself — rendering it would put a number in front of somebody
		// that the very next step disproves.
		if resp.AccountsLeft > 0 && resp.AccountsLeft <= lowCapacity {
			deps.Prompt.Step("%s", lowCapacityLine(resp.AccountsLeft))
		}
		return nil
	}

	// THE MARK IS MADE HERE AND THE WORDS ARE MADE BELOW. offerWaitlist
	// decides what the reader is told; that the run is over, and what it
	// costs, is this function's.
	return ui.ServerClosed(offerWaitlist(ctx, waitlistDeps{
		prompt: deps.Prompt,
		join:   deps.API,
		now:    now,
	}, promptedAddress(deps.Prompt), resp.ResetsAt))
}

// lowCapacityLine is the one line the open path may print.
func lowCapacityLine(left int) string {
	return fmt.Sprintf("Heads up: only %s left today, and it's first-come.",
		countOf(left, "trial slot"))
}

// capacityCheckFailure is what a check that could not be made ends as.
//
// THE THREE OUTCOMES DO NOT COST THE SAME, and that is a decision rather
// than an oversight. The kill switch is a closed door in exactly the
// sense the login flow already treats it as one, so it costs the
// closed-door code — the same condition must not cost different numbers
// depending on which step met it. A network failure or a server fault is
// something being wrong, and a script that retried it must not be told
// to come back after a reset that has nothing to do with the problem.
func capacityCheckFailure(err error) error {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		// A transport failure: no envelope, no code, nothing the server
		// said. The message already names the host it was talking to,
		// which is the part that tells a person whether they mis-set an
		// endpoint or lost their connection.
		return ui.NewFailure(
			ui.IDCapacityCheckUnreachable,
			"curious couldn't ask whether there is room today.",
			"That check runs before anything is uploaded, so the "+
				"run stopped here rather than walking you through a login and a "+
				"pack that might have nowhere to land.", ui.NextFreshDeploy,
			"Check your connection and run `curious deploy` again. "+uploadedNothing).Quoting(err.Error())
	}

	if apiErr.Code == wire.CodeMaintenance {
		// The server's message, verbatim, and NO retry time — the kill
		// switch has no reset anybody can honestly name.
		return ui.ServerClosed(ui.Quoted(
			ui.IDServiceUnavailable,
			"curious.pub is not taking deploys right now.",
			apiErr.Message, ui.NextWait,
			"Try again a little later. "+uploadedNothing))
	}

	return ui.Quoted(
		ui.IDCapacityCheckFailed,
		"curious couldn't ask whether there is room today.",
		apiErr.Message, ui.NextWait,
		"Try again in a moment. "+uploadedNothing)
}
