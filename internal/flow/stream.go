package flow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// # The numbers this file chooses, and what each of them bounds HERE
//
// Every constant below is chosen at this site, for what it bounds at this
// site. None is carried from anywhere else in this package because it
// looked similar: a constant carries its NUMBER and not the reason the
// number was picked, and the JSON client's own per-request deadline is
// the standing example — right for a small request and a small answer,
// impossible for a connection held open across a whole build.
const (
	// streamKeepAliveInterval is how often the stream sends a comment
	// frame when it has nothing else to say. It is the server's pace
	// rather than this client's choice, which is exactly why the window
	// below is expressed as a multiple of it: a number chosen
	// independently would drift away from the thing it is watching for.
	//
	// IT IS THE ONE NUMBER HERE THIS CLIENT CANNOT CHECK. Nothing in this
	// process can observe the far end's interval, so a server that slowed
	// its keep-alive past the window below would have this client
	// reconnecting on a healthy connection — visibly, in the copy, and
	// costing a replay rather than a deploy, which is why the multiple is
	// generous and why the failure is loud rather than silent. If that
	// interval ever moves, this constant has to move with it; there is no
	// mechanism here that would notice.
	streamKeepAliveInterval = 15 * time.Second

	// streamKeepAlivesMissed is how many of those may go missing before
	// this client decides the connection has stopped talking. THREE, and
	// the asymmetry is the argument: one missed frame is ordinary jitter,
	// two is a pattern, three is a connection that is not coming back.
	// Being wrong in this direction costs one reconnection, which replays
	// from the start and shows the user nothing twice; being wrong in the
	// other direction is a client that waits for ever on a dead socket.
	streamKeepAlivesMissed = 3

	// streamStallWindow bounds a SILENCE, not a build and not a request.
	// No amount of time is too long provided bytes keep moving, and
	// nothing here bounds the whole: a build can legitimately take as
	// long as it takes, and a total deadline cannot tell a long build
	// from a dead connection.
	streamStallWindow = streamKeepAlivesMissed * streamKeepAliveInterval

	// streamReconnectAttempts is how many times a stream that ended
	// abnormally is picked up again before the run gives up and says so.
	// FIVE is enough to ride out a network changing underneath a laptop
	// and few enough that a build log that is never coming back stops
	// rather than hangs. Every reconnection replays from the beginning,
	// so the cost of one is a little bandwidth and no repeated output.
	streamReconnectAttempts = 5

	// streamReconnectStep is the linear step of the delay schedule: the
	// nth reconnection waits n steps, capped below. Half a second because
	// somebody is watching this happen, and a schedule that doubles would
	// spend the last attempt making them wait rather than making the
	// attempt more likely to work.
	streamReconnectStep = 500 * time.Millisecond

	// streamReconnectCap bounds one wait between attempts. With the step
	// above the whole schedule is 0.5s, 1s, 1.5s, 2s, 2s — seven seconds
	// of waiting at worst, stated here rather than left to be worked out
	// from a formula.
	streamReconnectCap = 2 * time.Second
)

// The copy this step owns, held as named values so a row asserting one of
// them cannot drift into asserting a second copy of it — and so the value
// each line carries is the only thing that varies between them.
const (
	// startNarration reports what the server said the deploy is doing.
	// The value INFORMS and never BRANCHES: the next action is identical
	// whatever it says, so this line renders it and nothing switches on
	// it.
	startNarration = "The server says this deploy is "

	// phaseNarration reports what the build is doing now. An unknown
	// phase renders like any other — a display that errored on a value
	// the server added last week would fail in the way that looks like
	// nothing at all.
	phaseNarration = "Build phase: "

	// finishedNarration reports the status the stream ended with. It is
	// the BUILDER's claim rather than the settled truth: output
	// validation runs afterwards and can still refuse what the build
	// produced, so this client renders what it was told and hands the
	// question forward rather than treating the stream as an authority.
	finishedNarration = "The build finished: "

	// streamOpening is the line before the first byte of build output, so
	// a quiet build does not look like a hung command.
	streamOpening = "Streaming the build log…"

	// streamDropped and streamWentQuiet are the two ways a build log
	// stops, and they are two sentences because they are two different
	// things to be told. One is a connection that went away; the other is
	// a connection that is still up and has said nothing for longer than
	// this client is willing to wait. A reader debugging a flaky network
	// and a reader debugging a wedged relay need to be able to tell which
	// they are looking at.
	streamDropped   = "Lost the build log"
	streamWentQuiet = "The build log went quiet"

	// reconnectNarration closes both of them, because a build log that
	// silently starts again from an earlier point is worse than one that
	// says what happened.
	reconnectNarration = "; picking it up again"

	// errorNarration marks a diagnostic the server sent on the stream. It
	// EXPLAINS and never ends: a failed build still finishes with a
	// terminal event, so the ending condition is that event and never
	// this one.
	errorNarration = "error: "

	// buildMayBeRunning is the honest half of a start that never
	// answered. A failure after the request left this process is
	// indistinguishable from one before it.
	buildMayBeRunning = "The build may be running anyway"
)

