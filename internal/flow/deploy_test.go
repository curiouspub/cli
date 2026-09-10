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

func (p *journalPrompt) Result(format string, args ...any) {
	p.journal.note("printed: " + fmt.Sprintf(format, args...))
	p.scriptedPrompt.Result(format, args...)
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

	// store is the object store this API hands upload links to. The
	// API SIGNS the length it was told, so the store can refuse a body
	// that does not match it — the whole point of the pair being two
	// servers rather than one.
	store *objectStore

	// signSkew is added to the declared size before it is signed, so a
	// row can produce the one failure a correct client cannot make on
	// its own: a signature that does not cover the body being sent.
	signSkew int64

	// deployOutcome, deployID and expiresAt script the create.
	// expiresAt is ZERO by default and the response then OMITS the
	// field entirely, because that is what the server sends today — so
	// the ambiguous branch is the one the default harness exercises.
	deployOutcome outcome
	deployID      string
	expiresAt     time.Time
	creates       []wire.DeployCreateRequest
	createBearers []string

	// onCreate runs while the create is being served, before the reply.
	// It is how a row makes an answer never arrive.
	onCreate func(*http.Request)

	// uploadOverride replaces the link this API hands out, for the rows
	// that need one nothing answers on.
	uploadOverride string

	// refuseCreatesUntil is how many creates are answered "not
	// authenticated" before one is allowed through, so a row can watch
	// the run re-authenticate and try again — and watch it stop when a
	// second refusal follows the fresh login.
	refuseCreatesUntil int

	// startOutcome scripts the start's answer and startBody is the
	// success body it sends, written as raw JSON so a row can return a
	// status this build has never heard of — which is the only way to
	// drive the render-unknown obligation from the endpoint that
	// actually carries it.
	startOutcome outcome
	startBody    string

	// startHangsUp makes the start receive the request and then drop the
	// connection without answering, which is how a row produces "the
	// request left this process and no answer came back" WITH THE RUN
	// STILL ALIVE.
	//
	// A CANCELLED CONTEXT WILL NOT DO, and finding that out is what this
	// field is for. Cancelling the run to make an answer never arrive
	// also makes every subsequent request fail before it leaves the
	// machine — so a client that retried would send nothing, the server
	// would count nothing, and a row asserting "sent exactly once" would
	// pass against exactly the client it exists to refuse. Measured: the
	// mutation that adds a retry left that row green.
	startHangsUp bool

	// eventScripts is the queue of connections the event stream serves:
	// the first GET gets the first entry, the second the second, and once
	// the queue is exhausted the LAST entry repeats. That last part is
	// what lets a row say "every reconnection meets the same broken
	// stream" without writing the same script six times.
	eventScripts []eventScript
	eventGETs    int

	// eventGaps is the interval between the handler's own flushes, every
	// connection's collected together. A timing row reads it to say
	// whether it measured the client or the machine: a fixture that
	// itself paused past the window under test has measured the runner.
	eventGaps []time.Duration

	// publishOutcome scripts the publish's answer, publishSubdomain and
	// publishExpiresAt the success body, and publishBody replaces that
	// body with raw JSON — which is the only way to send a field this
	// build's type does not have.
	publishOutcome   outcome
	publishSubdomain string
	publishExpiresAt time.Time
	publishBody      string
	publishes        int

	// publishNotReadyFor is how many publishes are answered "not ready"
	// before the scripted outcome applies, so a row can watch the run
	// resolve the race the last step actually meets — and, with the
	// outcome ALSO set to not-ready, watch it give up.
	publishNotReadyFor int

	// publishHangsUp makes the publish receive the request and then drop
	// the connection without answering, which is how a row produces "the
	// request left this process and no answer came back" with the run
	// still alive. Cancelling the context would not do: it would also
	// stop anything that followed from ever leaving the machine.
	publishHangsUp bool

	// release is closed when the test ends, so a connection held open on
	// purpose cannot outlive its row.
	release chan struct{}
}

