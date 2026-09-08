package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The real terminal and the real HTTP client must satisfy the seams this
// path asks for. Without these lines either interface could drift into
// something only a test double implements, and the drift would not show
// up until the deploy sequence tried to pass a real one.
var (
	_ CapacityPrompter = (*ui.UI)(nil)
	_ WaitlistJoiner   = (*api.Client)(nil)
)

// capacityScript is a real HTTP server speaking the real wire contract,
// so every row below goes through the shipped client's own decoding: the
// capacity body, the error envelope and the retry policy are exercised
// rather than simulated.
//
// Its outcomes are STICKY rather than queued, unlike the login double's,
// and the difference is the retry policy rather than taste: both routes
// here are documented idempotent, so the client makes up to three
// attempts at a 5xx, and a queue would answer the second attempt with
// whatever came next — turning a row about one server answer into a row
// about three different ones.
type capacityScript struct {
	mu sync.Mutex

	capacityCalls int
	signups       []wire.WaitlistRequest
	strays        []string

	// body is the success answer to the capacity check.
	body wire.CapacityResponse

	// capacityOutcome and waitlistOutcome are zero for success.
	capacityOutcome outcome
	waitlistOutcome outcome
}

func (s *capacityScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch r.URL.Path {
	case "/v1/capacity":
		s.capacityCalls++
		writeOutcome(w, s.capacityOutcome, s.body)
	case "/v1/waitlist":
		var req wire.WaitlistRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.signups = append(s.signups, req)
		writeOutcome(w, s.waitlistOutcome, wire.WaitlistResponse{})
	default:
		// Recorded rather than silently answered, so a row that dials a
		// path this contract does not define fails as a wiring bug
		// instead of as a routing result.
		s.strays = append(s.strays, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

// writeOutcome renders one scripted answer. A zero outcome is success.
func writeOutcome(w http.ResponseWriter, o outcome, success any) {
	w.Header().Set("Content-Type", "application/json")
	if o.code == "" {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(success)
		return
	}
	if o.retryAfter != "" {
		w.Header().Set("Retry-After", o.retryAfter)
	}
	status := o.status
	if status == 0 {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wire.ErrorResponse{
		Error: wire.Error{Code: o.code, Message: o.message},
	})
}

func (s *capacityScript) checks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capacityCalls
}

func (s *capacityScript) joined() []wire.WaitlistRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.WaitlistRequest(nil), s.signups...)
}

// watchForStrays fails the run if it dialled a path this contract does
// not define. Such a call would otherwise arrive dressed as a routing
// result: the double answers 404, the client decodes an unparseable
// body, and the run stops for a reason that has nothing to do with the
// row.
func watchForStrays(t *testing.T, script *capacityScript) {
	t.Helper()
	t.Cleanup(func() {
		script.mu.Lock()
		defer script.mu.Unlock()
		if len(script.strays) > 0 {
			t.Errorf("the run called %v, which this contract does not define",
				script.strays)
		}
	})
}

// TestTheCapacityDoubleNoticesAPathTheContractDoesNotDefine is the
// positive control for the stray-path check every run relies on. That
// check passes when nothing was recorded, which is exactly what a
// detector that records nothing also looks like.
func TestTheCapacityDoubleNoticesAPathTheContractDoesNotDefine(t *testing.T) {
	script := &capacityScript{}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/capacityy")
	if err != nil {
		t.Fatalf("dialling the double: %v", err)
	}
	_ = resp.Body.Close()

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.strays) != 1 || script.strays[0] != "/v1/capacityy" {
		t.Fatalf("the double recorded %v, want the one path it was asked for — "+
			"the check every other run relies on is blind", script.strays)
	}
}

// offerRun is the offer under test, wired to a scripted terminal and a
// real server — the shape the login flow hands it to, where the address
// is already known.
type offerRun struct {
	prompt *scriptedPrompt
	script *capacityScript
	offer  WaitlistOffer
}

func newOfferRun(t *testing.T) *offerRun {
	t.Helper()

	script := &capacityScript{}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)
	watchForStrays(t, script)

	client, err := api.New(srv.URL)
	if err != nil {
		t.Fatalf("building the client against %s: %v", srv.URL, err)
	}

	prompt := &scriptedPrompt{}
	return &offerRun{
		prompt: prompt,
		script: script,
		offer:  NewWaitlistOffer(prompt, client, func() time.Time { return fixedNowLocal }),
	}
}

// call makes the offer with a reset four hours out, which is the case
// every row here is about unless it says otherwise.
func (r *offerRun) call(ctx context.Context) error {
	return r.offer(ctx, "someone@example.com", fixedNowLocal.Add(4*time.Hour))
}