// errStreamStalled is the cause the watchdog cancels with, so the branch
// that ends a connection knows WHY it ended rather than guessing from the
// shape of net/http's error.
var errStreamStalled = errors.New("the build log stopped sending")

// streamRenderer is the slice of the terminal this step needs, declared
// HERE by the consumer like every other seam in this package.
//
// IT IS TWO METHODS BECAUSE THE STREAM SPLIT IS TWO STREAMS. The build's
// own output is the user's — it goes to stdout, so a redirected stdout
// collects the build log and nothing else — and everything this client
// says ABOUT it is narration and goes to stderr.
type streamRenderer interface {
	Step(format string, args ...any)
	Result(format string, args ...any)
}

// streamDeps is everything the stream needs from outside itself.
type streamDeps struct {
	// Events opens a fresh connection to the build log. It is a function
	// rather than a client because RECONNECTING IS THIS STEP'S JOB: it is
	// the only place that knows how much has already been shown, and a
	// retry anywhere below it would replay the whole build into somebody's
	// terminal.
	Events func(ctx context.Context) (io.ReadCloser, error)

	// Render is the terminal, both halves of it.
	Render streamRenderer

	// DeployID is named in the copy when the log cannot be recovered, so
	// that somebody has something to ask about.
	DeployID string

	// StallTimeout is how long this step waits for the next byte before
	// deciding the connection has stopped talking. Zero means
	// streamStallWindow, which is what production passes; it is injected
	// for the reason the clock is, so a row can prove both halves of the
	// liveness rule in milliseconds instead of in minutes.
	StallTimeout time.Duration

	// ReconnectStep is the step of the delay schedule, injected for the
	// same reason. It changes the SPACING and never the shape: the
	// attempt count and the linear schedule are the shipped ones.
	ReconnectStep time.Duration
}

