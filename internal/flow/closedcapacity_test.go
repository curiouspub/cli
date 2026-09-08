package flow

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/pkg/wire"
)

// fixedZone is the "user's own timezone" every rendering row runs in.
//
// time.FixedZone RATHER THAN time.LoadLocation, and it is a constraint
// rather than a preference. Nothing in this module imports a zone
// database, so a named lookup answers from whatever the host happens to
// have — which on one of the three platforms this ships to is commonly
// nothing at all — and embedding one to fix that adds most of a megabyte
// to the shipped binary. The subject here is the RENDERING, not the
// host's zone table, so the zone is supplied rather than looked up.
//
// One hour east of UTC, so a row can tell a local rendering from a UTC
// one by reading it.
var fixedZone = time.FixedZone("CET", 60*60)

// fixedNowLocal is the shared instant, carried in that zone. The zone a
// reset is rendered in comes from the clock's own value, so this is
// where a row says which zone it means.
var fixedNowLocal = fixedNow.In(fixedZone)

// TestTheResetIsRenderedInTheZoneTheClockCarries. A bare UTC timestamp
// makes a person do arithmetic to find out whether waiting is worth it,
// and the offset is what tells a reader in the wrong place that the time
// is not theirs.
//
// The expected string is written out rather than derived from the
// renderer, so an assertion about a rendering is not produced by the
// thing it is asserting about.
//
// REQUIRED MUTATION, RUN, two of them. In reopensPhrase, render
// `resetsAt.UTC()` rather than `resetsAt.In(now.Location())`. Drop the
// relativePhrase half of the same Sprintf.
func TestTheResetIsRenderedInTheZoneTheClockCarries(t *testing.T) {
	// The reset arrives from the wire in whatever zone the server wrote
	// it in — here, UTC — which is exactly the case that would go
	// unnoticed if the renderer simply used the value's own location.
	resetsAt := fixedNow.Add(4 * time.Hour).UTC()

	phrase, known := reopensPhrase(resetsAt, fixedNowLocal)
	if !known {
		t.Fatal("a reset four hours out was reported as no reset at all")
	}
	// 12:00 UTC plus four hours, read one hour east.
	const want = "17:00 CET (+01:00) — in about 4 hours"
	if phrase != want {
		t.Errorf("rendered as %q, want %q", phrase, want)
	}
}

// TestNoResetTimeIsInvented. The server is entitled to send nothing
// usable, and adding nothing to the current time would tell the reader
// to come back immediately — the one answer that is certainly wrong.
//
// Each refusal is paired with the acceptance beside it, so neither
// direction can pass against a renderer that answers the same way twice.
//
// REQUIRED MUTATION, RUN: make reopensPhrase's guard never refuse.
func TestNoResetTimeIsInvented(t *testing.T) {
	for _, row := range []struct {
		name     string
		resetsAt time.Time
		known    bool
		after    string
	}{
		{"nothing at all", time.Time{}, false, "a little later"},
		{"the moment it is asked", fixedNowLocal, false, "a little later"},
		{"already past", fixedNowLocal.Add(-time.Hour), false, "a little later"},
		{"four hours out", fixedNowLocal.Add(4 * time.Hour), true,
			"after 17:00 CET (+01:00) — in about 4 hours"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if _, known := reopensPhrase(row.resetsAt, fixedNowLocal); known != row.known {
				t.Errorf("reopensPhrase reports known = %v, want %v", known, row.known)
			}
			if got := afterTheReset(row.resetsAt, fixedNowLocal); got != row.after {
				t.Errorf("afterTheReset renders %q, want %q", got, row.after)
			}
		})
	}
}

// TestTheRelativePhraseSpansItsBands. The bands are coarse on purpose —
// a reset learned from a header does not justify naming a minute four
// hours out — so the rows are the boundaries rather than samples.
//
// REQUIRED MUTATION, RUN: in countOf, return the plural form for n == 1.
func TestTheRelativePhraseSpansItsBands(t *testing.T) {
	for _, row := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "in under a minute"},
		{time.Minute, "in about 1 minute"},
		{15 * time.Minute, "in about 15 minutes"},
		{44 * time.Minute, "in about 44 minutes"},
		{time.Hour, "in about an hour"},
		{4 * time.Hour, "in about 4 hours"},
		{25 * time.Hour, "in about 1 day"},
		{50 * time.Hour, "in about 2 days"},
	} {
		t.Run(row.in.String(), func(t *testing.T) {
			if got := relativePhrase(row.in); got != row.want {
				t.Errorf("%s renders as %q, want %q", row.in, got, row.want)
			}
		})
	}
}

// TestTheLoginStopUsesTheSharedClosedCapacityCopy is the half of "one
// home" that can be measured before anything else consumes it: the
// login flow's own stop, rendering through the copy above rather than
// through a second wording beside it.
//
// It is a whole-run row rather than a call to closedCapacityFailure,
// because what is being asserted is which renderer the LOGIN FLOW
// reaches for, and a direct call would assert only that the renderer
// works.
//
// REQUIRED MUTATION, RUN: put retryAdvice(apiErr.RetryAfter, now) back
// in the login flow's capacity branch in place of closedCapacityFailure.
// Every other row in the login suite stays green, which is why this one
// exists.
func TestTheLoginStopUsesTheSharedClosedCapacityCopy(t *testing.T) {
	run := newLoginRun(t)
	run.deps.Now = func() time.Time { return fixedNowLocal }
	// No offer wired: the fallback stop is the one that renders this
	// copy, and a wired hand-off would supply words of its own.
	run.deps.Offer = nil
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111")}
	run.prompt.confirms = []answer{no()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusServiceUnavailable, wire.CodeCapacityClosed,
			"We're full for today.").after("14400"),
	}

	err := run.run(t)
	if err == nil {
		t.Fatal("a shut cap at the login ended the run at success")
	}
	got := rendered(err)
	for _, want := range []string{
		closedHeadline,
		"17:00 CET (+01:00) — in about 4 hours",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the login's own stop never said %q:\n%s", want, got)
		}
	}
}
