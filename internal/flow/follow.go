package flow

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/curiouspub/cli/pkg/wire"
)

// recentStreamLines is how many lines of build output a report carries.
//
// WHAT IT BOUNDS IS A MESSAGE, not a buffer and not a build. A build log
// runs to thousands of lines — an install alone can — and a report is
// read by something that has to hold all of it at once and decide what
// to do next. The tail is the useful end: whatever went wrong is at the
// bottom of a build log, and the top is the package manager listing
// what it downloaded.
//
// THE NUMBER IS A SCREENFUL rather than a measurement, and saying so is
// better than implying it was derived. Nothing here can know what a
// reader wants; what this has to avoid is the two failures at the ends,
// which are a report that carries one line and explains nothing, and a
// report that carries a whole build and buries it.
const recentStreamLines = 40

// StreamReport is everything ONE READING of a deploy's event stream
// said, as values.
//
// # It reports what the stream SAID, and never more than that
//
// There is no endpoint that answers a deploy's current state: the /v1
// surface has a create, a start, a publish and this stream, and nothing
// that reads a record back. So the only thing in reach is the log, and a
// live tail is not a state read.
//
// Reported is the whole of that distinction. A stream that has not
// reached its terminal event has not stated a status, and a report that
// filled Status in anyway — with the phase it last saw, with "building",
// with anything — would be this client inventing the one fact its caller
// asked for. The honest answer is that nothing has been reported yet,
// and it is a state of its own rather than a placeholder.
//
// WHAT THIS CANNOT TELL YOU, stated because the alternative is a reader
// assuming otherwise: a stream that stopped short because the build is
// still running and one that stopped short because the connection went
// away are the same report. That is not a gap in this type — the two are
// the same event from here, the caller's next action is the same for
// both (ask again), and a field guessing between them would be the
// invention this whole shape exists to refuse.
type StreamReport struct {
	// Reported is whether the stream reached its terminal event. Only
	// then does Status mean anything.
	Reported bool

	// Status is the DeployStatus the terminal event carried. Zero when
	// the stream did not reach one.
	Status wire.DeployStatus

	// Phase is the last phase the stream named, which is as much as it
	// said about where a build that has not finished had got to. Zero
	// when the stream named none.
	Phase wire.Phase

	// Log is the most recent lines of build output, oldest first and
	// bounded by recentStreamLines.
	Log []string

	// Errors are the diagnostics the stream carried, in the order it
	// carried them.
	//
	// THEY ARE NOT AN ENDING. The contract's own grammar says the error
	// event explains and never terminates — a failed build still ends
	// with its terminal event — so these sit beside the status rather
	// than in place of it, and a report can carry both, either, or
	// neither.
	Errors []wire.Error
}

// StreamReportDeps is everything ReadDeployStream needs from outside
// itself.
type StreamReportDeps struct {
	// StallTimeout is how long the reading waits for the next byte
	// before deciding the connection has stopped talking. Zero means the
	// build log's own window, which is what production passes; it is
	// injected for the reason every other window in this package is, so
	// a row can prove both halves of the rule in milliseconds instead of
	// in minutes.
	StallTimeout time.Duration

	// Events opens the connection. It is a function rather than a client
	// for the reason the build log's own is: what a caller wants opened
	// is one deploy's stream, and the id belongs to whoever is asking
	// rather than to this step.
	Events func(ctx context.Context) (io.ReadCloser, error)
}

