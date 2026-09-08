package flow

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/pack"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The real terminal must satisfy the seam the sequence asks for, and the
// real filesystem the one the walk reads through. Without these two
// lines either could drift into something only a double implements, and
// the drift would not show up until the command was run by hand.
var (
	_ DeployPrompter = (*ui.UI)(nil)
	_ pack.FS        = pack.OSFileSystem{}
)

// -------------------------------------------------------------------
// The instruments
// -------------------------------------------------------------------

// deployJournal is the ONE ordered record of what a run did, written to
// by the terminal, the filesystem and the server alike.
//
// It is one slice rather than three because the questions this file asks
// are about ORDER ACROSS those three — was the warning prompt answered
// before anything was sent, did the capacity check come before the login
// — and three separate logs cannot answer any of them. The mutex is not
// decoration: the server's handler runs on its own goroutine.
type deployJournal struct {
	mu     sync.Mutex
	events []string
}

func (j *deployJournal) note(event string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.events = append(j.events, event)
}

func (j *deployJournal) all() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.events...)
}

// journalPrompt is the scripted terminal with everything it says and
// asks copied into the shared journal. The embedded double keeps doing
// its own bookkeeping, so rows that only care about what was asked can
// still read it there.
type journalPrompt struct {
	scriptedPrompt
	journal *deployJournal
}

func (p *journalPrompt) Step(format string, args ...any) {
	p.journal.note("said: " + fmt.Sprintf(format, args...))
	p.scriptedPrompt.Step(format, args...)
}

func (p *journalPrompt) Line(prompt string) (string, error) {
	p.journal.note("asked: " + prompt)
	return p.scriptedPrompt.Line(prompt)
}

func (p *journalPrompt) Email(prompt string) (string, error) {
	p.journal.note("asked: " + prompt)
	return p.scriptedPrompt.Email(prompt)
}

func (p *journalPrompt) Confirm(question string, defaultYes bool) (bool, error) {
	p.journal.note("asked: " + question)
	return p.scriptedPrompt.Confirm(question, defaultYes)
}

// countingFS is the real filesystem with a tally.
//
// TRAVERSALS ARE COUNTED AT THE ROOT, which is what makes the number
// mean "how many times was this project walked": every descent below the
// root belongs to the walk that started at it, and a second walk starts
// by reading the root again.
//
// It also notes the first file the PACKER opens. Nothing else in the
// sequence opens a project file through this seam — the pre-flight
// checks have a filesystem of their own — except the walk reading an
// ignore file, which the fixtures here deliberately do not have.
type countingFS struct {
	root    string
	journal *deployJournal

	traversals int
	opened     []string
	packNoted  bool
}

func (c *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == c.root {
		c.traversals++
		c.journal.note("walk")
	}
	return pack.OSFileSystem{}.ReadDir(name)
}

func (c *countingFS) Open(name string) (io.ReadCloser, error) {
	c.opened = append(c.opened, name)
	if !c.packNoted && filepath.Base(name) != ".gitignore" {
		c.packNoted = true
		c.journal.note("pack")
	}
	return pack.OSFileSystem{}.Open(name)
}

// linkAddingFS is countingFS with one synthetic symbolic link in the
// root listing.
//
// IT IS SYNTHETIC ON PURPOSE. A real link needs a privilege one of the
// three platforms this ships to does not always grant, and the property
// under test is not the operating system's — it is that a warning the
// WALK produced reaches the same single prompt the pre-flight warnings
// do. The walk classifies from the directory entry alone and never
// resolves the link, so an entry that reports itself as one is exactly
// what it would see. The packer never opens it, because a skipped link
// is not in the file list.
type linkAddingFS struct {
	*countingFS
	name string
}

func (l *linkAddingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := l.countingFS.ReadDir(name)
	if err != nil || name != l.root {
		return entries, err
	}
	return append(entries, symlinkEntry{name: l.name}), nil
}

// symlinkEntry is a directory entry that reports itself as a symbolic
// link and nothing else. The walk asks for the type and the name; it
// never calls Info on a link, because it never follows one.
type symlinkEntry struct{ name string }

func (e symlinkEntry) Name() string      { return e.name }
func (e symlinkEntry) IsDir() bool       { return false }
func (e symlinkEntry) Type() fs.FileMode { return fs.ModeSymlink }
func (e symlinkEntry) Info() (fs.FileInfo, error) {
	return nil, fmt.Errorf("%s is a link and this walk does not follow one", e.name)
}

