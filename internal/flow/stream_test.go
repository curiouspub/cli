package flow

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/timing"
	"github.com/curiouspub/cli/pkg/wire"
)

// -------------------------------------------------------------------
// The start
// -------------------------------------------------------------------

// TestTheStartIsPostedOnceAndTheStreamFollows.
//
// A BARE COUNT OF ONE IS SATISFIED BY ANY REQUEST AT ALL, so this asserts
// the method, the path carrying the id THE CREATE RETURNED, the bearer
// header, that the answer was decoded, and that the events GET came
// after it. The id is deliberately not the harness default: a client that
// built the path out of anything but the create's answer would still
// produce a path of the right shape.
func TestTheStartIsPostedOnceAndTheStreamFollows(t *testing.T) {
	const id = "quick-koala-4f2a"

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.deployID = id
	run.script.startBody = `{"status":"` + string(wire.StatusQueued) + `"}`

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	startPath := deployPathPrefix + id + "/" + startAction
	eventsPath := deployPathPrefix + id + "/" + eventsAction
	if n := run.script.sentTo(startPath); n != 1 {
		t.Fatalf("the start was sent %d times to %s, want exactly 1", n, startPath)
	}
	if n := run.script.sentTo(eventsPath); n != 1 {
		t.Errorf("the stream was opened %d times at %s, want exactly 1", n, eventsPath)
	}

	events := run.journal.all()
	started := indexOfEvent(events, "POST "+startPath)
	streamed := indexOfEvent(events, "GET "+eventsPath)
	if started < 0 || streamed < 0 || started > streamed {
		t.Errorf("the start is at %d and the stream at %d:\n  %s",
			started, streamed, strings.Join(events, "\n  "))
	}

	run.script.mu.Lock()
	bearers := append([]string(nil), run.script.bearers...)
	paths := append([]string(nil), run.script.paths...)
	run.script.mu.Unlock()
	for i, p := range paths {
		if p != startPath && p != eventsPath {
			continue
		}
		if !strings.HasPrefix(bearers[i], "Bearer ") || bearers[i] == "Bearer " {
			t.Errorf("%s sent Authorization %q, want a bearer token", p, bearers[i])
		}
	}

	// The answer was DECODED, not merely received: the status the start
	// returned is the one rendered, and it is not the harness default.
	if want := startNarration + string(wire.StatusQueued) + "."; !strings.Contains(run.prompt.out.String(), want) {
		t.Errorf("the run never rendered the start's own status (%q):\n%s",
			want, run.prompt.out.String())
	}
}

// TestAStartThatNeverAnswersIsSentOnce is the row the no-retry rule rests
// on, and the ambiguous failure is the shape that tempts one: the request
// left, no answer came back, and asking again is the obvious move.
//
// It is the wrong move, and the reason is where the information lives.
// After the start returns, everything the user is waiting for arrives on
// the stream — so a client that reacts to silence by starting again is
// asking the one surface with nothing left to tell it.
//
// THE FAILURE IS A DROPPED CONNECTION AND NOT A CANCELLED RUN, and that
// is the whole instrument. The first version of this row cancelled the
// context to make the answer never arrive, which also stops the next
// request from leaving the machine at all — so the mutation below RAN AND
// STAYED GREEN: the retry happened, sent nothing, and was counted
// nowhere. A row that cannot fail for the right reason has never been
// audited, whatever colour it has been showing.
//
// REQUIRED MUTATION, run 2026-09-08: retry the start when it comes back
// with no answer. Reds on the count, 2 against 1.
func TestAStartThatNeverAnswersIsSentOnce(t *testing.T) {
	const id = "deploy-quiet"

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.deployID = id
	run.script.startHangsUp = true

	handoff, err := run.run()
	defer handoff.Release()
	if err == nil {
		t.Fatal("a start that never answered was reported as a running build")
	}

	if n := run.script.sentTo(deployPathPrefix + id + "/" + startAction); n != 1 {
		t.Errorf("the start was sent %d times, want exactly 1", n)
	}
	if n := run.script.eventConnections(); n != 0 {
		t.Errorf("the stream was opened %d times after a start that never answered, "+
			"want none", n)
	}

	text, code := renderedBytes(t, err)
	if code != 1 {
		t.Errorf("a start that never answered cost %d, want 1", code)
	}
	// The positive half: a message that says nothing would satisfy every
	// count above.
	if !strings.Contains(text, buildMayBeRunning) {
		t.Errorf("the failure never said the build may be running anyway:\n%s", text)
	}
}

// TestTheStartStatusIsRenderedAndNeverBranchedOn drives EVERY value the
// contract defines through the start, plus one the contract does not.
//
// Status INFORMS; it never BRANCHES. The client takes the identical next
// action for every value the field can carry — go read the stream — so a
// switch on it is the defect rather than the omission, and the row that
// can see one is the unknown value: it comes back from the START itself,
// because a value injected only into a done event or a phase never
// reaches this code at all.
//
// EACH TOKEN IS ASSERTED DISTINCTLY, as the whole rendered line, and
// every OTHER value's line is asserted absent. A renderer that printed
// one fixed word would pass a row that only checked the run did not
// error.
//
// REQUIRED MUTATION, run 2026-09-08: switch on the start's Status and
// error on an unrecognised value. The "future" subtest reds — "a start
// answering \"future\" stopped the run: The server said something curious
// does not understand." — and the other five stay green, which is what
// makes it a test of the additive rule rather than of the copy.
func TestTheStartStatusIsRenderedAndNeverBranchedOn(t *testing.T) {
	if len(wire.AllDeployStatuses) == 0 {
		t.Fatal("the contract enumerates no statuses, so this row would drive nothing")
	}

	// The contract's own enumeration, plus a value from outside it. The
	// unknown one is what makes this a test of the additive rule rather
	// than of a list.
	statuses := append(append([]wire.DeployStatus(nil), wire.AllDeployStatuses...),
		wire.DeployStatus("future"))

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			const id = "deploy-start-status"
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			run.script.deployID = id
			run.script.startBody = `{"status":"` + string(status) + `"}`

			handoff, err := run.run()
			defer handoff.Release()
			if err != nil {
				t.Fatalf("a start answering %q stopped the run: %v\n%s",
					status, err, rendered(err))
			}

			shown := run.prompt.out.String()
			if want := startNarration + string(status) + "."; !strings.Contains(shown, want) {
				t.Errorf("the run never rendered %q:\n%s", want, shown)
			}
			for _, other := range statuses {
				if other == status {
					continue
				}
				if unwanted := startNarration + string(other) + "."; strings.Contains(shown, unwanted) {
					t.Errorf("the run also rendered %q, so the line does not carry "+
						"the value it was given:\n%s", unwanted, shown)
				}
			}

			// AND THE STREAM STILL FOLLOWED. Without this the row is
			// passed by a client that renders the status and then stops,
			// which is exactly what a switch with an error default does.
			if n := run.script.sentTo(deployPathPrefix + id + "/" + eventsAction); n != 1 {
				t.Errorf("the events stream was opened %d times after a start "+
					"answering %q, want exactly 1", n, status)
			}
			if n := run.script.sentTo(deployPathPrefix + id + "/" + startAction); n != 1 {
				t.Errorf("the start was sent %d times, want exactly 1", n)
			}
		})
	}
}

