package flow

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// -------------------------------------------------------------------
// The instruments this file adds
// -------------------------------------------------------------------

// publishRun is an ordinary deploy wound all the way to its last step: a
// clean project, a first-time login, and a build that finishes. A row
// here then states only the thing it is about.
func publishRun(t *testing.T) *deployRun {
	t.Helper()
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	return run
}

// advancingClock moves on every reading, by step.
//
// IT EXISTS BECAUSE THE ONE BOUND IN THIS STEP IS MEASURED ON THE RUN'S
// OWN CLOCK. Every other row in this package runs against a frozen one,
// which is right for a rendering and useless for a window: frozen, the
// confirm window never closes, so the row that proves the asking STOPS
// could only be written by spending the real thirty seconds. Moving the
// clock is what lets it run in milliseconds without inventing a second
// bound for the test to hit.
func advancingClock(start time.Time, step time.Duration) func() time.Time {
	now := start
	return func() time.Time {
		reading := now
		now = now.Add(step)
		return reading
	}
}

// sentRequests is every request this run made, method and absolute URL,
// in the order they left the machine.
//
// IT READS THE ONE JOURNAL rather than counting at any single server,
// and that is the whole design of the row below it. A count taken at the
// scripted API cannot see a request to somewhere else; a count of
// "requests to hosts that are not the API" cannot see one made to the
// API host; and neither sees one made through a client this test does
// not own. Recording method and absolute URL wherever a request can be
// observed, into one list, is what lets a row ask about a DESTINATION
// rather than about a total.
func sentRequests(events []string) []string {
	var out []string
	for _, e := range events {
		if strings.HasPrefix(e, sentPrefix) {
			out = append(out, strings.TrimPrefix(e, sentPrefix))
		}
	}
	return out
}

// recordingDefaultTransport is the third place a request can be
// observed: the process's own default transport, which is what
// http.Get, http.Head and http.DefaultClient use.
//
// IT REFUSES EVERYTHING. A row here must never reach a real network, and
// a recorder that let a request through would be one — so the record is
// taken and the request is failed, which also makes a client that
// ignores the error visible in the journal rather than in a timeout.
type recordingDefaultTransport struct{ journal *deployJournal }

func (r *recordingDefaultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.journal.note(sentPrefix + req.Method + " " + req.URL.String())
	return nil, errors.New("this row refuses every request made through the default transport")
}

// requestHost is the host half of a recorded "METHOD absolute-URL"
// entry.
func requestHost(t *testing.T, request string) string {
	t.Helper()
	_, raw, found := strings.Cut(request, " ")
	if !found {
		t.Fatalf("a recorded request has no URL in it: %q", request)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("a recorded request is not a URL: %q: %v", request, err)
	}
	return u.Host
}

// lastNonEmptyLine is the last line of s that has anything on it.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// -------------------------------------------------------------------
// The address
// -------------------------------------------------------------------

// TestTheAddressIsBuiltFromTheLabelTheServerSent. The response carries a
// LABEL and not a URL, so the client composes one — and the row drives
// TWO runs whose responses carry different labels, because a single run
// cannot tell a client that read the response from one that printed a
// constant.
//
// THE LABEL DIFFERS FROM THE DEPLOY ID in both, which is the other
// mistake this shape can see: a client echoing the wrong field would
// produce a working-looking address, and only two values that differ
// make it visible.
//
// REQUIRED MUTATION, run 2026-09-09: render publishedURL of a fixed
// label rather than the response's. It reds on ONE of the two subtests
// and not the other, which is the correction worth keeping: the fixed
// label matches the first row's, so that row stays green and only
// "another" reports — "the run printed
// \"https://quick-koala-4f2a.curiously.dev\", want
// \"https://brisk-otter-91cd.curiously.dev\"". A single-label row would
// have gone green under the whole mutation, which is exactly why there
// are two.
func TestTheAddressIsBuiltFromTheLabelTheServerSent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		label    string
		deployID string
		want     string
	}{
		{"one label", "quick-koala-4f2a", "dpl-aaaa1111", "https://quick-koala-4f2a.curiously.dev"},
		{"another", "brisk-otter-91cd", "dpl-bbbb2222", "https://brisk-otter-91cd.curiously.dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := publishRun(t)
			run.script.publishSubdomain = tc.label
			run.script.deployID = tc.deployID

			handoff, err := run.run()
			if err != nil {
				t.Fatalf("Deploy: %v\n%s", err, rendered(err))
			}
			defer handoff.Release()

			if got := lastNonEmptyLine(run.prompt.results.String()); got != tc.want {
				t.Errorf("the run printed %q, want %q", got, tc.want)
			}
			if printed := run.prompt.results.String(); strings.Contains(printed, tc.deployID) {
				t.Errorf("the deploy id reached stdout, so the address may be built "+
					"from the wrong field:\n%s", printed)
			}
		})
	}
}