// streamBuild reads the build log to its end, rendering as it goes, and
// returns the status the stream finished with.
//
// # Reconnection replays, because there is no cursor
//
// The stream carries no resume point, so a reconnection re-sends
// everything from the beginning. This client therefore renders from the
// count of what it has already shown — and WHAT IT COUNTS is the whole of
// the rule.
//
// IT COUNTS THE EVENTS THE SERVER PERSISTS: build output and diagnostics.
// Progress labels are rendered and never counted. There are two replay
// shapes and they do not carry the same events: while the history is
// still held, a replay is typed, as it was; once it has been read back
// from storage instead, every line arrives as build output, because what
// is stored is the text of output and diagnostics and nothing else. A
// counter over output alone is wrong in the first shape — it skips one
// too few and prints a line twice — and a counter over every event is
// wrong in the second, because it counts progress labels the replay does
// not contain and skips past real output. Counting exactly what is
// persisted is right in both, because it is the same set the replay
// reconstructs.
//
// THAT IS A COUPLING TO THE SERVER, and naming it is the point: if what
// the far end writes down ever widens, this de-duplication breaks and
// nothing here can detect it. So the rows that cover this assert the
// OBSERVABLE property — each line reaching the terminal exactly once —
// rather than the mechanism, because a mechanism row would stay green
// against a server that had changed underneath it.
//
// A stream that ends without its terminating event ended ABNORMALLY — a
// dropped connection, a relay that went away, a subscriber the server
// gave up on — and is reconnected rather than reported as a result.
func streamBuild(ctx context.Context, deps streamDeps) (wire.DeployStatus, error) {
	deps.Render.Step("%s", streamOpening)

	shown := 0
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			deps.Render.Step("%s%s (attempt %d of %d)…",
				reconnectReason(lastErr), reconnectNarration,
				attempt, streamReconnectAttempts)
			select {
			case <-time.After(streamReconnectDelay(attempt, deps.ReconnectStep)):
			case <-ctx.Done():
				return "", streamLostFailure(deps.DeployID)
			}
		}

		status, finished, err := readStream(ctx, deps, &shown)
		if finished {
			return status, nil
		}
		lastErr = err

		// A REFUSAL THE SERVER ANSWERED IS NOT A DROPPED CONNECTION.
		// It arrived, it was decoded, and it will say the same thing next
		// time — so it stops the run rather than spending the budget a
		// broken connection needs.
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			return "", streamRefusedFailure(apiErr)
		}
		if ctx.Err() != nil {
			return "", streamLostFailure(deps.DeployID)
		}
		if attempt >= streamReconnectAttempts {
			return "", streamLostFailure(deps.DeployID)
		}
	}
}

// reconnectReason names WHICH of the two endings this was. The stall
// watchdog cancels with its own cause precisely so this question has an
// answer: without it the two are one indistinguishable "context
// canceled", and the client would have to describe a live connection as a
// lost one.
func reconnectReason(err error) string {
	if errors.Is(err, errStreamStalled) {
		return streamWentQuiet
	}
	return streamDropped
}

// streamReconnectDelay is the schedule, stated rather than derived: the
// nth attempt waits n steps, up to the cap.
func streamReconnectDelay(attempt int, step time.Duration) time.Duration {
	if step <= 0 {
		step = streamReconnectStep
	}
	delay := time.Duration(attempt) * step
	if delay > streamReconnectCap {
		return streamReconnectCap
	}
	return delay
}

// readStream reads ONE connection to its end.
//
// finished reports that the stream's terminating event arrived, which is
// the only ending that is a result. Every other ending — an error, an
// end of input, a connection that stopped talking — is a connection to be
// picked up again, and err says which so the caller can tell a refusal
// apart from a broken pipe.
func readStream(ctx context.Context, deps streamDeps, shown *int) (status wire.DeployStatus, finished bool, err error) {
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	stall := deps.StallTimeout
	if stall <= 0 {
		stall = streamStallWindow
	}
	// The watchdog is armed BEFORE the connection is opened, so a server
	// that accepts a connection and then never answers is covered by the
	// same rule as one that goes quiet halfway through. Every byte that
	// arrives pushes it back; a Reset that races a firing timer is
	// harmless, because by then the request is already cancelled and
	// nothing reads the timer again.
	watchdog := time.AfterFunc(stall, func() { cancel(errStreamStalled) })
	defer watchdog.Stop()

	body, err := deps.Events(reqCtx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = body.Close() }()

	// A bufio.Reader RATHER THAN THE STANDARD LINE SCANNER, and no size
	// is chosen here at all — which is the point. The scanner's default
	// gives up on a line past 64 KiB by reporting no more input, which is
	// byte-identical to a clean end of stream; combined with this step's
	// own rule that a stream ending without its terminating event is
	// reconnected, that is an infinite loop over a build that has already
	// finished. ReadString accumulates across fills, so a line of any
	// length arrives whole and this reader has no bound to choose.
	//
	// If a memory bound against a looping producer is ever genuinely
	// needed it must be EXPLICIT, must say what it bounds where it is
	// written, and must deliver the over-long line with a visible marker
	// — never drop it, and never end the stream.
	reader := bufio.NewReader(body)

	seen := 0
	var eventName string
	var data []byte
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			watchdog.Reset(stall)
		}
		if readErr != nil {
			// A PARTIAL FINAL LINE CANNOT COMPLETE AN EVENT — dispatch
			// needs the blank line that follows it — so there is nothing
			// to salvage, and the ending is reported with its cause so
			// the caller can tell a stall from an ordinary end of input.
			if cause := context.Cause(reqCtx); cause != nil && !errors.Is(cause, context.Canceled) {
				return "", false, cause
			}
			return "", false, readErr
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		switch {
		case line == "":
			if eventName == "" && len(data) == 0 {
				continue
			}
			status, terminal := renderEvent(deps.Render, eventName,
				strings.TrimSuffix(string(data), "\n"), &seen, shown)
			eventName, data = "", nil
			if terminal {
				return status, true, nil
			}
			continue

		case strings.HasPrefix(line, ":"):
			// A COMMENT FRAME: the keep-alive. It is consumed as evidence
			// the connection is alive — the watchdog was pushed back
			// above, which is the whole of its job — and it is never
			// rendered.
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventName = value
		case "data":
			// Repeated data lines join with a newline, which is what the
			// event format says and what a payload spanning lines needs.
			data = append(append(data, value...), '\n')
		}
		// Every other field — an id, a retry hint — is ignored. There is
		// no resume point to keep, which is why reconnection replays.
	}
}

