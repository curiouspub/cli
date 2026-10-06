package flow

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// harnessSettleBound bounds how long a row waits for its own event
// handlers to return before it reads their timelines. It is not a stall
// window: it bounds the fixture, not the client, and its expiry is its
// own failure message rather than a timeline read wrong.
const harnessSettleBound = 10 * time.Second

// TestTheHarnessNumbersConnectionsByArrival: the events server's timelines
// are in the order the connections ARRIVED, whatever order their handlers
// finish in. The finishing order is forced here, deterministically: the
// first connection is held open until the second has returned, so a
// harness that files a timeline when its handler returns puts the second
// connection first.
//
// It is the instrument behind every stall row's "the first connection's
// timeline", and on 2026-10-01 a loaded runner made the reconnect finish
// before the first handler noticed its client had gone. The row read the
// second connection as the first and reddened with the client behaving.
//
// REQUIRED MUTATION, run: file each timeline when its handler returns
// (append in a defer at the end of serveEvents, instead of into a slot
// reserved on arrival). This row reds naming the swapped endings.
func TestTheHarnessNumbersConnectionsByArrival(t *testing.T) {
	script := &deployScript{
		journal: &deployJournal{},
		release: make(chan struct{}),
		eventScripts: []eventScript{
			{frames: []string{logFrame("FIRST")}, hold: true},
			{frames: []string{logFrame("SECOND-CONNECTION")}},
		},
	}
	srv := httptest.NewServer(script)
	defer srv.Close()
	url := srv.URL + deployPathPrefix + "d-harness/" + eventsAction

	// The first connection: read its frame, then keep it open.
	first, err := http.Get(url)
	if err != nil {
		t.Fatalf("opening the first connection: %v", err)
	}
	defer first.Body.Close()
	if _, err := bufio.NewReader(first.Body).ReadString('\n'); err != nil {
		t.Fatalf("reading the first connection's frame: %v", err)
	}

	// The second connection: read it to the end, so its handler returns
	// while the first is still held.
	second, err := http.Get(url)
	if err != nil {
		t.Fatalf("opening the second connection: %v", err)
	}
	if _, err := io.Copy(io.Discard, second.Body); err != nil {
		t.Fatalf("reading the second connection: %v", err)
	}
	second.Body.Close()
	// Wait for the second handler to finish, found by its ending rather
	// than by its index, so a harness that numbers connections wrongly
	// still reaches the assertion that names the swap.
	waitUntil(t, func() bool {
		for _, tl := range script.timelines() {
			if len(tl) > 0 && strings.HasPrefix(tl[len(tl)-1].what, "wrote") {
				return true
			}
		}
		return false
	})

	// Only now does the first connection's handler return.
	close(script.release)
	got := script.settledTimelines(t, harnessSettleBound)

	if len(got) != 2 {
		t.Fatalf("the harness recorded %d timelines, want 2:\n%s", len(got), streamTimeline(got, nil))
	}
	firstEnd, secondEnd := got[0][len(got[0])-1].what, got[1][len(got[1])-1].what
	if firstEnd != "released" || !strings.HasPrefix(secondEnd, "wrote") {
		t.Errorf("timeline 0 ends %q and timeline 1 ends %q, want the held FIRST connection "+
			"(\"released\") at index 0 and the second (\"wrote …\") at index 1 — the harness is "+
			"numbering connections by when they finished, not when they arrived:\n%s",
			firstEnd, secondEnd, streamTimeline(got, nil))
	}
}

// The stall rows' machine-or-client guards read the next four rows'
// accessors, and the connection a stall drops is the one that stalled, so
// a fixture that forgot a dropped connection's gaps would quote only the
// survivors.

// harnessGapScript serves a script with the construction every row here
// shares and returns it with the events URL of one deploy.
func harnessGapScript(t *testing.T, scripts ...eventScript) (*deployScript, string) {
	t.Helper()
	script := &deployScript{
		journal:      &deployJournal{},
		release:      make(chan struct{}),
		eventScripts: scripts,
	}
	srv := httptest.NewServer(script)
	t.Cleanup(srv.Close)
	return script, srv.URL + deployPathPrefix + "d-harness/" + eventsAction
}

