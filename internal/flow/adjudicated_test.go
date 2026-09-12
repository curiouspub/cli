package flow

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/check"
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

// TestRequestRejectionsKeepTheirApprovedFamilies is the family split
// adjudicated after the first catalog pass.
//
// The second refusal after a fresh login has a materially different recovery
// contract from a bad request: it asks for a clock check and a fresh deploy.
// The create and login bad-request sites both say retrying cannot help and keep
// one family even though they are observed at different stages. Every authored
// byte is asserted here because the ruling changes only the first site's id.
func TestRequestRejectionsKeepTheirApprovedFamilies(t *testing.T) {
	apiMessage := "the server's exact refusal"
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name     string
		err      error
		wantID   ui.FailureID
		wantNext ui.NextAction
		wantWhat string
		wantWhy  string
		wantText string
	}{
		{
			name: "fresh login refused at create",
			err: createStopFailure(&api.APIError{
				Code: wire.CodeUnauthorized, Message: apiMessage,
			}, now),
			wantID:   ui.IDFreshLoginRefused,
			wantNext: ui.NextFreshDeploy,
			wantWhat: authenticationFailed,
			wantWhy: "curious logged in again and the server still would not " +
				"accept the\nrequest, so it stopped rather than keep asking.",
			wantText: "Check that this machine's clock is right, then run `curious deploy`\n" +
				"again. If it keeps happening, please get in touch. " + uploadedNothing,
		},
		{
			name: "bad request at create",
			err: createStopFailure(&api.APIError{
				Code: wire.CodeBadRequest, Message: apiMessage,
			}, now),
			wantID:   ui.IDClientRequestRejected,
			wantNext: ui.NextGiveUp,
			wantWhat: "The server wouldn't accept that archive.",
			wantWhy: "Sending the same thing again would not go any better, " +
				"so the run\nstopped here.",
			wantText: "Check that you are running a current version — `curious version` says\n" +
				"which one — and please report this if it keeps happening. " + uploadedNothing,
		},
		{
			name: "bad request at login",
			err: stopFailure(t.Context(), &api.APIError{
				Code: wire.CodeBadRequest, Message: apiMessage,
			}, LoginDeps{Endpoint: "https://api.example"}, "someone@example.com", now),
			wantID:   ui.IDClientRequestRejected,
			wantNext: ui.NextGiveUp,
			wantWhat: "curious sent something this server wouldn't accept.",
			wantWhy: "Sending it again would not go any better, so the run " +
				"stopped here rather than spending another of your attempts.",
			wantText: "Check that you are running a current version — `curious version` says\n" +
				"which one — and please report this if it keeps happening.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *ui.Failure
			if !errors.As(tc.err, &got) {
				t.Fatalf("stop returned %T, want a ui.Failure in the chain", tc.err)
			}
			if got.ID != tc.wantID || got.Next != tc.wantNext || got.What != tc.wantWhat ||
				got.Why != tc.wantWhy || got.NextText != tc.wantText || got.Detail != apiMessage {
				t.Errorf("failure = %#v, want id %q, action %q, what %q, why %q, next %q, detail %q",
					got, tc.wantID, tc.wantNext, tc.wantWhat, tc.wantWhy, tc.wantText, apiMessage)
			}
		})
	}
}

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
	archive := filepath.Join(t.TempDir(), "archive.tgz")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := uploadArchive(context.Background(), uploadDeps{
		URL: "https://store.example/archive", ArchivePath: archive, Bytes: 7,
		ExpiresAt: now.Add(time.Hour), Now: func() time.Time { return now },
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden,
				Body: io.NopCloser(strings.NewReader("store refusal")), Header: make(http.Header)}, nil
		}),
	})
	var failure *ui.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("uploadArchive returned %T, want a ui.Failure in the chain", err)
	}
	id, action, why, nextText := failure.ID, failure.Next, failure.Why, failure.NextText

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
	if !strings.Contains(nextText, "Please report this") ||
		!strings.Contains(nextText, "curious version") {
		t.Errorf("the next-step copy changed; it should ask for a report and the "+
			"version and nothing else:\n%s", nextText)
	}
	if !strings.Contains(why, "a fault in curious") {
		t.Errorf("the why no longer names this as our fault:\n%s", why)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestTheOtherTwoRefusalBranchesKeepTheirOwnFamilies is the control for
// the row above: a correction that collapsed all three branches onto one
// family would satisfy it and be wrong.
func TestTheOtherTwoRefusalBranchesKeepTheirOwnFamilies(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

	// No announced window: the server does not say when a link stops
	// working, so this client cannot tell which refusal it met.
	id, action, _, nextText := refusalCopy("store.example", time.Time{}, now)
	if id != ui.IDUploadRefusedUnexplained || action != ui.NextFreshDeploy {
		t.Errorf("unknown-expiry branch = (%q, %q), want (%q, %q)",
			id, action, ui.IDUploadRefusedUnexplained, ui.NextFreshDeploy)
	}
	// THE COPY WAS DISCARDED HERE — `_, _` — while this test was recorded
	// as what covers the next-step text the checker cannot resolve. It
	// asserted the id and the action and nothing else, so the obligation
	// had no coverage anywhere and the ledger said it did.
	if !strings.Contains(nextText, "twice") {
		t.Errorf("the unknown-expiry branch no longer asks the reader to report a "+
			"second identical failure, which is the only action available when "+
			"this client cannot say which refusal it met:\n%s", nextText)
	}

	// The window has closed: a fresh link is issued every run.
	id, action, _, nextText = refusalCopy("store.example", now.Add(-time.Hour), now)
	if id != ui.IDUploadLinkExpired || action != ui.NextFreshDeploy {
		t.Errorf("expired branch = (%q, %q), want (%q, %q)",
			id, action, ui.IDUploadLinkExpired, ui.NextFreshDeploy)
	}
	if !strings.Contains(nextText, "fresh link") {
		t.Errorf("the expired branch no longer tells the reader a fresh link is "+
			"issued every run, which is the whole reason this one is worth "+
			"retrying:\n%s", nextText)
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
			if want := "Try again after 12:01 UTC (+00:00) — in about 2 minutes. " +
				"Nothing has been uploaded and nothing has been deployed."; f.NextText != want {
				t.Errorf("next-step copy = %q, want %q", f.NextText, want)
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
		name     string
		err      error
		wantNext string
	}{
		{"at the publish", publishStopFailure(unknown, "dpl-abc", now),
			"Try again in a moment. If it keeps happening, updating curious may\n" +
				"help — this build may be older than the server."},
		{"at the start", startFailure(unknown),
			"Try again in a moment. If it keeps happening, updating curious may\n" +
				"help — this build may be older than the server."},
		{"at the stream", streamRefusedFailure(unknown),
			"Try again in a moment. If it keeps happening, updating curious may\n" +
				"help — this build may be older than the server."},
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
			if f.NextText != tc.wantNext {
				t.Errorf("next-step copy = %q, want the complete approved instruction %q",
					f.NextText, tc.wantNext)
			}
		})
	}
}

