package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// streamRecorder is a real server that speaks Server-Sent Events, so the
// rows below go through the shipped client's own request building and
// through a real connection rather than a stubbed round tripper.
type streamRecorder struct {
	mu sync.Mutex

	requests int
	paths    []string
	methods  []string
	bearers  []string
	accepts  []string

	// quiet is how long the handler says nothing before it writes
	// anything at all. It is what makes a total per-request deadline
	// visible from outside: a stream that is merely waiting for a build
	// looks exactly like this.
	quiet time.Duration

	// status, when non-zero, is answered instead of a stream.
	status int
}

func (s *streamRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	s.paths = append(s.paths, r.URL.Path)
	s.methods = append(s.methods, r.Method)
	s.bearers = append(s.bearers, r.Header.Get("Authorization"))
	s.accepts = append(s.accepts, r.Header.Get("Accept"))
	quiet, status := s.quiet, s.status
	s.mu.Unlock()

	if status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(wire.ErrorResponse{
			Error: wire.Error{Code: wire.CodeNotFound, Message: "no such deploy"},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if quiet > 0 {
		select {
		case <-time.After(quiet):
		case <-r.Context().Done():
			return
		}
	}
	_, _ = io.WriteString(w, "event: done\ndata: {\"status\":\"built\"}\n\n")
}

func (s *streamRecorder) seen() (int, []string, []string, []string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests,
		append([]string(nil), s.paths...),
		append([]string(nil), s.methods...),
		append([]string(nil), s.bearers...),
		append([]string(nil), s.accepts...)
}

// TestDeployStart_SendsTheTokenAndNamesTheDeploy. The start is
// authenticated per call, like the create and unlike the four
// unauthenticated ones, and it names the deploy the create returned.
//
// REQUIRED MUTATION, run 2026-09-08: drop the per-call bearer option
// from DeployStart. Reds on the Authorization assertion.
func TestDeployStart_SendsTheTokenAndNamesTheDeploy(t *testing.T) {
	var (
		mu      sync.Mutex
		paths   []string
		methods []string
		bearers []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		methods = append(methods, r.Method)
		bearers = append(bearers, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"status":"building"}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Token != ui.Secret(testToken) {
		t.Fatal("WithToken did not take effect — every assertion below would pass vacuously")
	}

	out, err := c.DeployStart(context.Background(), "quick-koala-4f2a")
	if err != nil {
		t.Fatalf("DeployStart: %v", err)
	}
	if out.Status != wire.StatusBuilding {
		t.Errorf("Status = %q, want %q", out.Status, wire.StatusBuilding)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("the start made %d requests, want exactly 1", len(paths))
	}
	if want := "/v1/deploys/quick-koala-4f2a/start"; paths[0] != want {
		t.Errorf("the start called %q, want %q", paths[0], want)
	}
	if methods[0] != http.MethodPost {
		t.Errorf("the start used %s, want POST", methods[0])
	}
	if want := "Bearer " + testToken; bearers[0] != want {
		t.Errorf("the start sent Authorization %q, want %q", bearers[0], want)
	}
}

// TestDeployEvents_IsAuthenticatedAndAsksForAStream.
//
// The absence half of every leak row next door needs a positive control,
// and this is the one for the stream: the bearer token IS sent here,
// because the event stream is an authenticated call on this project's own
// API rather than a link somebody else signed.
func TestDeployEvents_IsAuthenticatedAndAsksForAStream(t *testing.T) {
	rec := &streamRecorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body, err := c.DeployEvents(context.Background(), "quick-koala-4f2a")
	if err != nil {
		t.Fatalf("DeployEvents: %v", err)
	}
	defer func() { _ = body.Close() }()

	read, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	if !strings.Contains(string(read), "event: done") {
		t.Errorf("the stream body never arrived: %q", read)
	}

	count, paths, methods, bearers, accepts := rec.seen()
	if count != 1 {
		t.Fatalf("the stream made %d requests, want exactly 1", count)
	}
	if want := "/v1/deploys/quick-koala-4f2a/events"; paths[0] != want {
		t.Errorf("the stream called %q, want %q", paths[0], want)
	}
	if methods[0] != http.MethodGet {
		t.Errorf("the stream used %s, want GET", methods[0])
	}
	if want := "Bearer " + testToken; bearers[0] != want {
		t.Errorf("the stream sent Authorization %q, want %q", bearers[0], want)
	}
	if accepts[0] != "text/event-stream" {
		t.Errorf("the stream asked for %q, want text/event-stream", accepts[0])
	}
}

// TestDeployEvents_HasNoTotalDeadline is the row that keeps the JSON
// client's per-request timeout off a connection meant to stay open for
// the whole of a build.
//
// THE TIMEOUT IS MADE TINY AND THE STREAM MERELY QUIET, which is the only
// way to ask this question in milliseconds rather than in half a minute.
// Every other call this client makes bounds itself end to end with that
// value — correct for a small request and a small answer, fatal here,
// because a build that says nothing for a while is a build that is
// working and the server's own keep-alive cannot extend a TOTAL
// deadline.
//
// The ratio is the assertion: the stream is quiet for twenty times the
// deadline the client was constructed with, so a client that applied it
// cannot pass by being lucky.
//
// REQUIRED MUTATION, run 2026-09-08: wrap the request context in
// context.WithTimeout(ctx, c.timeout) inside DeployEvents, as every other
// call does. This reds — and it reds HARDER than predicted, which is
// worth keeping. The prediction was "the read dies at the deadline". What
// actually happens is "reading a stream that was quiet for 400ms: context
// canceled", instantly: the deferred cancel that every other call pairs
// with its timeout fires when DeployEvents RETURNS, and here the reader
// outlives the function that opened it. The shared shape is not merely
// mis-sized for a stream, it cannot be applied to one at all.
func TestDeployEvents_HasNoTotalDeadline(t *testing.T) {
	const deadline = 20 * time.Millisecond
	const quiet = 20 * deadline

	rec := &streamRecorder{quiet: quiet}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := New(srv.URL, WithToken(ui.Secret(testToken)), WithTimeout(deadline))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	started := time.Now()
	body, err := c.DeployEvents(context.Background(), "d-1")
	if err != nil {
		t.Fatalf("DeployEvents on a quiet stream: %v", err)
	}
	defer func() { _ = body.Close() }()

	read, err := io.ReadAll(body)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("reading a stream that was quiet for %v: %v", quiet, err)
	}
	if !strings.Contains(string(read), "event: done") {
		t.Errorf("the stream was cut before it finished: %q", read)
	}
	if elapsed < quiet {
		t.Fatalf("the whole call took %v, which is under the %v the handler waited "+
			"— the row did not spend long enough to prove anything", elapsed, quiet)
	}
}

// TestDeployEvents_ARefusedStreamIsAnAPIError. A refusal is answered in
// this project's own error envelope, so it comes back decoded rather than
// as a body the caller would have to parse — and the caller gets no
// reader to close, which is the shape that makes "defer close" safe at
// every call site.
func TestDeployEvents_ARefusedStreamIsAnAPIError(t *testing.T) {
	rec := &streamRecorder{status: http.StatusNotFound}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body, err := c.DeployEvents(context.Background(), "d-1")
	if err == nil {
		_ = body.Close()
		t.Fatal("a refused stream came back as a readable one")
	}
	if body != nil {
		t.Error("a refused stream handed back a reader, which nobody owns")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("the refusal is not an *APIError: %v", err)
	}
	if apiErr.Code != wire.CodeNotFound {
		t.Errorf("Code = %q, want %q", apiErr.Code, wire.CodeNotFound)
	}
	if apiErr.Message != "no such deploy" {
		t.Errorf("Message = %q, want the server's own", apiErr.Message)
	}
}

// TestDeployEvents_NeverPrintsTheToken. The stream's request is built by
// hand rather than through the shared path, which is exactly the sort of
// second implementation where a token gets formatted into an error while
// nobody is looking.
func TestDeployEvents_NeverPrintsTheToken(t *testing.T) {
	// An address nothing answers on, so the failure is a transport error
	// built by net/http out of the request this client made.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := dead.URL
	dead.Close()

	c, err := New(addr, WithToken(ui.Secret(testToken)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.DeployEvents(context.Background(), "d-1")
	if err == nil {
		t.Fatal("a stream to an address nothing answers on was opened")
	}
	for name, text := range map[string]string{
		"%v":  err.Error(),
		"%+v": strings.TrimSpace(err.Error()),
	} {
		if strings.Contains(text, testToken) {
			t.Errorf("the stream's failure carries the bearer token through %s:\n%s",
				name, text)
		}
	}
	// The positive half: without it, an error saying nothing at all would
	// satisfy the absence above.
	if !strings.Contains(err.Error(), "could not reach") &&
		!strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the failure names nothing a reader could act on:\n%v", err)
	}
}