// renderEvent renders one dispatched event and reports whether it ended
// the stream.
//
// seen counts the persisted events THIS connection has delivered and
// shown counts the ones already put in front of the reader, so an event
// whose number is not past what has been shown is a replay of something
// the reader has seen and is skipped.
func renderEvent(render streamRenderer, name, data string, seen, shown *int) (wire.DeployStatus, bool) {
	switch wire.EventType(name) {
	case wire.EventLog:
		// COUNTED BEFORE IT IS DECODED, and the order is load bearing. The
		// count means "how many persisted events has this connection
		// delivered", and the server persisted this one whether or not
		// this client could read it. Counting only what decodes leaves the
		// tally one short, and the next reconnection then re-renders a
		// line the reader has already seen — the exact failure the tally
		// exists to prevent, reached by being careful about the wrong
		// thing.
		//
		// Nothing is lost by skipping a payload that will not decode:
		// shown is ASSIGNED from seen rather than incremented, so the next
		// event that does render absorbs it.
		*seen++
		if *seen <= *shown {
			return "", false
		}
		var ev wire.LogEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return "", false
		}
		*shown = *seen
		// THE BUILD'S OWN OUTPUT, ESCAPED. Sanitize is what stands
		// between a project that prints an escape sequence into its own
		// build log and the reader's terminal, scrollback and pasted bug
		// report. The line is passed as an ARGUMENT and never as the
		// format, so a percent sign in somebody's output stays a percent
		// sign.
		render.Result("%s", ui.Sanitize(ev.Line))
		return "", false

	case wire.EventError:
		// Counted before it is decoded, for the reason above: it is
		// persisted too, and the tally is about what the replay will
		// contain rather than about what this client managed to read.
		*seen++
		if *seen <= *shown {
			return "", false
		}
		var ev wire.Error
		if json.Unmarshal([]byte(data), &ev) != nil {
			return "", false
		}
		*shown = *seen
		// IT GOES TO STDOUT WITH THE REST OF THE LOG, because that is
		// what it is: the server writes it into the same log the build
		// output goes into, and a reader collecting stdout would
		// otherwise be missing the line that explains the rest.
		render.Result("%s", ui.Sanitize(errorLine(ev)))
		return "", false

	case wire.EventPhase:
		var ev wire.PhaseEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return "", false
		}
		// NOT COUNTED, because it is not persisted — see streamBuild. It
		// is suppressed while a replay is still catching up so a
		// reconnection does not narrate the build's history a second
		// time.
		if *seen < *shown {
			return "", false
		}
		render.Step("%s%s.", phaseNarration, ui.Sanitize(string(ev.Phase)))
		return "", false

	case wire.EventDone:
		var ev wire.DoneEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return "", false
		}
		render.Step("%s%s.", finishedNarration, ui.Sanitize(string(ev.Status)))
		return ev.Status, true
	}

	// AN EVENT TYPE THIS BUILD PREDATES IS SKIPPED, and the stream keeps
	// reading. That is what makes the vocabulary additive: a client built
	// before a type existed must ignore it rather than treat the stream
	// as corrupt.
	return "", false
}