// deployScript is a real HTTP server speaking the real wire contract, so
// every row goes through the shipped client's own encoding and decoding.
//
// IT COUNTS EVERY REQUEST, not only the ones it recognises. "This run
// sent nothing" is the load-bearing assertion in this file and it is a
// count of zero — so the counter has to be reached by anything that
// leaves the machine, including a request to a path this contract does
// not define.
type deployScript struct {
	mu      sync.Mutex
	journal *deployJournal

	requests int
	paths    []string
	strays   []string
	bearers  []string

	capacity        wire.CapacityResponse
	capacityOutcome outcome
	verifyOutcome   outcome
	token           string
	waitlisted      []wire.WaitlistRequest
}

func (s *deployScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.requests++
	s.paths = append(s.paths, r.URL.Path)
	s.bearers = append(s.bearers, r.Header.Get("Authorization"))
	s.journal.note(r.Method + " " + r.URL.Path)

	switch r.URL.Path {
	case "/v1/capacity":
		s.reply(w, s.capacityOutcome, s.capacity)
	case "/v1/waitlist":
		var req wire.WaitlistRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.waitlisted = append(s.waitlisted, req)
		s.reply(w, outcome{}, wire.WaitlistResponse{})
	case "/v1/auth/start":
		s.reply(w, outcome{}, wire.AuthStartResponse{})
	case "/v1/auth/verify":
		s.reply(w, s.verifyOutcome, wire.AuthVerifyResponse{Token: s.token})
	default:
		s.strays = append(s.strays, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *deployScript) reply(w http.ResponseWriter, o outcome, success any) {
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

func (s *deployScript) sent() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// sentTo counts the requests that reached one path.
func (s *deployScript) sentTo(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.paths {
		if p == path {
			n++
		}
	}
	return n
}

// -------------------------------------------------------------------
// The harness
// -------------------------------------------------------------------

// deployRun is one configured run: a fresh config directory, a real
// server, a real client, a counting filesystem and a scripted terminal.
type deployRun struct {
	t       *testing.T
	journal *deployJournal
	prompt  *journalPrompt
	script  *deployScript
	fsys    *countingFS
	srv     *httptest.Server

	// tempParent is the directory the run is told to make its working
	// directory in, so "nothing was left behind" is a question about a
	// directory this test owns rather than about a guessed path.
	tempParent string

	installs int
	stops    int
	cleanup  func()

	deps DeployDeps
}

// newDeployRun wires a run against a project directory, with the door
// open, plenty of room and no stored login — so a row states only the
// thing it is about.
func newDeployRun(t *testing.T, root string) *deployRun {
	t.Helper()

	// A CONFIG PATH OF THIS TEST'S OWN, and CURIOUS_API_URL emptied: a
	// developer's own environment must not decide whether a row passes.
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "curious.json"))
	t.Setenv("CURIOUS_API_URL", "")

	journal := &deployJournal{}
	script := &deployScript{
		journal:  journal,
		capacity: wire.CapacityResponse{Open: true, AccountsLeft: 200},
		token:    "issued-token",
	}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		script.mu.Lock()
		defer script.mu.Unlock()
		if len(script.strays) > 0 {
			t.Errorf("the run called %v, which this contract does not define", script.strays)
		}
	})

	run := &deployRun{
		t:          t,
		journal:    journal,
		prompt:     &journalPrompt{journal: journal},
		script:     script,
		fsys:       &countingFS{root: root, journal: journal},
		srv:        srv,
		tempParent: t.TempDir(),
	}
	run.deps = DeployDeps{
		Dir:     root,
		Prompt:  run.prompt,
		APIURL:  srv.URL,
		TempDir: run.tempParent,
		FS:      run.fsys,
		Now:     func() time.Time { return fixedNowLocal },
		Interrupts: func(cleanup func()) func() {
			journal.note("interrupt handler installed")
			run.installs++
			run.cleanup = cleanup
			return func() { run.stops++ }
		},
	}
	return run
}

// storedToken writes a real config file holding a token, through the
// real store, so a row about a returning user is about the file that
// user would actually have.
func (r *deployRun) storedToken(token, issuedAgainst string) *deployRun {
	r.t.Helper()
	cfg, err := config.Load(issuedAgainst)
	if err != nil {
		r.t.Fatalf("loading a fresh config: %v", err)
	}
	if err := cfg.Save(ui.Secret(token), issuedAgainst); err != nil {
		r.t.Fatalf("storing a token: %v", err)
	}
	return r
}

