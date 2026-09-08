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
// measured is the worst gap this run saw; extra is whatever else the
// probe has to say, printed with it.
func report(t *testing.T, entry *timing.Entry, measured time.Duration, extra string) {
	t.Helper()
	leg := probeLeg()
	recorded, known := entry.Measurements[leg], false
	known = recorded.Measured()

	t.Logf("%s on %s: worst gap %v over %d runs, against a %v window%s",
		entry.Name, leg, measured, probeRuns, entry.Window, extra)

	if measured >= entry.Window {
		t.Errorf("%s measured a worst gap of %v on %s, at or past its own %v "+
			"window. The row this window stands behind could not have passed on "+
			"this machine in this run — this is not a margin that needs widening, "+
			"it is a measurement saying the environment and the window disagree.",
			entry.Name, measured, leg, entry.Window)
	}

	if !known {
		t.Errorf("%s has no recorded measurement on %s, and this run measured a "+
			"worst gap of %v over %d runs on %s.\nRecord it in internal/timing: "+
			"{WorstGap: %v, Runs: %d, Date: \"%s\"%s} — or run this again and "+
			"record the worst across the passes, with the run count to match, "+
			"which is how the darwin entry was taken. Nothing here may be seeded "+
			"from another leg's number: the buffers and the scheduler belong to "+
			"the kernel and the runner.",
			entry.Name, leg, measured, probeRuns, time.Now().Format("2006-01-02"),
			measured.Round(time.Microsecond), probeRuns,
			time.Now().Format("2006-01-02"),
			blockPointHint(entry))
		return
	}

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

// blockPointHint reminds a write-side entry that its record is
// incomplete without one, and says nothing at all on the read side,
// where the number does not exist.
func blockPointHint(entry *timing.Entry) string {
	if entry.Side == timing.Write {
		return ", BlockPoint: <see the block-point probe>"
	}
	return ""
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

// probeTransport is a transport of this probe's own. The upload takes
// the API client's, which on loopback differs in nothing that touches a
// socket buffer; what matters is that the body travels through
// progressReader over a real connection.
func probeTransport() *http.Transport {
	return &http.Transport{DisableCompression: true}
}

// TestProbeTheUploadStallGap measures the write side's governing
// quantity: the worst interval between two progress events while the far
// end reads at a paced rate.
//
// THE FIXTURE IS THE ROW'S OWN. The store consumes 64 KiB at a time with
// a pause between, which is what makes the client's writes wait on
// buffer space rather than on the network; the body is far past any
// plausible block point, so the client really does block. A probe run
// against a store that drained freely would measure the speed of
// loopback and call it a margin.
func TestProbeTheUploadStallGap(t *testing.T) {
	// EVERY NUMBER HERE IS THE ROW'S OWN, and that is not tidiness. A
	// cheaper shape was tried first — a third of the paced volume, one
	// connection kept warm across the runs — and it answered a
	// DIFFERENT question: gaps of 594ms against the row's 432ms, because
	// a connection that has sustained throughput for twenty runs has an
	// autotuned buffer the row never has. Reproduce the row or measure
	// something else.
	const (
		bodySize   = 12 << 20
		chunk      = 64 << 10
		pause      = 25 * time.Millisecond
		pacedBytes = 6 << 20
	)

	path := probeBody(t, bodySize)
	store := newObjectStore(t, &deployJournal{})
	store.readChunk = chunk
	store.readPause = pause
	store.pauseUntil = pacedBytes

	var worst time.Duration
	var samples int
	for run := 0; run < probeRuns; run++ {
		// A CONNECTION OF ITS OWN PER RUN, because the row gets one. The
		// gap being measured is set by how much buffer the kernel has
		// decided this connection deserves, and that grows with the
		// connection's age.
		transport := probeTransport()
		gap, n := oneUploadRun(t, store.url, path, bodySize, transport)
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

	report(t, &timing.UploadSlowIsNotStalled, worst,
		fmt.Sprintf(" (%d gaps sampled, store pacing %d KiB every %v)",
			samples, chunk>>10, pause))
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
	const (
		bodySize = 12 << 20
		// How long with no progress at all counts as blocked. It is far
		// past any scheduling hiccup on loopback and far under the
		// shipped stall window, so it can neither be tripped by noise
		// nor confused with the behaviour under test.
		quiet = 250 * time.Millisecond
	)

	path := probeBody(t, bodySize)
	store := newObjectStore(t, &deployJournal{})
	// The store reads a little and then stops, holding the request open.
	// That is what makes the client fill the buffer and stay there.
	store.stopReadingAfter = 64 << 10

	var worst int64
	for run := 0; run < probeRuns; run++ {
		transport := probeTransport()
		at := oneBlockPointRun(t, store.url, path, bodySize, quiet, transport)
		transport.CloseIdleConnections()
		if at > worst {
			worst = at
		}
	}

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
		if recorded.Measured() && recorded.BlockPoint != worst {
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

	partial := splitEvenly(logFrame("A-LINE-DELIVERED-IN-PIECES"), 28)
	partial = append(partial, done)

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
			script: eventScript{frames: partial, pace: 20 * time.Millisecond},
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
			report(t, tc.entry, worst, extra)
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