// -------------------------------------------------------------------
// The vocabularies the stream carries
// -------------------------------------------------------------------

// TestEveryPhaseIsRenderedIncludingOneTheContractDoesNotDefine. A phase
// is a progress label, not control flow: an unknown one is RENDERED, not
// switched on, because a display that errors on a value the server added
// last week fails in the way that looks like nothing at all.
func TestEveryPhaseIsRenderedIncludingOneTheContractDoesNotDefine(t *testing.T) {
	if len(wire.AllPhases) == 0 {
		t.Fatal("the contract enumerates no phases, so this row would drive nothing")
	}
	phases := append(append([]wire.Phase(nil), wire.AllPhases...), wire.Phase("future"))

	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			run.script.eventScripts = []eventScript{{frames: []string{
				phaseFrame(phase),
				doneFrame(wire.StatusBuilt),
			}}}

			handoff, err := run.run()
			defer handoff.Release()
			if err != nil {
				t.Fatalf("a phase of %q stopped the run: %v\n%s", phase, err, rendered(err))
			}

			shown := run.prompt.out.String()
			if want := phaseNarration + string(phase) + "."; !strings.Contains(shown, want) {
				t.Errorf("the run never rendered %q:\n%s", want, shown)
			}
			for _, other := range phases {
				if other == phase {
					continue
				}
				if unwanted := phaseNarration + string(other) + "."; strings.Contains(shown, unwanted) {
					t.Errorf("the run also rendered %q:\n%s", unwanted, shown)
				}
			}
		})
	}
}

// TestEveryTerminalStatusIsRenderedAndOnlyFailureCosts. done carries the
// BUILDER's claim rather than the validated truth — output validation runs
// afterwards and can still move a deploy to failed — so this client
// renders the status honestly and hands the question forward instead of
// treating the stream as the authority.
//
// What it does decide is the cost: a build the server says failed is a
// project problem and the log just watched is the explanation, so that
// one exits 1. Every other value continues, and BOTH halves are in one
// row on purpose — a client that always exits 1 passes the failure case
// alone.
func TestEveryTerminalStatusIsRenderedAndOnlyFailureCosts(t *testing.T) {
	statuses := append(append([]wire.DeployStatus(nil), wire.AllDeployStatuses...),
		wire.DeployStatus("future"))

	continued := 0
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			run.script.eventScripts = []eventScript{{frames: []string{
				logFrame("the build said something"),
				doneFrame(status),
			}}}

			handoff, err := run.run()
			defer handoff.Release()

			shown := run.prompt.out.String()
			if want := finishedNarration + string(status) + "."; !strings.Contains(shown, want) {
				t.Errorf("the run never rendered %q:\n%s", want, shown)
			}
			for _, other := range statuses {
				if other == status {
					continue
				}
				if unwanted := finishedNarration + string(other) + "."; strings.Contains(shown, unwanted) {
					t.Errorf("the run also rendered %q:\n%s", unwanted, shown)
				}
			}

			text, code := renderedBytes(t, err)
			if status == wire.StatusFailed {
				if err == nil {
					t.Fatal("a build the server said failed was reported as a success")
				}
				if code != 1 {
					t.Errorf("a failed build cost %d, want 1:\n%s", code, text)
				}
				return
			}
			if err != nil {
				t.Fatalf("a build that ended %q stopped the run: %v\n%s",
					status, err, rendered(err))
			}
			if code != 0 {
				t.Errorf("a build that ended %q cost %d, want 0", status, code)
			}
			continued++
		})
	}

	if continued == 0 {
		t.Error("every value ended the run, so the exit code above is a constant " +
			"rather than a decision")
	}
}

// TestAnUnknownEventTypeIsSkippedAndTheStreamKeepsReading.
//
// THE UNKNOWN IS PLACED BETWEEN KNOWN EVENTS and both sides are asserted
// rendered. Put just before done, the row is satisfied by a client that
// ignores everything except done.
//
// The known events are RANGED OVER the contract's own enumeration rather
// than listed here, so a new member joins the row automatically — and the
// helper refuses a member it does not know how to drive, which turns
// "somebody added an event type" into a failure rather than into silent
// under-coverage.
func TestAnUnknownEventTypeIsSkippedAndTheStreamKeepsReading(t *testing.T) {
	if len(wire.AllEventTypes) == 0 {
		t.Fatal("the contract enumerates no event types, so this row would drive nothing")
	}

	const unknownMarker = "PAYLOAD-OF-AN-EVENT-THIS-BUILD-PREDATES"
	unknown := rawFrame("cache_restored", `{"note":"`+unknownMarker+`"}`)

	var frames []string
	markers := map[wire.EventType]string{}
	for _, kind := range wire.AllEventTypes {
		if kind == wire.EventDone {
			// done ends the stream, so it can only be last. Everything
			// else is surrounded.
			continue
		}
		frame, marker := frameForEventType(t, kind)
		frames = append(frames, unknown, frame)
		markers[kind] = marker
	}
	frames = append(frames, unknown, doneFrame(wire.StatusBuilt))

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{{frames: frames}}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("an unknown event type stopped the run: %v\n%s", err, rendered(err))
	}

	shown := run.prompt.out.String() + run.prompt.results.String()
	for kind, marker := range markers {
		if !strings.Contains(shown, marker) {
			t.Errorf("the %s event either side of an unknown one was not rendered "+
				"(%q):\n%s", kind, marker, shown)
		}
	}
	if strings.Contains(shown, unknownMarker) {
		t.Errorf("the unknown event's payload was rendered:\n%s", shown)
	}
	if n := run.script.eventConnections(); n != 1 {
		t.Errorf("the stream was opened %d times, want 1 — an unknown event is "+
			"skipped, not a reason to start again", n)
	}
}

// frameForEventType drives one member of the contract's event vocabulary
// and returns the text its rendering must contain.
//
// The default is a FAILURE rather than a skip: a member nobody here knows
// how to drive is a row quietly covering less than its name says.
func frameForEventType(t *testing.T, kind wire.EventType) (frame, marker string) {
	t.Helper()
	switch kind {
	case wire.EventLog:
		return logFrame("MARKER-FOR-A-LOG-LINE"), "MARKER-FOR-A-LOG-LINE"
	case wire.EventPhase:
		return phaseFrame(wire.PhaseInstalling),
			phaseNarration + string(wire.PhaseInstalling) + "."
	case wire.EventError:
		return errorFrame(wire.CodeInternal, "MARKER-FOR-AN-ERROR-EVENT"),
			"MARKER-FOR-AN-ERROR-EVENT"
	case wire.EventDone:
		return doneFrame(wire.StatusBuilt), finishedNarration + string(wire.StatusBuilt) + "."
	}
	t.Fatalf("the contract defines the event type %q and this row does not know how "+
		"to drive it", kind)
	return "", ""
}

