package flow

import (
	"bufio"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/timing"
	"github.com/curiouspub/cli/pkg/wire"
)

// The probes: what the five stall windows are margins OVER.
// ---------------------------------------------------------------------
//
// A stall window bounds the gap between two PROGRESS EVENTS at this
// client, so the quantity to measure is that gap — not the pause the
// fixture asks for, which is the number a test author can see and the
// one the row does not depend on. The rows these probes stand behind
// each had a window chosen against the visible quantity, and the one
// that was sized that way at twelve times the fixture's pace failed
// about one run in six.
//
// # Two mechanisms, two methods, and they do not collapse
//
// THE WRITE SIDE — the archive upload. The client is pushing a body; the
// gap between two progress events is the time for the kernel's send
// buffer to free space, which is set by how fast the far end reads. It
// has a BLOCK POINT: the bytes handed over before the client stops
// making progress at all. A fixture smaller than that block point is
// swallowed whole and the row measures nothing, so the block point is
// itself a measurement rather than a detail — it is what says the
// fixture is big enough to exercise the mechanism it names. Instrument:
// progressReader.
//
// THE READ SIDE — the build log stream. The client is consuming; the gap
// is how long the far end waits before writing more, plus what the
// scheduler and the loopback stack add. NOTHING buffers on this client's
// behalf, so there is no block point, that number is not asked for here,
// and one recorded would be an invention. Instrument: streamProgress.
//
// The interval a read-side probe reports starts at the moment the
// watchdog is ARMED, which in the shipped code is before the connection
// is opened — so establishment is inside the first gap, exactly as the
// client experiences it.
//
// # Why these run in the ordinary suite
//
// Two of the three legs this project gates on can only be reached by a
// run on those runners, and the gate runs one command. So the probes are
// ordinary rows: a run on any leg reports that leg's number, and when
// the leg has no entry yet the probe FAILS carrying the number, because
// a red that hands the operator the datum is worth more than a red that
// sends them back to run something else.
//
// # What a probe refuses on, and what it only reports
//
// It refuses when the gap it measured has reached the WINDOW: on this
// machine, right now, the row it stands behind could not have passed,
// and that is a fact about this run rather than a distribution.
//
// It REPORTS, and does not refuse, when the gap merely exceeds the
// recorded worst. A record is a distribution taken over many runs;
// turning one unlucky sample into a red is "raise the number until the
// failures stop" wearing the other face, and it makes a suite red
// without producing any evidence. The five-times sizing rule is enforced
// against the RECORD, deterministically, beside the registry.

// probeRuns is how many consecutive runs stand behind a probe's number.
// Twenty, because one run is an outcome and a margin is a distribution:
// the window that failed one run in six passed the run that chose it.
const probeRuns = 20

// probeLeg is the leg this machine is, named the way the registry names
// it.
func probeLeg() timing.Leg {
	return timing.Leg(runtime.GOOS)
}

