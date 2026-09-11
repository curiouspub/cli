package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// report is one thing a tool said while it was working, kept as the two
// halves it was said in rather than as a formatted line. A recorder that
// joined them would be a second renderer, and the row would then be
// asserting against its own formatting.
type report struct {
	phase wire.Phase
	line  string
}

// recordingProgress is a sink that keeps everything it is given, in
// order. It is the instrument for both directions of this file: a tool
// that reports leaves entries in it, and a tool that does not leaves it
// empty.
//
// NO MUTEX, deliberately, and the reason is worth a line because the
// server package next door has one everywhere. A handler runs on the
// goroutine that dispatched it — there is no concurrency in this package
// at all — so a lock here would be protecting against a shape that does
// not exist and implying one that does.
type recordingProgress struct{ got []report }

func (r *recordingProgress) Report(phase wire.Phase, line string) {
	r.got = append(r.got, report{phase: phase, line: line})
}

// TestASinkHearsWhatAToolSaysAndNothingItDoesNot is the no-op default's
// row, and it is TWO CASES IN ONE because neither half can fail alone.
//
// THE EMPTY HALF IS THE CLAIM: a tool with nothing to report reports
// nothing, so an agent watching a quiet tool sees silence rather than
// invented noise. On its own that assertion passes against a sink that
// is never handed to anything, a Report that is never called by
// construction, and a harness whose handler never ran — every one of
// which is the same empty slice.
//
// THE REPORTING HALF IS WHAT MAKES THE EMPTY ONE MEAN SOMETHING. It runs
// the same sink through the same call path with a handler that does
// speak, so "empty" is established as a property of the tool rather than
// of the wiring.
//
// IT GOES THROUGH invoke RATHER THAN THROUGH A DIRECT HANDLER CALL,
// because invoke is where the sink is defaulted and where the containment
// sits. A row calling t.Handler itself would assert that a func passed
// its arguments on, which the compiler already knows.
func TestASinkHearsWhatAToolSaysAndNothingItDoesNot(t *testing.T) {
	t.Run("a tool that reports nothing", func(t *testing.T) {
		sink := &recordingProgress{}
		quiet := Tool{
			Name:    "quiet",
			Handler: func(context.Context, json.RawMessage, Progress) Result { return TextResult("done") },
		}

		result := invoke(context.Background(), quiet, nil, sink, newNotes(discardWriter{}))

		if result.IsError {
			t.Fatalf("the tool did not run: %+v", result)
		}
		if len(sink.got) != 0 {
			t.Errorf("a tool that reported nothing put %d entries on the sink: %+v", len(sink.got), sink.got)
		}
	})

	t.Run("a tool that reports", func(t *testing.T) {
		sink := &recordingProgress{}
		talkative := Tool{
			Name: "talkative",
			Handler: func(_ context.Context, _ json.RawMessage, progress Progress) Result {
				progress.Report(wire.PhaseInstalling, "added 41 packages")
				progress.Report(wire.PhaseBuilding, "building for production")
				return TextResult("done")
			},
		}

		result := invoke(context.Background(), talkative, nil, sink, newNotes(discardWriter{}))

		if result.IsError {
			t.Fatalf("the tool did not run: %+v", result)
		}
		want := []report{
			{phase: wire.PhaseInstalling, line: "added 41 packages"},
			{phase: wire.PhaseBuilding, line: "building for production"},
		}
		if len(sink.got) != len(want) {
			t.Fatalf("the sink heard %d reports, want %d: %+v", len(sink.got), len(want), sink.got)
		}
		for i, w := range want {
			if sink.got[i] != w {
				// ORDER IS ASSERTED, not just membership. Progress is a
				// narration of a sequence, and a client shown the build
				// phase before the install phase has been told something
				// false about a run that was fine.
				t.Errorf("report %d = %+v, want %+v", i, sink.got[i], w)
			}
		}
	})
}