// TestTheRunEndsWithTheCaveatAndThenTheAddress is the shape of the last
// thing this command does, and both halves of it are load-bearing.
//
// The CLOSING NARRATION carries the claim, the caveat and the next step,
// and it is the last thing written to stderr. The ADDRESS is the last
// thing written to stdout. They are asserted as the final two renderings
// IN THAT ORDER rather than as text present somewhere, because a client
// that printed the caveat and then buried the address earlier in the run
// would satisfy a contains-check and leave a reader with a warning about
// a link they have already scrolled past.
//
// IT CLAIMS NOTHING ABOUT REACHABILITY. An address is served a little
// after a deploy is published — observed once at about half a minute,
// which is a measurement rather than a bound — so "live", "is up" and
// "ready" are exactly the words this line may not use.
//
// REQUIRED MUTATION, run 2026-09-09: delete the propagation sentence
// from the closing narration. Reds here.
func TestTheRunEndsWithTheCaveatAndThenTheAddress(t *testing.T) {
	run := publishRun(t)

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	transcript := run.prompt.transcript
	if len(transcript) < 2 {
		t.Fatalf("the run said almost nothing: %v", transcript)
	}
	closing := transcript[len(transcript)-2]
	address := transcript[len(transcript)-1]

	if want := "printed: " + publishedURL(defaultPublishSubdomain); address != want {
		t.Fatalf("the last thing the run printed is %q, want %q", address, want)
	}
	if !strings.HasPrefix(closing, "said: ") {
		t.Fatalf("the address is not preceded by narration: %q", closing)
	}

	for _, want := range []string{
		publishedHeadline,
		"up to about a minute",
		"placeholder page",
		"wait a\nmoment and reload",
	} {
		if !strings.Contains(closing, want) {
			t.Errorf("the closing narration never said %q:\n%s", want, closing)
		}
	}

	// The three words a run must not use about an address it has not
	// tried. Lower-cased so a capitalised spelling cannot slip past.
	for _, forbidden := range []string{"live", "is up", "ready"} {
		if strings.Contains(strings.ToLower(closing), forbidden) {
			t.Errorf("the closing narration claims %q, which is a claim about "+
				"reachability this run never checked:\n%s", forbidden, closing)
		}
	}
}

// TestTheExpiryIsRenderedBothWaysThroughAWholeRun. A bare timestamp in a
// terminal is a thing people misread by a day, and a bare relative
// phrase cannot be put in a calendar, so the line carries both.
//
// THROUGH A WHOLE RUN AGAINST A FIXED CLOCK, deliberately. A formatter
// tested on its own can be perfectly correct beside a success path that
// never calls it, and this file has no other row that would notice.
//
// The expected string is written out rather than derived from the
// renderer: an assertion about a rendering must not be produced by the
// thing it is asserting about.
func TestTheExpiryIsRenderedBothWaysThroughAWholeRun(t *testing.T) {
	run := publishRun(t)

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	// The clock reads 13:00 one hour east of UTC; the harness's expiry
	// is 71 hours after it.
	const want = "This deploy expires at Fri 11 Sep 2026, 12:00 CET (+01:00) — in about 3 days."
	if narrated := run.prompt.out.String(); !strings.Contains(narrated, want) {
		t.Errorf("the run never rendered the expiry as %q:\n%s", want, narrated)
	}
}

// TestAnExpiryTheServerDidNotSendIsNotInvented is the other half of the
// row above. The window is the server's and nothing this client holds
// determines it, so an absent one renders no line rather than a sentence
// filling the space — and the run still ends normally, with the address.
func TestAnExpiryTheServerDidNotSendIsNotInvented(t *testing.T) {
	run := publishRun(t)
	run.script.publishExpiresAt = time.Time{}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	if narrated := run.prompt.out.String(); strings.Contains(narrated, expiresPrefix) {
		t.Errorf("the run invented an expiry the server never sent:\n%s", narrated)
	}
	if got := lastNonEmptyLine(run.prompt.results.String()); got != publishedURL(defaultPublishSubdomain) {
		t.Errorf("the run printed %q, want the address", got)
	}
}

// TestTheAddressIsOnStdoutAndTheNarrationIsNot. By the origin rule the
// build log established, the address is content a script may consume and
// everything said about it is not — so `curious deploy > url.txt` yields
// the address, and the claim, the caveat and the expiry are read by a
// person on stderr.
//
// EACH IS ASSERTED PRESENT ON ITS OWN STREAM AND ABSENT FROM THE OTHER,
// because either half alone passes for the wrong reason: a client that
// wrote everything to both streams satisfies "present", and one that
// wrote nothing anywhere satisfies "absent".
func TestTheAddressIsOnStdoutAndTheNarrationIsNot(t *testing.T) {
	run := publishRun(t)

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	printed, narrated := run.prompt.results.String(), run.prompt.out.String()
	address := publishedURL(defaultPublishSubdomain)

	if !strings.Contains(printed, address) {
		t.Errorf("the address never reached stdout:\n%s", printed)
	}
	if strings.Contains(narrated, address) {
		t.Errorf("the address reached stderr, so a redirected stdout is not the "+
			"whole machine-readable answer:\n%s", narrated)
	}
	if !strings.Contains(narrated, publishedHeadline) {
		t.Errorf("the outcome line never reached stderr:\n%s", narrated)
	}
	if strings.Contains(printed, publishedHeadline) {
		t.Errorf("narration reached stdout, so a redirected stdout is not just "+
			"the address:\n%s", printed)
	}
	if strings.Contains(printed, "up to about a minute") {
		t.Errorf("the caveat reached stdout:\n%s", printed)
	}
}

// -------------------------------------------------------------------
// Nothing asks the address for anything
// -------------------------------------------------------------------