// eventScript is ONE connection to the event stream: the frames it
// writes, and what it does when it has written them.
type eventScript struct {
	// frames are written and flushed in order, so a client sees them
	// arrive rather than finding them all in one read.
	frames []string

	// pace is how long the handler waits between frames. It is what
	// makes a stream that is merely slow distinguishable from one that
	// has stopped.
	pace time.Duration

	// hold keeps the connection OPEN after the last frame instead of
	// closing it, until the test ends or the client goes away.
	//
	// IT IS THE INSTRUMENT FOR TWO OPPOSITE ROWS. After a terminating
	// event it means a client that ignored that event HANGS rather than
	// passing: closing the connection would hand such a client an
	// end-of-stream it could mistake for the ending it failed to read.
	// After no terminating event it is a stream that has gone quiet with
	// nothing broken, which is what a liveness rule has to see.
	hold bool
}

// eventsPathSuffix and startPathSuffix name the two per-deploy endpoints
// this double serves. They are matched as a SHAPE rather than as whole
// paths because the id in the middle is the create's to choose, and a row
// asserting the id arrived is the point.
const (
	startAction      = "start"
	eventsAction     = "events"
	publishAction    = "publish"
	deployPathPrefix = "/v1/deploys/"
)

// deployAction splits a per-deploy path into the id and the action, and
// reports whether the path had that shape at all.
func deployAction(path string) (id, action string, ok bool) {
	if !strings.HasPrefix(path, deployPathPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, deployPathPrefix)
	id, action, found := strings.Cut(rest, "/")
	if !found || id == "" || action == "" {
		return "", "", false
	}
	return id, action, true
}

// eventScriptFor is the connection the nth event GET is served, with the
// last entry repeating once the queue runs out. The default — nothing
// scripted at all — is one line of build output and a build that
// finished, so every row in this file that is about something else still
// runs a deploy to its end.
func (s *deployScript) eventScriptFor(n int) eventScript {
	if len(s.eventScripts) == 0 {
		return eventScript{frames: []string{
			logFrame("astro build finished"),
			doneFrame(wire.StatusBuilt),
		}}
	}
	if n > len(s.eventScripts) {
		n = len(s.eventScripts)
	}
	return s.eventScripts[n-1]
}

