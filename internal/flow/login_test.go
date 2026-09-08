package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The real terminal and the real HTTP client must satisfy the seams this
// package asks for. Without these two lines either interface could drift
// into something only a test double implements, and the drift would not
// show up until the deploy sequence tried to pass a real one.
var (
	_ LoginPrompter = (*ui.UI)(nil)
	_ Authenticator = (*api.Client)(nil)
)

// errScriptExhausted is what the terminal double returns when a run asks
// it more questions than the row scripted. It is a DISTINCT sentinel
// rather than the cancellation one on purpose: a cancellation exits 0
// and would let a run that loops forever pass as a deliberate Ctrl-D.
var errScriptExhausted = errors.New("the scripted terminal ran out of answers")

// answer is one scripted reply. Text and Yes are read by different
// prompts; Err is how a row spells Ctrl-D or a broken pipe.
//
// blank is the one that needs a word. A bare newline at a yes/no prompt
// resolves to whatever DEFAULT the caller asked with, and that
// resolution belongs to the terminal rather than to this flow — so the
// double performs it here too. Without that, a row named for a bare
// newline would be measuring the answer the script happened to write
// down, and would keep passing with the default inverted.
type answer struct {
	text  string
	yes   bool
	blank bool
	err   error
}

func says(s string) answer { return answer{text: s} }
func yes() answer          { return answer{yes: true} }
func no() answer           { return answer{yes: false} }
func blank() answer        { return answer{blank: true} }
func aborts() answer       { return answer{err: ui.ErrAborted} }

// confirmAsk records one yes/no question exactly as it was put: the
// wording AND the default, because rendering "[Y/n]" while treating a
// blank line as no is a lie no caller can tell through that signature,
// and the default is the half a row can check from out here.
type confirmAsk struct {
	question   string
	defaultYes bool
}

// scriptedPrompt stands in for the terminal. It writes narration into a
// buffer so a row can compare BYTES rather than a reconstruction of
// them, and it records every question so "asked exactly once" is a fact
// about the run rather than a claim about it.
//
// It is a double rather than a real terminal driven by scripted stdin,
// and that is a deviation worth stating where its reader is: the real
// type decides "may I ask a question" from whether stdin AND stderr are
// both terminals, which a test cannot portably arrange on the three
// platforms this ships to — Windows has no pty to open. The seam is
// pinned to the real type by the compile-time assertion above, which is
// the same trade the pre-flight renderer's own double already makes.
type scriptedPrompt struct {
	out bytes.Buffer

	// results is STDOUT, and it is a SECOND buffer rather than more of
	// the first because the stream split is a claim two sinks can make
	// and one cannot. Narration, prompts and failures go to out; the
	// user's own build output goes here, so "a redirected stdout collects
	// the build log and nothing else" is something a row can check.
	results bytes.Buffer

	emails   []answer
	lines    []answer
	confirms []answer

	emailAsks   []string
	lineAsks    []string
	confirmAsks []confirmAsk

	// notInteractive makes every prompt report that there was nobody to
	// ask, which is what the real terminal does with no TTY on either
	// half of a question.
	notInteractive bool

	// transcript is everything this terminal did — narration and
	// questions interleaved, in the order it happened.
	//
	// The buffer and the per-prompt slices above cannot answer an
	// ORDERING question between the two: a question is recorded in a
	// slice and a line is written to a buffer, so a run that printed its
	// explanation after asking looks identical to one that printed it
	// before. Some rules are orders — nothing asks for an address until
	// the offer it is for has been accepted — and this is what can see
	// one.
	transcript []string
}

func (s *scriptedPrompt) Step(format string, args ...any) {
	fmt.Fprintf(&s.out, format+"\n", args...)
	s.transcript = append(s.transcript, "said: "+fmt.Sprintf(format, args...))
}

func (s *scriptedPrompt) Result(format string, args ...any) {
	fmt.Fprintf(&s.results, format+"\n", args...)
	s.transcript = append(s.transcript, "printed: "+fmt.Sprintf(format, args...))
}

func (s *scriptedPrompt) Email(prompt string) (string, error) {
	if s.notInteractive {
		return "", ui.ErrNotInteractive
	}
	s.emailAsks = append(s.emailAsks, prompt)
	s.transcript = append(s.transcript, "asked: "+prompt)
	next, ok := pop(&s.emails)
	if !ok {
		return "", errScriptExhausted
	}
	return next.text, next.err
}

func (s *scriptedPrompt) Line(prompt string) (string, error) {
	if s.notInteractive {
		return "", ui.ErrNotInteractive
	}
	s.lineAsks = append(s.lineAsks, prompt)
	s.transcript = append(s.transcript, "asked: "+prompt)
	next, ok := pop(&s.lines)
	if !ok {
		return "", errScriptExhausted
	}
	return next.text, next.err
}

func (s *scriptedPrompt) Confirm(question string, defaultYes bool) (bool, error) {
	if s.notInteractive {
		return false, ui.ErrNotInteractive
	}
	s.confirmAsks = append(s.confirmAsks, confirmAsk{question: question, defaultYes: defaultYes})
	s.transcript = append(s.transcript, "asked: "+question)
	next, ok := pop(&s.confirms)
	if !ok {
		return false, errScriptExhausted
	}
	if next.blank {
		// What the real prompt does with an empty line, so a row can say
		// "a bare newline" and mean it.
		return defaultYes, next.err
	}
	return next.yes, next.err
}

func pop(queue *[]answer) (answer, bool) {
	if len(*queue) == 0 {
		return answer{}, false
	}
	next := (*queue)[0]
	*queue = (*queue)[1:]
	return next, true
}

// asked counts how many times one question was put.
func (s *scriptedPrompt) asked(question string) int {
	n := 0
	for _, ask := range s.confirmAsks {
		if ask.question == question {
			n++
		}
	}
	return n
}

// outcome is one scripted HTTP answer. A zero Code means success.
type outcome struct {
	status     int
	code       wire.ErrorCode
	message    string
	retryAfter string
}

func fails(status int, code wire.ErrorCode, message string) outcome {
	return outcome{status: status, code: code, message: message}
}

func (o outcome) after(retryAfter string) outcome {
	o.retryAfter = retryAfter
	return o
}

// apiScript is a real HTTP server speaking the real wire contract, so
// every row below goes through the shipped client's own decoding: the
// error envelope, the unknown-code passthrough, and the Retry-After
// parse are exercised rather than simulated.
type apiScript struct {
	mu sync.Mutex

	starts   []wire.AuthStartRequest
	verifies []wire.AuthVerifyRequest
	strays   []string

	startOutcomes  []outcome
	verifyOutcomes []outcome

	token string
}

func (s *apiScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch r.URL.Path {
	case "/v1/auth/start":
		var req wire.AuthStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.starts = append(s.starts, req)
		s.reply(w, next(&s.startOutcomes), struct{}{})
	case "/v1/auth/verify":
		var req wire.AuthVerifyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.verifies = append(s.verifies, req)
		s.reply(w, next(&s.verifyOutcomes), wire.AuthVerifyResponse{Token: s.token})
	default:
		// Recorded rather than silently answered, so a row that dials a
		// path this contract does not define fails as a wiring bug
		// instead of as a routing result.
		s.strays = append(s.strays, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *apiScript) reply(w http.ResponseWriter, o outcome, success any) {
	if o.code == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(success)
		return
	}
	if o.retryAfter != "" {
		w.Header().Set("Retry-After", o.retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	status := o.status
	if status == 0 {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wire.ErrorResponse{
		Error: wire.Error{Code: o.code, Message: o.message},
	})
}

// next pops one scripted outcome; an exhausted queue answers success, so
// a row only has to script the answers it is actually about.
func next(queue *[]outcome) outcome {
	if len(*queue) == 0 {
		return outcome{}
	}
	o := (*queue)[0]
	*queue = (*queue)[1:]
	return o
}

func (s *apiScript) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.starts)
}

func (s *apiScript) verifyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.verifies)
}

func (s *apiScript) verifyBodies() []wire.AuthVerifyRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.AuthVerifyRequest(nil), s.verifies...)
}

func (s *apiScript) startBodies() []wire.AuthStartRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wire.AuthStartRequest(nil), s.starts...)
}

// offerCapture is what the waitlist seam saw. The seam is stubbed here
// so this flow is testable without the offer that will eventually be
// wired into it; the real hand-off is asserted where the two are wired
// together.
type offerCapture struct {
	called   int
	email    string
	resetsAt time.Time

	// ctxCarried reports whether the offer was handed the run's OWN
	// context rather than a fresh one. A seam that CAN carry a context
	// is not a seam that DOES, and an offer given a background context
	// keeps working right up until somebody presses Ctrl-C and the call
	// it makes carries on regardless.
	ctxCarried bool
}