// report is what every probe does with its number, and it is one
// function so that the five rows cannot drift into saying different
// things about the same situation.
//
// measured is the worst gap this run saw; pin is the socket-buffer pair
// it was measured under, or nil on the read side, where no buffer this
// client can set governs the gap; extra is whatever else the probe has
// to say, printed with it.
func report(t *testing.T, entry *timing.Entry, measured time.Duration, pin *timing.PinnedPair, extra string) {
	t.Helper()
	leg := probeLeg()
	recorded, known := entry.Measurements[leg], false
	known = recorded.Measured()

	t.Logf("%s on %s%s: worst gap %v over %d runs, against a %v window%s%s",
		entry.Name, leg, detectorNote(), measured, probeRuns, entry.Window,
		pinNote(pin), extra)

	if measured >= entry.Window {
		t.Errorf("%s measured a worst gap of %v on %s, at or past its own %v "+
			"window. The row this window stands behind could not have passed on "+
			"this machine in this run — this is not a margin that needs widening, "+
			"it is a measurement saying the environment and the window disagree.",
			entry.Name, measured, leg, entry.Window)
	}

	if !known {
		t.Errorf("%s has no recorded measurement on %s, and this run measured a "+
			"worst gap of %v over %d runs on %s%s.\nRecord it in internal/timing: "+
			"{WorstGap: %v, Runs: %d, Date: \"%s\"%s%s} — or run this again and "+
			"record the worst across the passes, with the run count to match, "+
			"which is how the darwin entry was taken.\nTWO THINGS THIS LINE ALONE "+
			"WILL NOT TELL YOU. make ci runs this package twice, plainly and under "+
			"the race detector, and the detector widens the gap — threefold on "+
			"darwin — so the number to keep is the WORSE of the two conditions and "+
			"this run was %s. And nothing here may be seeded from another leg's "+
			"number: the buffers and the scheduler belong to the kernel and the "+
			"runner.",
			entry.Name, leg, measured, probeRuns, time.Now().Format("2006-01-02"),
			detectorNote(),
			measured.Round(time.Microsecond), probeRuns,
			time.Now().Format("2006-01-02"),
			blockPointHint(entry), pinLiteral(pin), detectorPhrase())
		return
	}

	// A GAP AND THE CONDITION IT WAS TAKEN UNDER ARE ONE FACT. A record
	// whose pin this run could not reproduce is a margin over a quantity
	// this machine does not have, and comparing the two numbers while
	// ignoring that would be the whole defect this round removed.
	confirmPin(t, entry, leg, pin)

	// A TENTH PAST THE RECORD, not a nanosecond past it. The record is a
	// maximum over many runs, so an ordinary run beats it by a hair
	// fairly often; a note that fires on a microsecond is a note nobody
	// reads by the second week. A tenth is past the noise and far inside
	// the five-times margin the rule keeps.
	if measured > recorded.WorstGap+recorded.WorstGap/10 {
		t.Logf("%s: this run's %v is past the recorded worst of %v on %s (taken "+
			"over %d runs on %s). The record is the one that is stale, not this "+
			"run: retake it before trusting the margin.",
			entry.Name, measured, recorded.WorstGap, leg, recorded.Runs, recorded.Date)
	}
}

// detectorNote and detectorPhrase name the condition a run happened
// under, because the record's numbers depend on it and two runs of the
// same probe otherwise print lines that look identical and are not.
func detectorNote() string {
	if raceDetector {
		return " (-race)"
	}
	return ""
}

func detectorPhrase() string {
	if raceDetector {
		return "under it"
	}
	return "without it"
}

// blockPointHint reminds a write-side entry that its record is
// incomplete without one, and says nothing at all on the read side,
// where the number does not exist.
func blockPointHint(entry *timing.Entry) string {
	if entry.Side == timing.Write {
		return ", BlockPoint: <see the block-point probe>"
	}
	return ""
}

// pinNote is the one-line rendering of the condition a gap was measured
// under, for the log line a reader of a run's output sees.
//
// IT PRINTS THE READ-BACK BESIDE THE REQUEST, because on Linux the two
// differ by construction — the kernel stores twice what was asked for
// and hands the doubled number back — and a reader who sees only one of
// them cannot tell that from a clamp.
func pinNote(pin *timing.PinnedPair) string {
	if pin == nil {
		return ""
	}
	end := func(name string, p timing.Pin) string {
		if p.Err != "" {
			return fmt.Sprintf("%s NOT PINNED (%s)", name, p.Err)
		}
		return fmt.Sprintf("%s asked %d read back %d%s", name, p.Requested, p.ReadBack,
			sustainedNote(p.Sustained))
	}
	return fmt.Sprintf(" [%s; %s]", end("send", pin.Send), end("receive", pin.Receive))
}