// seen is the narration and the stop together — everything the offer put
// on screen, in the order a person met it.
func (r *offerRun) seen(err error) string {
	return r.prompt.out.String() + "\n" + rendered(err)
}

// TestTheConfirmationPromisesNothingTheCompanyMustKeep. Capacity reopens
// and it is first-come; nothing is held for anybody. The tempting
// sentence — that a place is waiting — would be a promise somebody would
// have to keep, and this is where it would be read.
//
// The forbidden list is paired with a required phrase, so the row cannot
// pass against copy that says nothing at all.
//
// REQUIRED MUTATION, RUN: add "and your spot is ready" to joinedStop's
// Why.
func TestTheConfirmationPromisesNothingTheCompanyMustKeep(t *testing.T) {
	run := newOfferRun(t)
	run.prompt.confirms = []answer{yes()}

	seen := run.seen(run.call(t.Context()))
	if !strings.Contains(seen, "first-come") {
		t.Fatalf("the confirmation never said the one true thing about the list:\n%s",
			seen)
	}
	for _, forbidden := range []string{"spot is ready", "reserved", "saved"} {
		if strings.Contains(strings.ToLower(seen), forbidden) {
			t.Errorf("the confirmation contains %q, which is a promise somebody "+
				"would then have to keep:\n%s", forbidden, seen)
		}
	}
}

// TestDecliningSendsNothingAndSaysWhenToComeBack. The offer is an offer:
// a no is an answer rather than a slower yes.
//
// REQUIRED MUTATION, RUN: drop the `if !join` branch from offerWaitlist
// so a decline falls through to the signup.
func TestDecliningSendsNothingAndSaysWhenToComeBack(t *testing.T) {
	run := newOfferRun(t)
	run.prompt.confirms = []answer{no()}

	err := run.call(t.Context())
	if err == nil {
		t.Fatal("the offer reported nothing after a decline")
	}
	if got := run.script.joined(); len(got) != 0 {
		t.Fatalf("declining the offer still signed the person up: %v", got)
	}
	seen := run.seen(err)
	for _, want := range []string{nothingDeployed, "17:00 CET (+01:00)"} {
		if !strings.Contains(seen, want) {
			t.Errorf("a decline never said %q:\n%s", want, seen)
		}
	}
	// The question, and its default. Rendering "[Y/n]" while treating a
	// bare newline as no is a lie no caller can tell through Confirm's
	// signature, and the default is the half a row can check from here.
	if got := run.prompt.confirmAsks; len(got) != 1 || got[0].question != waitlistQuestion ||
		!got[0].defaultYes {
		t.Errorf("the offer was put as %v, want %q once with a yes default",
			got, waitlistQuestion)
	}
}

// TestASignupThatFailedSaysSoAndNamesSomewhereToGo. Somebody who
// believes they are on a list stops checking, and finds out days later
// that nothing was recorded.
//
// The attempt count is asserted as "at least one" deliberately: the call
// is documented idempotent, so the client retries a server fault, and a
// row demanding exactly one here would be asserting the retry policy by
// accident.
//
// REQUIRED MUTATION, RUN: discard the error from the waitlist call in
// offerWaitlist and fall through to joinedStop.
func TestASignupThatFailedSaysSoAndNamesSomewhereToGo(t *testing.T) {
	run := newOfferRun(t)
	run.prompt.confirms = []answer{yes()}
	run.script.waitlistOutcome = fails(http.StatusInternalServerError,
		wire.CodeInternal, "The list is not accepting anyone at the moment.")

	err := run.call(t.Context())
	if err == nil {
		t.Fatal("a failed signup was reported as nothing at all")
	}
	if got := len(run.script.joined()); got < 1 {
		t.Fatalf("the offer made %d signup attempts, want at least 1", got)
	}

	seen := run.seen(err)
	for _, want := range []string{
		"didn't get you onto the list",
		"The list is not accepting anyone at the moment.",
		supportAddress,
	} {
		if !strings.Contains(seen, want) {
			t.Errorf("a failed signup never said %q:\n%s", want, seen)
		}
	}
	if strings.Contains(seen, "You're on the list.") {
		t.Errorf("a failed signup told the person it worked:\n%s", seen)
	}
}