// ctxMarker is how a row tells the run's context apart from any other.
type ctxMarker struct{}

// fixedNow is the clock every row that renders a time runs against, so a
// wall-clock assertion is about the arithmetic rather than about when
// the suite happened to run.
var fixedNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// loginRun is one configured run: the terminal double, the scripted
// server, and the dependencies Login is called with.
type loginRun struct {
	prompt *scriptedPrompt
	script *apiScript
	deps   LoginDeps
	offer  *offerCapture

	// saved is what the injected writer was handed, for the rows that do
	// not go all the way to a file.
	savedToken    ui.Secret
	savedEndpoint string
	saveErr       error
}

// newLoginRun wires a run against a real server and a real client.
//
// It ALWAYS points the config path at a temporary file, whether or not
// the row goes near one. A row that forgot would otherwise write into
// whoever is running the suite — the one failure mode a test harness
// must not have.
func newLoginRun(t *testing.T) *loginRun {
	t.Helper()

	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	script := &apiScript{token: "tok-scripted-value-never-printed"}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	client, err := api.New(srv.URL)
	if err != nil {
		t.Fatalf("building the client against %s: %v", srv.URL, err)
	}

	// A request to a path this contract does not define is a wiring bug,
	// and it would otherwise arrive dressed as a routing result: the
	// server answers 404, the client decodes an unparseable body, and
	// the run stops for a reason that has nothing to do with the row.
	t.Cleanup(func() {
		script.mu.Lock()
		defer script.mu.Unlock()
		if len(script.strays) > 0 {
			t.Errorf("the run called %v, which this contract does not define",
				script.strays)
		}
	})

	run := &loginRun{
		prompt: &scriptedPrompt{},
		script: script,
		offer:  &offerCapture{},
	}
	run.deps = LoginDeps{
		Prompt:   run.prompt,
		Auth:     client,
		Endpoint: srv.URL,
		Now:      func() time.Time { return fixedNow },
		Save: func(token ui.Secret, issuedAgainst string) error {
			run.savedToken, run.savedEndpoint = token, issuedAgainst
			return run.saveErr
		},
		Offer: func(ctx context.Context, email string, resetsAt time.Time) error {
			run.offer.called++
			run.offer.email = email
			run.offer.resetsAt = resetsAt
			carried, _ := ctx.Value(ctxMarker{}).(string)
			run.offer.ctxCarried = carried == "carried"
			return ui.NewFailure("Capacity is closed.", "stub", "stub")
		},
	}
	return run
}

func (r *loginRun) run(t *testing.T) error {
	t.Helper()
	return Login(context.WithValue(t.Context(), ctxMarker{}, "carried"), r.deps)
}

// output is everything the run put in front of a person.
func (r *loginRun) output() string { return r.prompt.out.String() }

// rendered turns an error into the text a person would actually see, so
// a row asserting on a message asserts on the shipped rendering rather
// than on a reconstruction of it.
func rendered(err error) string {
	if err == nil {
		return ""
	}
	var f *ui.Failure
	if errors.As(err, &f) {
		return strings.Join([]string{f.What, f.Why, f.Next}, "\n")
	}
	return err.Error()
}

// -------------------------------------------------------------------
// The rule this whole flow exists to keep.
// -------------------------------------------------------------------

// TestTheServerDoubleNoticesAPathTheContractDoesNotDefine is the
// positive control for the stray-path check every run in this file
// carries. That check passes when nothing was recorded, which is exactly
// what a detector that records nothing also looks like.
func TestTheServerDoubleNoticesAPathTheContractDoesNotDefine(t *testing.T) {
	script := &apiScript{}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/auth/startt")
	if err != nil {
		t.Fatalf("dialling the double: %v", err)
	}
	_ = resp.Body.Close()

	script.mu.Lock()
	defer script.mu.Unlock()
	if len(script.strays) != 1 || script.strays[0] != "/v1/auth/startt" {
		t.Fatalf("the double recorded %v, want the one path it was asked for — "+
			"the check every other run relies on is blind", script.strays)
	}
}

// TestLoginNeverRestartsAtTheEmailPrompt is the rule made a measurement:
// three wrong codes and two resends, and the address is asked for once.
func TestLoginNeverRestartsAtTheEmailPrompt(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111"), says("222222"), says("333333")}
	run.prompt.confirms = []answer{
		no(),     // marketing
		yes(),    // resend after the first wrong code
		yes(),    // resend after the second
		aborts(), // Ctrl-D at the third, which is how the run ends
	}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "That code is not right."),
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "That code is not right."),
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "That code is not right."),
	}

	err := run.run(t)
	if !errors.Is(err, ui.ErrAborted) {
		t.Fatalf("run ended with %v, want the cancellation sentinel", err)
	}
	if got := len(run.prompt.emailAsks); got != 1 {
		t.Errorf("the flow asked for the address %d times, want exactly 1: %v",
			got, run.prompt.emailAsks)
	}
	if got := run.script.startCount(); got != 3 {
		t.Errorf("auth/start was called %d times, want 3 (the first plus two resends)", got)
	}
	if got := run.script.verifyCount(); got != 3 {
		t.Errorf("auth/verify was called %d times, want 3", got)
	}
}

// TestNoFailureEdgeAssignsTheEmailState reads the machine itself, and it
// is here because the row above can only speak for the paths it scripts.
// The rule is that NO failure edge points back at the first state, and a
// failure edge added tomorrow would satisfy every behavioural row in
// this file while breaking it.
//
// So the assertion is structural: the first state is reached by the
// machine's own initialisation and by nothing else. An ordinary
// assignment putting it back is what a naive recovery path looks like in
// source, and it is what this refuses.
//
// WHAT IT DOES NOT SEE, said here rather than left to be discovered,
// because a guard with an undocumented blind spot reads as total
// coverage and ends the search. It matches assignment STATEMENTS, so it
// forbids the form the machine is actually written in and not every form
// a Go programmer could reach the same effect through: a helper
// returning the state, a switch expression, a slice of next-states
// indexed at run time. That is a live limit rather than a theoretical
// one — it holds today because every transition below is a plain
// assignment, and it would stop holding the moment somebody factors the
// transitions into a function. The behavioural row above is what covers
// the flow whatever shape the transitions take; this one covers the
// shapes the flow has.
//
// REQUIRED MUTATION, RUN: in login.go's recover state, change the
// declined-resend branch from `state = stateAskCode` to
// `state = stateAskEmail`. Nine rows red — this one plus
// TestRecoverDecliningAResendGoesStraightBackToTheCodePrompt, all three
// cooldown-hint rows, TestNoContractCodeEndsAsAnInternalFault,
// TestUnauthorizedEntersTheRetryLoop, TestInternalEntersTheRetryLoop and
// TestANetworkErrorEntersTheRetryLoop. The behavioural rows red because
// the run walks back to a prompt whose script is spent; this one reds
// because the edge exists at all, which is the half that keeps holding
// when somebody adds a recovery path nothing here scripts.
func TestNoFailureEdgeAssignsTheEmailState(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "login.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing the machine's own source: %v", err)
	}

	const state = "stateAskEmail"
	defines, assigns := 0, []string{}
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, rhs := range stmt.Rhs {
			ident, ok := rhs.(*ast.Ident)
			if !ok || ident.Name != state {
				continue
			}
			if stmt.Tok == token.DEFINE {
				defines++
				continue
			}
			assigns = append(assigns, fset.Position(stmt.Pos()).String())
		}
		return true
	})

	// The guard fails loudly if it scanned nothing: a machine that never
	// names its first state at all would otherwise report clean.
	if defines == 0 {
		t.Fatalf("found no declaration of the starting state %s in login.go — "+
			"this guard scanned nothing and cannot report anything", state)
	}
	if defines != 1 {
		t.Errorf("the starting state %s is established %d times, want exactly 1",
			state, defines)
	}
	if len(assigns) > 0 {
		t.Errorf("a transition assigns %s at %s — no failure edge may return to "+
			"the email prompt, which is the one rule this machine exists to keep",
			state, strings.Join(assigns, ", "))
	}
}

// -------------------------------------------------------------------
// Consent.
// -------------------------------------------------------------------

// TestLoginAsksAboutMarketingOnceAndCarriesTheAnswer pins both halves:
// the question is put once however many codes are typed, and the single
// answer travels on every verify body.
func TestLoginAsksAboutMarketingOnceAndCarriesTheAnswer(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111"), says("222222"), says("333333")}
	run.prompt.confirms = []answer{yes(), yes(), yes(), aborts()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
	}

	_ = run.run(t)

	if got := run.prompt.asked(consentQuestion); got != 1 {
		t.Errorf("the marketing question was asked %d times, want exactly 1: %v",
			got, run.prompt.confirmAsks)
	}
	bodies := run.script.verifyBodies()
	if len(bodies) != 3 {
		t.Fatalf("auth/verify was called %d times, want 3", len(bodies))
	}
	for i, body := range bodies {
		if !body.MarketingOptIn {
			t.Errorf("verify body %d carried marketing_opt_in=false, want the single "+
				"answer the user actually gave (true)", i)
		}
	}
}