// TestAnErrorEventExplainsAndDoesNotEndTheStream. error is DIAGNOSTIC; a
// failed build still ends with done, so the termination condition is
// "done arrived" and never "error arrived".
//
// THE FIXTURE HOLDS THE CONNECTION OPEN AFTER done, which is what makes
// the first half real: a client that returned at the error would hang
// here rather than pass, where closing the connection would have handed
// it an end-of-stream it could mistake for the ending it failed to read.
func TestAnErrorEventExplainsAndDoesNotEndTheStream(t *testing.T) {
	t.Run("the done after it is what ends the stream", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}
		run.script.eventScripts = []eventScript{{
			frames: []string{
				logFrame("BEFORE-THE-ERROR"),
				errorFrame(wire.CodeInternal, "THE-ERROR-ITSELF"),
				logFrame("AFTER-THE-ERROR"),
				doneFrame(wire.StatusFailed),
			},
			hold: true,
		}}

		handoff, err := run.run()
		defer handoff.Release()
		if err == nil {
			t.Fatal("a build that ended failed was reported as a success")
		}

		printed := run.prompt.results.String()
		for _, want := range []string{"BEFORE-THE-ERROR", "THE-ERROR-ITSELF", "AFTER-THE-ERROR"} {
			if !strings.Contains(printed, want) {
				t.Errorf("the build output never carried %q:\n%s", want, printed)
			}
		}
		if want := finishedNarration + string(wire.StatusFailed) + "."; !strings.Contains(run.prompt.out.String(), want) {
			t.Errorf("the run never rendered %q:\n%s", want, run.prompt.out.String())
		}
		if n := run.script.eventConnections(); n != 1 {
			t.Errorf("the stream was opened %d times, want 1 — an error event is "+
				"not a reason to start again", n)
		}
	})

	t.Run("a stream that ends after an error is reconnected, boundedly", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}
		// Every connection ends the same way, because the queue's last
		// entry repeats. An ending without done is abnormal whatever
		// came before it.
		run.script.eventScripts = []eventScript{{frames: []string{
			logFrame("BEFORE-THE-ERROR"),
			errorFrame(wire.CodeInternal, "THE-ERROR-ITSELF"),
		}}}

		handoff, err := run.run()
		defer handoff.Release()
		if err == nil {
			t.Fatal("a stream that never said done was reported as a finished build")
		}

		narrated := run.prompt.out.String()
		if !strings.Contains(narrated, streamDropped) {
			t.Errorf("the run never said the build log was lost:\n%s", narrated)
		}
		if strings.Contains(narrated, streamWentQuiet) {
			t.Errorf("a connection that closed was described as having gone "+
				"quiet:\n%s", narrated)
		}

		want := 1 + streamReconnectAttempts
		if n := run.script.eventConnections(); n != want {
			t.Errorf("the stream was opened %d times, want %d — one attempt and %d "+
				"reconnections, bounded rather than forever",
				n, want, streamReconnectAttempts)
		}

		text, code := renderedBytes(t, err)
		if code != 1 {
			t.Errorf("a stream that could not be re-established cost %d, want 1", code)
		}
		if !strings.Contains(text, "deploy-1") {
			t.Errorf("the failure does not name the deploy, so nobody can ask about "+
				"it:\n%s", text)
		}
		if want := strconv.Itoa(streamReconnectAttempts); !strings.Contains(text, want) {
			t.Errorf("the failure does not say how many times it tried (%s):\n%s",
				want, text)
		}
	})
}

// -------------------------------------------------------------------
// Reconnection, and what a reconnection must not do twice
// -------------------------------------------------------------------

// TestAReplayedStreamRendersEachLineExactlyOnce is the row that pays for
// the whole de-duplication rule.
//
// There is no cursor — the stream implements none — so a reconnection
// re-sends everything from the beginning. Getting this wrong duplicates
// the entire build log on every blip, which is the failure a user reports
// as "it printed everything twice" and nobody reproduces on a good
// connection.
//
// THE SECOND RESPONSE CARRIES THE COMPLETE ORIGINAL HISTORY plus what
// arrived after the cut. A fixture that returned only the tail would let
// a client with no skip logic pass.
//
// It asserts on the RENDERED OUTPUT rather than on events received: the
// claim is about what a person sees, and a client could count correctly
// and print wrongly.
//
// REQUIRED MUTATION, run 2026-09-08: drop the already-rendered count on
// reconnection. Reds with the whole history printed twice — "LINE-A was
// printed 2 times, want exactly 1", and the same for B and C — and reds
// the other replay shape below with it, which is the correct blast radius
// for a change to the one thing both rows are about.
func TestAReplayedStreamRendersEachLineExactlyOnce(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{
		// Cut: three lines and no done.
		{frames: []string{logFrame("LINE-A"), logFrame("LINE-B"), logFrame("LINE-C")}},
		// The replay: the whole history again, then what came after.
		{frames: []string{
			logFrame("LINE-A"), logFrame("LINE-B"), logFrame("LINE-C"),
			logFrame("LINE-D"), doneFrame(wire.StatusBuilt),
		}},
	}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}

	printed := run.prompt.results.String()
	for _, line := range []string{"LINE-A", "LINE-B", "LINE-C", "LINE-D"} {
		if n := strings.Count(printed, line); n != 1 {
			t.Errorf("%s was printed %d times, want exactly 1:\n%s", line, n, printed)
		}
	}
	if n := run.script.eventConnections(); n != 2 {
		t.Errorf("the stream was opened %d times, want 2 — a cut stream is "+
			"reconnected, and a reconnected one is not reconnected again", n)
	}
}

// TestTheEvictedReplayShapeAlsoRendersEachLineExactlyOnce is the same
// property in the OTHER replay shape, and it is the row that decides what
// the counter counts.
//
// There are two shapes and they do not carry the same events. While the
// build's history is still in memory a replay is TYPED — log, phase and
// error as they were. Once it has been read back from storage instead,
// every line arrives as a log, because what is stored is the text of logs
// and errors and nothing else.
//
// A counter over LOG EVENTS ALONE is wrong here: it skips one fewer than
// it should and prints a line twice. A counter over EVERY EVENT is wrong
// too: phase is not persisted, so it counts something the replay does not
// contain and skips past real output. Counting exactly what the server
// persists is right in both, which is why the counter is defined that
// way — and this row fails both of the other two.
//
// THAT DEFINITION IS A COUPLING TO THE SERVER and naming it is the point:
// if what the far end persists ever widens, this de-duplication breaks
// silently. So the assertion is the OBSERVABLE property — each line
// rendered exactly once — rather than the mechanism, because a mechanism
// row would stay green against a server that had changed underneath it.
func TestTheEvictedReplayShapeAlsoRendersEachLineExactlyOnce(t *testing.T) {
	const errText = "ERROR-LINE-E"

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{
		// Typed, and cut: a phase among the lines, and an error among
		// them.
		{frames: []string{
			logFrame("LINE-A"),
			phaseFrame(wire.PhaseBuilding),
			errorFrame(wire.CodeInternal, errText),
			logFrame("LINE-B"),
		}},
		// Read back from storage: every line a log, the phase gone
		// because it was never written down, then what came after.
		{frames: []string{
			logFrame("LINE-A"),
			logFrame(errText),
			logFrame("LINE-B"),
			logFrame("LINE-C"),
			doneFrame(wire.StatusBuilt),
		}},
	}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}

	printed := run.prompt.results.String()
	for _, line := range []string{"LINE-A", errText, "LINE-B", "LINE-C"} {
		n := strings.Count(printed, line)
		switch {
		case n == 0:
			t.Errorf("%s never reached the terminal — a counter over EVERY event "+
				"skips past it:\n%s", line, printed)
		case n > 1:
			t.Errorf("%s reached the terminal %d times — a counter over LOG events "+
				"alone prints it again:\n%s", line, n, printed)
		}
	}
	if n := run.script.eventConnections(); n != 2 {
		t.Errorf("the stream was opened %d times, want 2", n)
	}
}