// TestTheOfferHonoursTheContextItIsHanded. The seam's own doc states
// what an implementation owes the run — it returns, it honours the
// context, it does not panic — and nothing enforces any of it, so the
// obligation lands here.
//
// A cancelled run whose signup carried on regardless would keep working
// right up until somebody pressed Ctrl-C and watched a call outlive it.
//
// REQUIRED MUTATION, RUN: make the waitlist call in offerWaitlist use
// context.Background().
func TestTheOfferHonoursTheContextItIsHanded(t *testing.T) {
	// The positive control: with a live context the same offer signs the
	// person up. Without it, "the signup did not happen" is satisfied by
	// an offer that never signs anybody up.
	t.Run("positive control: a live context signs the person up", func(t *testing.T) {
		run := newOfferRun(t)
		run.prompt.confirms = []answer{yes()}

		err := run.call(t.Context())
		if got := len(run.script.joined()); got != 1 {
			t.Fatalf("the offer made %d signups against a live context, want 1", got)
		}
		if !strings.Contains(run.seen(err), "You're on the list.") {
			t.Errorf("a completed signup was not reported as one:\n%s", run.seen(err))
		}
	})

	run := newOfferRun(t)
	run.prompt.confirms = []answer{yes()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := run.call(ctx)
	if err == nil {
		t.Fatal("the offer reported nothing after a cancelled signup")
	}
	if got := len(run.script.joined()); got != 0 {
		t.Errorf("the offer reached the server %d times on a cancelled context", got)
	}
	if got := rendered(err); !strings.Contains(got, "didn't get you onto the list") {
		t.Errorf("a signup that never happened was not reported as one:\n%s", got)
	}
}

// TestTheOfferFitsTheLoginSeam wires the real offer into the real login
// flow, which is the only place the two are actually joined.
//
// THE ADDRESS IS NOT ASKED FOR TWICE, and that is the shape decision the
// seam records: the login path already has an address, so an offer that
// prompted internally would be this program forgetting something it was
// told a moment ago — and it would not fit a signature that takes one.
//
// REQUIRED MUTATION, RUN: build the offer with promptedAddress(p) rather
// than knownAddress(email) in NewWaitlistOffer.
func TestTheOfferFitsTheLoginSeam(t *testing.T) {
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	auth := &apiScript{token: "tok-scripted-value-never-printed"}
	waitlist := &capacityScript{}
	srv := httptest.NewServer(&pairedScript{auth: auth, waitlist: waitlist})
	t.Cleanup(srv.Close)
	watchForStrays(t, waitlist)
	t.Cleanup(func() {
		auth.mu.Lock()
		defer auth.mu.Unlock()
		if len(auth.strays) > 0 {
			t.Errorf("the run called %v, which this contract does not define",
				auth.strays)
		}
	})

	client, err := api.New(srv.URL)
	if err != nil {
		t.Fatalf("building the client against %s: %v", srv.URL, err)
	}

	prompt := &scriptedPrompt{
		emails: []answer{says("someone@example.com")},
		lines:  []answer{says("111111")},
		confirms: []answer{
			no(),  // marketing
			yes(), // the waitlist offer
		},
	}
	auth.verifyOutcomes = []outcome{
		fails(http.StatusServiceUnavailable, wire.CodeCapacityClosed,
			"We're full for today.").after("14400"),
	}

	clock := func() time.Time { return fixedNowLocal }
	stop := Login(t.Context(), LoginDeps{
		Prompt:   prompt,
		Auth:     client,
		Endpoint: srv.URL,
		Now:      clock,
		Save: func(ui.Secret, string) error {
			t.Error("a run that never verified wrote a token")
			return nil
		},
		Offer: NewWaitlistOffer(prompt, client, clock),
	})

	if stop == nil {
		t.Fatal("a shut cap at the login ended the run at success")
	}
	joined := waitlist.joined()
	if len(joined) != 1 || joined[0].Email != "someone@example.com" {
		t.Fatalf("the offer made %v, want one signup carrying the address the "+
			"person typed at the login", joined)
	}
	if got := len(prompt.emailAsks); got != 1 {
		t.Errorf("the address was asked for %d times across the run, want 1 — the "+
			"offer takes the address it is handed", got)
	}
	if code := exitCodeFor(t, stop); code != ui.ExitServerClosed {
		t.Errorf("exit code %d, want %d — the hand-off owns the words and the flow "+
			"owns the cost", code, ui.ExitServerClosed)
	}
	if !strings.Contains(prompt.out.String()+rendered(stop), "first-come") {
		t.Errorf("the offer's confirmation never reached the reader:\n%s\n%s",
			prompt.out.String(), rendered(stop))
	}
}

// pairedScript puts the login double and the capacity double behind one
// address, which is what the wired offer needs: a login that meets a
// shut cap and then signs somebody up talks to both.
//
// Each half keeps its OWN stray-path detector over its own routes, which
// is why this is a router rather than a third double: one double serving
// every path this contract defines could not notice a call landing in
// the wrong half.
type pairedScript struct {
	auth     *apiScript
	waitlist *capacityScript
}

func (p *pairedScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/waitlist" || r.URL.Path == "/v1/capacity" {
		p.waitlist.ServeHTTP(w, r)
		return
	}
	p.auth.ServeHTTP(w, r)
}