// TestLoginConsentDefaultsToNo covers the default and the wording. The
// bracketed hint itself is rendered by the terminal from the default
// passed here, and is asserted where that rendering lives; what this
// flow owns is the default it asks with, which is the half that decides
// what a bare newline means.
func TestLoginConsentDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply answer
		want  bool
	}{
		{name: "a bare newline takes the default", reply: blank(), want: false},
		{name: "an explicit no opts out", reply: no(), want: false},
		{name: "an explicit yes opts in", reply: yes(), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newLoginRun(t)
			run.prompt.emails = []answer{says("someone@example.com")}
			run.prompt.lines = []answer{says("123456")}
			run.prompt.confirms = []answer{tc.reply}

			if err := run.run(t); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			bodies := run.script.verifyBodies()
			if len(bodies) != 1 {
				t.Fatalf("auth/verify was called %d times, want 1", len(bodies))
			}
			if bodies[0].MarketingOptIn != tc.want {
				t.Errorf("marketing_opt_in was %v, want %v", bodies[0].MarketingOptIn, tc.want)
			}

			var consent *confirmAsk
			for i, ask := range run.prompt.confirmAsks {
				if ask.question == consentQuestion {
					consent = &run.prompt.confirmAsks[i]
					break
				}
			}
			if consent == nil {
				t.Fatalf("the marketing question was never asked: %v", run.prompt.confirmAsks)
			}
			if consent.defaultYes {
				t.Errorf("the marketing question was asked with a yes default; it must " +
					"default to no, which is what renders it as an opt-in")
			}
		})
	}
}

// TestConsentIsAskedAfterTheCodeAndBeforeTheFirstVerify pins the
// placement, which is an argument rather than an accident: it precedes a
// verify because it is a field in that request, and it follows the code
// entry so that somebody who gives up waiting for the mail is never
// asked a marketing question at all.
func TestConsentIsAskedAfterTheCodeAndBeforeTheFirstVerify(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{aborts()}
	run.prompt.confirms = []answer{no()}

	err := run.run(t)
	if !errors.Is(err, ui.ErrAborted) {
		t.Fatalf("run ended with %v, want the cancellation sentinel", err)
	}
	if got := run.prompt.asked(consentQuestion); got != 0 {
		t.Errorf("the marketing question was asked %d times to somebody who "+
			"abandoned at the code prompt, want 0", got)
	}
	if got := run.script.verifyCount(); got != 0 {
		t.Errorf("auth/verify was called %d times, want 0", got)
	}
}

// -------------------------------------------------------------------
// The copy the server's silence forces.
// -------------------------------------------------------------------

// TestStartCopyNeverClaimsAMailWasSent is the non-enumeration contract
// showing up as copy. The endpoint answers identically whether it sent a
// code, declined, was over a budget or was in a cooldown, so the client
// says where a code WOULD go and how to react when nothing arrives.
func TestStartCopyNeverClaimsAMailWasSent(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("123456")}
	run.prompt.confirms = []answer{no()}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	out := run.output()

	// The RENDERED line, not merely the address somewhere in the run.
	// The success line at the end names the address too, so "the output
	// contains the address" is satisfied by a flow that never says where
	// a code would go — measured: dropping this line entirely left that
	// weaker assertion green.
	if !strings.Contains(out, fmt.Sprintf(addressLine, "someone@example.com")) {
		t.Errorf("the output never names the address a code would go to:\n%s", out)
	}
	if !strings.Contains(out, "spam folder") {
		t.Errorf("the output never tells the reader what to do when nothing "+
			"arrives:\n%s", out)
	}
	for _, forbidden := range []string{"on its way", "we've sent", "we have sent", "check your inbox"} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Errorf("the output states as fact something the server refuses to "+
				"tell this client (%q):\n%s", forbidden, out)
		}
	}
}

// TestEveryStartRendersTheSameHonestCopy — the first send and every
// resend. A resend that dropped the caveat would leave the one user who
// most needs it, the one whose mail is not arriving, without it.
func TestEveryStartRendersTheSameHonestCopy(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111"), says("222222")}
	run.prompt.confirms = []answer{no(), yes()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
	}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if got := run.script.startCount(); got != 2 {
		t.Fatalf("auth/start was called %d times, want 2", got)
	}
	if got := strings.Count(run.output(), spamFolderLine); got != 2 {
		t.Errorf("the honest caveat was rendered %d times across 2 sends, want 2:\n%s",
			got, run.output())
	}
	if got := strings.Count(run.output(), fmt.Sprintf(addressLine, "someone@example.com")); got != 2 {
		t.Errorf("the address was named %d times across 2 sends, want 2:\n%s",
			got, run.output())
	}
}

// -------------------------------------------------------------------
// The code prompt.
// -------------------------------------------------------------------

// TestAMisshapenCodeNeverReachesTheServer protects the server's own
// five-attempt budget: a five-digit typo must not spend one of them.
//
// The well-formed entry comes FIRST and is asserted to have been sent.
// Without that positive control, a client that never dials at all would
// pass every zero-request assertion below it.
func TestAMisshapenCodeNeverReachesTheServer(t *testing.T) {
	t.Run("positive control: a well-formed code is sent", func(t *testing.T) {
		run := newLoginRun(t)
		run.prompt.emails = []answer{says("someone@example.com")}
		run.prompt.lines = []answer{says("123456")}
		run.prompt.confirms = []answer{no()}

		if err := run.run(t); err != nil {
			t.Fatalf("login failed: %v", err)
		}
		if got := run.script.verifyCount(); got != 1 {
			t.Fatalf("auth/verify was called %d times, want 1 — the zero-request "+
				"rows below prove nothing if this one does not dial", got)
		}
	})

	for _, misshapen := range []string{"12345", "abcdef", "", "1234567", "12 34 5"} {
		t.Run(fmt.Sprintf("%q is refused locally", misshapen), func(t *testing.T) {
			run := newLoginRun(t)
			run.prompt.emails = []answer{says("someone@example.com")}
			run.prompt.lines = []answer{says(misshapen), says("123456")}
			run.prompt.confirms = []answer{no()}

			if err := run.run(t); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if got := run.script.verifyCount(); got != 1 {
				t.Errorf("auth/verify was called %d times, want 1 — the mis-shaped "+
					"entry must be refused before a request is spent", got)
			}
			if got := len(run.prompt.lineAsks); got != 2 {
				t.Errorf("the code prompt was rendered %d times, want 2 (the refusal "+
					"and the retry)", got)
			}
			// A prompt that simply appears again is indistinguishable
			// from one the program ignored. Saying what was wrong is the
			// difference between a re-ask and a glitch — measured:
			// deleting the line moved no other row in this file.
			if !strings.Contains(run.output(), misshapenCodeLine) {
				t.Errorf("the entry was re-asked with no reason given, so the "+
					"reader has no way to tell a refusal from a glitch:\n%s",
					run.output())
			}
		})
	}
}

// TestAnUnreadableCodePromptGivesUpRatherThanSpinning.
//
// The bound is surface this flow ADDED rather than something asked of
// it, so it gets its own row. Against a terminal an unbounded re-prompt
// is fine — a person gets bored and presses Ctrl-D — but against a pipe
// that keeps yielding something the parser rejects it is a program that
// hangs, and a hang is the hardest thing a user can report, because
// there is nothing on screen to quote.
//
// It ends in the prompt sentinel the program already knows how to
// render, not in the unexpected-failure copy: somebody whose entries
// could not be read has not found a fault in this program.
func TestAnUnreadableCodePromptGivesUpRatherThanSpinning(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{
		says("nope"), says("nope"), says("nope"), says("nope"), says("123456"),
	}
	run.prompt.confirms = []answer{no()}

	err := run.run(t)
	if !errors.Is(err, ui.ErrNoAnswer) {
		t.Fatalf("the run ended with %v, want the bounded prompt's own sentinel", err)
	}
	if got := run.script.verifyCount(); got != 0 {
		t.Errorf("auth/verify was called %d times, want 0", got)
	}
	// The fifth answer was good. That it was never read is the bound
	// doing its job rather than the script running dry.
	if got := len(run.prompt.lineAsks); got != 4 {
		t.Errorf("the code prompt was rendered %d times, want 4", got)
	}
	if code := exitCodeFor(t, err); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

// TestAPastedCodeIsTolerated — surrounding whitespace, a leading hash,
// and the spaces a mail client puts between groups of digits.
func TestAPastedCodeIsTolerated(t *testing.T) {
	for _, entry := range []string{"123456", "  123456  ", "#123456", "# 123 456", "123 456"} {
		t.Run(fmt.Sprintf("%q", entry), func(t *testing.T) {
			run := newLoginRun(t)
			run.prompt.emails = []answer{says("someone@example.com")}
			run.prompt.lines = []answer{says(entry)}
			run.prompt.confirms = []answer{no()}

			if err := run.run(t); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			bodies := run.script.verifyBodies()
			if len(bodies) != 1 {
				t.Fatalf("auth/verify was called %d times, want 1", len(bodies))
			}
			if bodies[0].Code != "123456" {
				t.Errorf("the server received %q, want %q", bodies[0].Code, "123456")
			}
		})
	}
}

// TestALeadingZeroSurvives. The code is a string end to end: parsed as a
// number, 012345 becomes 12345, which fails forever and looks like a
// server fault.
func TestALeadingZeroSurvives(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("012345")}
	run.prompt.confirms = []answer{no()}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	bodies := run.script.verifyBodies()
	if len(bodies) != 1 {
		t.Fatalf("auth/verify was called %d times, want 1", len(bodies))
	}
	if bodies[0].Code != "012345" {
		t.Errorf("the server received %q, want %q", bodies[0].Code, "012345")
	}
}

