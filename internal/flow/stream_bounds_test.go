package flow

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/timing"
	"github.com/curiouspub/cli/pkg/wire"
)

// The rows below prove that every interval of reading the build log, from
// the dial to the end of the stream, has exactly one named bound: the
// transport's three before a response exists, and the stall window after.
//
// THE STALL WINDOW USED TO COVER THE FIRST HALF TOO. It was armed before
// the connection was opened, so it silently bounded connecting and the
// first byte as well as the silence it is named for, and the fixtures
// that sized it never measured that part. On a loaded runner a healthy
// connection was cut that way. Now the window starts when the response
// arrives, and a server that never answers is the transport's to refuse.

// testConnectionBounds are small enough for a row to watch them fire and
// far apart enough that which one fired is never in doubt.
const (
	testConnectBound = 2 * time.Second
	testTLSBound     = 2 * time.Second
	testHeaderBound  = 150 * time.Millisecond
)

// TestAServerThatNeverAnswersIsRefusedByTheHeaderBound: the server takes
// the request and writes nothing, not even headers. The transport's bound
// on answering refuses it, the person watching is told the build log "did
// not answer", and the stall watchdog is never armed, because nothing
// arrived for it to wait on.
//
// THE ROW HAS ITS OWN BOUND, because its failure mode is a hang. With the
// header bound removed, nothing else stands between the request and a
// server that never answers, so the deploy would wait for ever; the row
// waits a generous multiple of what the attempts should cost and reds if
// the deploy has not come back by then.
func TestAServerThatNeverAnswersIsRefusedByTheHeaderBound(t *testing.T) {
	stall := timing.StreamGoesQuiet.Window
	step := 10 * time.Millisecond

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.StreamStallTimeout = stall
	run.deps.StreamReconnectStep = step
	run.deps.ConnectTimeout, run.deps.TLSHandshakeTimeout, run.deps.ResponseHeaderTimeout =
		testConnectBound, testTLSBound, testHeaderBound
	trace := &streamTrace{}
	run.deps.StreamTrace = trace.record
	var scripts []eventScript
	for i := 0; i <= streamReconnectAttempts; i++ {
		scripts = append(scripts, eventScript{neverAnswer: true})
	}
	run.script.eventScripts = scripts

	// What the attempts should cost: one header bound each, plus the
	// schedule between them. The row allows five times that.
	expected := time.Duration(streamReconnectAttempts+1) * testHeaderBound
	for a := 1; a <= streamReconnectAttempts; a++ {
		expected += streamReconnectDelay(a, step)
	}
	own := 5 * expected

	type result struct{ err error }
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		handoff, err := run.run()
		handoff.Release()
		done <- result{err}
	}()
	var res result
	select {
	case res = <-done:
	case <-time.After(own):
		t.Fatalf("the deploy had not come back after %v, five times what its attempts should "+
			"cost: a server that never answers is waited on with nothing bounding the wait.\n"+
			"Both timelines so far:\n%s", own, streamTimeline(run.script.timelines(), trace.snapshot()))
	}
	elapsed := time.Since(started)

	timeline := streamTimeline(run.script.timelines(), trace.snapshot())
	if res.err == nil {
		t.Fatalf("a server that never answered produced a successful deploy after %v:\n%s", elapsed, timeline)
	}
	narrated := run.prompt.out.String()
	if !strings.Contains(narrated, streamNoAnswer) {
		t.Errorf("the run never said the build log did not answer:\n%s\n%s", narrated, timeline)
	}
	if strings.Contains(narrated, streamWentQuiet) {
		t.Errorf("a server that never answered was described as a stream that went quiet — "+
			"the stall window fired where the header bound should have:\n%s\n%s", narrated, timeline)
	}
	for _, ev := range trace.snapshot() {
		if ev.Kind == StreamTraceStalled || ev.Kind == StreamTraceOpened {
			t.Errorf("the reader's trace records %q, and nothing ever arrived for the stall "+
				"watchdog to be armed on:\n%s", ev.Kind, timeline)
			break
		}
	}
}

// TestASlowAnswerInsideTheHeaderBoundNeverTripsTheStall: the server holds
// its response back for several stall windows, then streams a whole build.
// That time belongs to the header bound, which it is well inside, so the
// stall watchdog — armed only once the response arrives — never fires and
// the stream is opened exactly once.
func TestASlowAnswerInsideTheHeaderBoundNeverTripsTheStall(t *testing.T) {
	stall := timing.StreamGoesQuiet.Window
	wait := 4 * stall

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.StreamStallTimeout = stall
	run.deps.ConnectTimeout, run.deps.TLSHandshakeTimeout, run.deps.ResponseHeaderTimeout =
		testConnectBound, testTLSBound, 20*wait
	trace := &streamTrace{}
	run.deps.StreamTrace = trace.record
	run.script.eventScripts = []eventScript{{
		answerAfter: wait,
		frames:      []string{logFrame("LINE-A"), doneFrame(wire.StatusBuilt)},
	}}

	handoff, err := run.run()
	defer handoff.Release()
	timeline := streamTimeline(run.script.timelines(), trace.snapshot())
	if err != nil {
		t.Fatalf("a server that answered %v late, inside the header bound, failed the deploy: "+
			"%v\n%s", wait, err, timeline)
	}
	if n := run.script.eventConnections(); n != 1 {
		t.Errorf("the stream was opened %d times, want 1 — the stall window covered the wait "+
			"for the answer, which is the header bound's:\n%s", n, timeline)
	}
	for _, ev := range trace.snapshot() {
		if ev.Kind == StreamTraceStalled {
			t.Errorf("the stall watchdog fired while the server had not yet answered:\n%s", timeline)
			break
		}
	}
}

// TestEachBoundHasItsOwnSentence: every way the build log can stop is told
// in its own words, because each points somewhere different — a network
// that cannot reach the host, a connection that cannot be secured, a
// server that never answered, a stream that went quiet, and anything else
// that dropped it. The phase comes from the client's own record of how
// far the request got, never from an error's wording.
func TestEachBoundHasItsOwnSentence(t *testing.T) {
	underlying := errors.New("the network said no")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"connect", &api.StreamOpenError{Stage: api.StageConnect, Err: underlying}, streamNoConnect},
		{"tls", &api.StreamOpenError{Stage: api.StageTLS, Err: underlying}, streamNoTLS},
		{"response", &api.StreamOpenError{Stage: api.StageResponse, Err: underlying}, streamNoAnswer},
		{"stalled", errStreamStalled, streamWentQuiet},
		{"wrapped open failure", fmt.Errorf("opening: %w",
			&api.StreamOpenError{Stage: api.StageResponse, Err: underlying}), streamNoAnswer},
		{"anything else", underlying, streamDropped},
	}
	sentences := map[string]string{}
	for _, c := range cases {
		got := reconnectReason(c.err)
		if got != c.want {
			t.Errorf("%s: the ending is told as %q, want %q", c.name, got, c.want)
		}
		if other, seen := sentences[got]; seen && got != streamNoAnswer {
			t.Errorf("%s and %s are told in the same words, %q", other, c.name, got)
		}
		sentences[got] = c.name
	}
}