// TestEveryStopOnThisPathNamesAnAction. The third part of a failure is
// the product: a message that stops a run without naming something the
// reader can do leaves them to guess, and the reader is usually somebody
// deploying their first site.
//
// The list is written out rather than discovered, and that is a stated
// limit rather than an oversight: nothing here can walk the constructors
// of a package, so a stop added later is not covered until somebody adds
// it. What the row buys is that every stop that exists today is checked,
// and it is the row a reviewer meets when a new one is written.
//
// REQUIRED MUTATION, RUN: empty the Next of declinedStop.
func TestEveryStopOnThisPathNamesAnAction(t *testing.T) {
	reset := fixedNowLocal.Add(4 * time.Hour)
	broken := errors.New("the server said no")

	for _, row := range []struct {
		name string
		f    *ui.Failure
	}{
		{"shut, with the server's own words", closedCapacityFailure("full", reset, fixedNowLocal)},
		{"shut, with nothing from the server", closedCapacityFailure("", reset, fixedNowLocal)},
		{"declined", declinedStop(reset, fixedNowLocal)},
		{"nobody to ask", noTerminalStop(reset, fixedNowLocal)},
		{"joined", joinedStop("someone@example.com", reset, fixedNowLocal)},
		{"the signup failed", signupFailedStop(broken, reset, fixedNowLocal)},
		{"no reset time to name", declinedStop(time.Time{}, fixedNowLocal)},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.f.What == "" || row.f.Why == "" || row.f.Next == "" {
				t.Errorf("half-filled failure (what=%q why=%q next=%q)",
					row.f.What, row.f.Why, row.f.Next)
			}
			whole := strings.Join([]string{row.f.What, row.f.Why, row.f.Next}, "\n")
			// A person whose deploy stopped has no way of knowing, from
			// out here, whether their files went anywhere.
			if !strings.Contains(whole, "Nothing has been uploaded") {
				t.Errorf("the stop never says whether anything was uploaded:\n%s", whole)
			}
		})
	}
}

// TestTheSupportAddressHasOneHome. An address typed into a string is an
// address that exists in two places the next time anybody needs one, and
// the two spellings are then one typo apart.
//
// WHAT IT DOES NOT SEE, said here rather than left to be discovered: it
// reads THIS PACKAGE's shipped sources and no others, so a second
// spelling in another package is outside its view. That is the scope the
// constant has today; a second consumer arriving is the moment to widen
// both.
//
// REQUIRED MUTATION, RUN: write the address inline in signupFailedStop
// instead of referencing the constant.
func TestTheSupportAddressHasOneHome(t *testing.T) {
	sources := shippedSources(t)
	if len(sources) == 0 {
		t.Fatal("this row read no sources at all")
	}

	// The subject exists. Without this, "no second home" and "read a
	// directory with no address in it at all" are the same green.
	declared := 0
	var offenders []string
	for path, body := range sources {
		for i, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "supportAddress = ") {
				declared++
				continue
			}
			// THE ADDRESS ITSELF, not a quoted literal of it. The first
			// version of this row looked for the whole-literal spelling
			// and was measured against the duplication that actually
			// happens — an address folded into a sentence, where no
			// closing quote follows it — and came back green. A row that
			// can only see one spelling of a value is a spelling checker.
			if !strings.Contains(line, supportAddress) {
				continue
			}
			offenders = append(offenders, fmt.Sprintf("%s:%d", path, i+1))
		}
	}
	if declared != 1 {
		t.Fatalf("the constant is declared %d times in this package's shipped "+
			"sources, want exactly 1 — with none, this row is scanning for a value "+
			"that is not here and can only report a clean sweep", declared)
	}
	if len(offenders) > 0 {
		t.Errorf("the support address is written out at %s; reference the "+
			"supportAddress constant instead", strings.Join(offenders, ", "))
	}
}

// shippedSources reads every Go source in this package that is compiled
// into the binary — test files excluded, because a fixture is allowed to
// spell a value out and is not shipped anywhere.
func shippedSources(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package's own directory: %v", err)
	}
	sources := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		sources[name] = string(body)
	}
	return sources
}
