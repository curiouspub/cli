package flow

import (
	"net/http"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// The start's two rate limits, driven through a whole run against the
// fake server. Each answer is the GOLDEN response: the status, code,
// Retry-After and message the server sends for that limit. Once the
// server half exists, a real refusal captured from it replaces these.
//
// The run's clock is 12:00 UTC (fixedNowLocal). The daily limit names the
// next reset, twelve hours out; the one-build-at-a-time limit names the
// end of the running build's lease, a few minutes out.
func TestARateLimitedStartSaysWhenAndStops(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retryAfter string
		message    string
		wantWhen   string
	}{
		{
			name:       "the day's builds are spent",
			retryAfter: "43200",
			message:    "This account has started its 10 builds for today. The count resets at 00:00 UTC.",
			wantWhen:   "in about 12 hours",
		},
		{
			name:       "one build at a time",
			retryAfter: "420",
			message:    "One build at a time: this account already has a build running.",
			wantWhen:   "in about 7 minutes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			run.script.startOutcome = outcome{
				status:     http.StatusTooManyRequests,
				code:       wire.CodeRateLimited,
				message:    tc.message,
				retryAfter: tc.retryAfter,
			}

			handoff, err := run.run()
			defer handoff.Release()
			if err == nil {
				t.Fatal("a refused start was reported as a success")
			}

			text, _ := renderedBytes(t, err)
			for _, want := range []string{
				tc.message,  // what the server said, quoted
				tc.wantWhen, // the time it named, used rather than dropped
				"The archive was uploaded",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("the failure never said %q:\n%s", want, text)
				}
			}
			if strings.Contains(text, "Nothing has been uploaded") {
				t.Errorf("the start's failure claims nothing was uploaded, after the upload:\n%s", text)
			}
			if strings.Contains(text, "Try again in a moment") {
				t.Errorf("the start's failure dropped the server's time for the generic advice:\n%s", text)
			}

			// The archive went up before the start was refused, and nothing
			// after the start ran: the start is never retried and a refused
			// build has no log to read.
			if puts := run.store.received(); len(puts) == 0 {
				t.Error("the store received nothing, so this row did not reach the start")
			}
			for _, e := range run.script.journal.all() {
				if strings.HasSuffix(e, "/"+eventsAction) || strings.HasSuffix(e, "/"+publishAction) {
					t.Errorf("the run went on after a refused start: %s", e)
				}
			}
			// The server journals each request twice, as its path and again
			// with sentPrefix and the absolute URL; the path entries count.
			starts := 0
			for _, e := range run.script.journal.all() {
				if strings.HasPrefix(e, "POST "+deployPathPrefix) && strings.HasSuffix(e, "/"+startAction) {
					starts++
				}
			}
			if starts != 1 {
				t.Errorf("the start was sent %d times, want exactly once", starts)
			}
		})
	}
}