func (r *deployRun) run() (*Handoff, error) {
	r.t.Helper()
	return Deploy(r.t.Context(), r.deps)
}

// leftBehind is everything still sitting in the directory the run was
// told to work in.
func (r *deployRun) leftBehind() []string {
	r.t.Helper()
	entries, err := os.ReadDir(r.tempParent)
	if err != nil {
		r.t.Fatalf("reading the working directory back: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// landmarks reduces a journal to the five steps whose order this file
// asserts, dropping everything else.
//
// The limits are deliberately absent, and their absence is a fact about
// what is OBSERVABLE rather than about what runs: a limit that passes
// says nothing, so its position can only be seen on a run it stops —
// which is the row below about a project over a limit sending nothing.
//
// A REPEATED LANDMARK COLLAPSES INTO ONE, because the question is where
// a step sat in the sequence and not how many lines it printed. Two
// hard-coded URLs in two files are two warnings and one pre-flight, and
// a row that counted them would be asserting the fixture's contents
// under a name about ordering.
func landmarks(events []string) []string {
	var out []string
	for _, e := range events {
		var step string
		switch {
		case e == "walk":
			step = "walk"
		case e == "pack":
			step = "pack"
		case strings.HasPrefix(e, "said: ") && strings.Contains(e, "http://localhost"):
			step = "pre-flight"
		case e == "GET /v1/capacity":
			step = "capacity"
		case e == "POST /v1/auth/verify":
			step = "login"
		default:
			continue
		}
		if len(out) > 0 && out[len(out)-1] == step {
			continue
		}
		out = append(out, step)
	}
	return out
}

// scriptedLogin fills in the answers a full first-time login needs: the
// warning prompt, an address, a code, and the consent question.
func (r *deployRun) scriptedLogin() *deployRun {
	r.prompt.emails = []answer{says("someone@example.com")}
	r.prompt.lines = []answer{says("123456")}
	return r
}

// fixtureProject is one of the committed project directories.
func fixtureProject(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "projects", name)
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("project fixture %s: %v", name, err)
	}
	if !info.IsDir() {
		t.Fatalf("project fixture %s is not a directory", name)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolving project fixture %s: %v", name, err)
	}
	return abs
}

// writeProject builds a project directory this test owns, for the shapes
// no committed fixture has.
func writeProject(t *testing.T, files map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
	}
	return root
}

// astroProject is the smallest tree that passes every pre-flight check,
// plus whatever a row adds to it.
func astroProject(extra map[string][]byte) map[string][]byte {
	files := map[string][]byte{
		"package.json":          []byte(`{"name":"row","private":true,"dependencies":{"astro":"^5.0.0"}}`),
		"package-lock.json":     []byte(`{"lockfileVersion":3}`),
		"src/pages/index.astro": []byte("<h1>hello</h1>"),
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

// -------------------------------------------------------------------
// The order
// -------------------------------------------------------------------

// TestTheSequenceRunsInTheOrderTheSpecSets is asserted as a RECORDED
// SEQUENCE rather than as "it worked", because every step in it also
// happens in the wrong order.
//
// The fixture carries a hard-coded development URL, which is what makes
// the pre-flight step visible from outside at all: a check that passes
// says nothing, so a run over a clean project could not tell a sequence
// that checked before dialling from one that dialled first.
//
// REQUIRED MUTATION, run 2026-09-08: move the capacity-and-login block
// in Deploy above the walk. The landmark sequence reds — and it is worth
// noticing WHAT it reds as, because that is the defect the order exists
// to prevent: capacity and login run first, so a first-timer in the
// wrong directory has spent a login before being told anything. Six
// rows in this file red together under it, which is the correct blast
// radius for a change to the one thing they are all about.
func TestTheSequenceRunsInTheOrderTheSpecSets(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "localhost-hits")).scriptedLogin()
	// Continue past the warnings, then decline the marketing question.
	run.prompt.confirms = []answer{yes(), no()}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	want := []string{"walk", "pre-flight", "capacity", "login", "pack"}
	if got := landmarks(run.journal.all()); !equalEvents(got, want) {
		t.Errorf("the run happened in the order %v, want %v\n\nfull journal:\n  %s",
			got, want, strings.Join(run.journal.all(), "\n  "))
	}
}

