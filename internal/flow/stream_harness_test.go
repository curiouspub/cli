package flow

import (
	"bufio"
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