// -------------------------------------------------------------------
// The retry loop.
// -------------------------------------------------------------------

// TestRecoverDecliningAResendGoesStraightBackToTheCodePrompt. They may
// already have a valid code in another window and simply fat-fingered
// it, so declining must not spend one of the four hourly sends.
func TestRecoverDecliningAResendGoesStraightBackToTheCodePrompt(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111"), says("222222")}
	run.prompt.confirms = []answer{no(), no()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
	}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if got := run.script.startCount(); got != 1 {
		t.Errorf("auth/start was called %d times, want 1 — declining a resend must "+
			"not spend a send", got)
	}
	if got := run.script.verifyCount(); got != 2 {
		t.Errorf("auth/verify was called %d times, want 2", got)
	}
}

// TestRecoverAcceptingAResendUsesTheSameAddress. The loop has no exit to
// the email prompt, so the address it resends to is the one already
// given — never one re-read from anywhere.
func TestRecoverAcceptingAResendUsesTheSameAddress(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111"), says("222222")}
	run.prompt.confirms = []answer{no(), yes()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
	}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	starts := run.script.startBodies()
	if len(starts) != 2 {
		t.Fatalf("auth/start was called %d times, want 2", len(starts))
	}
	if starts[0].Email != starts[1].Email {
		t.Errorf("the resend went to %q, want the same address as the first send, %q",
			starts[1].Email, starts[0].Email)
	}
	if starts[1].Email != "someone@example.com" {
		t.Errorf("the resend went to %q, want %q", starts[1].Email, "someone@example.com")
	}
}

// TestRecoverOffersTheWayOut. The loop deliberately has no edge back to
// the email prompt, so a user who typo'd their address can only be
// helped by copy — the client cannot detect that case, because the
// server will not say whether an address is real.
func TestRecoverOffersTheWayOut(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111")}
	run.prompt.confirms = []answer{no(), aborts()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
	}

	_ = run.run(t)

	if !strings.Contains(run.output(), escapeHatchLine) {
		t.Errorf("the retry loop never told the reader how to get out of it:\n%s",
			run.output())
	}
	var resend *confirmAsk
	for i, ask := range run.prompt.confirmAsks {
		if ask.question == resendQuestion {
			resend = &run.prompt.confirmAsks[i]
			break
		}
	}
	if resend == nil {
		t.Fatalf("the resend question was never asked: %v", run.prompt.confirmAsks)
	}
	if !resend.defaultYes {
		t.Errorf("the resend question was asked with a no default; it must default " +
			"to yes, which is what renders it as the offered action")
	}
}

// TestTheServersOwnMessageIsShownOnAFailedVerify. The server writes one
// byte-identical message for every reason a code can be refused, and it
// is the only thing either side can say about which one happened.
func TestTheServersOwnMessageIsShownOnAFailedVerify(t *testing.T) {
	const serverMessage = "That code is not valid. Check it and try again."

	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111")}
	run.prompt.confirms = []answer{no(), aborts()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, serverMessage),
	}

	_ = run.run(t)

	if !strings.Contains(run.output(), serverMessage) {
		t.Errorf("the server's own message never reached the reader:\n%s", run.output())
	}
	// And ONLY that. The transport error's own string carries the wire
	// code and the HTTP status, which are diagnostics for whoever wrote
	// this client and noise to somebody deploying their first site — and
	// rendering them is what happens by default, because the error value
	// is right there and it formats itself.
	for _, noise := range []string{string(wire.CodeUnauthorized), "401", "status"} {
		if strings.Contains(run.output(), noise) {
			t.Errorf("the retry loop rendered %q alongside the server's message:\n%s",
				noise, run.output())
		}
	}
}

// TestTheCooldownHintCountsConsecutiveRefusals. Three refusals with a
// server fault in the middle are not three consecutive wrong codes, and
// a hint about a cooldown is a claim about a run of them.
func TestTheCooldownHintCountsConsecutiveRefusals(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{
		says("111111"), says("222222"), says("333333"), says("444444"),
	}
	run.prompt.confirms = []answer{no(), no(), no(), no()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
		fails(http.StatusInternalServerError, wire.CodeInternal, "boom"),
	}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if got := run.script.verifyCount(); got != 4 {
		t.Fatalf("auth/verify was called %d times, want 4", got)
	}
	if strings.Contains(run.output(), cooldownHint) {
		t.Errorf("the cooldown hint appeared after a run of refusals that was "+
			"broken by a server fault:\n%s", run.output())
	}
}

// TestTheCooldownHintAppearsOnceAndDoesNotEndTheRun. The client adds
// copy, never a budget: the server owns the limits, and a second
// different limit invented here would give up while the server would
// still have answered.
//
// SIX consecutive refusals rather than four, and the number is the row.
// At four, "once per run" and "once every three refusals" are the same
// measurement — the second hint would fall outside the run. Six is the
// smallest count that separates them, which matters because the second
// reading is what an implementation reaches for naturally: reset the
// counter when the hint fires, and it fires again three refusals later
// at exactly the moment the reader has already been told.
func TestTheCooldownHintAppearsOnceAndDoesNotEndTheRun(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{
		says("111111"), says("222222"), says("333333"),
		says("444444"), says("555555"), says("666666"), says("777777"),
	}
	run.prompt.confirms = []answer{no(), no(), no(), no(), no(), no(), no()}
	for i := 0; i < 6; i++ {
		run.script.verifyOutcomes = append(run.script.verifyOutcomes,
			fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"))
	}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v — the loop must keep going, not stop on a "+
			"count the client invented", err)
	}
	if got := run.script.verifyCount(); got != 7 {
		t.Errorf("auth/verify was called %d times, want 7 — the loop is not bounded "+
			"by this client", got)
	}
	if got := strings.Count(run.output(), cooldownHint); got != 1 {
		t.Errorf("the cooldown hint appeared %d times across six consecutive "+
			"refusals, want exactly 1:\n%s", got, run.output())
	}
}

// TestTheCooldownHintWaitsForTheThirdFailure. It is a hint about a state
// the client genuinely cannot observe, so it is earned rather than
// offered on the first typo.
func TestTheCooldownHintWaitsForTheThirdFailure(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111"), says("222222"), says("333333")}
	run.prompt.confirms = []answer{no(), no(), no()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
		fails(http.StatusUnauthorized, wire.CodeUnauthorized, "no"),
	}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if strings.Contains(run.output(), cooldownHint) {
		t.Errorf("the cooldown hint appeared after only two wrong codes:\n%s",
			run.output())
	}
}

// -------------------------------------------------------------------
// Error routing, derived from the contract rather than from a list
// typed here.
// -------------------------------------------------------------------

// TestEveryContractErrorCodeHasAStatedRouting ranges the contract's own
// enumeration rather than a list written in this file, so a ninth code
// added to the wire arrives here already needing an answer. A code with
// no stated decision FAILS rather than falling into a default branch —
// that is the whole reason for ranging the enumeration.
//
// REQUIRED MUTATION, RUN: delete the maintenance row from errorRouting.
// This row reds, alone — and that it is alone is the measurement worth
// keeping. The behavioural maintenance row stays GREEN, because a code
// with no stated routing falls through to a stop that renders the
// server's message and no retry time, which is what that row asks for.
// So the run keeps looking correct while a decision nobody made is being
// taken by a fallback, and only ranging the contract's own list sees it.
func TestEveryContractErrorCodeHasAStatedRouting(t *testing.T) {
	if len(wire.AllErrorCodes) == 0 {
		t.Fatal("the contract enumerates no error codes — this row scanned nothing")
	}
	for _, code := range wire.AllErrorCodes {
		if _, stated := routeFor(code); !stated {
			t.Errorf("the contract defines %q and this flow has no stated routing "+
				"for it — decide whether it enters the retry loop or stops the run",
				code)
		}
	}
}