// sustainedNote says what the buffer actually was while bodies moved,
// and says it in the words that distinguish the three outcomes: nobody
// looked, it held, it did not hold.
//
// A READ-BACK AND A RANGE ARE TWO FACTS AND THE LINE PRINTS BOTH,
// because on darwin they disagree by a factor of thirty and a reader
// seeing only the first would take the record at its word. This is the
// one place a run says so out loud.
func sustainedNote(s *timing.Sustained) string {
	if s == nil {
		return " (never sampled while a body moved)"
	}
	if s.Steady() {
		return fmt.Sprintf(" and HELD there across %d samples", s.Samples)
	}
	return fmt.Sprintf(" but the kernel ran it from %d to %d across %d samples "+
		"while the body moved, so the read-back is not the condition the gap was "+
		"measured under", s.Low, s.High, s.Samples)
}

// pinLiteral is the same fact as Go source, so an operator on a leg with
// no record can paste the whole measurement in rather than transcribe
// four numbers out of a log line.
//
// A RECORD IS PASTED OR IT IS RETYPED, and a retyped number is a number
// with a transcription error waiting in it — which is exactly the kind
// of defect a margin cannot show, because it goes on passing.
func pinLiteral(pin *timing.PinnedPair) string {
	if pin == nil {
		return ""
	}
	end := func(p timing.Pin) string {
		if p.Err != "" {
			return fmt.Sprintf("{Requested: %d, Err: %q}", p.Requested, p.Err)
		}
		sustained := ""
		if s := p.Sustained; s != nil {
			sustained = fmt.Sprintf(", Sustained: &timing.Sustained{Samples: %d, Low: %d, High: %d}",
				s.Samples, s.Low, s.High)
		}
		return fmt.Sprintf("{Requested: %d, ReadBack: %d%s}", p.Requested, p.ReadBack, sustained)
	}
	return fmt.Sprintf(", Pin: &timing.PinnedPair{Send: timing.Pin%s, Receive: timing.Pin%s}",
		end(pin.Send), end(pin.Receive))
}

// -------------------------------------------------------------------
// The write side
// -------------------------------------------------------------------

// probeBody is a file of random bytes large enough that no send buffer
// between this client and a store on loopback swallows it whole. It is
// written once and reopened per run, so every run reads from disk the
// way the real upload does.
func probeBody(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating the probe body: %v", err)
	}
	if _, err := io.CopyN(file, rand.Reader, size); err != nil {
		t.Fatalf("filling the probe body: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("closing the probe body: %v", err)
	}
	return path
}

// TestProbeTheUploadStallGap measures the write side's governing
// quantity: the worst interval between two progress events while the far
// end reads at a paced rate, over a connection whose buffers are PINNED
// at both ends.
//
// THE FIXTURE IS THE ROW'S OWN, down to the derivation. The store
// consumes one chunk at a time with a pause between, which is what makes
// the client's writes wait on buffer space rather than on the network;
// the body comes from pacingFor, the same call the row makes, so the two
// cannot drift apart as the window moves.
//
// THE PIN IS THE ROW'S OWN TOO, and it has to be. A gap measured over an
// autotuned socket is a margin over a number nobody chose: the same
// probe over a warm connection reported 594 ms where a fresh one
// reported 434 ms. Both ends are pinned, because a socket option has an
// end — pin the sender alone and this measures the receiver's
// autotuning.
func TestProbeTheUploadStallGap(t *testing.T) {
	entry := &timing.UploadSlowIsNotStalled
	pacing := pacingFor(t, entry.Window)

	path := probeBody(t, pacing.bodySize)
	store := newObjectStore(t, &deployJournal{}, pinnedBuffer)
	store.readChunk = pacing.chunk
	store.readPause = pacing.pause
	store.pauseUntil = pacing.pacedBytes

	// ONE TRANSPORT ACROSS THE RUNS, AND A CONNECTION PER RUN. The row
	// gets one connection, and the gap being measured is set by how much
	// buffer this connection has — which is why every run closes its
	// idle connection rather than reusing it. The transport is shared
	// only so the send-buffer pin has one record to accumulate into; it
	// dials afresh each time.
	transport, client := pinnedTransport(pinnedBuffer)
	t.Cleanup(transport.CloseIdleConnections)
	client.watch(t, pacedPause)
	store.pin.watch(t, pacedPause)

	var worst time.Duration
	var samples int
	for run := 0; run < probeRuns; run++ {
		gap, n := oneUploadRun(t, store.url, path, pacing.bodySize, transport)
		transport.CloseIdleConnections()
		samples += n
		if gap > worst {
			worst = gap
		}
	}
	if samples == 0 {
		t.Fatal("the probe recorded no gap at all, so its silence is about an " +
			"instrument that stopped working rather than about a fast machine")
	}
	pinsWereApplied(t, client, store.pin)

	report(t, entry, worst, observedPin(client, store.pin),
		fmt.Sprintf(" (%d gaps sampled, store pacing %d KiB every %v over a "+
			"%d-byte body, %d of it paced)",
			samples, pacing.chunk>>10, pacing.pause, pacing.bodySize, pacing.pacedBytes))
}