// TestTheProjectIsWalkedExactlyOnce. One walk feeds the development-URL
// scan, the limits and the packer; walking again for any of them would
// let one of the three disagree with the list the user was shown and
// agreed to.
//
// THE POSITIVE CONTROL IS A COUNT OF ONE ON A RUN THAT BARELY HAPPENED.
// This asserts a small number, which a counter wired to nothing also
// produces — so the second row runs a project that is refused at
// pre-flight and still expects 1, not 0.
//
// REQUIRED MUTATION, run 2026-09-08: walk a second time in Deploy — pass
// the files from a fresh pack.Walk to pack.Prepare. The first row reds on
// 2; the control stays green, because a project refused at pre-flight
// never reaches a packer to walk for. THE COMMENT FIRST WRITTEN HERE
// CLAIMED BOTH WOULD RED, and it was wrong in the way a prediction about
// coverage usually is — the control measures a different thing, which is
// the point of it.
func TestTheProjectIsWalkedExactlyOnce(t *testing.T) {
	t.Run("a project that deploys", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}

		handoff, err := run.run()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}
		defer handoff.Release()

		if run.fsys.traversals != 1 {
			t.Errorf("the project was walked %d times, want exactly 1", run.fsys.traversals)
		}
	})

	t.Run("control: a project that is refused still walked once", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "astro-absent"))

		if _, err := run.run(); err == nil {
			t.Fatal("a project with no astro dependency deployed")
		}
		if run.fsys.traversals != 1 {
			t.Errorf("the project was walked %d times, want exactly 1 — a zero here "+
				"would mean the counter is wired to nothing and every count above "+
				"is meaningless", run.fsys.traversals)
		}
	})
}

// -------------------------------------------------------------------
// Local truths before global state
// -------------------------------------------------------------------

// TestABrokenProjectSendsNothingAndAGoodOneSends is the assertion this
// whole ordering exists for, and its positive control is in the same
// function on purpose.
//
// A COUNT OF ZERO IS WHAT A CLIENT THAT WAS NEVER BUILT ALSO PRODUCES.
// The instrument is the server, so a zero from it means "nothing
// arrived" — which is the same reading whether the run declined to send
// or could not have sent. The second half runs the same harness to a
// finish and requires a non-zero count, so the zero above means the run
// chose not to send rather than that nothing here can.
//
// REQUIRED MUTATION, run 2026-09-08: move the capacity-and-login block
// above the walk in Deploy. The broken half reds on a non-zero count
// while the control stays green.
func TestABrokenProjectSendsNothingAndAGoodOneSends(t *testing.T) {
	t.Run("a project that cannot deploy", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "astro-absent"))

		_, err := run.run()
		if err == nil {
			t.Fatal("a project with no astro dependency deployed")
		}
		if sent := run.script.sent(); sent != 0 {
			t.Errorf("a broken project sent %d requests (%v), want none — nothing "+
				"local had to be paid for over the network", sent, run.script.paths)
		}
	})

	t.Run("control: a project that deploys does send", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}

		handoff, err := run.run()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}
		defer handoff.Release()

		if sent := run.script.sent(); sent == 0 {
			t.Error("the control sent nothing either, so the zero above says nothing " +
				"about this sequence and everything about the instrument")
		}
	})
}

// TestAnUnattendedRunOnABrokenProjectPrintsTheFix is the shape that
// would have surfaced in continuous integration, and it is the reason
// pre-flight precedes the login rather than a nicety of it.
//
// With a fresh config there is never a token, so login-first reaches the
// email prompt, finds nobody to ask, and ends with a message about
// needing a terminal — which says nothing about the project and is not
// the sentence anybody can act on.
//
// REQUIRED MUTATION, run 2026-09-08: move the capacity-and-login block
// above the walk. This reds with the terminal message in place of the
// fix, exit code 1 either way — which is why the assertion is on the
// words rather than on the number.
func TestAnUnattendedRunOnABrokenProjectPrintsTheFix(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "astro-absent"))
	run.prompt.notInteractive = true

	_, err := run.run()
	if err == nil {
		t.Fatal("a project with no astro dependency deployed")
	}

	text, code := renderedBytes(t, err)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(text, "astro as a dependency") {
		t.Errorf("the run did not print the fix for the thing that is wrong:\n%s", text)
	}
	if strings.Contains(text, "needs a terminal") {
		t.Errorf("the run complained about having nobody to ask, on a project it "+
			"could have refused without asking anything:\n%s", text)
	}
}

// -------------------------------------------------------------------
// The token, the capacity check and the login
// -------------------------------------------------------------------