// TestAHardStopCarriesItsCheckFamilyIntoTheFailure is the scenario the
// contract checker cannot establish statically.
//
// ownCopy turns a check.Finding into a ui.Failure, and BOTH the family
// id and the next-step copy arrive at run time off the finding. No
// analysis of that call site can say which id it carries — the answer
// depends on which producer made the finding — so the checker reports it
// unresolved and points here.
//
// # Why a check is not a family
//
// astro-dep is ONE check over five conditions with five different
// remedies. If the failure took its identity from the CheckID, a reader
// who hit "package.json isn't valid JSON" and a reader who hit "astro
// isn't a dependency" would be sent to the same troubleshooting entry,
// which can only carry one fix. The family travels on the finding for
// exactly that reason.
func TestAHardStopCarriesItsCheckFamilyIntoTheFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		next string
		want ui.FailureID
	}{
		{"a producer that wrote its own next step",
			"Add astro to package.json and run it again.",
			ui.FailureID("astro-dep-absent")},
		{"a producer that wrote none", "",
			ui.FailureID("lockfile-missing")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ownCopy(check.Finding{
				CheckID:   check.IDAstroDep,
				FailureID: string(tc.want),
				Severity:  check.SeverityHardStop,
				Message:   "something to fix",
				What:      "Something to fix.",
				Why:       "Because of a reason.",
				Next:      tc.next,
			})
			if f.ID != tc.want {
				t.Errorf("id = %q, want %q — the family travels on the finding, "+
					"because one check covers several remedies", f.ID, tc.want)
			}
			// AND THE COPY IS NEVER BLANK, which is the other half the
			// checker could not establish here. A producer that wrote no
			// next step gets the standing one; a producer that wrote one
			// keeps it byte for byte.
			if strings.TrimSpace(f.NextText) == "" {
				t.Error("the failure carries a blank next step")
			}
			if tc.next != "" && f.NextText != tc.next {
				t.Errorf("the producer's own words were changed:\n got %q\nwant %q",
					f.NextText, tc.next)
			}
		})
	}
}

// TestABlankNextStepFallsBackAndANonBlankOneIsKeptExactly covers the
// ruled predicate change: blank, not empty.
//
// A producer supplying whitespace used to pass the old `== ""` test and
// hand a failure a next-step paragraph made of spaces, which renders as
// a blank line and tells a reader nothing.
func TestABlankNextStepFallsBackAndANonBlankOneIsKeptExactly(t *testing.T) {
	for _, tc := range []struct {
		name, next string
		wantOwn    bool
	}{
		{"empty", "", false},
		{"whitespace only", "   \n\t ", false},
		{"non-blank is preserved byte for byte", " \tDo the specific thing.\n ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ownCopy(check.Finding{
				CheckID: check.IDLockfile, FailureID: string(check.FamilyLockfileMissing),
				Severity: check.SeverityHardStop, Message: "m",
				What: "W.", Why: "Y.", Next: tc.next,
			})
			switch {
			case tc.wantOwn && f.NextText != tc.next:
				t.Errorf("the producer's copy was altered:\n got %q\nwant %q",
					f.NextText, tc.next)
			case !tc.wantOwn && f.NextText == tc.next:
				t.Errorf("a blank next step was kept as %q rather than falling back",
					f.NextText)
			case strings.TrimSpace(f.NextText) == "":
				t.Error("the fallback itself is blank")
			}
		})
	}
}