// oneUploadRun PUTs the body once and returns the worst interval between
// two progress events, and how many intervals it saw.
func oneUploadRun(t *testing.T, url, path string, size int64, transport *http.Transport) (time.Duration, int) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the probe body: %v", err)
	}
	defer func() { _ = file.Close() }()

	var last time.Time
	var worst time.Duration
	gaps := 0
	body := &progressReader{r: file, progress: func(int) {
		now := time.Now()
		if !last.IsZero() {
			if gap := now.Sub(last); gap > worst {
				worst = gap
			}
			gaps++
		}
		last = now
	}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, url, body)
	if err != nil {
		t.Fatalf("building the probe request: %v", err)
	}
	req.ContentLength = size

	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatalf("the probe upload failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("draining the probe response: %v", err)
	}
	return worst, gaps
}

// TestProbeTheUploadBlockPoint measures the other half of the write
// side: the bytes this client hands over before it stops making progress
// at all.
//
// IT IS A MEASUREMENT AND NOT A DETAIL. Every write-side row needs a
// fixture larger than this number, or the client finishes writing before
// the far end has read anything and the row measures nothing — a "slow"
// store it never waited for and a "wedged" one it had already finished
// with. Both would pass. The number belongs in the record so that the
// next person to size a fixture is sizing it against something.
//
// IT IS ALSO THE ROW THAT CAN SAY A LEG IS UNSOUND. A leg that buffers a
// whole body has no block point below the fixture size, and there the
// upload rows prove nothing however green they are — which is a finding
// about the rows rather than a number to tune, and this probe refuses
// rather than reporting a block point it did not observe.
func TestProbeTheUploadBlockPoint(t *testing.T) {
	// How long with no progress at all counts as blocked. It is far past
	// any scheduling hiccup on loopback, and it is a QUANTITY OF ITS OWN
	// rather than a fraction of the stall window: this probe answers
	// "how many bytes fit", and tying its quiescence threshold to the
	// window would make the answer move whenever the window did.
	const quiet = 250 * time.Millisecond

	// THE ROW'S OWN BODY, because the question this probe answers is
	// whether the ROW's fixture is large enough to make the client
	// block. A body of some other size would answer it about some other
	// fixture.
	bodySize := pacingFor(t, timing.UploadSlowIsNotStalled.Window).bodySize

	path := probeBody(t, bodySize)
	store := newObjectStore(t, &deployJournal{}, pinnedBuffer)
	// The store reads a little and then stops, holding the request open.
	// That is what makes the client fill the buffer and stay there.
	store.stopReadingAfter = 64 << 10

	transport, client := pinnedTransport(pinnedBuffer)
	t.Cleanup(transport.CloseIdleConnections)
	client.watch(t, pacedPause)
	store.pin.watch(t, pacedPause)

	var worst int64
	for run := 0; run < probeRuns; run++ {
		at := oneBlockPointRun(t, store.url, path, bodySize, quiet, transport)
		transport.CloseIdleConnections()
		if at > worst {
			worst = at
		}
	}
	// THE BLOCK POINT IS A NUMBER UNDER A CONDITION TOO, so this probe
	// runs under the same pin its gap-measuring sibling does. On darwin
	// the two conditions measured 819,200 bytes pinned against 3,014,656
	// autotuned — a factor of 3.7, and the reason a fixture sized
	// against one of them says nothing about the other.
	pinsWereApplied(t, client, store.pin)
	t.Logf("block point measured under %s", pinNote(observedPin(client, store.pin)))

	if worst >= bodySize {
		t.Fatalf("the client handed over the whole %d-byte body without ever "+
			"blocking on %s. There is no block point below this fixture here, so "+
			"the upload rows on this leg are not measuring a send buffer draining "+
			"— they are measuring a body that fitted. That is a finding about "+
			"those rows and not a number to tune.", int64(bodySize), probeLeg())
	}

	for _, entry := range []*timing.Entry{&timing.UploadSlowIsNotStalled, &timing.UploadWedgedStops} {
		recorded := entry.Measurements[probeLeg()]
		t.Logf("%s on %s: block point %d bytes (%.1f MiB), worst of %d runs",
			entry.Name, probeLeg(), worst, float64(worst)/(1<<20), probeRuns)
		// A MISSING BLOCK POINT REFUSES, exactly as a missing gap does
		// next door, and it did not until this round. It only LOGGED —
		// and a t.Logf on a passing row prints nowhere unless somebody
		// runs the suite verbose, so the paste hint one function over
		// said "BlockPoint: <see the block-point probe>" while pointing
		// an operator at output their gate does not show them. A leg
		// whose gap was recorded from a CI run therefore could not have
		// its block point recorded from the same run, and the registry
		// row that requires one would red with nowhere to get it.
		//
		// A reference in OUTPUT is read mid-procedure by whoever is least
		// able to verify it. This one now resolves.
		if !recorded.Measured() || recorded.BlockPoint <= 0 {
			t.Errorf("%s has no recorded block point on %s, and this run measured "+
				"%d bytes over %d runs.\nRecord it in internal/timing beside that "+
				"leg's gap: BlockPoint: %d.\nWithout it nobody can tell whether the "+
				"fixture was ever large enough to make the client block, and a row "+
				"that never blocked measured nothing at all.",
				entry.Name, probeLeg(), worst, probeRuns, worst)
			continue
		}
		if recorded.BlockPoint != worst {
			t.Logf("%s records a block point of %d and this run measured %d; a "+
				"block point moves with the kernel's buffer autotuning, so the "+
				"record is the worst seen rather than a constant",
				entry.Name, recorded.BlockPoint, worst)
		}
	}
}

