package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/timing"
	"github.com/curiouspub/cli/pkg/wire"
)

// framesFrom is a stream a row writes out in full, handed over as the
// connection ReadDeployStream opens.
//
// NO SERVER, DELIBERATELY. What is under test is the reading, and the
// connection is a parameter precisely so a row can supply one: an
// httptest server here would add a second thing that can fail and would
// measure net/http on the way past. The one row that DOES need a real
// failure to open the connection supplies a function that refuses,
// which is the same seam.
func framesFrom(frames ...string) StreamReportDeps {
	return StreamReportDeps{
		Events: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(strings.Join(frames, ""))), nil
		},
	}
}

// TestAStreamThatFinishedReportsTheStatusItCarried and
// TestAStreamThatStoppedShortReportsNoStatusAtAll are TWO ROWS FOR ONE
// PROPERTY, and neither can establish it alone.
//
// The property is that this reports what the stream said and never more.
// A reader that always answered "not yet reported" satisfies the second
// row perfectly; one that answered `built` whatever arrived satisfies
// the first. Only the pair distinguishes a reader that is looking from
// one that has an opinion.
//
// REQUIRED MUTATION, run 2026-09-11: set Reported unconditionally,
// before the scan. The short row reds — Reported true with an empty
// status — and this one stays green, which is the asymmetry that makes
// them two rows.
func TestAStreamThatFinishedReportsTheStatusItCarried(t *testing.T) {
	report, err := ReadDeployStream(t.Context(), framesFrom(
		phaseFrame(wire.PhaseInstalling),
		logFrame("added 41 packages"),
		phaseFrame(wire.PhaseBuilding),
		logFrame("astro build finished"),
		doneFrame(wire.StatusBuilt),
	))
	if err != nil {
		t.Fatalf("ReadDeployStream: %v", err)
	}

	if !report.Reported {
		t.Error("a stream that reached its terminal event reported nothing")
	}
	if report.Status != wire.StatusBuilt {
		t.Errorf("status = %q, want the one the terminal event carried (%q)",
			report.Status, wire.StatusBuilt)
	}
	if report.Phase != wire.PhaseBuilding {
		t.Errorf("phase = %q, want the last one the stream named (%q)",
			report.Phase, wire.PhaseBuilding)
	}
	assertLog(t, report.Log, []string{"added 41 packages", "astro build finished"})
	if len(report.Errors) != 0 {
		t.Errorf("a clean stream carried %d diagnostics: %+v", len(report.Errors), report.Errors)
	}
}

// TestAStreamThatStoppedShortReportsNoStatusAtAll is the half the whole
// three-state shape exists for.
//
// There is no endpoint that answers a deploy's state, so a build still
// running has said nothing about how it ends — and the tempting answer,
// reporting the last PHASE as though it were a status, is wrong in the
// way that is hardest to notice: "building" is a member of both
// vocabularies, so a caller switching on it would be right until the day
// it was not.
//
// WHAT THE STREAM DID SAY IS STILL REPORTED, which is the other half:
// the phase and the output are what a caller can act on while there is
// no status, and a row asserting only the absence would pass against a
// reader that threw the whole connection away.
func TestAStreamThatStoppedShortReportsNoStatusAtAll(t *testing.T) {
	report, err := ReadDeployStream(t.Context(), framesFrom(
		phaseFrame(wire.PhaseInstalling),
		logFrame("added 41 packages"),
		commentFrame(),
	))
	if err != nil {
		t.Fatalf("ReadDeployStream: %v", err)
	}

	if report.Reported {
		t.Error("a stream that never reached its terminal event reported a status anyway")
	}
	if report.Status != "" {
		t.Errorf("status = %q, want nothing at all — the stream said none", report.Status)
	}
	if report.Phase != wire.PhaseInstalling {
		t.Errorf("phase = %q, want the last one the stream named (%q)",
			report.Phase, wire.PhaseInstalling)
	}
	assertLog(t, report.Log, []string{"added 41 packages"})
}

// TestTheDiagnosticsAStreamCarriedAreReportedBesideItsStatus.
//
// The contract's grammar says an error event EXPLAINS and never ends, so
// a failed build still reaches its terminal event — which means a report
// carrying one is carrying both, and a reader that stopped at the first
// diagnostic would throw away the status it was asked for.
//
// REQUIRED MUTATION, run 2026-09-11: return true from the error branch,
// which is the shape of a reader that treats a diagnostic as the end.
// Reds here on Reported and on the status.
func TestTheDiagnosticsAStreamCarriedAreReportedBesideItsStatus(t *testing.T) {
	report, err := ReadDeployStream(t.Context(), framesFrom(
		logFrame("running astro build"),
		errorFrame(wire.CodeInternal, "the build produced no output"),
		doneFrame(wire.StatusFailed),
	))
	if err != nil {
		t.Fatalf("ReadDeployStream: %v", err)
	}

	if !report.Reported || report.Status != wire.StatusFailed {
		t.Errorf("reported=%v status=%q, want the terminal event's own status (%q) — "+
			"a diagnostic does not end a stream",
			report.Reported, report.Status, wire.StatusFailed)
	}
	want := wire.Error{Code: wire.CodeInternal, Message: "the build produced no output"}
	if len(report.Errors) != 1 || report.Errors[0] != want {
		t.Errorf("diagnostics = %+v, want exactly %+v", report.Errors, want)
	}
}