// TestAStoredTokenSkipsTheCapacityCheckAndTheLogin. The daily cap counts
// ACCOUNTS and an account is spent at the verify step, so a run that
// needs no account must not spend a request asking about one — and must
// certainly not be stopped by the answer.
//
// REQUIRED MUTATION, run 2026-09-08: call CapacityGate unconditionally
// in Deploy, outside the no-token branch. Reds on the capacity count.
func TestAStoredTokenSkipsTheCapacityCheckAndTheLogin(t *testing.T) {
	root := fixtureProject(t, "valid")
	run := newDeployRun(t, root)
	run.storedToken("stored-token", run.srv.URL)

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	for _, path := range []string{"/v1/capacity", "/v1/auth/start", "/v1/auth/verify"} {
		if n := run.script.sentTo(path); n != 0 {
			t.Errorf("a run holding a token called %s %d times, want none", path, n)
		}
	}
	if got := handoff.Client.Token; got != ui.Secret("stored-token") {
		t.Error("the hand-off client is not carrying the stored token")
	}
}

// TestAClosedDoorDoesNotStopARunThatAlreadyHasAToken. Capacity is about
// making an account, so a returning user meets a shut cap only if the
// gate is asked a question that is not theirs.
func TestAClosedDoorDoesNotStopARunThatAlreadyHasAToken(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid"))
	run.storedToken("stored-token", run.srv.URL)
	run.script.capacity = wire.CapacityResponse{
		Open:     false,
		ResetsAt: fixedNowLocal.Add(3 * time.Hour),
	}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("a shut account cap stopped a run that needed no account: %v\n%s",
			err, rendered(err))
	}
	handoff.Release()
}

// TestAStoredTokenIsReusedOnlyForTheEndpointItCameFrom. The stored pair
// is a token AND the endpoint it was issued against, so a token from a
// local development server is never sent to production: the run logs in
// again instead.
//
// THE CONTROL IS THE WHOLE ROW, and it was added because the first
// version of it did not discriminate at all. Asserting only that a
// mismatched endpoint produces a login is satisfied by a sequence that
// never consults the endpoint, never reuses any token, and logs in every
// single time. Measured, not reasoned about: with config.Load handed a
// constant instead of the resolved endpoint, the mismatch half stayed
// GREEN and only the control went red. The comment that first stood here
// predicted the opposite, in both halves.
//
// REQUIRED MUTATION, run 2026-09-08: pass a constant string to
// config.Load in Deploy instead of the resolved endpoint. The CONTROL
// reds; the mismatch half stays green.
func TestAStoredTokenIsReusedOnlyForTheEndpointItCameFrom(t *testing.T) {
	t.Run("a token issued somewhere else is not reused", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.storedToken("somewhere-elses-token", "https://api.example.com")
		run.prompt.confirms = []answer{no()}

		handoff, err := run.run()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}
		defer handoff.Release()

		if n := run.script.sentTo("/v1/auth/verify"); n != 1 {
			t.Errorf("the run verified %d times, want 1 — a token issued somewhere "+
				"else is not a login", n)
		}
		if got := handoff.Client.Token; got != ui.Secret("issued-token") {
			t.Error("the hand-off client is not carrying the token this login issued")
		}
	})

	t.Run("control: a token issued here is reused without a login", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid"))
		run.storedToken("this-endpoints-token", run.srv.URL)

		handoff, err := run.run()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}
		defer handoff.Release()

		if n := run.script.sentTo("/v1/auth/verify"); n != 0 {
			t.Errorf("the run verified %d times against a token it already had — so "+
				"the half above proves nothing about the endpoint and everything "+
				"about a sequence that logs in every time", n)
		}
		if got := handoff.Client.Token; got != ui.Secret("this-endpoints-token") {
			t.Error("the hand-off client is not carrying the stored token")
		}
	})
}

// -------------------------------------------------------------------
// The warning prompt
// -------------------------------------------------------------------