// TestAPayloadThatWillNotDecodeStillCountsTowardsTheReplay is the third
// shape of the same rule, and it is the one a careful implementation gets
// wrong.
//
// The count means "how many persisted events has this connection
// delivered". The server persisted an event whether or not this client
// could read its payload — so counting only what DECODES leaves the tally
// one short, and the next reconnection re-renders a line the reader has
// already seen. Being careful about the wrong thing reaches the exact
// failure the tally exists to prevent.
//
// THE REPLAY MUST BE READABLE WHERE THE ORIGINAL WAS NOT, or this row
// cannot see the difference at all — measured, after a first version that
// replayed the same broken frame and left the mutation GREEN. With the
// same payload on both connections the two orders agree, because the
// tally is ASSIGNED from the count rather than incremented and the next
// readable event absorbs the gap. The shapes only diverge when the replay
// carries something this client can read, which is the ordinary case
// rather than a contrived one: the stored form of a line is text, and it
// decodes when a mangled typed event did not.
//
// REQUIRED MUTATION, run 2026-09-08: count a log event only after its
// payload decodes. Reds — "LINE-B reached the terminal 2 times".
func TestAPayloadThatWillNotDecodeStillCountsTowardsTheReplay(t *testing.T) {
	// An event of a type this client knows, carrying a payload it cannot
	// read. A server that grew a field is not this; this is a bug at the
	// far end, and the question is only what it costs here.
	broken := rawFrame(string(wire.EventLog), `{"line": `)

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{
		{frames: []string{broken, logFrame("LINE-B")}},
		{frames: []string{
			logFrame("LINE-A-READABLE-THIS-TIME"),
			logFrame("LINE-B"), logFrame("LINE-C"),
			doneFrame(wire.StatusBuilt),
		}},
	}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("a payload that would not decode stopped the run: %v\n%s",
			err, rendered(err))
	}

	printed := run.prompt.results.String()
	for _, line := range []string{"LINE-B", "LINE-C"} {
		if n := strings.Count(printed, line); n != 1 {
			t.Errorf("%s reached the terminal %d times, want exactly 1:\n%s",
				line, n, printed)
		}
	}
	// The event the first connection could not read is one the reader has
	// already been counted as having seen, so its readable replacement is
	// skipped rather than shown late. That is the cost of this rule, and
	// it is stated here rather than left to be discovered: one line of a
	// build log, lost to a payload the server sent wrong.
	if strings.Contains(printed, "LINE-A-READABLE-THIS-TIME") {
		t.Errorf("a replayed line the reader was already counted as having seen "+
			"was shown late:\n%s", printed)
	}
	if n := run.script.eventConnections(); n != 2 {
		t.Errorf("the stream was opened %d times, want 2", n)
	}
}

// -------------------------------------------------------------------
// Liveness
// -------------------------------------------------------------------

// TestAStreamThatStopsTalkingIsReconnected. Liveness is a STALL, not a
// duration: a build that is quiet is a build that is working, and no
// deadline can tell one from a connection that has died. The window
// bounds the gap between two bytes, and nothing bounds the whole.
//
// REQUIRED MUTATION, run 2026-09-08: remove the stall watchdog. The row
// then hangs on a connection nobody is going to close and dies of the
// test binary's own timeout — "panic: test timed out after 15s, running
// tests: TestAStreamThatStopsTalkingIsReconnected" — which IS the failure
// it is about: without this rule a finished build waits for ever.
//
// THE FIRST ATTEMPT AT THAT MUTATION WAS NOT ONE. Changing only the
// timer's initial duration left the row green, because the timer is also
// reset on every line that arrives, so the first frame armed it again at
// the real window. A mutation that removes half a mechanism measures the
// other half; both the arming and the reset have to go.
func TestAStreamThatStopsTalkingIsReconnected(t *testing.T) {
	stall := timing.StreamGoesQuiet.Window

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.StreamStallTimeout = stall
	run.script.eventScripts = []eventScript{
		// Alive, then silent, and never closed: nothing about this
		// connection is broken, which is why only a stall rule can see
		// it.
		{frames: []string{logFrame("LINE-A")}, hold: true},
		{frames: []string{logFrame("LINE-A"), logFrame("LINE-B"), doneFrame(wire.StatusBuilt)}},
	}

	started := time.Now()
	handoff, err := run.run()
	defer handoff.Release()
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}

	if n := run.script.eventConnections(); n != 2 {
		t.Fatalf("the stream was opened %d times, want 2 — a stream that stopped "+
			"talking is reconnected", n)
	}
	if elapsed < stall {
		t.Errorf("the run took %v, which is under the %v window — it cannot have "+
			"given up because of the stall rule", elapsed, stall)
	}
	printed := run.prompt.results.String()
	for _, line := range []string{"LINE-A", "LINE-B"} {
		if n := strings.Count(printed, line); n != 1 {
			t.Errorf("%s was printed %d times, want exactly 1:\n%s", line, n, printed)
		}
	}

	// AND IT SAYS WHICH ENDING THIS WAS. A connection that is still up and
	// has gone silent is a different thing to be told about from one that
	// went away, and the pair of assertions is what stops the two
	// sentences collapsing into one — the other half is on the row about a
	// stream that ends after an error.
	narrated := run.prompt.out.String()
	if !strings.Contains(narrated, streamWentQuiet) {
		t.Errorf("the run never said the build log had gone quiet:\n%s", narrated)
	}
	if strings.Contains(narrated, streamDropped) {
		t.Errorf("a connection that was still up was described as lost:\n%s", narrated)
	}
}

// keepAlivePace and keepAliveBeats are the keep-alive fixture, STATED,
// and shared by the row and the probe beside it.
//
// THEY WERE ALREADY CONSTANTS AND THE PROBE HAD ITS OWN COPY OF ONE OF
// THEM, which is the defect worth naming here rather than the values.
// The row ran forty beats at fifteen milliseconds and the probe ran
// twenty at a fifteen written out again in its own file — so the number
// recorded as this row's margin was measured over a fixture half the
// length of the row's, and nothing anywhere said so. A probe measures
// the row's own shape or it answers a different question; that rule has
// already been paid for once on the write side, where a connection kept
// warm across runs reported 594 ms against a fresh one's 434.
//
// Six hundred milliseconds of nothing but comment frames is what makes
// the row able to see a client that stopped counting them as proof of
// life: it is twice the window at the time of writing, and the row
// asserts that relation rather than assuming it, because the window
// comes from the registry and can be raised there by a leg this machine
// is not.
const (
	keepAlivePace  = 15 * time.Millisecond
	keepAliveBeats = 40
)

// keepAliveFrames is that fixture: nothing but keep-alives, then an
// ending. One builder, so the row and the probe cannot drift.
func keepAliveFrames() []string {
	frames := make([]string, 0, keepAliveBeats+1)
	for i := 0; i < keepAliveBeats; i++ {
		frames = append(frames, commentFrame())
	}
	return append(frames, doneFrame(wire.StatusBuilt))
}