// TestAHandlerAlwaysHasASinkToReportTo is the other half of "Progress is
// never nil", and it exists because the alternative fails in the worst
// possible place.
//
// A nil sink would put `if progress != nil` inside every tool that
// reports anything. The one that forgets does not misbehave quietly — it
// panics, inside somebody's deploy, and the containment turns that into
// a call that failed for no reason the user can act on. So the default
// is applied where the handler is called, and this row is what would
// notice it being removed.
//
// THE HANDLER REPORTS TWICE rather than once, so that a default applied
// by handing over a fresh value per call would still be exercised on the
// second.
func TestAHandlerAlwaysHasASinkToReportTo(t *testing.T) {
	var sawProgress Progress
	reporter := Tool{
		Name: "reporter",
		Handler: func(_ context.Context, _ json.RawMessage, progress Progress) Result {
			sawProgress = progress
			progress.Report(wire.PhaseQueued, "")
			progress.Report(wire.PhaseBuilding, "still here")
			return TextResult("done")
		},
	}

	// nil, which is what a caller that has nobody to report to would
	// otherwise be tempted to pass.
	result := invoke(context.Background(), reporter, nil, nil, newNotes(discardWriter{}))

	if result.IsError {
		// A panic inside the handler is contained and comes back as an
		// error result, so this is what a nil reaching a tool looks like
		// from out here rather than a failed test binary.
		t.Fatalf("reporting to the default sink did not survive the call: %+v", result)
	}
	if sawProgress == nil {
		t.Fatal("the handler was given a nil sink")
	}
}

// serveContextKey and serveContextMark are how the row below tells the
// context Serve was given apart from one the dispatcher made up. A value
// is the only thing that survives being passed through and cannot be
// produced by accident: a fresh context.Background() is indistinguishable
// from the real one by every other property, including not being nil.
type serveContextKey struct{}

const serveContextMark = "the context Serve was handed"

// TestADispatchedCallGetsTheContextServeWasGiven is the plumbing row.
// The two rows above call invoke directly, which is the right place to
// assert what invoke does and says nothing about whether the DISPATCHER
// reaches it with anything real.
//
// IT ASSERTS IDENTITY AND NOT PRESENCE, and the difference is the whole
// row. "The handler got a non-nil context" passes against a callTool
// that mints its own with context.Background(), which is precisely the
// mistake worth having a row about: the parameter would still be there,
// still be non-nil, still compile, and be a context nothing outside this
// package could ever cancel. A marked value threaded in through Serve is
// the only assertion that can tell the two apart.
//
// THE SINK IS CHECKED AS A SMOKE TEST RATHER THAN AS THE CLAIM, and
// saying so is better than implying coverage this cannot have: invoke
// substitutes the no-op for a nil, so a dispatcher passing nil produces
// a non-nil sink here by construction and this row could not see it.
// What it does see is that the sink a dispatched tool gets can be
// REPORTED THROUGH without ending the call — a sink that is merely
// non-nil and panics on use would red on the result.
func TestADispatchedCallGetsTheContextServeWasGiven(t *testing.T) {
	var (
		ran         bool
		sawMark     any
		sawProgress Progress
	)

	s := testServer()
	s.Register(Tool{
		Name: "inspect",
		Handler: func(ctx context.Context, _ json.RawMessage, progress Progress) Result {
			ran = true
			sawMark = ctx.Value(serveContextKey{})
			sawProgress = progress
			progress.Report(wire.PhaseStarting, "here")
			return TextResult("done")
		},
	})

	ctx := context.WithValue(context.Background(), serveContextKey{}, serveContextMark)
	var out, logw bytes.Buffer
	mustWithin(t, serveDeadline, "Serve", func() {
		if err := s.Serve(ctx, strings.NewReader(callMessage("1", "inspect", `{}`)+"\n"), &out, &logw); err != nil {
			t.Errorf("Serve: %v", err)
		}
	})

	replies := decodeReplies(t, out.String())
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1: %q", len(replies), out.String())
	}
	if got := resultOf(t, replies[0]); got.IsError {
		t.Fatalf("the dispatched call failed: %+v", got)
	}

	if !ran {
		t.Fatal("the handler never ran, so this row asserted nothing")
	}
	if sawMark != serveContextMark {
		t.Errorf("the tool's context carries %v, want the value Serve was handed (%q) — "+
			"a dispatcher that makes its own context hands every tool one nothing can cancel",
			sawMark, serveContextMark)
	}
	if sawProgress == nil {
		t.Error("a dispatched call handed the tool a nil sink")
	}
}

// discardWriter is a diagnostic stream nothing reads. These rows are
// about the sink rather than about what the operator is told, and the
// panic path has its own file.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