// TestTheWarningPromptIsAnsweredBeforeAnythingIsSent, and it carries the
// WALK's warnings as well as the pre-flight checks'.
//
// One question for all of them is the rule; this is the half of it that
// crosses a package boundary, because the walk reports its findings from
// somewhere the pre-flight engine cannot see. Two producers, one prompt.
//
// REQUIRED MUTATION, run 2026-09-08: drop tree.Results from the
// check.Combine call in Deploy. It reds — and it reds as a COVERAGE
// REFUSAL rather than as a missing warning line, so every row that gets
// as far as the report reds with it. That blast radius is the gate doing
// exactly what the comment beside that call claims: forgetting a
// producer does not make a quietly smaller report, it makes no report.
func TestTheWarningPromptIsAnsweredBeforeAnythingIsSent(t *testing.T) {
	root := fixtureProject(t, "localhost-hits")
	run := newDeployRun(t, root).scriptedLogin()
	run.deps.FS = &linkAddingFS{countingFS: run.fsys, name: "shortcut"}
	run.prompt.confirms = []answer{yes(), no()}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	if asked := run.prompt.asked("Continue anyway?"); asked != 1 {
		t.Errorf("the run asked to continue %d times, want exactly 1 for every "+
			"warning it found", asked)
	}

	shown := run.prompt.out.String()
	for _, want := range []string{"http://localhost", "shortcut"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the warnings shown do not mention %q:\n%s", want, shown)
		}
	}

	// The ordering half: the question came before anything left the
	// machine, so nobody is asked to approve a run that already started.
	events := run.journal.all()
	prompt := indexOfEvent(events, "asked: Continue anyway?")
	first := firstRequest(events)
	if prompt < 0 {
		t.Fatalf("no prompt in the journal:\n  %s", strings.Join(events, "\n  "))
	}
	if first >= 0 && first < prompt {
		t.Errorf("the run sent %s before asking:\n  %s", events[first],
			strings.Join(events, "\n  "))
	}
}

// -------------------------------------------------------------------
// What each ending costs
// -------------------------------------------------------------------

// TestWhatEachEndingCosts is one assertion per exit code, and the codes
// are the product: a script branches on them.
//
// THE DISTINCTION IS WHO DECLINED. Zero means the run is not a fault —
// it worked, or the person said no. One means something is wrong: the
// project, the network, the server. Three means the SERVICE declined
// while the project is fine, which is the only one worth retrying
// unchanged.
//
// REQUIRED MUTATION, run 2026-09-08: return ui.NewFailure instead of
// ui.ServerClosed from capacityCheckFailure's maintenance branch. The
// maintenance row reds on 3 against 1; every other row stays green.
func TestWhatEachEndingCosts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		project  string
		wantCode int
		arrange  func(*deployRun)
	}{
		{
			name:     "a deploy that worked",
			project:  "valid",
			wantCode: 0,
			arrange: func(r *deployRun) {
				r.scriptedLogin().prompt.confirms = []answer{no()}
			},
		},
		{
			name:     "the user declined the warnings",
			project:  "localhost-hits",
			wantCode: 0,
			arrange: func(r *deployRun) {
				r.prompt.confirms = []answer{no()}
			},
		},
		{
			name:     "the project cannot be built",
			project:  "astro-absent",
			wantCode: 1,
			arrange:  func(r *deployRun) {},
		},
		{
			name:     "the server broke",
			project:  "valid",
			wantCode: 1,
			arrange: func(r *deployRun) {
				r.script.capacityOutcome = fails(http.StatusInternalServerError,
					wire.CodeInternal, "something went wrong")
			},
		},
		{
			name:     "the door is shut for today",
			project:  "valid",
			wantCode: ui.ExitServerClosed,
			arrange: func(r *deployRun) {
				r.script.capacity = wire.CapacityResponse{
					Open:     false,
					ResetsAt: fixedNowLocal.Add(3 * time.Hour),
				}
				// Decline the waitlist: the run still stopped, and it
				// stopped because the service said no.
				r.prompt.confirms = []answer{no()}
			},
		},
		{
			name:     "the kill switch is on",
			project:  "valid",
			wantCode: ui.ExitServerClosed,
			arrange: func(r *deployRun) {
				r.script.capacityOutcome = fails(http.StatusServiceUnavailable,
					wire.CodeMaintenance, "curious.pub is down for maintenance")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, tc.project))
			tc.arrange(run)

			handoff, err := run.run()
			defer handoff.Release()

			_, code := renderedBytes(t, err)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (error: %v)", code, tc.wantCode, err)
			}
		})
	}
}

// -------------------------------------------------------------------
// What a refused run leaves behind
// -------------------------------------------------------------------