// TestNoContractCodeEndsAsAnInternalFault asserts the routing on its
// OUTCOME rather than on the branch, which is the only form that catches
// a code added to the contract tomorrow. A code with no handling of its
// own falls through to the program's unexpected-failure copy, which tells
// the reader this is a fault in curious with nothing here for them to fix
// and invites a bug report — for a server saying something perfectly
// ordinary.
func TestNoContractCodeEndsAsAnInternalFault(t *testing.T) {
	if len(wire.AllErrorCodes) == 0 {
		t.Fatal("the contract enumerates no error codes — this row scanned nothing")
	}
	for _, code := range wire.AllErrorCodes {
		t.Run(string(code), func(t *testing.T) {
			res := runVerifyCode(t, code, http.StatusBadRequest, "server copy", "900")
			if res.err == nil {
				return // recovered and finished; nothing was rendered as a fault
			}
			var f *ui.Failure
			if !errors.As(res.err, &f) {
				t.Fatalf("%q ends the run with %T, which the program renders as an "+
					"internal fault — every code the contract defines must end in "+
					"copy somebody wrote", code, res.err)
			}
			if f.What == "" || f.Why == "" || f.Next == "" {
				t.Errorf("%q ends in a half-filled failure (what=%q why=%q next=%q); "+
					"a hard stop that names no action leaves the reader to guess",
					code, f.What, f.Why, f.Next)
			}
		})
	}
}

// codeRun runs one whole login against a verify that fails with code,
// then succeeds, and reports what the failure did. The second, good code
// is what makes a run that ENTERS the retry loop terminate; a run that
// stops never reaches it.
type codeRun struct {
	err         error
	out         string
	resendAsked int
	verifies    int
	offer       offerCapture
}

func runVerifyCode(t *testing.T, code wire.ErrorCode, status int, message, retryAfter string) codeRun {
	t.Helper()

	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111"), says("222222")}
	run.prompt.confirms = []answer{no(), no()}
	run.script.verifyOutcomes = []outcome{
		fails(status, code, message).after(retryAfter),
	}

	err := run.run(t)
	return codeRun{
		err:         err,
		out:         run.output(),
		resendAsked: run.prompt.asked(resendQuestion),
		verifies:    run.script.verifyCount(),
		offer:       *run.offer,
	}
}

// fingerprint is everything a run made observable: what the reader saw,
// what the run cost, and what the waitlist seam was handed.
func (r codeRun) fingerprint() string {
	return strings.Join([]string{
		r.out,
		rendered(r.err),
		fmt.Sprintf("offer=%d/%s/%s", r.offer.called, r.offer.email, r.offer.resetsAt),
	}, "\x00")
}

// TestNoCodeCarryingARetryTimeEntersTheRetryLoop is the first direction
// of the membership check, and it is asserted by BEHAVIOUR against the
// contract's own predicate rather than by repeating code names. A code
// the contract guarantees a retry time for is the server saying "wait
// this long"; looping on it is the one thing this client must not do.
func TestNoCodeCarryingARetryTimeEntersTheRetryLoop(t *testing.T) {
	if len(wire.AllErrorCodes) == 0 {
		t.Fatal("the contract enumerates no error codes — this row scanned nothing")
	}
	for _, code := range wire.AllErrorCodes {
		t.Run(string(code), func(t *testing.T) {
			res := runVerifyCode(t, code, http.StatusBadRequest, "server copy", "900")
			entered := res.resendAsked > 0
			if entered && wire.CarriesRetryAfter(code) {
				t.Errorf("%q carries a retry time and this flow put it in the retry "+
					"loop; a code the server has already said to wait on must stop", code)
			}
			if !entered && res.verifies != 1 {
				t.Errorf("%q stopped the run but auth/verify ran %d times, want 1",
					code, res.verifies)
			}
		})
	}
}

// TestEveryCodeCarryingARetryTimeStopsAndUsesIt is the second direction.
// "Uses it" is asserted without naming any renderer: the same run is
// made twice with two different retry times, and the observable outcome
// must DIFFER. A flow that parsed the header and dropped it produces one
// fingerprint twice.
//
// REQUIRED MUTATION, RUN, two of them, because the two codes reach the
// retry time by different routes and one edit cannot cover both. In
// retryAdvice, render now rather than now.Add(retryAfter): this row's
// rate_limited case reds, with TestRateLimitedStopsWithAWallClockTime
// and TestCapacityClosedStopsWithNoOfferWired. In stopFailure's capacity
// branch, hand the offer now rather than now.Add(retryAfter): this row's
// capacity_closed case reds, with TestCapacityClosedHandsOffThroughTheSeam.
func TestEveryCodeCarryingARetryTimeStopsAndUsesIt(t *testing.T) {
	carriers := 0
	for _, code := range wire.AllErrorCodes {
		if !wire.CarriesRetryAfter(code) {
			continue
		}
		carriers++
		t.Run(string(code), func(t *testing.T) {
			short := runVerifyCode(t, code, http.StatusTooManyRequests, "server copy", "60")
			long := runVerifyCode(t, code, http.StatusTooManyRequests, "server copy", "900")

			if short.resendAsked > 0 || long.resendAsked > 0 {
				t.Fatalf("%q must stop the run rather than offer a resend", code)
			}
			if short.err == nil || long.err == nil {
				t.Fatalf("%q must end the run with a failure, got %v / %v",
					code, short.err, long.err)
			}
			if short.fingerprint() == long.fingerprint() {
				t.Errorf("%q produced an identical outcome for a 60-second and a "+
					"900-second retry time, so the time the server sent was dropped:\n%s",
					code, long.fingerprint())
			}
		})
	}
	if carriers == 0 {
		t.Fatal("the contract names no code that carries a retry time — this row " +
			"scanned nothing")
	}
}

// -------------------------------------------------------------------
// The routing decisions themselves, which cannot be derived from the
// contract.
// -------------------------------------------------------------------

// TestUnauthorizedEntersTheRetryLoop. The generic auth failure IS the
// wrong-or-expired-code case: the server returns one byte-identical
// refusal for a wrong code, an expired one, a lost race, a cooldown, an
// unknown identity and identity-scoped rate limiting. This is the row
// that fails if somebody keys the loop on the malformed-input code
// instead.
//
// REQUIRED MUTATION, RUN: swap the two rows in errorRouting so that the
// generic auth failure stops and malformed input recovers — the obvious
// misreading, written out. Twelve rows red: this one and
// TestBadRequestStopsTheRun, plus every whole-run row that walks the
// retry loop at all, because a client that gives up on the one
// recoverable failure has no retry loop left to walk.
func TestUnauthorizedEntersTheRetryLoop(t *testing.T) {
	res := runVerifyCode(t, wire.CodeUnauthorized, http.StatusUnauthorized, "That code is not right.", "")
	if res.resendAsked != 1 {
		t.Fatalf("the resend prompt appeared %d times, want 1 — a wrong or expired "+
			"code is the one recoverable failure and must not end the run", res.resendAsked)
	}
	if res.err != nil {
		t.Errorf("the run ended with %v, want a completed login after the retry", res.err)
	}
	if res.verifies != 2 {
		t.Errorf("auth/verify ran %d times, want 2", res.verifies)
	}
}

// TestBadRequestStopsTheRun. Malformed INPUT, not a bad code: the client
// validates code shape locally, so this means the request itself is
// wrong and resending the same malformed thing cannot help.
func TestBadRequestStopsTheRun(t *testing.T) {
	res := runVerifyCode(t, wire.CodeBadRequest, http.StatusBadRequest, "Email is required.", "")
	if res.resendAsked != 0 {
		t.Errorf("the resend prompt appeared %d times, want 0", res.resendAsked)
	}
	if res.err == nil {
		t.Fatal("the run continued past a malformed request, want a stop")
	}
	if res.verifies != 1 {
		t.Errorf("auth/verify ran %d times, want 1", res.verifies)
	}
}