// TestNothingRequestsTheAddressThisRunPrints. This client does not poll,
// does not sleep and does not check: an address is served a little after
// a deploy is published, one location's answer is not the claim being
// made, and machinery that waited would have to live where every surface
// inherits it rather than in this one command.
//
// THE RECORD IS EVERY METHOD AND ABSOLUTE URL, from all three places a
// request can be seen — the scripted API, the object store, and the
// process's default transport — rather than a count of requests to hosts
// that are not the API. A count like that cannot see a GET issued to the
// API host, and a count taken at one server cannot see a request made
// anywhere else at all.
//
// THE POSITIVE CONTROL IS THE UPLOAD, which shows a non-zero count to
// the object store on the same run: without it, a client that made no
// requests whatsoever would pass this row, and so would an instrument
// that recorded nothing.
//
// WHAT IT DOES NOT SEE, said here rather than left to be discovered: a
// request made through a transport that is neither the process default
// nor one either scripted server answers on. Nothing in this package
// constructs one — the upload borrows the API client's transport, and
// every other request goes through that client — so the uncovered case
// is a client somebody would have to introduce, and introducing it is
// the change that should carry its own row.
//
// REQUIRED MUTATION, run 2026-09-09: add a GET of the composed address
// before printing it. Reds here; the object-store control stays green.
func TestNothingRequestsTheAddressThisRunPrints(t *testing.T) {
	run := publishRun(t)

	saved := http.DefaultTransport
	http.DefaultTransport = &recordingDefaultTransport{journal: run.journal}
	t.Cleanup(func() { http.DefaultTransport = saved })

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	address, err := url.Parse(publishedURL(defaultPublishSubdomain))
	if err != nil {
		t.Fatalf("parsing the composed address: %v", err)
	}

	sent := sentRequests(run.journal.all())
	if len(sent) == 0 {
		t.Fatal("the record is empty, so every assertion below would pass for the " +
			"wrong reason")
	}
	for _, request := range sent {
		if requestHost(t, request) == address.Host {
			t.Errorf("the run asked the published address for something: %q\n\n"+
				"everything it sent:\n  %s", request, strings.Join(sent, "\n  "))
		}
	}

	// The positive control: this run really did send things, and one of
	// them went to the object store.
	if puts := run.store.received(); len(puts) == 0 {
		t.Error("the object store received nothing, so a count of zero anywhere " +
			"else says nothing about what this client does")
	}

	// The second control, for the instrument rather than the client: a
	// request made through the process's default transport must appear
	// in the record. Without it, replacing the recorder with a no-op
	// leaves the loop above green over a list it never grew.
	before := len(sentRequests(run.journal.all()))
	probe, err := http.NewRequest(http.MethodGet, publishedURL("control-probe"), nil)
	if err != nil {
		t.Fatalf("building the control request: %v", err)
	}
	if _, probeErr := http.DefaultClient.Do(probe); probeErr == nil {
		t.Fatal("the recording transport answered a request instead of refusing it")
	}
	if after := len(sentRequests(run.journal.all())); after != before+1 {
		t.Fatalf("a request through the default transport was not recorded (%d "+
			"entries before, %d after) — every assertion above is then a claim "+
			"about a record nothing writes to", before, after)
	}
}

// -------------------------------------------------------------------
// The contract's own rules
// -------------------------------------------------------------------

// TestAnUnknownFieldInThePublishResponseIsIgnored. The contract is
// additive-only, so a server is entitled to send a field this build has
// never heard of, and a client that treated one as an error would be a
// client the next release breaks.
func TestAnUnknownFieldInThePublishResponseIsIgnored(t *testing.T) {
	run := publishRun(t)
	run.script.publishBody = `{"subdomain":"quick-koala-4f2a",` +
		`"expires_at":"2026-09-11T11:00:00Z",` +
		`"a_field_from_a_later_server":{"nested":[1,2,3]}}`

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	if got := lastNonEmptyLine(run.prompt.results.String()); got != publishedURL("quick-koala-4f2a") {
		t.Errorf("the run printed %q, want the address", got)
	}
}

// TestEveryContractCodeHasAPublishRoute reads the contract's own
// enumeration rather than a list typed beside it, so a code added later
// arrives already needing an answer here. A hand-written copy of the
// vocabulary agrees with itself by construction and never looks at the
// contract at all.
func TestEveryContractCodeHasAPublishRoute(t *testing.T) {
	if len(wire.AllErrorCodes) == 0 {
		t.Fatal("the contract enumerates no codes, so this row would pass over nothing")
	}
	for _, code := range wire.AllErrorCodes {
		route, stated := publishRouting[code]
		if !stated {
			t.Errorf("the contract defines %q and the publish has no stated route for it", code)
			continue
		}
		// AND THE VALUE IS ONE OF THE TWO. Membership alone was
		// satisfied by publishRoute(999), which routes to a stop by
		// accident rather than by decision — the map would have been
		// exhaustive over the contract and meaningless.
		if route != publishAskAgain && route != publishStop {
			t.Errorf("the publish routes %q to %d, which is neither of the two "+
				"routes this file defines", code, route)
		}
	}
	for code := range publishRouting {
		found := false
		for _, known := range wire.AllErrorCodes {
			if code == known {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the publish routes %q, which the contract does not define", code)
		}
	}
}

// -------------------------------------------------------------------
// When the build outcome arrives late
// -------------------------------------------------------------------

// TestABuildTheServerWouldNotTakeIsNotCalledAPublishingProblem is the
// obligation this step inherited from the one before it: the build log's
// terminal event carries the BUILDER's claim, posted before the check
// that runs on what the build produced — so a run somebody has just
// watched finish can be refused at the last call, and the copy must say
// what actually happened.
//
// THE WORD IS THE ASSERTION. A refusal here is a build outcome arriving
// late, and a message naming this last step would put the reader's
// attention on the one part of the run that worked.
//
// THE POSITIVE CONTROL IS THE SUCCESS LINE, which does name it: without
// that half, a client whose copy never used the word anywhere would pass
// the absence check while saying nothing at all.
//
// REQUIRED MUTATION, run 2026-09-09: make the headline "Publishing this
// deploy failed." It reds on TWO assertions rather than the predicted
// one — "the failure blames the last step of the run" and "the failure
// never said \"The build finished\"" — because the mutated headline both
// adds the word and removes the sentence naming the build. The control
// subtest stays green, and so does the row below, whose three refusals
// still render identically as each other.
func TestABuildTheServerWouldNotTakeIsNotCalledAPublishingProblem(t *testing.T) {
	run := publishRun(t)
	run.script.publishOutcome = fails(http.StatusConflict, wire.CodeDeployFailed,
		`this deploy is "failed" and cannot be published — only a built deploy can be`)

	_, err := run.run()
	if err == nil {
		t.Fatal("the run continued past a deploy the server would not take")
	}
	text, code := renderedBytes(t, err)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.Contains(strings.ToLower(text), "publish") {
		t.Errorf("the failure blames the last step of the run:\n%s", text)
	}
	for _, want := range []string{
		"The build finished",
		nothingDeployed,
		"deploy-1",
		"`curious deploy` again",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the failure never said %q:\n%s", want, text)
		}
	}

	t.Run("control: the success line does name it", func(t *testing.T) {
		ok := publishRun(t)
		handoff, runErr := ok.run()
		if runErr != nil {
			t.Fatalf("Deploy: %v\n%s", runErr, rendered(runErr))
		}
		defer handoff.Release()
		if !strings.Contains(strings.ToLower(ok.prompt.out.String()), "publish") {
			t.Errorf("a run that worked never named what it did:\n%s", ok.prompt.out.String())
		}
	})
}