// TestARefusedRunPacksNothingAndLeavesNothingBehind covers every way a
// run can stop before the pack, and its control is the run that does
// pack.
//
// THE CONTROL IS WHAT MAKES THE EMPTY DIRECTORIES MEAN ANYTHING. Each
// refusal asserts that a directory is empty, which is also true of a
// sequence that never writes anything at all — so the last row requires
// an archive to be there during the run, and gone after Release.
//
// REQUIRED MUTATION, run 2026-09-08: create the working directory at the
// top of Deploy instead of immediately before the pack. All three
// refusals red on a leftover directory; the control stays green.
func TestARefusedRunPacksNothingAndLeavesNothingBehind(t *testing.T) {
	oversize := make([]byte, wire.MaxSourceFileBytes+1)

	for _, tc := range []struct {
		name    string
		root    func(*testing.T) string
		arrange func(*deployRun)
	}{
		{
			name:    "a pre-flight hard stop",
			root:    func(t *testing.T) string { return fixtureProject(t, "astro-absent") },
			arrange: func(r *deployRun) {},
		},
		{
			name: "a warning the user declined",
			root: func(t *testing.T) string { return fixtureProject(t, "localhost-hits") },
			arrange: func(r *deployRun) {
				r.prompt.confirms = []answer{no()}
			},
		},
		{
			name: "a project over a local limit",
			root: func(t *testing.T) string {
				return writeProject(t, astroProject(map[string][]byte{
					"public/enormous.bin": oversize,
				}))
			},
			arrange: func(r *deployRun) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newDeployRun(t, tc.root(t))
			tc.arrange(run)

			handoff, err := run.run()
			if err == nil {
				handoff.Release()
				t.Fatal("the run finished, so it is not a refusal")
			}
			if handoff != nil {
				t.Error("a refused run handed back a request")
			}
			if left := run.leftBehind(); len(left) != 0 {
				t.Errorf("the run left %v behind", left)
			}
			// What each of these ENDINGS costs is asserted once, in the
			// exit-code table above — and it is not one answer: a user
			// who declined is not a fault and exits 0. Repeating a
			// "non-zero" claim here would be wrong for that row and would
			// make this one about two subjects.
			if run.script.sent() != 0 {
				t.Errorf("a refused run sent %v", run.script.paths)
			}
		})
	}

	t.Run("control: a run that packs leaves the archive, and Release takes it", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}

		handoff, err := run.run()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}
		if _, statErr := os.Stat(handoff.ArchivePath); statErr != nil {
			t.Fatalf("the archive is not there during the run: %v", statErr)
		}
		handoff.Release()
		if _, statErr := os.Stat(handoff.ArchivePath); !os.IsNotExist(statErr) {
			t.Errorf("the archive survived Release: %v", statErr)
		}
		if left := run.leftBehind(); len(left) != 0 {
			t.Errorf("Release left %v behind", left)
		}
	})
}

// -------------------------------------------------------------------
// The hand-off
// -------------------------------------------------------------------

// TestTheHandoffIsTheFormedRequest. Five fields, four of which the
// server never sees, which is why this is an internal type rather than
// the one-field wire request it feeds.
//
// Each field is checked against the artefact rather than against a
// number written down beside it: the size comes from a stat of the file,
// the entry count from the walk's own list.
//
// REQUIRED MUTATION, run 2026-09-08: hand back the SOURCE total in
// Handoff.Bytes rather than the archive's size. Reds on the size — which
// is the mistake worth a row, because the server signs the upload with
// that number as an exact length and the wrong one refuses every byte.
func TestTheHandoffIsTheFormedRequest(t *testing.T) {
	root := fixtureProject(t, "valid")
	run := newDeployRun(t, root).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	info, err := os.Stat(handoff.ArchivePath)
	if err != nil {
		t.Fatalf("the hand-off names an archive that is not there: %v", err)
	}
	if handoff.Bytes != info.Size() {
		t.Errorf("Bytes = %d, want the archive's own size %d", handoff.Bytes, info.Size())
	}
	if len(handoff.SHA256) != 64 {
		t.Errorf("SHA256 = %q, want a hex digest", handoff.SHA256)
	}

	tree, err := pack.Walk(pack.OSFileSystem{}, root)
	if err != nil {
		t.Fatalf("walking the fixture to count it: %v", err)
	}
	if handoff.Entries != len(tree.Files) {
		t.Errorf("Entries = %d, want the %d files the walk found", handoff.Entries, len(tree.Files))
	}
	if handoff.Client == nil {
		t.Fatal("the hand-off carries no client")
	}

	// The wire request this feeds carries ONE of these five fields.
	if req := (wire.DeployCreateRequest{Bytes: handoff.Bytes}); req.Bytes != info.Size() {
		t.Errorf("the wire request would declare %d bytes, want %d", req.Bytes, info.Size())
	}
}