// TestRateLimitedStopsWithAWallClockTime. The retry time is rendered as
// a time of day rather than a duration, because "try again in 900
// seconds" is arithmetic the reader has to do while annoyed.
//
// AND IT COMES THROUGH THE SHARED RENDERER, which is what the relative
// assertion below is for. Until 2026-09-08 this stop had a layout of its
// own — a bare time of day, no numeric offset, no relative phrase —
// while a shut account cap rendered all three. Two walls, two answers to
// the same question, in one program. Nothing pinned the difference,
// which is why unifying them turned no row red and this assertion had to
// be written after the change rather than found by it.
//
// REQUIRED MUTATION, RUN: give retryAdvice back its own layout —
//
//	return fmt.Sprintf("Try again after %s. Nothing has been uploaded.",
//		now.Add(retryAfter).Format("15:04 MST"))
//
// Reds here on the relative phrase, and on the offset inside want.
func TestRateLimitedStopsWithAWallClockTime(t *testing.T) {
	res := runVerifyCode(t, wire.CodeRateLimited, http.StatusTooManyRequests,
		"Too many requests from this network.", "900")

	if res.resendAsked != 0 {
		t.Errorf("the resend prompt appeared %d times, want 0 — the loop must not "+
			"spin against a wall", res.resendAsked)
	}
	if res.err == nil {
		t.Fatal("the run continued past a rate limit, want a stop")
	}
	want := fixedNow.Add(15 * time.Minute).Format(resetsLayout)
	if !strings.Contains(rendered(res.err), want) {
		t.Errorf("the stop never named the time to come back (%s):\n%s",
			want, rendered(res.err))
	}
	// The relative half is the part a reader acts on: a time of day
	// answers "when", and only this answers "is that worth waiting for".
	// A wall the user meets here must not read differently from the one
	// they meet at a shut account cap.
	if got := rendered(res.err); !strings.Contains(got, "in about 15 minutes") {
		t.Errorf("the stop named a time of day with no relative phrase, so this "+
			"wall reads differently from the capacity wall:\n%s", got)
	}
	if res.verifies != 1 {
		t.Errorf("auth/verify ran %d times, want 1", res.verifies)
	}
}

// TestRateLimitedWithNoRetryTimeSaysNothingItCannotKnow. The contract
// guarantees the header for this code, so its absence is a server that
// broke its own promise — and inventing a time from a zero duration
// would tell the reader to come back immediately.
func TestRateLimitedWithNoRetryTimeSaysNothingItCannotKnow(t *testing.T) {
	res := runVerifyCode(t, wire.CodeRateLimited, http.StatusTooManyRequests,
		"Too many requests from this network.", "")

	if res.err == nil {
		t.Fatal("the run continued past a rate limit, want a stop")
	}
	if got := rendered(res.err); strings.Contains(got, fixedNow.Format(resetsLayout)) {
		t.Errorf("the stop named the current time as the time to come back:\n%s", got)
	}
}

// TestCapacityClosedHandsOffThroughTheSeam. Capacity is spent at verify,
// so it can close between the check and here — a genuine race. The reset
// time travels with the hand-off, so the offer can name a return time
// without a second capacity call.
func TestCapacityClosedHandsOffThroughTheSeam(t *testing.T) {
	res := runVerifyCode(t, wire.CodeCapacityClosed, http.StatusServiceUnavailable,
		"We're full for today.", "900")

	if res.resendAsked != 0 {
		t.Errorf("the resend prompt appeared %d times, want 0", res.resendAsked)
	}
	if res.offer.called != 1 {
		t.Fatalf("the waitlist seam was called %d times, want 1", res.offer.called)
	}
	if res.offer.email != "someone@example.com" {
		t.Errorf("the seam was handed %q, want the address the run used", res.offer.email)
	}
	want := fixedNow.Add(15 * time.Minute)
	if !res.offer.resetsAt.Equal(want) {
		t.Errorf("the seam was handed a reset time of %s, want %s — the retry time "+
			"the server sent is what lets the offer name a return time",
			res.offer.resetsAt, want)
	}
	if !res.offer.ctxCarried {
		t.Error("the seam was handed a context that is not the run's own, so a " +
			"cancelled run would not cancel the call the offer makes")
	}
	if res.err == nil {
		t.Error("the run continued past a closed capacity, want a stop")
	}
}

// TestCapacityClosedStopsEvenIfTheOfferReportsNothing. Login returns nil
// ONLY when a token was saved. An offer that hands back no error would
// otherwise end the run at zero with no credential, and the deploy that
// follows would fail somewhere much less explicable.
func TestCapacityClosedStopsEvenIfTheOfferReportsNothing(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111")}
	run.prompt.confirms = []answer{no()}
	run.deps.Offer = func(context.Context, string, time.Time) error { return nil }
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusServiceUnavailable, wire.CodeCapacityClosed, "full").after("900"),
	}

	err := run.run(t)
	if err == nil {
		t.Fatal("the run reported success with no token saved")
	}
	if run.savedToken != "" {
		t.Errorf("a token was written on a run that never verified: %v", run.savedToken)
	}
}

// TestCapacityClosedStopsWithNoOfferWired. The seam is optional until it
// is wired, and an unwired flow must still stop and still name the time.
func TestCapacityClosedStopsWithNoOfferWired(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111")}
	run.prompt.confirms = []answer{no()}
	run.deps.Offer = nil
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusServiceUnavailable, wire.CodeCapacityClosed, "full").after("900"),
	}

	err := run.run(t)
	if err == nil {
		t.Fatal("the run continued past a closed capacity with no offer wired")
	}
	want := fixedNow.Add(15 * time.Minute).Format(resetsLayout)
	if !strings.Contains(rendered(err), want) {
		t.Errorf("the stop never named the time to come back (%s):\n%s", want, rendered(err))
	}
	// The UNWIRED path is its own branch and needs its own assertion.
	// Measured: with the wired hand-off supplying the copy, the table
	// over the contract's codes never reaches this line at all, so
	// dropping the mark here moved nothing.
	if !errors.Is(err, ui.ErrServerClosed) {
		t.Error("the stop carries no closed-door mark, so a run that met a closed " +
			"cap with no offer wired costs the same as a broken project")
	}
	if code := exitCodeFor(t, err); code != ui.ExitServerClosed {
		t.Errorf("exit code %d, want %d", code, ui.ExitServerClosed)
	}
}

// TestMaintenanceStopsWithTheServerMessageAndNoTime is the asymmetry the
// membership check pins: maintenance is a stop, and the kill switch has
// no reset the server can honestly name, so nothing here may invent one.
func TestMaintenanceStopsWithTheServerMessageAndNoTime(t *testing.T) {
	const serverMessage = "curious.pub is down for maintenance. Back shortly."

	res := runVerifyCode(t, wire.CodeMaintenance, http.StatusServiceUnavailable, serverMessage, "")
	if res.resendAsked != 0 {
		t.Errorf("the resend prompt appeared %d times, want 0", res.resendAsked)
	}
	if res.err == nil {
		t.Fatal("the run continued past the kill switch, want a stop")
	}
	got := rendered(res.err)
	if !strings.Contains(got, serverMessage) {
		t.Errorf("the server's message was not rendered verbatim:\n%s", got)
	}
	if strings.Contains(got, fixedNow.Format(resetsLayout)) {
		t.Errorf("the stop rendered a retry time for a code that carries none:\n%s", got)
	}
	if wire.CarriesRetryAfter(wire.CodeMaintenance) {
		t.Error("the contract now says maintenance carries a retry time; this row's " +
			"whole point was that it does not")
	}
}

// TestForbiddenStopsWithTheServerMessage. No handler returns it today,
// and a code with no stated routing fails by this flow's own rule — so
// it has one, and the one it has is a stop.
func TestForbiddenStopsWithTheServerMessage(t *testing.T) {
	const serverMessage = "That account cannot deploy."

	res := runVerifyCode(t, wire.CodeForbidden, http.StatusForbidden, serverMessage, "")
	if res.resendAsked != 0 {
		t.Errorf("the resend prompt appeared %d times, want 0", res.resendAsked)
	}
	if res.err == nil {
		t.Fatal("the run continued, want a stop")
	}
	if !strings.Contains(rendered(res.err), serverMessage) {
		t.Errorf("the server's message was not rendered verbatim:\n%s", rendered(res.err))
	}
}

// TestNotFoundNamesTheBaseURLItCalled. The route not existing is almost
// always a mis-set endpoint variable rather than a server fault, so the
// message names the thing that is actually wrong.
func TestNotFoundNamesTheBaseURLItCalled(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111")}
	run.prompt.confirms = []answer{no()}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusNotFound, wire.CodeNotFound, "no such route"),
	}

	err := run.run(t)
	if err == nil {
		t.Fatal("the run continued past a missing route, want a stop")
	}
	if !strings.Contains(rendered(err), run.deps.Endpoint) {
		t.Errorf("the stop never named the base URL it called (%s):\n%s",
			run.deps.Endpoint, rendered(err))
	}
}