// TestKeepAliveFramesAreProofOfLifeAndAreNeverRendered is the other half
// of the pair, and without it the stall rule could be satisfied by a
// client that gives up on any quiet connection.
//
// The comment frames a stream sends are the only thing crossing the
// connection for most of a slow build. They are consumed as evidence and
// never shown.
//
// THE MARGIN IS MEASURED RATHER THAN ASSUMED, and the evidence is in
// internal/timing rather than in this comment: the window, the quantity
// it bounds, the reader it was measured through, the worst gap on each
// leg, the run count and the date. The fixture ALSO reports the widest
// gap between two of its own flushes, which is a different number and a
// cheaper one — it says whether this particular run measured the client
// or the machine. Sizing the window against the fixture's own pacing
// knob alone is how a timing row becomes a flake with a schedule.
//
// REQUIRED MUTATION, run 2026-09-08: count only EVENTS as proof of life,
// not comment frames. Reds — "a stream carrying nothing but keep-alives
// was abandoned" — after the run spends its whole reconnect budget on a
// connection that was never broken.
//
// THAT MUTATION FIRST RAN GREEN, and the cause was in the fixture rather
// than in the client. A comment frame was being written with a blank line
// after it, which it does not need — nothing is dispatched — and the
// blank line is ordinary traffic to a reader that is not counting
// comments as traffic. The fixture was measuring a property the client
// did not have to have. A row is only as good as the bytes it sends.
func TestKeepAliveFramesAreProofOfLifeAndAreNeverRendered(t *testing.T) {
	// THE STREAM MUST STAY QUIET FOR LONGER THAN THE WINDOW, or this row
	// cannot see anything: the fixture above is twice the window in
	// nothing but comment frames, so a client that did not count them as
	// proof of life would have given up twice over. The row asserts that
	// relation below rather than assuming it, because the window now
	// comes from the registry and could be raised there by a leg this
	// machine is not.
	//
	// THE MARGIN THE OTHER WAY IS MEASURED RATHER THAN COMPUTED FROM THE
	// PACE. What the client depends on is not the pace this row asks for
	// but the interval it actually gets, which includes whatever the
	// scheduler and the loopback stack add — and that measurement, per
	// leg, is in internal/timing. The fixture still reports its own
	// widest gap on every run, so this row refuses rather than flakes
	// when that gap has eaten the margin.
	stall := timing.StreamKeepAlivesAreProofOfLife.Window

	// The quiet the row buys must outlast the window, whatever the
	// registry currently says the window is.
	if quiet := keepAliveBeats * keepAlivePace; quiet <= stall {
		t.Fatalf("the fixture is quiet for %v against a %v window — this row cannot "+
			"see a client that stopped counting keep-alives as proof of life",
			quiet, stall)
	}

	frames := keepAliveFrames()

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.StreamStallTimeout = stall
	run.script.eventScripts = []eventScript{{frames: frames, pace: keepAlivePace}}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("a stream carrying nothing but keep-alives was abandoned: %v\n%s",
			err, rendered(err))
	}

	widest := run.script.widestGap()
	// THE MEASUREMENT IS PRINTED, not only compared. A margin nobody can
	// read is a margin nobody can check has stopped being one — and this
	// number is a property of the runner rather than of this code, so it
	// is the number a red on another machine would be about.
	t.Logf("the fixture's widest gap between flushes was %v, against a %v window "+
		"and a %v pace", widest, stall, keepAlivePace)
	if widest >= stall {
		t.Fatalf("the fixture itself paused %v between flushes, past the %v window "+
			"— this row measured the machine rather than the client", widest, stall)
	}
	if n := run.script.eventConnections(); n != 1 {
		t.Errorf("the stream was opened %d times over %v of keep-alives, want 1 — "+
			"a comment frame is proof of life", n, keepAliveBeats*keepAlivePace)
	}
	if printed := run.prompt.results.String(); strings.Contains(printed, "keep-alive") {
		t.Errorf("a keep-alive frame was rendered:\n%s", printed)
	}
	if shown := run.prompt.out.String(); strings.Contains(shown, "keep-alive") {
		t.Errorf("a keep-alive frame was narrated:\n%s", shown)
	}
}

// -------------------------------------------------------------------
// The reader's bound
// -------------------------------------------------------------------

// TestALineLargerThanTheDefaultScannerBufferArrivesWhole.
//
// THE FAILURE BEING GUARDED IS NOT A LOST LINE. The standard line scanner
// gives up at 64 KiB by reporting no more input, which is byte-identical
// to a clean end of stream — and this client treats a stream that ends
// without done as abnormal and reconnects. So a hidden cap is an infinite
// loop over a build that has already finished: reconnect, replay from the
// start, hit the same line, stop, reconnect. A cap nobody chose is still
// a cap, and it fails in the shape that looks like success.
//
// The row therefore asserts three things: the line arrives whole, the
// done AFTER it is received, and NO reconnection happened.
//
// REQUIRED MUTATION, run 2026-09-08: read the stream with the standard
// scanner at its default buffer. Reds, and reds AS THE LOOP rather than
// as a lost line: "a build log line of 65547 bytes stopped the run:
// curious lost the build log and could not pick it up again ... 5
// attempts to re-establish it did not last either". The build had already
// finished. The only reason that run ended at all is the reconnect budget
// this task also bounds — which is the two rules holding each other up,
// and the reason the row asserts no reconnection happened rather than
// only that the line arrived.
func TestALineLargerThanTheDefaultScannerBufferArrivesWhole(t *testing.T) {
	// One byte past the size at which the standard scanner gives up.
	const oversize = 64*1024 + 1
	line := "HEAD-" + strings.Repeat("x", oversize) + "-TAIL"

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{{
		frames: []string{logFrame(line), doneFrame(wire.StatusBuilt)},
		hold:   true,
	}}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("a build log line of %d bytes stopped the run: %v\n%s",
			len(line), err, rendered(err))
	}

	printed := run.prompt.results.String()
	if !strings.Contains(printed, line) {
		t.Errorf("the %d-byte line did not arrive whole: %d bytes were printed, "+
			"head %q tail %q", len(line), len(printed),
			printed[:min(40, len(printed))],
			printed[max(0, len(printed)-40):])
	}
	if n := run.script.eventConnections(); n != 1 {
		t.Errorf("the stream was opened %d times, want 1 — an oversized line that "+
			"ends the read looks exactly like a finished stream, and gets retried "+
			"for ever", n)
	}
}

// -------------------------------------------------------------------
// What this package hands the renderer
// -------------------------------------------------------------------

// THE ESCAPING MOVED, AND SO DID ITS PROOF. This path used to escape its
// own lines and these rows asserted the result — which proved the call
// site rather than the property, and the proof of that is the defect it
// missed: a failure three packages along carried a server's sentence to
// the terminal with an OSC window-retitle in it, unescaped, while a row
// here said the build log was safe. The escaping now happens once, at
// the rendering boundary in internal/ui, and the rows that assert it are
// ranged over that whole surface in internal/ui/boundary_test.go.
//
// WHAT IS LEFT HERE IS THIS PACKAGE'S OWN JOB: hand the far end's bytes
// over unchanged. A client that trimmed, dropped or re-encoded a line on
// its way to the renderer would be a defect no boundary can see, because
// by then the bytes are gone.