// serveEvents writes one scripted connection.
//
// IT RUNS WITHOUT THE SCRIPT'S MUTEX, and that is not an optimisation: a
// held-open stream keeps this goroutine for the life of the row, and
// holding the mutex with it would deadlock every assertion made
// afterwards — including the ones about the start that ran before it.
func (s *deployScript) serveEvents(w http.ResponseWriter, r *http.Request, script eventScript, release <-chan struct{}) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	last := time.Now()
	var gaps []time.Duration
	for _, frame := range script.frames {
		if script.pace > 0 {
			select {
			case <-time.After(script.pace):
			case <-release:
				return
			case <-r.Context().Done():
				return
			}
		}
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}
		now := time.Now()
		gaps = append(gaps, now.Sub(last))
		last = now
	}

	s.mu.Lock()
	s.eventGaps = append(s.eventGaps, gaps...)
	s.mu.Unlock()

	if script.hold {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

// sentPrefix marks a journal entry that records ONE request leaving this
// machine, method and absolute URL. It is a prefix rather than a second
// slice because the question it answers — "everything this run sent" —
// ranges over two servers and a transport, and three separate lists
// cannot be read in order.
const sentPrefix = "sent: "

// absoluteURL is the address a request was actually made to, rebuilt
// from what the server received. The scheme is the one these doubles
// serve on; what matters to the rows that read this is the HOST, since a
// path alone cannot tell one destination from another.
func absoluteURL(r *http.Request) string {
	return "http://" + r.Host + r.URL.RequestURI()
}

// servePublish answers the last call of a deploy.
//
// The success body is written BY HAND for the same reason the create's
// is: a zero expiry must be ABSENT rather than encoded as the zero
// instant, because "the server did not tell me" and "the server told me
// the epoch" are two different things and only an omitted key models the
// first.
func (s *deployScript) servePublish(w http.ResponseWriter, r *http.Request) {
	// THIS RUNS WITH s.mu ALREADY HELD BY ServeHTTP, so the increment is
	// synchronised and taking the lock here would deadlock — which it
	// did, once, while this comment's first draft was being written.
	// What was NOT synchronised was the READ: rows reached
	// run.script.publishes straight off the struct from the test
	// goroutine, and the hang-up branch below leaves this handler's
	// goroutine alive after the client has given up, so a row asserting
	// "the publish was sent exactly once" was reading a counter another
	// goroutine could still be writing. Found by the race pass this
	// round adds, on its first full run over this package, in a fixture
	// that had been that way since the row was written. The reads now go
	// through publishCount.
	s.publishes++

	if s.publishHangsUp {
		// The request arrived and is not answered: the connection goes
		// away underneath it.
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, hijackErr := hijacker.Hijack(); hijackErr == nil {
				_ = conn.Close()
				return
			}
		}
		panic("this server cannot drop a connection, so the row that needs one " +
			"would silently be measuring something else")
	}

	if s.publishes <= s.publishNotReadyFor {
		s.reply(w, fails(http.StatusConflict, wire.CodeDeployNotReady,
			`this deploy is "building" and cannot be given an address yet`), nil)
		return
	}
	if s.publishOutcome.code != "" {
		s.reply(w, s.publishOutcome, nil)
		return
	}

	body := s.publishBody
	if body == "" {
		fields := map[string]any{"subdomain": s.publishSubdomain}
		if !s.publishExpiresAt.IsZero() {
			fields["expires_at"] = s.publishExpiresAt.Format(time.RFC3339Nano)
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			panic(err)
		}
		body = string(encoded)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
	_ = r
}

func (s *deployScript) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()

	s.requests++
	s.paths = append(s.paths, r.URL.Path)
	s.bearers = append(s.bearers, r.Header.Get("Authorization"))
	s.journal.note(r.Method + " " + r.URL.Path)
	s.journal.note(sentPrefix + r.Method + " " + absoluteURL(r))

	if _, action, ok := deployAction(r.URL.Path); ok && action == eventsAction {
		s.eventGETs++
		script, release := s.eventScriptFor(s.eventGETs), s.release
		s.mu.Unlock()
		s.serveEvents(w, r, script, release)
		return
	}

	defer s.mu.Unlock()

	if _, action, ok := deployAction(r.URL.Path); ok {
		if action == publishAction {
			s.servePublish(w, r)
			return
		}
		if action != startAction {
			s.strays = append(s.strays, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if s.startHangsUp {
			// The request arrived and is not answered: the connection
			// goes away underneath it.
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, hijackErr := hijacker.Hijack(); hijackErr == nil {
					_ = conn.Close()
					return
				}
			}
			panic("this server cannot drop a connection, so the row that needs one " +
				"would silently be measuring something else")
		}
		if s.startOutcome.code != "" {
			s.reply(w, s.startOutcome, nil)
			return
		}
		body := s.startBody
		if body == "" {
			body = `{"status":"` + string(wire.StatusBuilding) + `"}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, body)
		return
	}

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
	case "/v1/deploys":
		var req wire.DeployCreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.creates = append(s.creates, req)
		s.createBearers = append(s.createBearers, r.Header.Get("Authorization"))
		if s.onCreate != nil {
			s.onCreate(r)
		}
		if len(s.creates) <= s.refuseCreatesUntil {
			s.reply(w, fails(http.StatusUnauthorized, wire.CodeUnauthorized,
				"this request was not authenticated"), nil)
			return
		}
		if s.deployOutcome.code != "" {
			s.reply(w, s.deployOutcome, nil)
			return
		}
		if s.store != nil {
			s.store.sign(req.Bytes + s.signSkew)
		}
		s.replyCreated(w, req)
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

// replyCreated writes the create's success body. It writes the JSON by
// hand for one reason: a ZERO expiry must be ABSENT from the body rather
// than encoded as the zero instant, because "the server did not tell me"
// and "the server told me the epoch" are the two readings this client
// must never confuse, and only an omitted key models the first.
func (s *deployScript) replyCreated(w http.ResponseWriter, req wire.DeployCreateRequest) {
	uploadURL := s.uploadOverride
	if uploadURL == "" && s.store != nil {
		uploadURL = s.store.url
	}
	id := s.deployID
	if id == "" {
		id = "deploy-1"
	}
	body := map[string]any{"deploy_id": id, "upload_url": uploadURL}
	if !s.expiresAt.IsZero() {
		body["expires_at"] = s.expiresAt.Format(time.RFC3339Nano)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
	_ = req
}

// -------------------------------------------------------------------
// The object store
// -------------------------------------------------------------------

// objectStore is the SECOND in-process server, and its separateness is
// the instrument. The upload leaves this client for a different origin,
// carries no bearer token, and answers with a status and a body the /v1
// contract does not define — so a single double could not tell a row
// which of the two was talked to, which is the one thing several rows
// here are entirely about.
type objectStore struct {
	mu      sync.Mutex
	journal *deployJournal

	// url is the "presigned" target the create hands out.
	url string

	// signed is the exact Content-Length this store's signature covers,
	// and signedSet says whether one was ever issued. A store that
	// checks nothing makes a wrong length look correctly refused, so
	// the check is MODELLED here rather than assumed.
	signed    int64
	signedSet bool

	// status, when non-zero, is the answer whatever the length is. body
	// is the store's own error document, and location turns the answer
	// into a redirect.
	status   int
	body     string
	location string

	// acceptAnyLength turns the signature check off. A row whose
	// subject is the number the CREATE declared wants it: a store that
	// refuses the upload turns a wrong declaration into a refusal, and
	// then the row reds on the refusal without ever saying which of the
	// two sizes was sent.
	acceptAnyLength bool

	puts []storePut

	// readChunk and readPause make this store SLOW BUT PROGRESSING: it
	// consumes the body readChunk bytes at a time, pausing readPause
	// between them. A row uses it to spend several stall windows on one
	// upload while never letting the gap between two bytes reach one.
	readChunk int
	readPause time.Duration

	// pauseUntil is how many bytes are consumed at the paced rate before
	// the rest is drained at full speed. The tail matters: once the
	// client has handed its last byte to the socket there is no progress
	// left to report, so a paced drain of the remainder would look like a
	// stall to a client that had done nothing wrong.
	pauseUntil int64

	// stopReadingAfter makes it WEDGED: it consumes that many bytes and
	// then stops reading, holding the request open without ever
	// finishing it. That is the failure a total deadline and a stall
	// timer tell apart, and the only shape that can show the difference.
	stopReadingAfter int64

	// release is closed when the test ends, so a wedged handler cannot
	// outlive its row.
	release chan struct{}

	// pin is what the kernel said when this store's listener asked for
	// the SO_RCVBUF it was built with. It is the FAR HALF of the write
	// side's pin: the gap a write-side row bounds is the time for the
	// client's send buffer to free space, and that is governed by how
	// fast this end drains and how much window it advertises at a time.
	// Pinning only the client would measure this end's autotuning; see
	// socketpin_test.go.
	//
	// THE SIZE IS NOT A FIELD HERE, and that is the round-2 defect
	// removed rather than a tidying. It was one — written after the
	// server had already started — so a connection could be accepted
	// before the size existed, autotune, and be counted into the same
	// record as the pinned ones. The size now travels in the listener
	// wrapper, fixed before the wrapper is installed, so there is no
	// moment at which the store is accepting and unpinned and nothing
	// left for a mutex to protect.
	pin *socketPin

	srv *httptest.Server
}

// storePut is one request this store actually received.
type storePut struct {
	method        string
	contentLength int64
	bodyLength    int64
	authorization string
	status        int
}

// newObjectStore builds the store double, with the receive buffer every
// connection it accepts will be pinned to fixed HERE, before the
// listener that will accept them is wrapped.
//
// THE PIN IS A PARAMETER RATHER THAN A FIELD, and the reason is a
// measurement. Round 2 set it afterwards, on the started server: the
// listener was already accepting, so a connection could arrive before
// the size existed and autotune, and the run's record then covered two
// connections under two different conditions. Under -race the write and
// the accept-side read were reported as the data race they were, and
// the run's own "two connections disagree" refusal reported the
// consequence — 392384 on one and 131072 on the next. A parameter has no
// such window: the size is known before anything can be accepted on the
// listener it is installed in, which is what makes the mutex that used
// to guard it unnecessary rather than merely absent.
//
// receivePin of zero pins nothing, which is what every row that is not
// about a stall window wants: the pin narrows a socket deliberately, and
// a row about an error message has no business running through one.
func newObjectStore(t *testing.T, journal *deployJournal, receivePin int) *objectStore {
	t.Helper()
	store := &objectStore{journal: journal, release: make(chan struct{}), pin: &socketPin{}}
	// UNSTARTED, so the listener can be wrapped before anything is
	// accepted on it. The only moment a receive buffer can be set before
	// the client starts filling it is the accept, which is over long
	// before a handler is called.
	srv := httptest.NewUnstartedServer(store)
	srv.Listener = &pinnedListener{Listener: srv.Listener, size: receivePin, pin: store.pin}
	srv.Start()
	store.srv = srv
	// The release closes FIRST, so a wedged handler is let go before
	// srv.Close waits for it. Cleanups run last-registered-first.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(store.release) })
	// A query string carrying something signature-shaped, because the
	// rule this client keeps is about a credential in a query string
	// and a target with none could not show it being kept.
	store.url = srv.URL + "/o/source.tgz?Sig=" + storeSignature
	return store
}

// storeSignature is the sentinel every row uses to ask "did the signed
// URL reach anywhere it should not". It is a fixture value and names no
// real credential.
const storeSignature = "SENTINEL-SIGNATURE-VALUE"

// closeNow shuts this store down before the run that would talk to it,
// which is how a row gets an address nothing answers on without naming a
// host or needing a resolver.
func (s *objectStore) closeNow() { s.srv.Close() }

func (s *objectStore) sign(length int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signed = length
	s.signedSet = true
}

func (s *objectStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The reading policy is snapshotted under the lock and the body is
	// then read WITHOUT it: a wedged handler holds this goroutine for the
	// life of the row, and holding the mutex too would deadlock every
	// assertion the row makes afterwards.
	s.mu.Lock()
	chunk, pause, stopAfter, release := s.readChunk, s.readPause, s.stopReadingAfter, s.release
	pauseUntil := s.pauseUntil
	s.mu.Unlock()

	// THE BODY IS COUNTED AND NOT KEPT. Nothing anywhere asks this
	// fixture what the bytes WERE — only how many arrived — and the
	// version that accumulated them appended up to a whole archive per
	// request, reallocating and copying a growing slice while the paced
	// loop was running, on the critical path of the very gap the
	// write-side probe measures.
	//
	// THAT WAS A HYPOTHESIS ABOUT THE PROBE'S TAIL AND THE MEASUREMENT
	// REFUTED IT, which is why it is written down here rather than
	// claimed. On darwin, 2026-09-10, the worst gap over one hundred runs
	// was 162.4 ms with the body counted against 221.3 ms with it kept —
	// and the per-pass maxima on both sides of the change ran from about
	// 105 ms to about 220 ms, so the two sets overlap almost completely
	// and the difference is where each set's outlier happened to land.
	// The allocator was not what sets this tail. The change stays because
	// it removes a whole-archive allocation from every upload row in the
	// suite and costs nothing; it is not the reason the number moved,
	// because the number did not move.
	var bodyLength int64
	switch {
	case stopAfter > 0:
		_, _ = io.CopyN(io.Discard, r.Body, stopAfter)
		<-release
		return
	case pause > 0 && chunk > 0:
		buf := make([]byte, chunk)
		var paced int64
		for paced < pauseUntil {
			n, err := io.ReadFull(r.Body, buf)
			paced += int64(n)
			if err != nil {
				break
			}
			select {
			case <-time.After(pause):
			case <-release:
				return
			}
		}
		rest, _ := io.Copy(io.Discard, r.Body)
		bodyLength = paced + rest
	default:
		bodyLength, _ = io.Copy(io.Discard, r.Body)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	status := http.StatusOK
	switch {
	case s.status != 0:
		status = s.status
	case s.signedSet && !s.acceptAnyLength && r.ContentLength != s.signed:
		// The one refusal a real store makes for a body whose length
		// does not match what was signed. It is the SAME status a
		// closed window gets, which is the whole reason this client
		// cannot read the cause off the response.
		status = http.StatusForbidden
	}

	s.puts = append(s.puts, storePut{
		method:        r.Method,
		contentLength: r.ContentLength,
		bodyLength:    bodyLength,
		authorization: r.Header.Get("Authorization"),
		status:        status,
	})
	if s.journal != nil {
		s.journal.note(r.Method + " (object store)")
		s.journal.note(sentPrefix + r.Method + " " + absoluteURL(r))
	}

	if s.location != "" {
		w.Header().Set("Location", s.location)
	}
	w.WriteHeader(status)
	if status != http.StatusOK && s.body != "" {
		_, _ = w.Write([]byte(s.body))
	}
}

// received is every request this store actually saw.
func (s *objectStore) received() []storePut {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storePut(nil), s.puts...)
}

// storeErrorDocument is a well-formed error document of the shape an
// object store answers with. It exists so a row can prove the rendered
// message is the AUTHORED one rather than something read out of a body
// this client has no business parsing.
const storeErrorDocument = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<Error><Code>RefusedByTheStore</Code>` +
	`<Message>Do not render me.</Message>` +
	`<RequestId>abc123</RequestId></Error>`

// -------------------------------------------------------------------
// The event frames
// -------------------------------------------------------------------

// The frame builders below write REAL Server-Sent Events: an event name,
// a data line carrying the contract's own JSON, and the blank line that
// dispatches it. They go through encoding/json rather than through a
// hand-written string, so a control byte in a fixture is escaped the way
// a server would escape it and the client decodes what a server would
// really have sent.

// rawFrame is the general form, for the two shapes the contract's Go
// types cannot express: an event type this build has never heard of, and
// a field value outside the vocabulary an enum can hold.
func rawFrame(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

// commentFrame is a keep-alive: no event, no data, nothing to render. The
// stream sends these so a connection with nothing to say still proves it
// is there.
//
// IT IS ONE LINE AND NOT TWO, and the difference turned out to matter. A
// comment needs no blank line after it — it dispatches nothing — and
// writing one anyway hands the client an ordinary empty line, which is
// enough to look like traffic to a reader that is not counting comments
// as traffic. Measured: with the trailing blank line, the mutation that
// makes only EVENTS count as proof of life left this row green.
func commentFrame() string { return ": keep-alive\n" }

func logFrame(line string) string {
	data, err := json.Marshal(wire.LogEvent{Line: line})
	if err != nil {
		// Unreachable for a struct of strings. Panicking rather than
		// swallowing keeps a broken fixture from producing a frame the
		// client would then be blamed for.
		panic(err)
	}
	return rawFrame(string(wire.EventLog), string(data))
}

func phaseFrame(phase wire.Phase) string {
	return rawFrame(string(wire.EventPhase), `{"phase":"`+string(phase)+`"}`)
}

func errorFrame(code wire.ErrorCode, message string) string {
	data, err := json.Marshal(wire.Error{Code: code, Message: message})
	if err != nil {
		panic(err)
	}
	return rawFrame(string(wire.EventError), string(data))
}

func doneFrame(status wire.DeployStatus) string {
	return rawFrame(string(wire.EventDone), `{"status":"`+string(status)+`"}`)
}

// eventConnections is how many times the stream was opened.
// publishCount is how many publishes this fixture has answered, read
// under the lock the handler writes it under.
func (s *deployScript) publishCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishes
}

func (s *deployScript) eventConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventGETs
}

// widestGap is the longest interval the fixture itself left between two
// flushes. A timing row reads it to tell "the client gave up too early"
// from "this machine paused", which are otherwise the same red.
func (s *deployScript) widestGap() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	var widest time.Duration
	for _, g := range s.eventGaps {
		if g > widest {
			widest = g
		}
	}
	return widest
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
	store   *objectStore
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
// thing it is about. Its object store pins nothing, which is what every
// row that is not about a stall window wants.
func newDeployRun(t *testing.T, root string) *deployRun {
	t.Helper()
	return newDeployRunPinnedAt(t, root, 0)
}

// newDeployRunPinnedAt is the same run with the store's receive buffer
// pinned to receivePin from the instant its listener exists.
//
// IT IS A SECOND CONSTRUCTOR RATHER THAN A SETTER, which is the whole of
// this whole change in one line: a setter can only run after the listener is already
// accepting, and a connection accepted in that gap is a connection under
// a condition nobody chose. Two constructors is the cost of there being
// no such gap.
func newDeployRunPinnedAt(t *testing.T, root string, receivePin int) *deployRun {
	t.Helper()

	// A CONFIG PATH OF THIS TEST'S OWN, and CURIOUS_API_URL emptied: a
	// developer's own environment must not decide whether a row passes.
	t.Setenv("CURIOUS_CONFIG", filepath.Join(t.TempDir(), "curious.json"))
	t.Setenv("CURIOUS_API_URL", "")

	journal := &deployJournal{}
	store := newObjectStore(t, journal, receivePin)
	script := &deployScript{
		journal:  journal,
		capacity: wire.CapacityResponse{Open: true, AccountsLeft: 200},
		token:    "issued-token",
		store:    store,
		release:  make(chan struct{}),
		// The label the address is built from. It is set here rather than
		// defaulted inside the handler so a row can read the value the
		// run will actually be given.
		publishSubdomain: defaultPublishSubdomain,
		// The publish expiry is NON-ZERO by default, the opposite of the
		// create's, because the two model different servers: this field
		// is one the client cannot derive and the server does populate,
		// where the create's is published in the contract and not sent
		// yet. Each default is the answer the real server gives today.
		publishExpiresAt: fixedNowLocal.Add(71 * time.Hour),
	}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)
	// The release closes FIRST, so a stream held open on purpose is let
	// go before srv.Close waits for it. Cleanups run
	// last-registered-first, which is the same order the object store
	// next door relies on.
	t.Cleanup(func() { close(script.release) })
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
		store:      store,
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
		// THE RECONNECT DELAY IS INJECTED FOR EVERY ROW, not only the
		// ones about reconnecting, for the same reason the clock is: the
		// shipped schedule is seconds and a row that reconnects five
		// times would spend all of them waiting. Nothing here chooses a
		// different SCHEDULE — the shape and the attempt count are the
		// shipped ones — only a step short enough to run in a suite.
		StreamReconnectStep: time.Millisecond,
		Interrupts: func(cleanup func()) func() {
			journal.note("interrupt handler installed")
			run.installs++
			run.cleanup = cleanup
			return func() { run.stops++ }
		},
	}
	return run
}

// defaultPublishSubdomain is the label the scripted server hands out
// unless a row says otherwise. It is deliberately NOT the deploy id: a
// client echoing the wrong field of the wrong response would produce a
// working-looking address, and two values that differ are the only thing
// that can see it.
const defaultPublishSubdomain = "quick-koala-4f2a"

// buildLogOutput is the build's own output as it reached stdout, with
// the address the run ends with taken off the end.
//
// STDOUT NOW CARRIES TWO THINGS, and a row about one of them has to say
// which: the build log, line by line as the server wrote it, and then
// the address, once, as the last thing the command prints. Splitting
// them here rather than in every row keeps the address's POSITION
// asserted in one place — and asserts it rather than trimming whatever
// happened to be last, which would quietly absorb a client that printed
// something else there.
func buildLogOutput(t *testing.T, run *deployRun) string {
	t.Helper()
	printed := run.prompt.results.String()
	want := publishedURL(run.script.publishSubdomain) + "\n"
	if !strings.HasSuffix(printed, want) {
		t.Fatalf("stdout does not end with the address %q:\n%q", want, printed)
	}
	return strings.TrimSuffix(printed, want)
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

// uploadingTo points this run's create at a store somebody else owns,
// so two runs can be observed by ONE handler. "The store received both"
// is not a claim two separate handlers can make.
func (r *deployRun) uploadingTo(store *objectStore) *deployRun {
	r.script.store = store
	r.store = store
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
		case e == "POST /v1/deploys":
			step = "create"
		case e == "PUT (object store)":
			step = "upload"
		case strings.HasPrefix(e, "POST "+deployPathPrefix) && strings.HasSuffix(e, "/"+startAction):
			step = "start"
		case strings.HasPrefix(e, "GET "+deployPathPrefix) && strings.HasSuffix(e, "/"+eventsAction):
			step = "stream"
		case strings.HasPrefix(e, "POST "+deployPathPrefix) && strings.HasSuffix(e, "/"+publishAction):
			step = "publish"
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

	want := []string{"walk", "pre-flight", "capacity", "login", "pack", "create",
		"upload", "start", "stream", "publish"}
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

	// The wire request this feeds carries ONE of these six fields.
	if req := (wire.DeployCreateRequest{Bytes: handoff.Bytes}); req.Bytes != info.Size() {
		t.Errorf("the wire request would declare %d bytes, want %d", req.Bytes, info.Size())
	}
}

// TestASuccessfulRunSaysWhatItPackedAndHowItEnded. A command that packs
// an archive and then says nothing reads as one that failed quietly, so
// a run that worked says what it made and how it finished.
//
// The closing narration itself has its own rows next door; what belongs
// here is that the run's two halves both speak — the receipt from the
// pack, and the outcome from the last step.
func TestASuccessfulRunSaysWhatItPackedAndHowItEnded(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	shown := run.prompt.out.String()
	for _, want := range []string{"Packed ", "into an archive of ", publishedHeadline} {
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