// TestInternalEntersTheRetryLoop. The verify may not have consumed the
// code, so this is recoverable in the same sense a dropped connection
// is.
func TestInternalEntersTheRetryLoop(t *testing.T) {
	res := runVerifyCode(t, wire.CodeInternal, http.StatusInternalServerError, "boom", "")
	if res.resendAsked != 1 {
		t.Fatalf("the resend prompt appeared %d times, want 1", res.resendAsked)
	}
	if res.err != nil {
		t.Errorf("the run ended with %v, want a completed login after the retry", res.err)
	}
}

// TestAnUnknownCodeStopsAndShowsTheServersMessage. This row deliberately
// uses a code the contract does not define, so it is the one case the
// ranged coverage check above cannot reach and the only one that has to
// be written by hand.
func TestAnUnknownCodeStopsAndShowsTheServersMessage(t *testing.T) {
	const invented = wire.ErrorCode("teapot_closed")
	const serverMessage = "This build predates whatever just happened."

	for _, known := range wire.AllErrorCodes {
		if known == invented {
			t.Fatalf("%q is now part of the contract, so this row no longer tests "+
				"an unknown code", invented)
		}
	}

	res := runVerifyCode(t, invented, http.StatusBadRequest, serverMessage, "")
	if res.resendAsked != 0 {
		t.Errorf("the resend prompt appeared %d times, want 0", res.resendAsked)
	}
	if res.err == nil {
		t.Fatal("the run continued past a code it does not know, want a stop")
	}
	if !strings.Contains(rendered(res.err), serverMessage) {
		t.Errorf("the server's own message was replaced rather than shown:\n%s",
			rendered(res.err))
	}
}

// TestANetworkErrorEntersTheRetryLoop. A retry may simply work, and a
// dropped connection says nothing about whether the code was any good.
func TestANetworkErrorEntersTheRetryLoop(t *testing.T) {
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	client, err := api.New(deadURL)
	if err != nil {
		t.Fatalf("building the client: %v", err)
	}

	prompt := &scriptedPrompt{
		emails:   []answer{says("someone@example.com")},
		lines:    []answer{says("111111")},
		confirms: []answer{no(), aborts()},
	}
	runErr := Login(t.Context(), LoginDeps{
		Prompt:   prompt,
		Auth:     client,
		Endpoint: deadURL,
		Now:      func() time.Time { return fixedNow },
		Save:     func(ui.Secret, string) error { return nil },
	})

	// The first START already fails against a closed listener, so the
	// recovery offer is what proves the failure did not end the run.
	if got := prompt.asked(resendQuestion); got != 1 {
		t.Errorf("the resend prompt appeared %d times, want 1 — a transport failure "+
			"is worth another go", got)
	}
	if !errors.Is(runErr, ui.ErrAborted) {
		t.Errorf("the run ended with %v, want the cancellation the script sent", runErr)
	}
}

// -------------------------------------------------------------------
// Saving the token.
// -------------------------------------------------------------------

// TestASuccessfulLoginSavesThePairAndNeverPrintsTheToken. The endpoint
// written beside the token is the one this flow CALLED — never a value
// read back from anywhere, because nothing else knows which endpoint
// issued it.
func TestASuccessfulLoginSavesThePairAndNeverPrintsTheToken(t *testing.T) {
	run := newLoginRun(t)
	run.script.token = "tok-live-do-not-print-me"
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("123456")}
	run.prompt.confirms = []answer{no()}

	if err := run.run(t); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if string(run.savedToken) != "tok-live-do-not-print-me" {
		t.Errorf("the writer was handed %q", string(run.savedToken))
	}
	if run.savedEndpoint != run.deps.Endpoint {
		t.Errorf("the writer was handed endpoint %q, want the one this flow called, %q",
			run.savedEndpoint, run.deps.Endpoint)
	}
	if n := strings.Count(run.output(), "tok-live-do-not-print-me"); n != 0 {
		t.Errorf("the token appeared %d times in what the reader saw:\n%s", n, run.output())
	}
}

// TestTheTokenIsNeverPrintedOnAnyPath sweeps every ending this flow has
// rather than the happy one alone. A token in scrollback is worse than a
// re-login, and the path most tempted to print one is the one where the
// file could not be written.
//
// THE POSITIVE CONTROL IS THE FIRST SUBTEST, and without it this whole
// row is one broken detector away from unconditional green: an absence
// assertion over a channel that captures nothing passes for ever. So a
// value with the token's own shape is driven through both channels this
// row reads, and it is found.
func TestTheTokenIsNeverPrintedOnAnyPath(t *testing.T) {
	const token = "tok-live-do-not-print-me"

	t.Run("positive control: the channels this row reads can see such a value", func(t *testing.T) {
		prompt := &scriptedPrompt{}
		prompt.Step("%s", token)
		planted := prompt.out.String() + "\n" +
			rendered(ui.NewFailure(token, token, token))
		if n := strings.Count(planted, token); n != 4 {
			t.Fatalf("a planted value was found %d times across the narration "+
				"buffer and the rendered failure, want 4 — the assertions below "+
				"read these two channels and prove nothing if they are blind", n)
		}
	})

	for _, tc := range []struct {
		name    string
		saveErr error
	}{
		{name: "saved"},
		{name: "the write failed", saveErr: errors.New("could not write /tmp/x/config.json: read-only")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newLoginRun(t)
			run.script.token = token
			run.saveErr = tc.saveErr
			run.prompt.emails = []answer{says("someone@example.com")}
			run.prompt.lines = []answer{says("123456")}
			run.prompt.confirms = []answer{no()}

			err := run.run(t)
			everything := run.output() + "\n" + rendered(err)

			// The run must have got far enough to HOLD a token, or the
			// absence below is about a flow that never reached one.
			if run.savedToken == "" {
				t.Fatalf("the run never reached a token, so this row is asserting "+
					"the absence of something that was never in hand: %v", err)
			}
			if n := strings.Count(everything, token); n != 0 {
				t.Errorf("the token appeared %d times across everything this run "+
					"emitted:\n%s", n, everything)
			}
		})
	}
}

// TestAFailedWriteNamesThePathAndSaysItIsSafeToTryAgain. A repeat verify
// for an identity that already holds a token costs no account capacity —
// it reissues and revokes the predecessor — so the instruction is an
// instruction rather than an apology.
func TestAFailedWriteNamesThePathAndSaysItIsSafeToTryAgain(t *testing.T) {
	const path = "/somewhere/curious/config.json"

	run := newLoginRun(t)
	run.saveErr = fmt.Errorf("could not write %s: read-only file system", path)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("123456")}
	run.prompt.confirms = []answer{no()}

	err := run.run(t)
	if err == nil {
		t.Fatal("a failed write reported success")
	}
	got := rendered(err)
	if !strings.Contains(got, path) {
		t.Errorf("the failure never named the file it could not write:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "log in again") {
		t.Errorf("the failure never told the reader that logging in again is safe:\n%s", got)
	}
	if code := exitCodeFor(t, err); code == 0 {
		t.Error("a failed write exited 0")
	}
}

// TestTheDefaultWriterLoadsBeforeItSaves. The unknown-field map lives on
// the value the loader returns, so a configuration built fresh carries a
// nil one and writing it DROPS every field a newer release wrote. This
// row goes all the way to a real file, through the real writer, because
// that is the only place the hazard is observable.
//
// It pins BOTH hazards, and they point in opposite directions, which is
// why the endpoint already in the file differs from the one this run
// calls. REQUIRED MUTATION, RUN, one for each: build a fresh
// configuration value instead of loading one, and the future field is
// gone; take the endpoint from the loaded value instead of the argument,
// and the file records the endpoint the PREVIOUS token came from. This
// row reds alone for both, so it is the only thing standing over either.
func TestTheDefaultWriterLoadsBeforeItSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("CURIOUS_CONFIG", path)

	script := &apiScript{token: "tok-fresh"}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)

	// The endpoint already in the file is DIFFERENT from the one this
	// run calls, and that is what makes the row able to tell the two
	// hazards apart. With the same value in both places, a writer that
	// reused the loaded endpoint and one that passed the endpoint it
	// called would write identical files.
	const staleEndpoint = "http://127.0.0.1:1/"
	existing := fmt.Sprintf(
		`{"version":1,"token":"tok-old","api_url":%q,"a_field_from_the_future":{"kept":true}}`,
		staleEndpoint)
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("writing the existing config: %v", err)
	}

	client, err := api.New(srv.URL)
	if err != nil {
		t.Fatalf("building the client: %v", err)
	}
	prompt := &scriptedPrompt{
		emails:   []answer{says("someone@example.com")},
		lines:    []answer{says("123456")},
		confirms: []answer{no()},
	}
	if err := Login(t.Context(), LoginDeps{
		Prompt:   prompt,
		Auth:     client,
		Endpoint: srv.URL,
		Now:      func() time.Time { return fixedNow },
	}); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the config back: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(written, &back); err != nil {
		t.Fatalf("the config this flow wrote is not JSON: %v\n%s", err, written)
	}
	if _, kept := back["a_field_from_the_future"]; !kept {
		t.Errorf("a field a newer release wrote was dropped by this login:\n%s", written)
	}
	if back["token"] != "tok-fresh" {
		t.Errorf("the new token was not written: %v", back["token"])
	}
	if back["api_url"] != srv.URL {
		t.Errorf("the stored endpoint is %v, want the one this flow called, %s",
			back["api_url"], srv.URL)
	}

	// The mode is the writer's own property and is asserted on every
	// platform by the package that owns it; here it is a wiring check,
	// and Windows does not carry the bits this reads.
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat: %v", statErr)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("the config was left as %04o, want 0600", got)
		}
	}
}