// errorLine is how a diagnostic reads on the log. The code goes beside
// the message because it is the half a bug report can be searched for,
// and the message is the half a person can act on.
func errorLine(ev wire.Error) string {
	if ev.Code == "" {
		return errorNarration + ev.Message
	}
	return errorNarration + ev.Message + " (" + string(ev.Code) + ")"
}

// startFailure is what a start that did not succeed ends the run as.
//
// THE START IS NEVER RETRIED, and this function is where that shows: it
// has one job, which is to say what happened. The endpoint is honest
// about a repeat — a start on a deploy already building answers with that
// status rather than an error — so a retry would not be wrong, and it
// would still be the wrong instinct, because after this call everything
// the user is waiting for arrives on the build log. A client that answers
// silence by starting again is asking the one surface with nothing left
// to tell it.
func startFailure(err error) error {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return ui.NewFailure(
			"curious didn't hear back after asking the server to build.",
			err.Error()+"\n\n"+buildMayBeRunning+" — curious cannot tell whether the\n"+
				"request arrived, and it does not ask twice: everything you would be\n"+
				"waiting for arrives on the build log rather than in a second answer\n"+
				"to this question.",
			"Run `curious deploy` again when the connection is back.")
	}

	if apiErr.Code == wire.CodeMaintenance {
		// The kill switch. The service declined; the project is fine, and
		// the archive is already where it was going.
		return ui.ServerClosed(ui.NewFailure(
			"curious.pub is not building right now.",
			apiErr.Message,
			"Try again a little later."))
	}

	// EVERY OTHER CODE ENDS THE RUN THE SAME WAY, so there is no routing
	// table here: one would have a single column. What the reader needs
	// is what the server said, and the contract is additive-only, so this
	// build may be older than the code it is being shown.
	return ui.NewFailure(
		"The server wouldn't start the build.",
		apiErr.Message,
		"Run `curious deploy` again. If it keeps happening, updating curious may\n"+
			"help — this build may be older than the server.")
}

// streamRefusedFailure is what a stream the server REFUSED ends the run
// as. It is deliberately not a reconnection: the answer arrived, it was
// decoded, and it will say the same thing next time.
func streamRefusedFailure(apiErr *api.APIError) error {
	if apiErr.Code == wire.CodeMaintenance {
		return ui.ServerClosed(ui.NewFailure(
			"curious.pub stopped sending the build log.",
			apiErr.Message,
			"Try again a little later."))
	}
	return ui.NewFailure(
		"The server wouldn't send the build log.",
		apiErr.Message,
		"Run `curious deploy` again. If it keeps happening, please report it.")
}

// streamLostFailure is what a build log that could not be picked up again
// ends the run as.
//
// IT NAMES THE DEPLOY, because that is the one thing a person can ask
// about afterwards, and it says how many times this client tried, because
// a bounded thing that stops should say it was bounded rather than look
// like it gave up at random.
func streamLostFailure(deployID string) error {
	return ui.NewFailure(
		"curious lost the build log and could not pick it up again.",
		fmt.Sprintf("The connection to the build log dropped, and %d attempts to "+
			"re-establish\nit did not last either. THE BUILD ITSELF IS NOT AFFECTED "+
			"— it is running on\nthe server, and this was only the window onto it.\n\n"+
			"The deploy is %s.", streamReconnectAttempts, ui.Sanitize(deployID)),
		"Run `curious deploy` again when the connection is steadier.")
}

// buildFailedFailure is what a build the server says failed ends the run
// as. The log the user has just watched IS the explanation, so this says
// where to look rather than inventing a reason of its own.
func buildFailedFailure() error {
	return ui.NewFailure(
		"The build failed.",
		"The server ran the build and it did not finish. What went wrong is in\n"+
			"the log above rather than here, and it is a problem in the project\n"+
			"rather than in curious or the service.",
		"Fix what the log reports, then run `curious deploy` again.")
}