// TestTheReportKeepsTheEndOfALongBuildLog.
//
// A bounded tail is easy to write backwards, and both mistakes look the
// same from a green suite that only counts: keeping the FIRST lines
// buys a report full of a package manager's inventory with whatever went
// wrong dropped off the end, which is the one thing a caller is reading
// it for.
//
// THE FIXTURE IS ONE LINE PAST THE BOUND, so the row measures the cut
// rather than a comfortable middle — and every line is numbered, so
// "which end survived" is an assertion rather than a count.
//
// REQUIRED MUTATION, run 2026-09-11: keep the head instead — return
// lines[:recentStreamLines]. Reds here naming line 0 against line 1.
func TestTheReportKeepsTheEndOfALongBuildLog(t *testing.T) {
	frames := make([]string, 0, recentStreamLines+1)
	for i := 0; i <= recentStreamLines; i++ {
		frames = append(frames, logFrame(fmt.Sprintf("line %d", i)))
	}
	frames = append(frames, doneFrame(wire.StatusBuilt))

	report, err := ReadDeployStream(t.Context(), framesFrom(frames...))
	if err != nil {
		t.Fatalf("ReadDeployStream: %v", err)
	}

	if len(report.Log) != recentStreamLines {
		t.Fatalf("the report carried %d lines, want the bound (%d)",
			len(report.Log), recentStreamLines)
	}
	if got, want := report.Log[0], "line 1"; got != want {
		t.Errorf("the report starts at %q, want %q — the OLDEST line is the one "+
			"a full tail drops", got, want)
	}
	if got, want := report.Log[len(report.Log)-1], fmt.Sprintf("line %d", recentStreamLines); got != want {
		t.Errorf("the report ends at %q, want %q", got, want)
	}
}

// TestTheDiagnosticsAreBoundedLikeTheLogIs.
//
// A report is read by something that holds all of it at once, and a
// server message is server-sized — so the bound on the output is not a
// tidiness and neither is the one beside it. THE COLLECTION THAT WAS
// LEFT OFF IS THE HAZARD: a stream that emits diagnostics and never
// reaches its terminal event is read to exhaustion by design, so an
// unbounded slice there is this process growing until it dies, in a
// server that has already answered every other call it will get.
//
// THE FIXTURE NEVER FINISHES, which is what makes it the dangerous shape
// rather than a long successful build. It also keeps this row honest
// about which cap it is measuring: the log is empty throughout.
//
// REQUIRED MUTATION, run 2026-09-11: drop the bound from
// appendRecentError. Reds here with one diagnostic per frame and nothing
// dropped — the state this row was written after finding, because the
// bound shipped with no row and a mutation of it came back green.
func TestTheDiagnosticsAreBoundedLikeTheLogIs(t *testing.T) {
	frames := make([]string, 0, recentStreamLines+1)
	for i := 0; i <= recentStreamLines; i++ {
		frames = append(frames, errorFrame(wire.CodeInternal, fmt.Sprintf("fault %d", i)))
	}

	report, err := ReadDeployStream(t.Context(), framesFrom(frames...))
	if err != nil {
		t.Fatalf("ReadDeployStream: %v", err)
	}

	if len(report.Errors) != recentStreamLines {
		t.Fatalf("the report carried %d diagnostics, want the bound (%d)",
			len(report.Errors), recentStreamLines)
	}
	// THE END SURVIVES, like the log's does. The last thing a failing
	// build said is what a reader is looking for, and a bound that kept
	// the beginning would drop it on exactly the streams that have most
	// to say.
	if got, want := report.Errors[0].Message, "fault 1"; got != want {
		t.Errorf("the diagnostics start at %q, want %q — the OLDEST is the one a full "+
			"tail drops", got, want)
	}
	if got, want := report.Errors[len(report.Errors)-1].Message,
		fmt.Sprintf("fault %d", recentStreamLines); got != want {
		t.Errorf("the diagnostics end at %q, want %q", got, want)
	}
}

// TestAStreamThatWouldNotOpenIsAnErrorRatherThanAReport is the boundary
// this call draws, and it is a row because both sides of it are easy to
// get wrong in opposite directions.
//
// A stream that could not be opened has told the caller nothing — there
// is no phase, no output, and no honest "not yet reported" to make,
// because nothing was ever read. Reporting it as a report would say the
// deploy is mid-build when what actually happened is that the server
// refused. Once bytes ARE arriving the rule inverts, which is the row
// above.
func TestAStreamThatWouldNotOpenIsAnErrorRatherThanAReport(t *testing.T) {
	refused := errors.New("the server would not send the build log")
	_, err := ReadDeployStream(t.Context(), StreamReportDeps{
		Events: func(context.Context) (io.ReadCloser, error) { return nil, refused },
	})
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the reason the connection could not be opened", err)
	}
}