// TestEveryControlByteReachesTheRendererUnchanged ranges the set rather
// than listing bytes, so one cannot be missed by being forgotten.
//
// The assertion is byte equality with what the server sent, which is
// two-sided by construction: dropping the byte fails it, escaping the
// byte here fails it too, and both are things this package must not do.
//
// REQUIRED MUTATION, run 2026-09-09: escape the line in this path before
// handing it over — the change this commit removes. Reds on every one of
// the 33, with "the renderer was handed \"a\\x00z\", want \"a\x00z\"",
// because escaping twice at two layers is exactly the two-surfaces
// arrangement the move exists to end.
func TestEveryControlByteReachesTheRendererUnchanged(t *testing.T) {
	var control []byte
	for b := 0x00; b < 0x20; b++ {
		control = append(control, byte(b))
	}
	control = append(control, 0x7f)
	if len(control) != 33 {
		t.Fatalf("the row built %d control bytes, want 33", len(control))
	}

	sent := make([]string, 0, len(control))
	frames := make([]string, 0, len(control)+1)
	for _, b := range control {
		line := "a" + string([]byte{b}) + "z"
		sent = append(sent, line)
		frames = append(frames, logFrame(line))
	}
	frames = append(frames, doneFrame(wire.StatusBuilt))

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{{frames: frames}}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}

	// THE RECORD OF WHAT WAS HANDED OVER, not the rendered buffer. The
	// terminal double delegates to the real renderer now, so its buffers
	// hold escaped output — which is right, and is the other row's
	// subject. What this row is about is the bytes this package passes
	// ON, before anything has touched them.
	var handed []string
	for _, entry := range run.journal.all() {
		if line, ok := strings.CutPrefix(entry, "printed: "); ok {
			handed = append(handed, line)
		}
	}
	// The address is the last thing printed, and it is not build output.
	if n := len(handed); n > 0 && handed[n-1] == publishedURL(run.script.publishSubdomain) {
		handed = handed[:n-1]
	}
	if len(handed) != len(sent) {
		t.Fatalf("the renderer was handed %d lines, want %d", len(handed), len(sent))
	}
	for i, line := range handed {
		if line != sent[i] {
			t.Errorf("line %d was changed on its way to the renderer:\n got %q\nwant %q",
				i, line, sent[i])
		}
	}
}

// -------------------------------------------------------------------
// The stream split
// -------------------------------------------------------------------

// TestTheBuildLogGoesToStdoutAndTheNarrationToStderr. A redirected stdout
// collects the build log and nothing else, which is the property that
// makes `curious deploy > build.log` mean something.
func TestTheBuildLogGoesToStdoutAndTheNarrationToStderr(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.eventScripts = []eventScript{{frames: []string{
		phaseFrame(wire.PhaseBuilding),
		logFrame("BUILD-OUTPUT-LINE"),
		doneFrame(wire.StatusBuilt),
	}}}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}

	printed, narrated := run.prompt.results.String(), run.prompt.out.String()
	if !strings.Contains(printed, "BUILD-OUTPUT-LINE") {
		t.Errorf("the build's own output did not reach stdout:\n%s", printed)
	}
	if strings.Contains(narrated, "BUILD-OUTPUT-LINE") {
		t.Errorf("the build's own output was also narrated to stderr:\n%s", narrated)
	}
	for _, want := range []string{
		phaseNarration + string(wire.PhaseBuilding) + ".",
		finishedNarration + string(wire.StatusBuilt) + ".",
	} {
		if !strings.Contains(narrated, want) {
			t.Errorf("this client's own narration (%q) did not reach stderr:\n%s",
				want, narrated)
		}
		if strings.Contains(printed, want) {
			t.Errorf("this client's own narration (%q) went to stdout, which is "+
				"the build log's:\n%s", want, printed)
		}
	}
}

// TestTheStreamsFailureNamesTheDeployAndNeverTheToken. The stream is the
// one authenticated call built outside the shared request path, which is
// exactly where a credential gets formatted into a message by whoever
// adds the next branch.
func TestTheStreamsFailureNamesTheDeployAndNeverTheToken(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid"))
	run.storedToken(streamTestToken, run.srv.URL)
	// Every connection is refused by the server with an envelope it
	// wrote, so the failure travels the path that has the token in hand.
	run.script.eventScripts = []eventScript{{frames: []string{
		logFrame("LINE-A"),
	}}}

	handoff, err := run.run()
	defer handoff.Release()
	if err == nil {
		t.Fatal("a stream that never said done was reported as a finished build")
	}

	text, _ := renderedBytes(t, err)
	surfaces := map[string]string{
		"the rendered failure": text,
		"what the run printed": run.prompt.out.String(),
		"the build log":        run.prompt.results.String(),
	}
	for name, surface := range surfaces {
		if strings.Contains(surface, streamTestToken) {
			t.Errorf("%s carries the bearer token:\n%s", name, surface)
		}
	}
	// The positive half. Without it an empty message satisfies the above.
	if !strings.Contains(text, "deploy-1") {
		t.Errorf("the failure names no deploy, so the absences above are also "+
			"satisfied by silence:\n%s", text)
	}
}

// streamTestToken is a fixture value and names no real credential.
const streamTestToken = "stream-bearer-sentinel-value"

// TestOriginDecidesTheStream is the routing rule stated as two rows, and
// the pair is the whole of it: one thing the SERVER said and one thing
// this CLIENT said, each asserted present on its own stream and absent
// from the other.
//
// The rule is provenance rather than shape — an error event is rendered
// through a sentence of this client's own and still belongs to stdout,
// because the server writes that same text into the same log it writes
// the build output into. A saved log missing the line that explains why
// the rest of it stops is worse than one carrying a sentence somebody
// did not expect.
//
// REQUIRED MUTATION: swap the writer on either. Sending the error to
// stderr reds the first row; sending the reconnect notice to stdout reds
// the second. Each reds ALONE, which is what makes them two rows rather
// than one row asserted twice.
func TestOriginDecidesTheStream(t *testing.T) {
	t.Run("what the server said goes to stdout", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}
		run.script.eventScripts = []eventScript{{
			frames: []string{
				errorFrame(wire.CodeInternal, "THE-SERVERS-OWN-DIAGNOSTIC"),
				doneFrame(wire.StatusFailed),
			},
			hold: true,
		}}

		handoff, err := run.run()
		defer handoff.Release()
		if err == nil {
			t.Fatal("a build that ended failed was reported as a success")
		}

		printed, narrated := run.prompt.results.String(), run.prompt.out.String()
		if !strings.Contains(printed, "THE-SERVERS-OWN-DIAGNOSTIC") {
			t.Errorf("the server's diagnostic did not reach stdout, so a redirected "+
				"log would be missing the line explaining why the build stopped:\n%s",
				printed)
		}
		if strings.Contains(narrated, "THE-SERVERS-OWN-DIAGNOSTIC") {
			t.Errorf("the server's diagnostic was also written to stderr:\n%s", narrated)
		}
	})

	t.Run("what this client said goes to stderr", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
		run.prompt.confirms = []answer{no()}
		// Two connections: the first ends without `done`, which is the
		// abnormal ending this client narrates and retries.
		run.script.eventScripts = []eventScript{
			{frames: []string{logFrame("BEFORE-THE-CUT")}},
			{frames: []string{
				logFrame("BEFORE-THE-CUT"),
				logFrame("AFTER-THE-CUT"),
				doneFrame(wire.StatusBuilt),
			}, hold: true},
		}

		handoff, err := run.run()
		defer handoff.Release()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}

		printed, narrated := run.prompt.results.String(), run.prompt.out.String()
		if !strings.Contains(narrated, reconnectNarration) {
			t.Errorf("this client never said on stderr that it was picking the "+
				"stream up again:\n%s", narrated)
		}
		if strings.Contains(printed, reconnectNarration) {
			t.Errorf("this client's reconnect notice went to stdout, which is the "+
				"build log's — a saved log would carry a line the server never "+
				"wrote:\n%s", printed)
		}
		// The positive control for the capture itself: the build's own
		// output DID reach stdout in the same run, so "absent from
		// stdout" is not a claim about an empty buffer.
		if !strings.Contains(printed, "AFTER-THE-CUT") {
			t.Fatalf("no build output reached stdout at all, so the absence "+
				"asserted above is about an empty stream:\n%s", printed)
		}
	})
}