// TestAStateNobodyNamedIsNotGivenAStoryOfItsOwn.
//
// The state the server observed reaches this client only inside a
// sentence written for a person, and parsing that back is what this
// project refuses to do with prose. So the copy must be true whatever
// the state was — including one this build has never heard of, which the
// contract routes to the terminal code on purpose.
//
// THE ASSERTION IS THAT TWO REFUSALS NAMING DIFFERENT STATES RENDER
// IDENTICALLY. A client that echoed the server's sentence would differ
// (the sentences differ); one that mapped a state to a line of its own
// would differ too. Comparing the two renderings is the only shape that
// can see either, because a contains-check over one of them cannot.
//
// The control is that the rendering is not empty and does name the
// deploy — otherwise two blank strings would agree perfectly.
func TestAStateNobodyNamedIsNotGivenAStoryOfItsOwn(t *testing.T) {
	refusals := []struct {
		name    string
		code    wire.ErrorCode
		message string
	}{
		{"a state this build knows", wire.CodeDeployFailed,
			`this deploy is "failed" and cannot be published — only a built deploy can be`},
		{"a state from a later server", wire.CodeDeployFailed,
			`this deploy is "quarantined" and cannot be published — only a built deploy can be`},
		{"the one code an older server had", wire.CodeBadRequest,
			`this deploy is "queued" and cannot be published — only a built deploy can be`},
	}

	var first string
	for i, refusal := range refusals {
		run := publishRun(t)
		run.script.publishOutcome = fails(http.StatusConflict, refusal.code, refusal.message)

		_, err := run.run()
		if err == nil {
			t.Fatalf("%s: the run continued past a refusal", refusal.name)
		}
		text, code := renderedBytes(t, err)
		if code != 1 {
			t.Errorf("%s: exit code = %d, want 1", refusal.name, code)
		}
		if !strings.Contains(text, "deploy-1") {
			t.Fatalf("%s: the failure names no deploy, so comparing renderings "+
				"would be comparing two things that say nothing:\n%s", refusal.name, text)
		}
		if i == 0 {
			first = text
			continue
		}
		if text != first {
			t.Errorf("%s renders differently from the first refusal, so this client "+
				"is telling a story about a state it cannot check:\n\ngot:\n%s\n\nwant:\n%s",
				refusal.name, text, first)
		}
	}
}