// -------------------------------------------------------------------
// Nobody to ask, and a wiring mistake.
// -------------------------------------------------------------------

// TestWithoutATerminalNothingIsSpent. The failure has to arrive before
// any request: there is no environment-variable token override in this
// surface, so the honest answer is to say a terminal is needed.
func TestWithoutATerminalNothingIsSpent(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.notInteractive = true

	err := run.run(t)
	if !errors.Is(err, ui.ErrNotInteractive) {
		t.Fatalf("the run ended with %v, want the no-terminal sentinel", err)
	}
	if got := run.script.startCount(); got != 0 {
		t.Errorf("auth/start was called %d times with nobody to ask, want 0", got)
	}
	if got := run.script.verifyCount(); got != 0 {
		t.Errorf("auth/verify was called %d times with nobody to ask, want 0", got)
	}
	if code := exitCodeFor(t, err); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

// TestAnEndpointThatCannotBeComparedIsRefusedUpFront. The pair written
// to the file is validated together, so an endpoint the writer will
// refuse is worth refusing before somebody types a code and spends one
// of the five attempts on a run that cannot end.
func TestAnEndpointThatCannotBeComparedIsRefusedUpFront(t *testing.T) {
	// The positive control comes first: with a good endpoint the same
	// script gets past this check and reaches the person. Without it,
	// "the user was asked nothing" is satisfied by a flow that asks
	// nobody anything ever.
	t.Run("positive control: a usable endpoint reaches the prompt", func(t *testing.T) {
		run := newLoginRun(t)
		run.prompt.emails = []answer{aborts()}

		_ = run.run(t)
		if got := len(run.prompt.emailAsks); got != 1 {
			t.Fatalf("the email prompt was rendered %d times against a usable "+
				"endpoint, want 1 — the rows below prove nothing if the flow never "+
				"asks anything", got)
		}
	})

	for _, endpoint := range []string{"", "not a url at all", "mailto:someone@example.com"} {
		t.Run(fmt.Sprintf("%q", endpoint), func(t *testing.T) {
			run := newLoginRun(t)
			run.deps.Endpoint = endpoint
			run.prompt.emails = []answer{says("someone@example.com")}

			err := run.run(t)
			if err == nil {
				t.Fatal("the run continued with an endpoint it cannot store")
			}
			// The SPECIFIC refusal, not merely an error: a row whose
			// success condition is "something went wrong" passes against
			// a flow that is broken for an entirely different reason.
			if !errors.Is(err, ErrEndpointUnusable) {
				t.Errorf("the run ended with %v, want the wiring refusal", err)
			}
			if got := len(run.prompt.emailAsks); got != 0 {
				t.Errorf("the user was asked %d questions before the wiring was "+
					"checked, want 0", got)
			}
			if got := run.script.startCount(); got != 0 {
				t.Errorf("auth/start was called %d times, want 0", got)
			}
			if endpoint != "" && strings.Contains(err.Error(), endpoint) {
				t.Errorf("the refusal echoes the endpoint it was given; a base URL "+
					"can carry credentials and this message is not the place to "+
					"find that out:\n%s", err)
			}
		})
	}
}

// -------------------------------------------------------------------
// What a stop COSTS, pinned as it stands today.
// -------------------------------------------------------------------

// closedDoorCodes is what "the server is closed to you right now" means
// in terms of this contract's codes.
//
// The SET being ranged comes from the contract; this expectation is
// local, which is the arrangement that lets the table below notice a
// ninth code without inheriting an answer for it. Capacity closed and
// the kill switch are the door being shut. Rate limiting deliberately is
// NOT: it is about pace rather than access, and its stop tells the
// reader when to come back rather than that they are shut out.
var closedDoorCodes = map[wire.ErrorCode]bool{
	wire.CodeCapacityClosed: true,
	wire.CodeMaintenance:    true,
}

// TestOnlyTheClosedDoorStopsExitThree is the exit-code table, and it
// replaces a row that asserted every stop costs 1.
//
// That row was right when it was written and it did the job it was for:
// a third exit code could not appear without somebody coming here and
// saying so. This is that. What it may not do is keep its old name — a
// test whose name disagrees with its body is a comment that lies with
// the authority of code, and the name is what the next reader trusts
// before the assertions.
//
// BOTH DIRECTIONS, because a table that only checked the scoped pair
// would pass just as happily if every stop cost 3. The codes inside the
// scope cost ExitServerClosed and carry the mark; every other stop costs
// the ordinary code and does not.
func TestOnlyTheClosedDoorStopsExitThree(t *testing.T) {
	inScope, outOfScope := 0, 0
	for _, code := range wire.AllErrorCodes {
		if route, stated := routeFor(code); !stated || route != routeStop {
			continue
		}
		if closedDoorCodes[code] {
			inScope++
		} else {
			outOfScope++
		}
	}
	// The cardinality both ways. Either half being empty makes the
	// corresponding direction vacuously true, and an empty scope is the
	// likelier accident.
	if inScope != len(closedDoorCodes) {
		t.Fatalf("%d of the %d closed-door codes reach a stop; a code that never "+
			"stops cannot be asserted to stop with a particular cost",
			inScope, len(closedDoorCodes))
	}
	if outOfScope == 0 {
		t.Fatal("every stop is inside the scope, so this table cannot tell a " +
			"scoped cost from a blanket one")
	}

	for _, code := range wire.AllErrorCodes {
		route, stated := routeFor(code)
		if !stated || route != routeStop {
			continue
		}
		t.Run(string(code), func(t *testing.T) {
			res := runVerifyCode(t, code, http.StatusBadRequest, "server copy", "900")
			if res.err == nil {
				t.Fatalf("%q did not stop the run", code)
			}

			wantMark := closedDoorCodes[code]
			if got := errors.Is(res.err, ui.ErrServerClosed); got != wantMark {
				t.Errorf("%q carries the closed-door mark = %v, want %v", code, got, wantMark)
			}

			want := 1
			if wantMark {
				want = ui.ExitServerClosed
			}
			if got := exitCodeFor(t, res.err); got != want {
				t.Errorf("%q exits %d, want %d", code, got, want)
			}
		})
	}
}

// TestTheOfferKeepsItsWordsAndTheFlowKeepsTheCost. A wired hand-off owns
// what the reader is told; it does not own what the run costs, and it
// must not have to know. Marking the offer's own error here is what
// spares the change that wires a real one up from deciding an exit code
// as a side effect.
func TestTheOfferKeepsItsWordsAndTheFlowKeepsTheCost(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{says("someone@example.com")}
	run.prompt.lines = []answer{says("111111")}
	run.prompt.confirms = []answer{no()}
	run.deps.Offer = func(context.Context, string, time.Time) error {
		return ui.NewFailure("Want a nudge when a slot frees up?", "Because.", "Say yes.")
	}
	run.script.verifyOutcomes = []outcome{
		fails(http.StatusServiceUnavailable, wire.CodeCapacityClosed, "full").after("900"),
	}

	err := run.run(t)
	if err == nil {
		t.Fatal("the run continued past a closed capacity")
	}
	if !strings.Contains(rendered(err), "Want a nudge when a slot frees up?") {
		t.Errorf("the offer's own words were replaced:\n%s", rendered(err))
	}
	if code := exitCodeFor(t, err); code != ui.ExitServerClosed {
		t.Errorf("exit code %d, want %d — the cost is this flow's to decide, not "+
			"the hand-off's", code, ui.ExitServerClosed)
	}
}

// TestACancellationIsNotAFault. Ctrl-D at any prompt is a deliberate act,
// and a non-zero code would make every wrapper script treat it as a
// fault.
func TestACancellationIsNotAFault(t *testing.T) {
	run := newLoginRun(t)
	run.prompt.emails = []answer{aborts()}

	err := run.run(t)
	if !errors.Is(err, ui.ErrAborted) {
		t.Fatalf("the run ended with %v, want the cancellation sentinel", err)
	}
	if code := exitCodeFor(t, err); code != 0 {
		t.Errorf("a cancellation exits %d, want 0", code)
	}
}