// ReadDeployStream reads a deploy's event stream to its end and reports
// what it said.
//
// # It is a second READER, not a second parser
//
// The framing, the field names, the blank-line dispatch and the comment
// frames are the protocol's, and they are read by scanEvents, which the
// build log's own renderer uses too. What differs here is only what
// happens to a dispatched event: that one renders as it goes and this
// one collects. Two parsers for one stream is how two halves of a client
// come to disagree about what the server sent.
//
// # It bounds a SILENCE, because nothing else does
//
// The connection this opens carries no deadline of its own — a total one
// cannot express "is this making progress", and a build may legitimately
// take as long as it takes. What it does have is a far end that sends a
// keep-alive on a published interval, so a silence longer than several
// of those is a connection that has stopped talking.
//
// THAT BOUND IS LOAD-BEARING HERE IN A WAY IT IS NOT FOR THE RENDERER,
// and the difference is the host. The build log is read by a command
// that ends when it ends; this is read by a server that dispatches one
// call at a time on one goroutine, so a read that never returns takes
// every later call with it — the listing, the ping, every other tool —
// and the client cannot cancel, because a cancellation is a message that
// server is no longer reading. A relay that dies without closing its
// socket produces exactly that, and it is the condition the window
// exists for.
//
// The window is the build log's own. That is not a constant carried here
// because it looked similar: it is the SAME quantity at the same site —
// a silence on this stream, against that server's keep-alive — so the
// argument that chose the number is the argument that applies.
//
// A STALL ENDS THE READING RATHER THAN FAILING IT, which is the same
// answer every other early ending gets here: what the stream did say is
// reported, and Reported stays false.
//
// # It does not reconnect, and it does not de-duplicate
//
// Both belong to the renderer and neither belongs here. Reconnecting
// exists so a person watching a build does not lose the window onto it;
// this call is one question with one answer, and a caller that wants to
// ask again can ask again. De-duplication exists because a replay would
// print lines somebody has already read — and this reads ONE connection,
// so there is nothing to have read twice.
//
// # A stream that ends early is a RESULT rather than an error
//
// The returned error is for a stream that could not be opened at all: an
// unknown deploy, a refused token, a server that would not answer. Once
// bytes are arriving, every way the reading can stop short — the build
// is still running and the caller gave up waiting, the relay went away,
// the history had been evicted — produces a report with Reported false,
// which is exactly what that field is for. Reporting those as errors
// would throw away the phase and the output the stream did deliver,
// which is the whole of what a caller can act on.
func ReadDeployStream(ctx context.Context, deps StreamReportDeps) (StreamReport, error) {
	// THE WATCHDOG IS ARMED BEFORE THE CONNECTION IS OPENED, so a server
	// that accepts and then never answers is covered by the same rule as
	// one that goes quiet halfway through.
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := deps.StallTimeout
	if stall <= 0 {
		stall = streamStallWindow
	}
	watchdog := time.AfterFunc(stall, cancel)
	defer watchdog.Stop()

	body, err := deps.Events(reqCtx)
	if err != nil {
		return StreamReport{}, err
	}
	defer func() { _ = body.Close() }()

	var report StreamReport
	// THE SCAN'S OWN ENDING IS DELIBERATELY NOT READ, and this is the
	// one place in this package where an ignored error is the right
	// answer. scanEvents stops for exactly two reasons — the terminal
	// event, which sets Reported, or the reader stopping, which does not
	// — and the second is not a failure of this call: see the doc above.
	// EVERY READ THAT MOVED BYTES PUSHES THE WATCHDOG BACK, which is why
	// it is keyed to the connection rather than to the parser on top of
	// it: bytes are the unit that crosses a socket, and a server writing
	// one long line slowly is delivering continuously while a
	// line-counting reader sees nothing.
	_ = scanEvents(bufio.NewReader(&streamProgress{r: body, seen: func() {
		watchdog.Reset(stall)
	}}), func(name, data string) bool {
		switch wire.EventType(name) {
		case wire.EventLog:
			var ev wire.LogEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				return false
			}
			report.Log = appendRecent(report.Log, ev.Line)

		case wire.EventPhase:
			var ev wire.PhaseEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				return false
			}
			report.Phase = ev.Phase

		case wire.EventError:
			var ev wire.Error
			if json.Unmarshal([]byte(data), &ev) != nil {
				return false
			}
			// BOUNDED LIKE THE LOG IS, and for the same reason: a
			// report is read by something that holds all of it at once,
			// and a server message is server-sized. A stream that emits
			// diagnostics and never terminates is read to exhaustion by
			// design, so an unbounded slice here is this process growing
			// until it dies — the cap on the log next door with the one
			// collection beside it left off.
			report.Errors = appendRecentError(report.Errors, ev)

		case wire.EventDone:
			var ev wire.DoneEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				// A terminal event this client cannot read has not told
				// it a status, so it has not ended the stream either.
				// Stopping here would report "not yet" while claiming to
				// have finished reading; carrying on lets a well-formed
				// one arrive, and a stream that sends no other ends with
				// the honest answer.
				return false
			}
			report.Status = ev.Status
			report.Reported = true
			return true
		}
		// AN EVENT TYPE THIS BUILD PREDATES IS SKIPPED and the reading
		// continues, which is the additive rule for the event
		// vocabulary — the same answer the renderer gives.
		return false
	})
	return report, nil
}

// appendRecent adds one line to a tail bounded by recentStreamLines,
// dropping the oldest when it is full.
//
// IT KEEPS THE END RATHER THAN THE BEGINNING, which is the opposite of
// what a cap usually does and is the point of this function existing. A
// build log's beginning is a package manager's inventory; its end is
// what happened.
func appendRecentError(carried []wire.Error, next wire.Error) []wire.Error {
	carried = append(carried, next)
	if len(carried) > recentStreamLines {
		carried = carried[len(carried)-recentStreamLines:]
	}
	return carried
}

func appendRecent(lines []string, line string) []string {
	lines = append(lines, line)
	if len(lines) > recentStreamLines {
		lines = lines[len(lines)-recentStreamLines:]
	}
	return lines
}