// TestEachRefusalHasItsOwnCopyAndItsOwnCost. One fixture does not prove
// the others: the codes reach this client for different reasons and cost
// different things, and a table with one row would be a mapping nobody
// verified.
//
// The maintenance row is the only one that is not a failure of this run
// at all — the door is shut, the project is fine — which is why it costs
// a different number.
func TestEachRefusalHasItsOwnCopyAndItsOwnCost(t *testing.T) {
	table := []struct {
		name     string
		outcome  outcome
		wantCode int
		says     []string
		neverSay []string
	}{
		{
			name: "the deploy is unknown",
			outcome: fails(http.StatusNotFound, wire.CodeNotFound,
				"no deploy with that id"),
			wantCode: 1,
			says: []string{"The server doesn't know that deploy.", "deploy-1",
				"no deploy with that id", nothingDeployed},
		},
		{
			name: "the service declined",
			outcome: fails(http.StatusServiceUnavailable, wire.CodeMaintenance,
				"curious.pub is having a lie down"),
			wantCode: ui.ExitServerClosed,
			says: []string{"curious.pub is having a lie down", nothingDeployed,
				"Try again a little later."},
		},
		{
			name: "the server could not complete it",
			outcome: fails(http.StatusInternalServerError, wire.CodeInternal,
				"curious.pub is at capacity and cannot do this right now. This is "+
					"ours to fix, not something you need to retry."),
			wantCode: 1,
			says: []string{"The server couldn't finish the deploy.",
				"ours to fix", "deploy-1", nothingDeployed},
			// The server has already said not to retry. Advice to the
			// contrary, printed directly underneath it, is this client
			// contradicting the sentence above it.
			neverSay: []string{"Try again"},
		},
		{
			name: "a code this build predates",
			outcome: fails(http.StatusTeapot, wire.ErrorCode("a_code_from_a_later_server"),
				"something this build has never heard of"),
			wantCode: 1,
			says: []string{"something this build has never heard of", "deploy-1",
				nothingDeployed, "updating curious may"},
		},
		{
			// Both codes for one event: a build the server will not take.
			// The row next door is about the WORD this copy must not use;
			// these two are here because the completeness check below
			// asks about every stop code, and an exemption naming
			// another row is a pointer that goes stale.
			name: "the build was refused",
			outcome: fails(http.StatusConflict, wire.CodeDeployFailed,
				`this deploy is "failed" and cannot be published`),
			wantCode: 1,
			says: []string{"The build finished", "deploy-1", nothingDeployed,
				"`curious deploy` again"},
			neverSay: []string{"publish"},
		},
		{
			name: "an older server saying the same thing",
			outcome: fails(http.StatusBadRequest, wire.CodeBadRequest,
				"not a built deploy"),
			wantCode: 1,
			says:     []string{"The build finished", "deploy-1", nothingDeployed},
			neverSay: []string{"publish"},
		},
		{
			name: "the login is not accepted here",
			outcome: fails(http.StatusUnauthorized, wire.CodeUnauthorized,
				"that token is not valid"),
			wantCode: 1,
			says: []string{"The server wouldn't give this deploy an address.",
				"that token is not valid", "deploy-1", nothingDeployed},
		},
		{
			name: "the login is not allowed to",
			outcome: fails(http.StatusForbidden, wire.CodeForbidden,
				"that deploy belongs to somebody else"),
			wantCode: 1,
			says: []string{"The server wouldn't give this deploy an address.",
				"that deploy belongs to somebody else", "deploy-1", nothingDeployed},
		},
		{
			name: "the server asks for a pause",
			outcome: fails(http.StatusTooManyRequests, wire.CodeRateLimited,
				"too many of those just now").after("90"),
			wantCode: 1,
			says: []string{"The server is asking for a pause.",
				"too many of those just now", "deploy-1", nothingDeployed, "Try again"},
		},
		{
			name: "the door is shut for today",
			outcome: fails(http.StatusTooManyRequests, wire.CodeCapacityClosed,
				"full for today").after("3600"),
			wantCode: ui.ExitServerClosed,
			says: []string{closedHeadline, "full for today", "deploy-1",
				nothingDeployed, "Try again"},
		},
	}
	// EACH IS MEASURED, NOT CLAIMED. The table used to name four of the
	// eight codes the routing table stops on, and the name said "each" —
	// which is the species of defect this file is otherwise careful
	// about: a row that measures less than it is called. The check runs
	// against the routing table, so a code added to the contract needs
	// copy here rather than inheriting somebody else's silently.
	driven := map[wire.ErrorCode]bool{}
	for _, tc := range table {
		driven[tc.outcome.code] = true
	}
	for code, route := range publishRouting {
		if route != publishStop {
			continue
		}
		if !driven[code] {
			t.Errorf("the publish stops on %q and no row here says what a person "+
				"is told when it does", code)
		}
	}

	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			run := publishRun(t)
			run.script.publishOutcome = tc.outcome

			_, err := run.run()
			if err == nil {
				t.Fatal("the run continued past a refused publish")
			}
			text, code := renderedBytes(t, err)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			for _, want := range tc.says {
				if !strings.Contains(text, want) {
					t.Errorf("the failure never said %q:\n%s", want, text)
				}
			}
			for _, unwanted := range tc.neverSay {
				if strings.Contains(text, unwanted) {
					t.Errorf("the failure said %q:\n%s", unwanted, text)
				}
			}
		})
	}
}

// -------------------------------------------------------------------
// The race
// -------------------------------------------------------------------

// TestADeployThatIsNotReadyYetIsAskedAgain. The build log's terminal
// event is the builder's own claim and the record can still be catching
// up when this call is made — a race rather than a failure, and one this
// client resolves instead of reporting.
//
// It says so ONCE. A line per ask would turn a race that resolved in two
// seconds into a wall of text about a wait that ended fine.
func TestADeployThatIsNotReadyYetIsAskedAgain(t *testing.T) {
	run := publishRun(t)
	run.script.publishNotReadyFor = 2
	run.deps.PublishRetryInterval = time.Millisecond

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	if run.script.publishes != 3 {
		t.Errorf("the run asked %d times, want 3 — two refusals and the answer",
			run.script.publishes)
	}
	narrated := run.prompt.out.String()
	if got := strings.Count(narrated, buildStillBeingChecked); got != 1 {
		t.Errorf("the run said it was waiting %d times, want exactly 1:\n%s",
			got, narrated)
	}
	if got := lastNonEmptyLine(run.prompt.results.String()); got != publishedURL(defaultPublishSubdomain) {
		t.Errorf("the run printed %q, want the address", got)
	}
}