// oneBlockPointRun writes until the client has made no progress for
// quiet, and returns how many bytes it had handed over by then.
func oneBlockPointRun(t *testing.T, url, path string, size int64, quiet time.Duration, transport *http.Transport) int64 {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the probe body: %v", err)
	}
	defer func() { _ = file.Close() }()

	var sent atomic.Int64
	var moved atomic.Int64 // a counter of progress events, to spot quiescence
	body := &progressReader{r: file, progress: func(n int) {
		sent.Add(int64(n))
		moved.Add(1)
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		t.Fatalf("building the probe request: %v", err)
	}
	req.ContentLength = size

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := (&http.Client{Transport: transport}).Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	// Poll rather than instrument the reader with a timer: the question
	// is "has anything moved lately", and a counter read from outside
	// answers it without putting a clock inside the instrument.
	last := moved.Load()
	still := time.Duration(0)
	const step = 25 * time.Millisecond
	for still < quiet {
		time.Sleep(step)
		if now := moved.Load(); now != last {
			last, still = now, 0
			continue
		}
		still += step
	}
	at := sent.Load()
	cancel()
	<-done
	return at
}

// -------------------------------------------------------------------
// The read side
// -------------------------------------------------------------------

// TestProbeTheStreamStallGaps measures the read side's governing
// quantity for each of the three stream windows: the worst interval
// between the watchdog being armed, or the previous byte arriving, and
// the next byte arriving.
//
// THE THREE ARE MEASURED SEPARATELY AND NOT CARRIED BETWEEN, even though
// all three are read side through one reader. The interval a row depends
// on includes the pace its own fixture keeps, and the three fixtures
// pace differently — one does not pace at all. A number taken at one
// pace does not bound a row running at a slower one, and carrying it
// would be the very defect this package exists against wearing a
// permitted name.
func TestProbeTheStreamStallGaps(t *testing.T) {
	done := doneFrame(wire.StatusBuilt)

	keepAlive := make([]string, 0, 21)
	for i := 0; i < 20; i++ {
		keepAlive = append(keepAlive, commentFrame())
	}
	keepAlive = append(keepAlive, done)

	// THE ROW'S OWN FIXTURE, down to the derivation, for the reason the
	// write-side probe takes its body from pacingFor: a probe measuring
	// a cheaper shape answers a different question, and the two cannot
	// drift apart if they call the same function.
	partial := append(partialLineFrames(t, timing.StreamPartialLineIsNotAStall.Window), done)

	cases := []struct {
		entry  *timing.Entry
		script eventScript
	}{
		{
			// One frame and then silence, which is what the row sends.
			// With no pace, the only interval there is to measure is the
			// one from arming to the first byte: connection
			// establishment plus delivery.
			entry:  &timing.StreamGoesQuiet,
			script: eventScript{frames: []string{logFrame("LINE-A"), done}},
		},
		{
			entry:  &timing.StreamKeepAlivesAreProofOfLife,
			script: eventScript{frames: keepAlive, pace: 15 * time.Millisecond},
		},
		{
			entry:  &timing.StreamPartialLineIsNotAStall,
			script: eventScript{frames: partial, pace: partialLinePace},
		},
	}

	for _, tc := range cases {
		t.Run(tc.entry.Name, func(t *testing.T) {
			script := &deployScript{
				journal:      &deployJournal{},
				release:      make(chan struct{}),
				eventScripts: []eventScript{tc.script},
			}
			srv := httptest.NewServer(script)
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(script.release) })

			var worst time.Duration
			var samples int
			for run := 0; run < probeRuns; run++ {
				gap, n := oneStreamRun(t, srv.URL)
				samples += n
				if gap > worst {
					worst = gap
				}
			}
			if samples == 0 {
				t.Fatal("the probe recorded no arrival at all, so its silence is " +
					"about an instrument that stopped working")
			}

			// THE FIXTURE'S OWN WIDEST FLUSH GAP, reported beside the
			// client's. It is the number that says whether a red here is
			// about the client or about a machine that paused: a fixture
			// that itself stopped for longer than the window has
			// measured the runner.
			extra := fmt.Sprintf(" (%d arrivals sampled; the fixture's own widest "+
				"gap between flushes was %v)", samples, script.widestGap())
			// NIL PIN, and that is the two mechanisms held apart rather
			// than an omission. Nothing buffers on this client's behalf
			// while it reads: the gap is the far end's pacing plus the
			// scheduler, and no socket buffer this client can set
			// governs it. A pin recorded here would be a condition that
			// had no bearing on the number beside it.
			report(t, tc.entry, worst, nil, extra)
		})
	}
}

// oneStreamRun opens one connection and returns the worst interval
// between the watchdog's arming — here, the instant before the request
// goes out — and each byte arriving, and how many arrivals it saw.
func oneStreamRun(t *testing.T, base string) (time.Duration, int) {
	t.Helper()

	var worst time.Duration
	arrivals := 0
	// ARMED BEFORE THE CONNECTION IS OPENED, which is where the shipped
	// watchdog is armed. Measuring from the first byte instead would
	// leave establishment out of a number the client's own timer
	// includes.
	last := time.Now()
	seen := func() {
		now := time.Now()
		if gap := now.Sub(last); gap > worst {
			worst = gap
		}
		last = now
		arrivals++
	}

	resp, err := http.Get(base + "/v1/deploys/probe/events")
	if err != nil {
		t.Fatalf("opening the probe stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The same shape the client reads with: a bufio.Reader over
	// streamProgress, pulling whole lines.
	reader := bufio.NewReader(&streamProgress{r: resp.Body, seen: seen})
	for {
		_, err := reader.ReadString('\n')
		if err != nil {
			break
		}
	}
	return worst, arrivals
}