// partialLinePace is how fast the partial-line fixture flushes one
// piece of its single line, and it is shared by the row and the probe
// beside it so the two cannot pace differently while claiming to measure
// the same thing.
const partialLinePace = 20 * time.Millisecond

// partialLineDelivery is how long that fixture spends delivering its one
// line, and it is a STATED CONSTANT rather than a quantity derived from
// the window.
//
// # AN INSTRUMENT THAT FOLLOWS ITS OWN READING CANNOT CONVERGE
//
// This used to be three and a half windows' worth, computed. The
// reasoning was sound in isolation: the row asserts it spent three
// windows on one line, so the fixture has to outlast three windows, so
// derive it and it can never fall behind. What that missed is the loop
// it closes. The gap this window is a margin over IS the fixture's own
// pause between flushes — measured, across twenty-one passes of this
// probe, the client's worst gap and the fixture's widest flush gap
// agreed to within a millisecond every time — and the maximum of a
// heavy-tailed sample grows with how long you look. So a wider window
// delivered for longer, a longer delivery found a larger maximum, and
// five times that maximum asked for a wider window. The window went 150
// to 250 to 350 to 400 milliseconds inside one round, every step of it
// the sizing rule being applied correctly to a fresh number.
//
// Stated, the loop is cut: the fixture is the same length whatever the
// window becomes, so the maximum measured under it is a fixed quantity
// and five times it is a number that stays put.
//
// # WHY IT IS GENEROUSLY LARGER THAN THE ASSERTION NEEDS
//
// The row asserts three windows and wants three and a half for margin,
// so at a 250 ms window this constant only has to be 875 ms. It is a
// second and a half, which is nearly twice that, and the extra is
// deliberate: a constant sitting at the boundary is one that reds the
// first time a leg measures slower, and each of those reds is a person
// having to choose a new number under time pressure. The cost of the
// headroom is a fixed 600 milliseconds per run of the row and of each
// probe run, which is a price worth paying once to stop paying attention.
//
// RAISING IT IS A DELIBERATE ACT AND THE ROW SAYS SO. When a window
// grows past what this covers, the row refuses with both numbers rather
// than a fixture quietly growing to meet it.
const partialLineDelivery = 1500 * time.Millisecond

// partialLineMarker is the text the row looks for on stdout. It is
// repeated as the line grows, because what the row asserts is that the
// line arrived, not that it arrived once.
const partialLineMarker = "A-LINE-DELIVERED-IN-PIECES"

// partialLineFrames is one log frame cut into partialLineDelivery's
// worth of one-byte pieces.
//
// IT CANNOT SEE THE WINDOW, and that is the point rather than an
// omission. A helper handed the window is a helper that can be made to
// follow it, and following it is what stopped the margin converging —
// see partialLineDelivery. The window is checked AGAINST this fixture,
// by the row that asserts something about it, which is a different
// direction and a different function.
//
// IT GROWS THE LINE AND THEN CHECKS, rather than computing a count and
// trusting it. splitEvenly cuts by SIZE: ask it for forty-three pieces
// of a sixty-byte string and it returns thirty, because it rounds the
// piece size up and then runs out of string. A count handed straight to
// it is a fixture quietly smaller than the arithmetic says. So the loop
// asks for what it got — and it grows by ONE BYTE at a time, so that
// what it got is what was asked for rather than the first multiple of a
// repeated marker to clear it. Growing by a whole marker overshot the
// stated delivery by nearly forty per cent, which would have made the
// constant above a number that describes no fixture.
func partialLineFrames(t *testing.T) []string {
	t.Helper()
	pieces := int(partialLineDelivery / partialLinePace)
	line := partialLineMarker
	// A bound, so a helper that can never satisfy its own condition
	// fails as a test rather than as a hung run.
	for grow := 0; grow <= pieces; grow++ {
		frames := splitEvenly(logFrame(line), len(logFrame(line)))
		if len(frames) >= pieces {
			// EXACTLY, not at least. One byte per growth step means the
			// first length to reach the target is the target, unless the
			// marker's own frame was already past it — which is a
			// constant chosen too small for the fixture to describe, and
			// is worth a refusal rather than a silent overshoot.
			if len(frames) != pieces {
				t.Fatalf("the shortest frame this helper can build is %d pieces and "+
					"partialLineDelivery asks for %d. The stated delivery is shorter "+
					"than one frame of the marker, so the constant describes a fixture "+
					"that cannot exist — raise it, or shorten the marker.",
					len(frames), pieces)
			}
			return frames
		}
		line += "-"
	}
	t.Fatalf("no line this helper is willing to build reaches %d pieces, so the row "+
		"it feeds delivers for less than the stated %v", pieces, partialLineDelivery)
	return nil
}

// splitEvenly cuts s into n pieces, so a fixture can deliver one frame
// as a sequence of partial writes rather than in a single flush.
func splitEvenly(s string, n int) []string {
	size := (len(s) + n - 1) / n
	var out []string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		out = append(out, s[i:end])
	}
	return out
}