// TestADeployThatIsNeverReadyStopsAndSaysWhatItSaw. The asking is
// bounded by a window measured on the run's own clock, so a build that
// never resolves ends the run rather than hanging it.
//
// IT DOES NOT SAY THE BUILD FAILED, because nothing said so: the last
// thing the server reported was that it was still working. What the copy
// carries is that the run stopped asking, and that nothing has been
// deployed.
func TestADeployThatIsNeverReadyStopsAndSaysWhatItSaw(t *testing.T) {
	run := publishRun(t)
	run.script.publishOutcome = fails(http.StatusConflict, wire.CodeDeployNotReady,
		`this deploy is "building" and cannot be given an address yet`)
	run.deps.PublishRetryInterval = time.Millisecond
	// Ten seconds a reading against a thirty-second window: the asking
	// runs out in milliseconds of real time without a second bound being
	// invented for the test to hit.
	run.deps.Now = advancingClock(fixedNowLocal, 10*time.Second)

	_, err := run.run()
	if err == nil {
		t.Fatal("the run continued past a deploy that was never ready")
	}
	text, code := renderedBytes(t, err)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"could not be confirmed in time",
		nothingDeployed,
		"deploy-1",
		"`curious deploy` again",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the failure never said %q:\n%s", want, text)
		}
	}
	if run.script.publishes < 2 {
		t.Errorf("the run asked %d times, want more than one — a bound that stops "+
			"on the first answer is not a retry", run.script.publishes)
	}
	if run.script.publishes > 10 {
		t.Errorf("the run asked %d times, which is not a bounded wait",
			run.script.publishes)
	}
}

// -------------------------------------------------------------------
// The answer that never came
// -------------------------------------------------------------------

// TestAPublishWithNoAnswerClaimsNeitherOutcome. This is the create's own
// problem one call later: the request may have landed. A failure after
// it left this process is indistinguishable from one before it, so the
// only honest sentence names both possibilities — and says why there is
// no address to offer, because the address arrives in the answer that
// did not come.
func TestAPublishWithNoAnswerClaimsNeitherOutcome(t *testing.T) {
	run := publishRun(t)
	run.script.publishHangsUp = true

	_, err := run.run()
	if err == nil {
		t.Fatal("the run continued past a publish nothing answered")
	}
	text, code := renderedBytes(t, err)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{
		"may or may not",
		"deploy-1",
		"`curious deploy` again",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the failure never said %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, nothingDeployed) {
		t.Errorf("the failure claims nothing was deployed, which is the one thing "+
			"this client cannot know here:\n%s", text)
	}
	if run.script.publishes != 1 {
		t.Errorf("the publish was sent %d times, want exactly 1 — asking again is "+
			"not a way of finding out whether the first one landed",
			run.script.publishes)
	}
}

// TestEveryPublishFailureLeavesTheArchiveRemoved. A failure at the last
// step must leave the hand-off UNTAKEN, so the deferred tidy-up still
// runs: a run that handed over a file nobody will release is a temp file
// left on somebody's machine by an error path.
func TestEveryPublishFailureLeavesTheArchiveRemoved(t *testing.T) {
	// EVERY is measured, not asserted by naming four of eight. The stop
	// codes come from the routing table itself, so a code added to the
	// contract arrives here with a row of its own rather than leaving
	// the name overclaiming quietly.
	cases := []struct {
		name    string
		arrange func(*deployRun)
	}{}
	for code, route := range publishRouting {
		if route != publishStop {
			continue
		}
		code := code
		cases = append(cases, struct {
			name    string
			arrange func(*deployRun)
		}{string(code), func(r *deployRun) {
			r.script.publishOutcome = fails(http.StatusConflict, code,
				"the server refused it").after("60")
		}})
	}
	if len(cases) < 8 {
		t.Fatalf("the routing table names %d stop codes, so this row is about "+
			"fewer paths than the client has", len(cases))
	}

	for _, tc := range append(cases, []struct {
		name    string
		arrange func(*deployRun)
	}{
		{"nothing answered", func(r *deployRun) { r.script.publishHangsUp = true }},
		{"the label was not one", func(r *deployRun) { r.script.publishSubdomain = "" }},
		{"it was never ready", func(r *deployRun) {
			r.script.publishOutcome = fails(http.StatusConflict, wire.CodeDeployNotReady,
				"still building")
			r.deps.PublishRetryInterval = time.Millisecond
			r.deps.Now = advancingClock(fixedNowLocal, 10*time.Second)
		}},
	}...) {
		t.Run(tc.name, func(t *testing.T) {
			run := publishRun(t)
			tc.arrange(run)

			handoff, err := run.run()
			if err == nil {
				t.Fatal("the run continued past a refused publish")
			}
			if handoff != nil {
				t.Error("a refused run handed over an archive, so the caller now owns " +
					"a file the failure path expected to remove itself")
			}
			if left := run.leftBehind(); len(left) != 0 {
				t.Errorf("the run left %v behind", left)
			}
		})
	}
}

// -------------------------------------------------------------------
// A wait that was cut off from outside
// -------------------------------------------------------------------

// waitingForever is a publish that answers "not ready" every time, and
// says when it has been asked.
type waitingForever struct {
	asks  int
	asked chan struct{}
}

func (w *waitingForever) DeployPublish(context.Context, string) (*wire.DeployPublishResponse, error) {
	w.asks++
	if w.asked != nil {
		close(w.asked)
		w.asked = nil
	}
	return nil, &api.APIError{Code: wire.CodeDeployNotReady, Message: "still building"}
}