// REQUIRED MUTATION, run 2026-10-06: five edits to the fixture's frame
// loop, each run against these four rows alone with the race detector on,
// and each restored byte for byte afterwards.
//   - Fold only after the loop (no deferred fold): the dropped-mid-stream
//     row reds on "the fixture kept 1 per-connection entries for 2
//     connections, want 2", and the first-wait row on "the fixture kept 0
//     per-connection entries for 1 connection, want 1". The other two stay
//     green.
//   - Register a second fold at arrival, ahead of the pre-loop exits: the
//     never-reached row reds on "the fixture kept 1 per-connection entries
//     for a connection that never answered, want 0", and the two dropped
//     rows red on a doubled count.
//   - Record the open interval at a hang-up: the first-wait row reds on
//     "the entry is 158.125µs, want exactly 0" and on the pooled widest
//     gap, and no other row moves.
//   - Defer the fold with its argument evaluated at registration: the
//     dropped-mid-stream row reds on "the dropped connection's entry is
//     0s, want at least 20ms", and the held row on the same figure while
//     the connection is held.
//   - Fold after the hold (a defer in the handler itself): the held row
//     reds at its wait bound, "a harness condition did not hold within
//     10s", after ten seconds.
// Each of the four rows reds under at least one of them.

// TestAConnectionDroppedMidStreamKeepsItsGaps: a connection whose client
// hangs up after the first frame still leaves the gaps it completed, in
// both the pooled list and its own per-connection entry. The relation a
// lost connection breaks is "the pooled maximum is the maximum of the
// per-connection maxima", and the entry count must equal the connection
// count; a fixture that records only connections that finish normally
// reports one entry for two connections here.
func TestAConnectionDroppedMidStreamKeepsItsGaps(t *testing.T) {
	const pace = 20 * time.Millisecond
	frames := make([]string, 50)
	for i := range frames {
		frames[i] = logFrame("DROPPED")
	}
	script, url := harnessGapScript(t,
		eventScript{frames: frames, pace: pace},
		eventScript{frames: []string{logFrame("A"), logFrame("B"), logFrame("C")}, pace: pace},
	)

	dropped, err := http.Get(url)
	if err != nil {
		t.Fatalf("opening the first connection: %v", err)
	}
	if _, err := bufio.NewReader(dropped.Body).ReadString('\n'); err != nil {
		t.Fatalf("reading the first connection's frame: %v", err)
	}
	dropped.Body.Close()

	control, err := http.Get(url)
	if err != nil {
		t.Fatalf("opening the second connection: %v", err)
	}
	if _, err := io.Copy(io.Discard, control.Body); err != nil {
		t.Fatalf("reading the second connection: %v", err)
	}
	control.Body.Close()

	got := script.settledTimelines(t, harnessSettleBound)
	per := script.widestPerConnection()
	if n := script.eventConnections(); n != 2 {
		t.Fatalf("the fixture counted %d connections, want 2:\n%s", n, streamTimeline(got, nil))
	}
	if len(per) != 2 {
		t.Fatalf("the fixture kept %d per-connection entries for 2 connections, want 2 — "+
			"exactly one per connection that reached its frames: %v\n%s",
			len(per), per, streamTimeline(got, nil))
	}
	if per[0] < pace {
		t.Errorf("the dropped connection's entry is %v, want at least %v", per[0], pace)
	}
	if per[1] < pace {
		t.Errorf("the control connection's entry is %v, want at least %v", per[1], pace)
	}
	widest := script.widestGap()
	if widest < pace {
		t.Errorf("the pooled widest gap is %v, want at least %v", widest, pace)
	}
	var maxPer time.Duration
	for _, d := range per {
		if d > maxPer {
			maxPer = d
		}
	}
	if widest != maxPer {
		t.Errorf("the pooled widest gap is %v but the widest per-connection entry is %v (%v), "+
			"want them equal", widest, maxPer, per)
	}

	if len(got) != 2 {
		t.Fatalf("the harness recorded %d timelines, want 2:\n%s", len(got), streamTimeline(got, nil))
	}
	if end := got[0][len(got[0])-1].what; end != "client hung up" && end != "write failed" {
		t.Errorf("the dropped connection's timeline ends %q, want \"client hung up\" or "+
			"\"write failed\":\n%s", end, streamTimeline(got, nil))
	}
	if end := got[1][len(got[1])-1].what; !strings.HasPrefix(end, "wrote") {
		t.Errorf("the control connection's timeline ends %q, want a \"wrote …\" mark:\n%s",
			end, streamTimeline(got, nil))
	}
}

