package flow

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The three action corrections the failure catalog adjudicated, each
// with a row at its caller.
//
// # Why these rows exist, stated once for all three
//
// Every one of these was a failure whose WORDS said one thing and whose
// ACTION said another. None of them was a wording problem: the copy was
// right in all three cases and had been for months. What was wrong was
// the machine-readable half — the part a terminal reader never sees and
// an agent acts on.
//
// THE ROWS ARE AT THE CALLERS, not at the constructors, because that is
// where the pairing is decided. A row asserting that GiveUp means GiveUp
// would pass forever without saying anything about whether this refusal
// carries it.
//
// AND ALL THREE CORRECTIONS PASSED THE SUITE BEFORE THESE ROWS EXISTED,
// which is the fact worth keeping. Three actions changed and nothing
// reddened, because nothing anywhere asserted what action a failure
// carried. The catalog's general validator now requires an action to be
// one of the four; these rows require it to be the RIGHT one.

// TestARefusalInsideTheWindowSaysGiveUp is adjudicated correction #1.
//
// The store answers one refusal for a link whose window closed and for a
// body whose length does not match the signature. Inside the window it
// can only be the second, which is a fault that reproduces forever — and
// the copy has always said so, asking for a report and the version
// rather than a retry. The action said FreshDeploy, telling a reader to
// do the one thing the sentence above it explains cannot help.
func TestARefusalInsideTheWindowSaysGiveUp(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	// The window is still open, which is what makes the diagnosis a
	// mismatch rather than an expiry.
	id, action, why, next := refusalCopy("store.example", now.Add(time.Hour), now)

	if id != ui.IDUploadSignatureMismatch {
		t.Errorf("id = %q, want %q — a mismatch inside the window is not the "+
			"family for a refusal nobody could diagnose", id, ui.IDUploadSignatureMismatch)
	}
	if action != ui.NextGiveUp {
		t.Errorf("action = %q, want %q. Retrying cannot help: the same archive "+
			"produces the same signature and the same refusal, forever",
			action, ui.NextGiveUp)
	}
	// THE COPY IS UNCHANGED, asserted rather than assumed. The
	// correction was to the action alone, and a row that let the words
	// drift while checking the action would be the same defect facing
	// the other way.
	if !strings.Contains(next, "Please report this") || !strings.Contains(next, "curious version") {
		t.Errorf("the next-step copy changed; it should ask for a report and the "+
			"version and nothing else:\n%s", next)
	}
	if !strings.Contains(why, "a fault in curious") {
		t.Errorf("the why no longer names this as our fault:\n%s", why)
	}
}

// TestTheOtherTwoRefusalBranchesKeepTheirOwnFamilies is the control for
// the row above: a correction that collapsed all three branches onto one
// family would satisfy it and be wrong.
func TestTheOtherTwoRefusalBranchesKeepTheirOwnFamilies(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	// No announced window: the server does not say when a link stops
	// working, so this client cannot tell which refusal it met.
	id, action, _, _ := refusalCopy("store.example", time.Time{}, now)
	if id != ui.IDUploadRefusedUnexplained || action != ui.NextFreshDeploy {
		t.Errorf("unknown-expiry branch = (%q, %q), want (%q, %q)",
			id, action, ui.IDUploadRefusedUnexplained, ui.NextFreshDeploy)
	}

	// The window has closed: a fresh link is issued every run.
	id, action, _, _ = refusalCopy("store.example", now.Add(-time.Hour), now)
	if id != ui.IDUploadLinkExpired || action != ui.NextFreshDeploy {
		t.Errorf("expired branch = (%q, %q), want (%q, %q)",
			id, action, ui.IDUploadLinkExpired, ui.NextFreshDeploy)
	}
}

// TestRateLimitingWaitsEverywhere is adjudicated correction #2.
//
// The contract promises a time for this code — wire.CarriesRetryAfter
// says so — and the copy has always rendered it. An action of GiveUp,
// which means something outside this run must change first, contradicted
// a sentence naming the minute the run may be repeated.
//
// THE ROW IS BOTH CALLERS, because the defect was that two of the three
// disagreed with the third: publish already waited.
func TestRateLimitingWaitsEverywhere(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	apiErr := &api.APIError{Code: wire.CodeRateLimited,
		Message: "slow down", RetryAfter: 90 * time.Second}

	if !wire.CarriesRetryAfter(wire.CodeRateLimited) {
		t.Fatal("the contract no longer promises a retry time for this code, " +
			"which is the whole reason this action is Wait")
	}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"at the create", createStopFailure(apiErr, now)},
		{"at the login", stopFailure(t.Context(), apiErr,
			LoginDeps{Endpoint: "https://api.example"}, "someone@example.com", now)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f *ui.Failure
			if !errors.As(tc.err, &f) {
				t.Fatalf("the stop is a %T", tc.err)
			}
			if f.ID != ui.IDRateLimited {
				t.Errorf("id = %q, want %q", f.ID, ui.IDRateLimited)
			}
			if f.Next != ui.NextWait {
				t.Errorf("action = %q, want %q — the server named the minute this "+
					"may be tried again, and GiveUp means it never can be",
					f.Next, ui.NextWait)
			}
		})
	}
}

// TestAnAnswerThisBuildCannotReadWaits is adjudicated correction #3.
//
// A code this binary predates is the additive contract meeting an older
// client. Three of the five sites told the reader to run the deploy
// again; the other two told them to wait. **A fresh run cannot fix an
// answer the client cannot read** — the same server will send the same
// unrecognised code to the same build — so the action is Wait and the
// copy names the likely cause, which is that this build is older than
// the server.
//
// One family across five sites, because the stage that met the answer is
// not a family: the diagnosis and the remedy are identical at all five.
func TestAnAnswerThisBuildCannotReadWaits(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	// A code no routing table states. The contract is additive, so the
	// server is entitled to introduce one, and that is exactly the
	// condition this family is about.
	unknown := &api.APIError{Code: wire.ErrorCode("teapot"), Message: "I am a teapot"}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"at the publish", publishStopFailure(unknown, "dpl-abc", now)},
		{"at the start", startFailure(unknown)},
		{"at the stream", streamRefusedFailure(unknown)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f *ui.Failure
			if !errors.As(tc.err, &f) {
				t.Fatalf("the stop is a %T", tc.err)
			}
			if f.ID != ui.IDServerAnswerUnrecognised {
				t.Errorf("id = %q, want %q — one family, because the stage that met "+
					"the answer is not a diagnosis", f.ID, ui.IDServerAnswerUnrecognised)
			}
			if f.Next != ui.NextWait {
				t.Errorf("action = %q, want %q. A fresh run sends the same request "+
					"from the same build to the same server, and gets the same "+
					"answer it could not read", f.Next, ui.NextWait)
			}
			// AND THE COPY CARRIES THE LIKELY CAUSE, because "try again"
			// with no reason is advice a reader cannot act on twice.
			if !strings.Contains(f.NextText, "older than the server") {
				t.Errorf("the copy does not name the likely cause:\n%s", f.NextText)
			}
		})
	}
}