// stoppedFromOutside drives the wait with a context that is ALREADY over,
// and returns what the run ends as.
//
// NOT A WHOLE RUN, AND NOT A CLOCK RACE. The first shape of these two
// rows gave a whole deploy a 150ms deadline and asserted publish copy —
// which held here and, on the second Windows run it ever had, killed the
// CREATE instead and asserted the wrong step's words. A wall-clock
// deadline racing a multi-step run is a margin nobody measured, which is
// the thing this project has a whole task about. The context is settled
// before the call, so both arms are decided rather than timed.
func stoppedFromOutside(t *testing.T, ctx context.Context) (*waitingForever, error) {
	t.Helper()
	client := &waitingForever{}
	_, err := publishDeploy(ctx, publishDeps{
		Client:        client,
		DeployID:      "deploy-1",
		Render:        silentRenderer{},
		Now:           func() time.Time { return fixedNowLocal },
		RetryInterval: time.Minute,
	})
	return client, err
}

// TestACancelledRunSaysCancelledAndClaimsNothingAboutTheServer.
//
// THE TWO ARMS OF ONE SELECT USED TO RETURN THE SAME FAILURE, and that
// failure was written for the other one: "the server was still working
// on it 30s later. curious stopped asking rather than wait
// indefinitely." Neither clause is true of a cancellation — seconds may
// have passed, and it was not curious that stopped. Somebody who
// interrupted their own run was handed a story about the far end.
//
// REQUIRED MUTATION, run 2026-09-09: collapse the arms, returning
// publishNotConfirmedFailure for ctx.Done() again. Reds twice — on the
// exit code (1, want 130) and on the copy, which starts naming a window
// nobody waited out.
func TestACancelledRunSaysCancelledAndClaimsNothingAboutTheServer(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	client, err := stoppedFromOutside(t, ctx)
	if err == nil {
		t.Fatal("a cancelled wait reported success")
	}
	if client.asks == 0 {
		t.Fatal("the wait was never entered, so this row is not about it")
	}

	text, code := renderedBytes(t, err)
	if code != 130 {
		t.Errorf("exit code = %d, want 130 — a run that was stopped rather than "+
			"finished:\n%s", code, text)
	}
	if got := strings.TrimSpace(text); got != "cancelled" {
		t.Errorf("a cancelled run said more than the word:\n%q", got)
	}
	// THE CLAIM THAT MUST NOT BE THERE. Anything about the server, the
	// window, or who stopped asking is invented at this point.
	for _, forbidden := range []string{"server", "30s", "stopped asking", "indefinitely"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("the cancellation copy claims %q:\n%s", forbidden, text)
		}
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ctx.Err() was discarded: %v", err)
	}
	if !errors.Is(err, ui.ErrInterrupted) {
		t.Errorf("the failure is not marked as an interruption: %v", err)
	}
}

// TestADeadlineOnTheRunStillReadsAsATimeout is the other arm, and the
// control for the row above: the copy that was wrong for a cancellation
// is exactly right here, so it must still be produced.
func TestADeadlineOnTheRunStillReadsAsATimeout(t *testing.T) {
	// A deadline in the past: already exceeded, with no waiting.
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()

	client, err := stoppedFromOutside(t, ctx)
	if err == nil {
		t.Fatal("a wait whose deadline had passed reported success")
	}
	if client.asks == 0 {
		t.Fatal("the wait was never entered, so this row is not about it")
	}

	text, code := renderedBytes(t, err)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 — a deadline is a failure, not a "+
			"cancellation:\n%s", code, text)
	}
	if !strings.Contains(text, "could not be confirmed in time") {
		t.Errorf("a deadline lost the copy written for it:\n%s", text)
	}
	if errors.Is(err, ui.ErrInterrupted) {
		t.Errorf("a deadline was reported as an interruption: %v", err)
	}
}

// -------------------------------------------------------------------
// A label that is not one
// -------------------------------------------------------------------

// TestALabelThatIsNotOneIsRefusedRatherThanPrinted.
//
// THE ONE FIELD A CLIENT CANNOT DERIVE is also the one it cannot check
// against anything else. A 200 carrying "" used to reach the end of the
// command and print "https://.curiously.dev" as the site's address, with
// exit 0 — on the single line a script reads.
//
// REQUIRED MUTATION, run 2026-09-09: remove the label check from
// DeployPublish. The empty-label row reds with stdout carrying
// "https://.curiously.dev" and an exit code of 0.
func TestALabelThatIsNotOneIsRefusedRatherThanPrinted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		label string
	}{
		{"nothing at all", ""},
		{"a host rather than a label", "evil.com"},
		{"a hyphen at the end", "quick-koala-"},
		{"a byte no label may carry", "quick koala"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := publishRun(t)
			run.script.publishSubdomain = tc.label

			_, err := run.run()
			if err == nil {
				t.Fatal("the run printed an address built from a label that is not one")
			}
			text, code := renderedBytes(t, err)
			if code == 0 {
				t.Errorf("a refused label cost nothing:\n%s", text)
			}
			// NO ADDRESS ON STDOUT. The build log legitimately went
			// there and stays; what must not be there is anything a
			// script reading for the site's address could take for one.
			printed := run.prompt.results.String()
			if strings.Contains(printed, "//") {
				t.Errorf("stdout carried something shaped like an address for a "+
					"label that is not one: %q", printed)
			}
			if strings.Contains(printed, siteBaseDomain) {
				t.Errorf("stdout named the site domain for a label that is not "+
					"one: %q", printed)
			}
			if !strings.Contains(text, "deploy-1") {
				t.Errorf("the failure never named the deploy:\n%s", text)
			}
			// NOT REPORTED AS A TRANSPORT FAILURE. The answer arrived.
			if strings.Contains(text, "may or may not") {
				t.Errorf("an answer that arrived was reported as one that might "+
					"not have:\n%s", text)
			}
		})
	}

	// The control: the ordinary label still produces an address.
	t.Run("control: a real label still prints", func(t *testing.T) {
		ok := publishRun(t)
		handoff, err := ok.run()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}
		defer handoff.Release()
		if got := lastNonEmptyLine(ok.prompt.results.String()); got !=
			publishedURL(defaultPublishSubdomain) {
			t.Errorf("the address stopped being printed: %q", got)
		}
	})
}