// TestAConnectionDroppedDuringItsFirstWaitIsCountedZeroWide: a client that
// leaves before the first frame was due completed no gap at all, and the
// fixture says so with an entry of exactly zero rather than with no entry,
// because a missing entry makes the per-connection list shorter than the
// connection count and every reader pairing them by index misaligned.
func TestAConnectionDroppedDuringItsFirstWaitIsCountedZeroWide(t *testing.T) {
	script, url := harnessGapScript(t, eventScript{frames: []string{logFrame("LATE")}, pace: time.Second})

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("opening the connection: %v", err)
	}
	resp.Body.Close()

	got := script.settledTimelines(t, harnessSettleBound)
	per := script.widestPerConnection()
	if n := script.eventConnections(); n != 1 {
		t.Fatalf("the fixture counted %d connections, want 1:\n%s", n, streamTimeline(got, nil))
	}
	if len(per) != 1 {
		t.Fatalf("the fixture kept %d per-connection entries for 1 connection, want 1: %v\n%s",
			len(per), per, streamTimeline(got, nil))
	}
	if per[0] != 0 {
		t.Errorf("the entry is %v, want exactly 0", per[0])
	}
	if w := script.widestGap(); w != 0 {
		t.Errorf("the pooled widest gap is %v, want 0", w)
	}
}

// TestAConnectionThatNeverReachedItsFramesRecordsNothing: a connection
// that never answered has no frame loop to leave, so it is counted as a
// connection but contributes no per-connection entry — the other side of
// the zero-wide row above, and the reason that row's entry is not simply
// "every connection gets one".
func TestAConnectionThatNeverReachedItsFramesRecordsNothing(t *testing.T) {
	script, url := harnessGapScript(t, eventScript{neverAnswer: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	waitUntil(t, func() bool {
		tl := script.timelines()
		return len(tl) == 1 && len(tl[0]) > 0 && tl[0][0].what == "arrived"
	})
	cancel()
	select {
	case <-finished:
	case <-time.After(harnessSettleBound):
		t.Fatalf("the client did not return within %v of being cancelled", harnessSettleBound)
	}

	got := script.settledTimelines(t, harnessSettleBound)
	if n := script.eventConnections(); n != 1 {
		t.Errorf("the fixture counted %d connections, want 1:\n%s", n, streamTimeline(got, nil))
	}
	if per := script.widestPerConnection(); len(per) != 0 {
		t.Errorf("the fixture kept %d per-connection entries for a connection that never "+
			"answered, want 0: %v", len(per), per)
	}
}

// TestAHeldConnectionsGapsAreReadableWhileItIsStillHeld: the gaps are
// recorded before the hold wait, not after it, so a row that reads the
// accessors while the stream is deliberately kept open sees them. A
// fixture that folded its gaps in only when the hold ended would answer
// zero entries for exactly the stalled connection a stall row is about.
func TestAHeldConnectionsGapsAreReadableWhileItIsStillHeld(t *testing.T) {
	const pace = 20 * time.Millisecond
	script, url := harnessGapScript(t, eventScript{frames: []string{logFrame("HELD")}, pace: pace, hold: true})

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("opening the connection: %v", err)
	}
	open := true
	defer func() {
		if open {
			resp.Body.Close()
		}
	}()
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatalf("reading the frame: %v", err)
	}

	waitUntil(t, func() bool { return len(script.widestPerConnection()) == 1 })
	if per := script.widestPerConnection(); per[0] < pace {
		t.Errorf("the held connection's entry is %v, want at least %v", per[0], pace)
	}
	if w := script.widestGap(); w < pace {
		t.Errorf("the pooled widest gap is %v while the connection is held, want at least %v", w, pace)
	}

	resp.Body.Close()
	open = false
	script.settledTimelines(t, harnessSettleBound)
}

// waitUntil polls cond until it holds, bounded by harnessSettleBound.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(harnessSettleBound)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("a harness condition did not hold within %v", harnessSettleBound)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