// TestAnEventTypeThisBuildPredatesDoesNotEndTheReading pins the additive
// rule for the event vocabulary on this reader as well as on the
// renderer.
//
// A server may add an event type at any time, and a client built before
// it existed has to skip it and keep reading. The failure this forbids
// is quiet: a reader that stopped would report "not yet" for a build
// that had finished two events later, which is indistinguishable from a
// build that is still running.
func TestAnEventTypeThisBuildPredatesDoesNotEndTheReading(t *testing.T) {
	report, err := ReadDeployStream(t.Context(), framesFrom(
		rawFrame("heartbeat", `{"beats":1}`),
		logFrame("still here"),
		doneFrame(wire.StatusBuilt),
	))
	if err != nil {
		t.Fatalf("ReadDeployStream: %v", err)
	}
	if !report.Reported || report.Status != wire.StatusBuilt {
		t.Errorf("reported=%v status=%q, want the reading to have carried on past an "+
			"event type this build does not know", report.Reported, report.Status)
	}
	assertLog(t, report.Log, []string{"still here"})
}

// heldOpen is a connection that delivers its frames and then GOES SILENT
// WITHOUT CLOSING, which is the condition a stall window exists for and
// the one an end-of-input fixture cannot produce.
//
// IT ABORTS ON ITS REQUEST'S CONTEXT, because that is what the real body
// does: cancelling the request that produced it makes the next read
// fail. A fake that ignored the context would leave the read blocked for
// ever and the row would hang rather than fail — measuring the harness
// instead of the watchdog, and reporting it as a suite that never
// finished.
type heldOpen struct {
	ctx       context.Context
	remaining string
	release   <-chan struct{}
}

func (h *heldOpen) Read(p []byte) (int, error) {
	if h.remaining != "" {
		n := copy(p, h.remaining)
		h.remaining = h.remaining[n:]
		return n, nil
	}
	select {
	case <-h.ctx.Done():
		return 0, h.ctx.Err()
	case <-h.release:
		return 0, io.EOF
	}
}

func (h *heldOpen) Close() error { return nil }

// TestASilentStreamEndsTheReadingRatherThanWaiting.
//
// # The failure is not a slow call, it is a dead server
//
// This reading has no deadline of its own — a total one cannot express
// "is this making progress", and a build takes as long as it takes — so
// the only thing that ends a connection which has stopped talking is a
// silence window. WITHOUT ONE THE CALL NEVER RETURNS, and the host it
// runs in dispatches one call at a time on one goroutine: the listing,
// the ping and every other tool go with it, and the client cannot cancel
// because a cancellation is a message that server is no longer reading.
// A relay that dies without closing its socket produces exactly this.
//
// THE FIXTURE DELIVERS AND THEN HOLDS, so the row is about a silence
// rather than an end of input — the four rows above all end their
// stream, and every one of them would pass against a reader with no
// window at all.
//
// THE WINDOW IS CARRIED FROM THE BUILD LOG'S OWN QUIET ROW, with its
// reason: the same quantity, at the same site, through the same
// instrument — the interval from the watchdog being armed, before the
// connection is opened, to the first byte arriving — against the same
// server's keep-alive. It is not a number picked here because it looked
// small enough.
//
// REQUIRED MUTATION, run 2026-09-11: delete the watchdog from
// ReadDeployStream. This row stops returning: it fails on its own
// deadline rather than on an assertion, which is the shape a wedge has
// and the reason the deadline is here rather than a bare call.
func TestASilentStreamEndsTheReadingRatherThanWaiting(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	window := timing.StreamGoesQuiet.Window
	done := make(chan StreamReport, 1)
	go func() {
		report, err := ReadDeployStream(t.Context(), StreamReportDeps{
			StallTimeout: window,
			Events: func(ctx context.Context) (io.ReadCloser, error) {
				return &heldOpen{
					ctx: ctx,
					remaining: phaseFrame(wire.PhaseInstalling) +
						logFrame("added 41 packages"),
					release: release,
				}, nil
			},
		})
		if err != nil {
			t.Errorf("ReadDeployStream: %v", err)
		}
		done <- report
	}()

	// GENEROUSLY PAST THE WINDOW, because what is being measured is
	// whether the reading ends AT ALL rather than exactly when. A row
	// that failed on a slow machine would be reporting the runner as a
	// wedge, which is the one thing this must not do.
	select {
	case report := <-done:
		if report.Reported {
			t.Error("a stream that only went silent reported a status")
		}
		if report.Phase != wire.PhaseInstalling {
			t.Errorf("phase = %q, want the last one the stream named before it went "+
				"quiet (%q)", report.Phase, wire.PhaseInstalling)
		}
		assertLog(t, report.Log, []string{"added 41 packages"})
	case <-time.After(50 * window):
		t.Fatalf("the reading did not end %s after the stream went silent. "+
			"Nothing bounds this call but the silence window, and the server that "+
			"makes it dispatches one call at a time — so a reading that never "+
			"returns takes every later call with it.", 50*window)
	}
}

// assertLog compares a report's tail with what the stream sent, position
// by position.
func assertLog(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the report carried %d lines, want %d:\ngot  %q\nwant %q",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