// TestASuccessfulRunSaysWhatItPackedAndWhereItStops. A command that
// packs an archive and then says nothing reads as one that failed
// quietly, and a person who is told the upload is not built yet does not
// file a bug about it.
func TestASuccessfulRunSaysWhatItPackedAndWhereItStops(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	shown := run.prompt.out.String()
	for _, want := range []string{"Packed ", "into an archive of ", stopsHere} {
		if !strings.Contains(shown, want) {
			t.Errorf("the run never said %q:\n%s", want, shown)
		}
	}
}

// -------------------------------------------------------------------
// Ctrl-C
// -------------------------------------------------------------------

// TestTheRunHandsTheInterruptHandlerATidyUp is what stands between a
// Ctrl-C during the pack and a half-written archive left on the machine
// for ever.
//
// IT ASSERTS THE TIDY-UP, NOT A SIGNAL. The shipped handler ends the
// process by letting the signal kill it, so a row that raised a real
// interrupt would kill the test binary rather than fail — measured next
// door, in the package that owns the handler, where the property is
// covered out of process. What belongs here is the half this sequence
// owns: that a handler is installed before anything is written, and that
// the function it was handed removes what the run put on the machine.
//
// REQUIRED MUTATION, run 2026-09-08: stop calling deps.Interrupts in
// Deploy. This reds on the install count; nothing else in the file
// notices, which is exactly why the row exists.
func TestTheRunHandsTheInterruptHandlerATidyUp(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}

	if run.installs != 1 {
		t.Fatalf("the run installed %d interrupt handlers, want exactly 1", run.installs)
	}
	if run.cleanup == nil {
		t.Fatal("the run installed a handler with no tidy-up, which is the whole " +
			"of what installing one buys")
	}

	// It was installed BEFORE the pack, which is the only position that
	// helps: a handler armed afterwards covers nothing that was being
	// written while it was not.
	events := run.journal.all()
	installed := indexOfEvent(events, "interrupt handler installed")
	packed := indexOfEvent(events, "pack")
	if installed < 0 || packed < 0 || installed > packed {
		t.Errorf("the handler went in at %d and the pack began at %d:\n  %s",
			installed, packed, strings.Join(events, "\n  "))
	}

	if _, statErr := os.Stat(handoff.ArchivePath); statErr != nil {
		t.Fatalf("the archive is not there to be removed: %v", statErr)
	}
	run.cleanup()
	if left := run.leftBehind(); len(left) != 0 {
		t.Errorf("the tidy-up the handler was given left %v behind", left)
	}

	// Release after the tidy-up has already run: a caller's defer fires
	// whatever else happened, and removing an absent directory twice
	// must not be an error anybody sees.
	handoff.Release()
	if run.stops != 1 {
		t.Errorf("the handler was released %d times, want exactly 1 — a run that "+
			"finished must give the signal back to the runtime", run.stops)
	}
}

// -------------------------------------------------------------------
// The directory
// -------------------------------------------------------------------

// TestADirectoryThatIsNotOneIsRefusedByName. The message names the path,
// because the commonest cause is a typo or the wrong working directory
// and neither is diagnosable from "that did not work".
//
// The control is a directory that IS one: a refusal that named the path
// for everything, including a real project, would satisfy both rows.
func TestADirectoryThatIsNotOneIsRefusedByName(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-project")
	file := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(file, []byte("<html>"), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	for _, tc := range []struct {
		name string
		dir  string
		says string
	}{
		{"a path that is not there", missing, "There is nothing at"},
		{"a file instead of a directory", file, "is a file, not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newDeployRun(t, tc.dir)

			_, err := run.run()
			if err == nil {
				t.Fatal("the run continued")
			}
			text, code := renderedBytes(t, err)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(text, tc.says) {
				t.Errorf("the message does not say %q:\n%s", tc.says, text)
			}
			if !strings.Contains(text, tc.dir) {
				t.Errorf("the message does not name %s:\n%s", tc.dir, text)
			}
			if run.script.sent() != 0 {
				t.Errorf("a run that never found a project sent %v", run.script.paths)
			}
		})
	}

	t.Run("control: a real project is not refused", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}

		handoff, err := run.run()
		if err != nil {
			t.Fatalf("a real project was refused: %v\n%s", err, rendered(err))
		}
		handoff.Release()
	})
}

// -------------------------------------------------------------------
// Small helpers
// -------------------------------------------------------------------

func equalEvents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func indexOfEvent(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

// firstRequest is where the first thing left the machine, or -1.
func firstRequest(events []string) int {
	for i, e := range events {
		if strings.HasPrefix(e, "GET /") || strings.HasPrefix(e, "POST /") {
			return i
		}
	}
	return -1
}