// TestBytesArrivingWithoutANewlineAreNotAStall. The watchdog's promise is
// that the connection is MOVING, and a line is not the unit that moves —
// bytes are. A server writing one long line slowly is delivering
// continuously; a client that only notices completed lines sees nothing
// happening at all, cuts a healthy connection, and replays the whole
// build to get back to where it was.
//
// The fixture spends several stall windows on ONE line, with every gap a
// small fraction of a window. Under a line-counting watchdog the run
// fails; under a byte-counting one it finishes.
//
// REQUIRED MUTATION: reset the watchdog on the completed line instead of
// on the read.
func TestBytesArrivingWithoutANewlineAreNotAStall(t *testing.T) {
	stall := timing.StreamPartialLineIsNotAStall.Window

	// THE FIXTURE IS STATED AND THE WINDOW IS CHECKED AGAINST IT, which
	// is the reverse of what this row used to do and the whole of the
	// difference. The piece count used to be derived from the window, on
	// the reasoning that a fixture which follows the window can never
	// fall behind it. It cannot, and that is exactly the problem: the
	// gap this window is a margin over is the fixture's own pause
	// between flushes, so a longer fixture measures a larger maximum and
	// five times that asks for a longer fixture. See
	// partialLineDelivery.
	//
	// So the fixture is a constant, and the relation the row needs is
	// asserted rather than arranged. THREE AND A HALF WINDOWS, not
	// three: the halves are a margin over the ASSERTION below, so a
	// scheduling hiccup cannot fail a row about something else, and they
	// are a different margin from the five-times rule beside the
	// registry, which is a margin over the GAP.
	//
	// REQUIRED MUTATIONS, RE-RUN ON THE TIP 2026-09-10, from both
	// directions — because this check is a RELATION and breaking only
	// one side of it proves half of a rule.
	//
	//  1. Drop partialLineDelivery to 700 ms against the 230 ms window.
	//     Reds here: "the fixture is stated at 700ms of delivery and a
	//     230ms window asks for 805ms".
	//  2. Leave the fixture alone and raise the window to 500 ms. Reds
	//     here too: "stated at 1.5s … and a 500ms window asks for 1.75s".
	//     That is the direction this check actually exists for — a leg
	//     measuring slower is how the window grows, and nobody editing
	//     the registry is looking at this file.
	//
	// The probe beside the row stays green under both, which is correct:
	// it measures gaps and asserts nothing about spending three windows.
	//
	// AND AN EARLIER ATTEMPT AT THE FIRST ONE REDDENED SOMEWHERE ELSE,
	// which is worth keeping. At a 400 ms window, 700 ms of delivery was
	// short enough that the helper's own floor fired first — "the
	// shortest frame this helper can build is 56 pieces and
	// partialLineDelivery asks for 35" — because a stated delivery
	// shorter than one frame of the marker describes a fixture that
	// cannot be built at all. That red comes out of the probe as well as
	// the row, and it is a second falsifier rather than a hole.
	if 2*partialLineDelivery < 7*stall {
		t.Fatalf("the fixture is stated at %v of delivery and a %v window asks for %v "+
			"— three and a half of itself — so this row cannot spend three windows on "+
			"one line.\nThe fixture does not follow the window on purpose: a fixture "+
			"derived from the window makes the measurement below it follow its own "+
			"reading, and that margin went 150 to 250 to 350 to 400 ms inside one "+
			"round without settling. Raise partialLineDelivery deliberately, and pay "+
			"the runtime it costs.",
			partialLineDelivery, stall, 7*stall/2)
	}
	frames := append(partialLineFrames(t), doneFrame(wire.StatusBuilt))

	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.StreamStallTimeout = stall
	run.script.eventScripts = []eventScript{{
		frames: frames,
		pace:   partialLinePace,
		hold:   true,
	}}

	started := time.Now()
	handoff, err := run.run()
	defer handoff.Release()
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("a stream delivering bytes continuously was treated as stalled "+
			"after %v: %v\n%s", elapsed, err, rendered(err))
	}
	if conns := run.script.eventConnections(); conns != 1 {
		t.Errorf("the stream was opened %d times, want 1 — a healthy connection "+
			"was cut and replayed", conns)
	}
	if elapsed < 3*stall {
		t.Fatalf("the run took %v, under %v, so it never spent long enough on one "+
			"line for a line-counting watchdog to fire", elapsed, 3*stall)
	}
	if printed := run.prompt.results.String(); !strings.Contains(printed, partialLineMarker) {
		t.Errorf("the line delivered in pieces never reached stdout:\n%s", printed)
	}

	// THE FIXTURE'S OWN WIDEST PAUSE, checked the way the keep-alive row
	// beside this one checks its own, and added because a runner
	// supplied the instance. On a hosted macOS runner this row's probe
	// measured a 272 ms gap between two arrivals while the FIXTURE's own
	// widest gap between flushes was 220 ms — the server goroutine was
	// starved, and the client was blamed for it.
	//
	// A window is a margin over the quantity this client depends on. A
	// gap that is mostly the fixture failing to write is a measurement
	// about the machine, and widening the window until it goes quiet is
	// the "raise the number until the failures stop" this whole
	// arrangement exists to replace. So it is named instead.
	//
	// ITS MUTATION IS NOT AVAILABLE FROM THIS FIXTURE'S KNOBS, and that
	// is worth writing down rather than leaving as an untested line. The
	// only knob that widens the fixture's own gap is the pace, and a
	// pace past the window makes the client legitimately stall — run
	// 2026-09-10 at fourteen times the pace, the row reds one assertion
	// EARLIER, on "a stream delivering bytes continuously was treated as
	// stalled", and never reaches here. What reaches here is a fixture
	// that paces correctly and is starved once, which is a machine event
	// rather than a setting. Its sibling guard in the keep-alive row has
	// the same shape and the same limitation, and the instance that
	// justifies both was supplied by a hosted runner rather than by a
	// mutation.
	widest := run.script.widestGap()
	t.Logf("the fixture's widest gap between flushes was %v, against a %v window "+
		"and a %v pace", widest, stall, partialLinePace)
	if widest >= stall {
		t.Fatalf("the fixture itself paused %v between flushes, past the %v window "+
			"— this row measured the machine rather than the client", widest, stall)
	}
}

// TestAPhaseIsNotNarratedTwiceAcrossAReconnect. The persisted-event tally
// suppresses narration while a replay catches up, and at the moment it
// catches up exactly — the cut fell right after a phase — the phase is
// replayed with the tally already equal, so it renders a second time.
//
// Every reconnect after that repeats it, so the marker multiplies on the
// one surface a person is watching.
//
// REQUIRED MUTATION: suppress on the tally alone (drop the check that the
// phase is the one already shown).
func TestAPhaseIsNotNarratedTwiceAcrossAReconnect(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	// The cut falls immediately after the phase, which is the boundary
	// the tally cannot see.
	run.script.eventScripts = []eventScript{
		{frames: []string{
			logFrame("BEFORE-THE-PHASE"),
			phaseFrame(wire.PhaseBuilding),
		}},
		{frames: []string{
			logFrame("BEFORE-THE-PHASE"),
			phaseFrame(wire.PhaseBuilding),
			logFrame("AFTER-THE-CUT"),
			doneFrame(wire.StatusBuilt),
		}, hold: true},
	}

	handoff, err := run.run()
	defer handoff.Release()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}

	narrated := run.prompt.out.String()
	want := phaseNarration + string(wire.PhaseBuilding) + "."
	if got := strings.Count(narrated, want); got != 1 {
		t.Errorf("the phase was narrated %d times, want exactly 1 — a reconnect "+
			"repeated the marker it was cut after:\n%s", got, narrated)
	}
	// The positive control: a phase this client has NOT shown before
	// still gets through, so the fix cannot be "never narrate a phase".
	if !strings.Contains(narrated, want) {
		t.Errorf("the phase was never narrated at all:\n%s", narrated)
	}
	if printed := run.prompt.results.String(); !strings.Contains(printed, "AFTER-THE-CUT") {
		t.Errorf("the run did not get past the reconnect:\n%s", printed)
	}
}