// -------------------------------------------------------------------
// The bound, in real time
// -------------------------------------------------------------------

// alwaysBuilding answers "not ready" for ever, and counts.
type alwaysBuilding struct{ asks int }

func (a *alwaysBuilding) DeployPublish(context.Context, string) (*wire.DeployPublishResponse, error) {
	a.asks++
	return nil, &api.APIError{Code: wire.CodeDeployNotReady, Message: "still building"}
}

// silentRenderer takes the narration and says nothing about it.
type silentRenderer struct{}

func (silentRenderer) Step(string, ...any)   {}
func (silentRenderer) Result(string, ...any) {}

// TestTheAskingStopsInsideItsOwnWindow.
//
// THE ONE ROW HERE THAT IS NOT A WHOLE RUN, and the reason is the thing
// being measured. The window is a bound on REAL elapsed time; a whole
// run against the shipped window would spend thirty real seconds, which
// is not a row anybody keeps. So this drives the loop directly with the
// bound made small and the pace made large — the arrangement that makes
// the defect visible at all.
//
// WITHOUT THE CLAMP the sleep between two asks runs past the deadline it
// was just checked against, so the run takes window + interval. With the
// shipped numbers that is one second on thirty; with a pace injected
// through the seam beside it, it is the whole of the bound again.
//
// REQUIRED MUTATION, run 2026-09-09: remove the clamp, sleeping the full
// interval. Reds with an elapsed time of about the interval rather than
// about the window.
func TestTheAskingStopsInsideItsOwnWindow(t *testing.T) {
	const window = 150 * time.Millisecond
	client := &alwaysBuilding{}

	started := time.Now()
	_, err := publishDeploy(t.Context(), publishDeps{
		Client:        client,
		DeployID:      "deploy-1",
		Render:        silentRenderer{},
		Now:           time.Now,
		ConfirmWindow: window,
		// Ten times the window. Unclamped, one sleep of this is the
		// whole of the row's failure.
		RetryInterval: 10 * window,
	})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("the asking never stopped")
	}
	if client.asks < 2 {
		t.Fatalf("the run asked %d times — a bound that stops on the first "+
			"answer is not what this row is about", client.asks)
	}
	// One window, plus room for the latency of the ask that discovers
	// the window has closed. Half a window of slack on a bound this
	// small is generous and still nowhere near interval.
	if limit := window + window/2; elapsed > limit {
		t.Errorf("the asking took %s against a %s window — the sleep is not "+
			"clamped to what is left of it (the pace was %s)",
			elapsed.Round(time.Millisecond), window, 10*window)
	}
}

// -------------------------------------------------------------------
// The contract's own retry-after set
// -------------------------------------------------------------------

// TestEveryCodeTheContractSaysCarriesATimeRendersOne is keyed to
// wire.CarriesRetryAfter rather than to a list typed here, so it cannot
// drift from the contract: a code that joins the set arrives already
// needing an answer, and one that leaves it cannot be left behind.
//
// Whether this endpoint emits either code today is the server's
// business and is recorded as an open question. A client that threw
// away a time it was promised would be wrong either way.
//
// REQUIRED MUTATION, run 2026-09-09: delete the CodeRateLimited and
// CodeCapacityClosed cases so both fall to the standing copy. Reds on
// both, on the missing retry time.
func TestEveryCodeTheContractSaysCarriesATimeRendersOne(t *testing.T) {
	carried := 0
	for _, code := range wire.AllErrorCodes {
		if !wire.CarriesRetryAfter(code) {
			continue
		}
		carried++
		t.Run(string(code), func(t *testing.T) {
			run := publishRun(t)
			run.script.publishOutcome = fails(http.StatusTooManyRequests, code,
				"not right now").after("90")

			_, err := run.run()
			if err == nil {
				t.Fatalf("%s did not stop the run", code)
			}
			text, _ := renderedBytes(t, err)
			if !strings.Contains(text, "Try again") {
				t.Errorf("%s threw away the time the contract says it carries:\n%s",
					code, text)
			}
			if !strings.Contains(text, nothingDeployed) {
				t.Errorf("%s never said nothing was deployed:\n%s", code, text)
			}
		})
	}
	if carried == 0 {
		t.Fatal("the contract says no code carries Retry-After, so this row " +
			"ranged over nothing")
	}
}

// TestAClosedDoorAtThePublishCostsTheScopedCode. The door is not a
// fault, and the scoped exit code is how a script tells "come back
// later" from "this went wrong".
func TestAClosedDoorAtThePublishCostsTheScopedCode(t *testing.T) {
	run := publishRun(t)
	run.script.publishOutcome = fails(http.StatusTooManyRequests,
		wire.CodeCapacityClosed, "full for today").after("3600")

	_, err := run.run()
	if err == nil {
		t.Fatal("a closed door did not stop the run")
	}
	text, code := renderedBytes(t, err)
	if code != ui.ExitServerClosed {
		t.Errorf("exit code = %d, want %d:\n%s", code, ui.ExitServerClosed, text)
	}
}
