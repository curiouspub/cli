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

// probeRunsUnpaced is the run count for a probe whose fixture does not
// pace, and it exists because A MAXIMUM IS A STATISTIC ABOUT A SAMPLE
// SIZE and this suite was comparing two of them as though it were not.
//
// The three read-side probes all report "the worst gap over twenty
// runs", and that phrase hides a factor of seventy. A paced fixture
// delivers dozens of frames per run, so twenty runs sample the gap 820
// and 1,520 times; the unpaced one writes its frames and goes silent, so
// twenty runs sample it about twenty-five times. The five-times sizing
// rule is then applied to both maxima as if they were the same kind of
// number.
//
// A THOUSAND COSTS EIGHTY MILLISECONDS, measured on darwin: 1,239
// arrivals in 0.08 s, against 0.00 s for twenty. There is no argument
// for the smaller sample once the price is that.
//
// WHAT THE LARGER SAMPLE ACTUALLY FOUND, recorded because it killed the
// hypothesis that produced it. The reason for looking was a suspicion
// that this entry's small maximum was an artefact of under-sampling —
// that a hundred times the draws would find a tail and the window would
// rise. It did not: 2,000 runs on darwin reported 1.605 ms against
// 1.879 ms over twenty, which is the SAME quantity and not a longer
// tail. The change is kept anyway, because the reason to sample
// comparably does not depend on the answer being interesting, and the
// legs whose tails nobody has seen are the two this machine is not.
const probeRunsUnpaced = 1000

// probeArmBudget is how long ONE arm of a probe may spend before it
// stops and reports what it managed.
//
// # A COLLAPSE IS A ROW, NEVER A PACKAGE KILL
//
// This is the constant behind that rule, and the rule was bought rather
// than reasoned. A control that varied one socket buffer walked into a
// configuration under which a hosted runner does not finish: it sat at
// 24m1s and took the test binary down at the suite's twenty-five-minute
// timeout. What died with it was not only that control — it was the
// block point and all three read-side numbers from the probes further
// down the same binary, measurements that had nothing to do with the
// question being asked. A leg's entire evidence, lost to one row's
// environment.
//
// So every arm here is bounded well under the package timeout, an arm
// that runs out reports "did not complete on <leg> within <n>s" with the
// runs it managed and the worst it saw, and the probe carries on to the
// next one. An arm that will not finish is a RESULT about that leg — on
// one of them it is currently the most informative result available —
// and a result is worth more than a panic.
//
// FOUR MINUTES is about four times what the most expensive arm here
// costs when it behaves: twenty upload runs at roughly three seconds
// each. It is a budget rather than an expectation, and the several arms
// this package runs have to fit inside one timeout together, which is
// what keeps it from being generous.
//
// REQUIRED MUTATION, RUN 2026-09-10: set this to one second. All four
// bounded arms report and none of them takes the binary down —
//
//	UploadSlowIsNotStalled did not complete on darwin within 1s: 0 of
//	20 runs, worst gap so far 108.137167ms
//	the upload block point did not complete on darwin within 1s: 3 of
//	20 runs, largest block point so far 786432 bytes
//	StreamKeepAlivesAreProofOfLife did not complete on darwin within
//	1s: 1 of 20 runs, worst gap so far 18.155125ms
//	StreamPartialLineIsNotAStall did not complete on darwin within 1s:
//	0 of 20 runs, worst gap so far 22.952125ms
//
// — and the go-quiet arm, which finishes a thousand runs in eighty
// milliseconds, stays green beside them. That last part is the control:
// a budget that cut every arm would prove nothing about cutting the
// right one.
//
// It also found a defect in the reporter it was proving. The shared
// message took a Duration, so the block-point arm — whose quantity is a
// count of BYTES — printed "worst so far 0s". The caller renders its own
// quantity now.
const probeArmBudget = 4 * time.Minute

// reportIncompleteArm is the one place an unfinished arm is written
// down, so the three probes cannot describe the same situation in three
// different ways.
// worstSoFar is rendered by the CALLER rather than typed as a duration
// here, because the three probes do not all measure a duration: the
// block-point arm's number is a count of bytes, and a reporter that
// insisted on a Duration printed "worst so far 0s" against an arm whose
// worst so far was three runs' worth of block points. A shared message
// is worth having; a shared message that renders one caller's number as
// another caller's unit is not.
func reportIncompleteArm(t *testing.T, name string, done, want int, worstSoFar string, elapsed time.Duration) {
	t.Helper()
	t.Errorf("%s did not complete on %s%s within %ds: %d of %d runs, %s.\n"+
		"That is a RESULT about this leg and not a broken instrument — the arm was cut "+
		"at its budget so the rest of this binary could still report. Record the row as "+
		"it stands; a leg that cannot finish a shape is telling you something about the "+
		"shape.", name, probeLeg(), detectorNote(), int(elapsed.Seconds()), done, want,
		worstSoFar)
}

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
func report(t *testing.T, entry *timing.Entry, runs int, measured time.Duration, pin *timing.PinnedPair, extra string) {
	t.Helper()
	leg := probeLeg()
	recorded, known := entry.Measurements[leg], false
	known = recorded.Measured()

	t.Logf("%s on %s%s: worst gap %v over %d runs, against a %v window%s%s",
		entry.Name, leg, detectorNote(), measured, runs, entry.Window,
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
			entry.Name, leg, measured, runs, time.Now().Format("2006-01-02"),
			detectorNote(),
			measured.Round(time.Microsecond), runs,
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

	// BOUNDED, like every arm in this file. See probeArmBudget: the row
	// that taught this package the lesson took a whole leg's evidence
	// down with it, and this probe is one of the ones that died.
	ctx, cancel := context.WithTimeout(context.Background(), probeArmBudget)
	defer cancel()

	var worst time.Duration
	var samples int
	completed := 0
	started := time.Now()
	for run := 0; run < probeRuns; run++ {
		gap, n, err := oneUploadRun(ctx, store.url, path, pacing.bodySize, transport)
		transport.CloseIdleConnections()
		samples += n
		if gap > worst {
			worst = gap
		}
		if err != nil {
			// THE BUDGET EXPIRING IS THE RESULT; anything else is a
			// broken instrument, and the two are told apart rather than
			// folded together.
			if ctx.Err() == nil {
				t.Fatalf("the probe upload failed on run %d of %d: %v", run+1, probeRuns, err)
			}
			break
		}
		completed++
	}
	if samples == 0 {
		t.Fatal("the probe recorded no gap at all, so its silence is about an " +
			"instrument that stopped working rather than about a fast machine")
	}
	pinsWereApplied(t, client, store.pin)
	if completed < probeRuns {
		reportIncompleteArm(t, entry.Name, completed, probeRuns,
			fmt.Sprintf("worst gap so far %v", worst), time.Since(started))
		return
	}

	report(t, entry, probeRuns, worst, observedPin(client, store.pin),
		fmt.Sprintf(" (%d gaps sampled, store pacing %d KiB every %v over a "+
			"%d-byte body, %d of it paced)",
			samples, pacing.chunk>>10, pacing.pause, pacing.bodySize, pacing.pacedBytes))
}

// oneUploadRun PUTs the body once and returns the worst interval between
// two progress events, how many intervals it saw, and what went wrong.
//
// IT TAKES A CONTEXT AND RETURNS AN ERROR RATHER THAN FAILING, because
// its caller has a budget and a fatal inside the loop is exactly the
// shape that cannot be cut short. See probeArmBudget.
func oneUploadRun(ctx context.Context, url, path string, size int64, transport *http.Transport) (time.Duration, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("opening the probe body: %w", err)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return worst, gaps, fmt.Errorf("building the probe request: %w", err)
	}
	req.ContentLength = size

	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return worst, gaps, fmt.Errorf("the probe upload failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return worst, gaps, fmt.Errorf("draining the probe response: %w", err)
	}
	return worst, gaps, nil
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

	// BOUNDED, like every arm here. This probe's own loop already waits
	// for quiescence rather than for completion, so it cannot hang on a
	// slow upload — but it CAN be slow enough to matter on a leg where
	// the far end is crawling, and an arm that eats the package's
	// timeout takes the rest of the binary's numbers with it.
	ctx, cancel := context.WithTimeout(context.Background(), probeArmBudget)
	defer cancel()

	var worst int64
	completed := 0
	started := time.Now()
	for run := 0; run < probeRuns; run++ {
		at := oneBlockPointRun(t, ctx, store.url, path, bodySize, quiet, transport)
		transport.CloseIdleConnections()
		if at > worst {
			worst = at
		}
		if ctx.Err() != nil {
			break
		}
		completed++
	}
	// THE BLOCK POINT IS A NUMBER UNDER A CONDITION TOO, so this probe
	// runs under the same pin its gap-measuring sibling does. On darwin
	// the two conditions measured 819,200 bytes pinned against 3,014,656
	// autotuned — a factor of 3.7, and the reason a fixture sized
	// against one of them says nothing about the other.
	pinsWereApplied(t, client, store.pin)
	t.Logf("block point measured under %s", pinNote(observedPin(client, store.pin)))
	if completed < probeRuns {
		reportIncompleteArm(t, "the upload block point", completed, probeRuns,
			fmt.Sprintf("largest block point so far %d bytes", worst), time.Since(started))
		return
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
func oneBlockPointRun(t *testing.T, parent context.Context, url, path string, size int64, quiet time.Duration, transport *http.Transport) int64 {
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

	// DERIVED FROM THE ARM'S BUDGET, so a run cannot outlive the arm it
	// belongs to. The cancel below is what ends the run once the client
	// has gone quiet; the parent is what ends it if the arm has.
	ctx, cancel := context.WithCancel(parent)
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
	for still < quiet && ctx.Err() == nil {
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

	// THE ROW'S OWN FIXTURE, built by the row's own helpers, for the
	// reason the write-side probe takes its body from pacingFor: a probe
	// measuring a cheaper shape answers a different question, and the
	// two cannot drift apart if they call the same function. That is not
	// a hypothetical here — this probe ran twenty keep-alive beats
	// against a row that runs forty, and the number it produced was
	// recorded as that row's margin.
	partial := append(partialLineFrames(t), done)

	cases := []struct {
		entry  *timing.Entry
		script eventScript
		// runs is this case's own run count, because a maximum is a
		// statistic about a sample size and these three fixtures produce
		// wildly different numbers of samples per run. See
		// probeRunsUnpaced.
		runs int
	}{
		{
			// One frame and then silence, which is what the row sends.
			// With no pace, the only interval there is to measure is the
			// one from arming to the first byte: connection
			// establishment plus delivery.
			entry:  &timing.StreamGoesQuiet,
			script: eventScript{frames: []string{logFrame("LINE-A"), done}},
			// ONE ARRIVAL PER RUN, near enough, so twenty runs is a
			// twenty-sample maximum sitting beside two fifteen-hundred
			// sample ones and being compared with them by the same rule.
			runs: probeRunsUnpaced,
		},
		{
			entry:  &timing.StreamKeepAlivesAreProofOfLife,
			script: eventScript{frames: keepAliveFrames(), pace: keepAlivePace},
			runs:   probeRuns,
		},
		{
			entry:  &timing.StreamPartialLineIsNotAStall,
			script: eventScript{frames: partial, pace: partialLinePace},
			runs:   probeRuns,
		},
	}

	for _, tc := range cases {
		t.Run(tc.entry.Name, func(t *testing.T) {
			// THE RECORDED PACE IS CHECKED AGAINST THE FIXTURE ABOUT TO
			// RUN, which is what makes it a condition rather than a
			// transcription. A read-side gap IS the fixture's own pause
			// between flushes to within a millisecond, so an entry whose
			// stated pace has drifted from the fixture is an entry whose
			// number is about something else — and a number and its
			// condition are one fact.
			confirmPace(t, tc.entry, tc.script)

			script := &deployScript{
				journal:      &deployJournal{},
				release:      make(chan struct{}),
				eventScripts: []eventScript{tc.script},
			}
			srv := httptest.NewServer(script)
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(script.release) })

			// BOUNDED, like every arm here. These are the cheapest arms
			// in the file — the whole read side costs under a minute —
			// and they are bounded anyway, because the rule is that
			// NOTHING a probe measures may take the suite down with it,
			// and a rule with an exception for the cheap cases is a rule
			// that stops applying the first time a cheap case is not.
			ctx, cancel := context.WithTimeout(context.Background(), probeArmBudget)
			defer cancel()

			var worst time.Duration
			var samples int
			completed := 0
			started := time.Now()
			for run := 0; run < tc.runs; run++ {
				gap, n, err := oneStreamRun(ctx, srv.URL)
				samples += n
				if gap > worst {
					worst = gap
				}
				if err != nil {
					if ctx.Err() == nil {
						t.Fatalf("the probe stream failed on run %d of %d: %v",
							run+1, tc.runs, err)
					}
					break
				}
				completed++
			}
			if samples == 0 {
				t.Fatal("the probe recorded no arrival at all, so its silence is " +
					"about an instrument that stopped working")
			}
			if completed < tc.runs {
				reportIncompleteArm(t, tc.entry.Name, completed, tc.runs,
					fmt.Sprintf("worst gap so far %v", worst), time.Since(started))
				return
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
			report(t, tc.entry, tc.runs, worst, nil, extra)
		})
	}
}

// confirmPace refuses when the fixture this probe is about to run is not
// the one the registry says the number was measured under.
//
// IT IS THE READ SIDE'S confirmPin, and it exists for the same reason.
// A window is five times a gap and a gap is that number only under one
// condition; on the write side that condition is a pair of socket
// buffers, and here it is the fixture's own pacing. The difference is
// that this condition is a constant in this repository rather than a
// kernel's answer, so it can be checked exactly rather than compared
// within a band.
//
// A RECORD IS PASTED OR IT IS RETYPED. Two numbers written in two files
// agree on the day they are written and not afterwards, and the failure
// is silent: a fixture lengthened here and not recorded there leaves a
// margin standing over a measurement of something shorter. This is the
// one place the two meet.
//
// REQUIRED MUTATION, RUN 2026-09-10: set the keep-alive entry's Flushes
// to one less than the fixture's. Reds here, naming both counts, and
// nothing in internal/timing moves — which is the tie doing its job,
// since the registry alone cannot see a fixture.
func confirmPace(t *testing.T, entry *timing.Entry, script eventScript) {
	t.Helper()
	if entry.Pace == nil {
		// The registry guard reds on this on the read side, alone and
		// with a better message. Reporting it twice would make the
		// second report look like a second problem.
		return
	}
	if got := len(script.frames); got != entry.Pace.Flushes {
		t.Errorf("timing.%s records a fixture of %d flushes and the fixture this probe "+
			"is about to run has %d.\nA read-side gap is the fixture's own pause "+
			"between flushes, so a number taken over a different fixture is a margin "+
			"over a different quantity. Record what ships, or ship what is recorded.",
			entry.Name, entry.Pace.Flushes, got)
	}
	if script.pace != entry.Pace.Interval {
		t.Errorf("timing.%s records a fixture pace of %v and this probe is about to run "+
			"one at %v.\nThe pace is most of the gap being measured, so these are two "+
			"different measurements wearing one name.",
			entry.Name, entry.Pace.Interval, script.pace)
	}
}

// oneStreamRun opens one connection and returns the worst interval
// between the watchdog's arming — here, the instant before the request
// goes out — and each byte arriving, and how many arrivals it saw.
func oneStreamRun(ctx context.Context, base string) (time.Duration, int, error) {
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/v1/deploys/probe/events", nil)
	if err != nil {
		return worst, arrivals, fmt.Errorf("building the probe stream request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return worst, arrivals, fmt.Errorf("opening the probe stream: %w", err)
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
	return worst, arrivals, ctx.Err()
}

// -------------------------------------------------------------------
// The receive-end control, and it is LAST in this file on purpose
// -------------------------------------------------------------------
//
// Go runs a file's rows in source order, so anything below a row that
// runs long is a row whose number nobody gets. This one has already
// taken a hosted runner to its twenty-five-minute timeout once and taken
// the block point and all three read-side numbers down with it. It is
// bounded now — see probeArmBudget — and it is also placed where a
// failure of that bound costs the least.

// TestProbeWhatTheReceiveEndIsWorth holds the SEND end still and varies
// only the receive end, so that what each end is worth is a measurement
// on each leg rather than an argument made once and applied to three.
//
// # WHY THE QUESTION EXISTS
//
// One of the three legs cannot hold a receive pin: its kernel moves an
// accepted socket's receive buffer whatever SO_RCVBUF asked for, so the
// gap there is taken under a buffer that wanders across a factor of five
// while the body goes out. A story was available that made this not
// matter — that the send end governs and the receive end is a detail —
// and a story that lets a leg's numbers stand is exactly the kind that
// has to be measured rather than believed.
//
// # WHERE IT RUNS, AND WHY NOT EVERYWHERE
//
// WINDOWS is the leg where all three arms complete, so it is where the
// full table comes from. LINUX runs the same bounded arms ONCE: two of
// them complete and the third does not, and that row IS this leg's
// answer rather than a hole in its table — see pinnedBuffer, where the
// collapse and both of the hypotheses that were killed about it are
// recorded. DARWIN is not asked at all, and that is not an omission: a
// control varies one end while the other stays where it is put, and this
// is the leg whose kernel will not let it.
//
// # IT REFUSES SO THAT IT PRINTS, AND IT DOES NOT STAY
//
// A passing row's log output goes nowhere a gate shows anybody, and a
// control whose whole product is a table has to red to deliver it. It is
// removed in the commit that records the table, the way the pin-size
// table before it was taken by an instrument that is not in the tree
// either.
func TestProbeWhatTheReceiveEndIsWorth(t *testing.T) {
	switch probeLeg() {
	case timing.Windows, timing.Linux:
	default:
		// NOT A SKIP, because a skip nobody declared fails this
		// repository's suite wrapper and a declared one would be a line
		// in a manifest outliving a row meant to be temporary.
		t.Logf("the receive-end control varies one end while the other stays put, "+
			"and on %s the kernel moves the end being varied — nothing measured here",
			probeLeg())
		return
	}

	entry := &timing.UploadSlowIsNotStalled
	pacing := pacingFor(t, entry.Window)
	path := probeBody(t, pacing.bodySize)

	arms := []struct {
		name    string
		receive int
	}{
		// LARGER THAN THE SEND BUFFER, which is the arm the registry's
		// own record stands closest to.
		{"receive pinned at 256 KiB", 256 << 10},
		// NOT PINNED AT ALL, which is what a leg that cannot hold a pin
		// is actually running.
		{"receive unpinned", 0},
		// SMALLER THAN THE SEND BUFFER, BY A FACTOR OF EIGHT, and LAST
		// on purpose: this is the configuration one hosted runner has
		// already been watched failing to finish, and an arm that may
		// not return should not stand in front of two that will.
		{"receive pinned at 16 KiB", 16 << 10},
	}

	table := fmt.Sprintf("what the receive end is worth on %s%s, send held at %d bytes, "+
		"%d runs per arm:\n", probeLeg(), detectorNote(), pinnedBuffer, probeRuns)
	for _, arm := range arms {
		var row string
		t.Run(arm.name, func(t *testing.T) {
			row = runReceiveArm(t, arm.name, arm.receive, pacing, path)
		})
		table += "  " + row + "\n"
	}

	t.Errorf("%s\nTHIS ROW IS A CONTROL AND IT REFUSES SO THAT IT PRINTS. It is one "+
		"leg's table and it is recorded as one leg's: what each end of this connection "+
		"is worth is measured on the leg it is claimed about, and each leg's window "+
		"comes from its own measurement under its own recorded condition. Record the "+
		"table beside pinnedBuffer and delete this row.", table)
}

// runReceiveArm runs one arm and reports its row immediately, so that an
// arm which runs long cannot take the arms before it down with it.
func runReceiveArm(t *testing.T, name string, receive int, pacing uploadPacing, path string) string {
	t.Helper()

	store := newObjectStore(t, &deployJournal{}, receive)
	store.readChunk = pacing.chunk
	store.readPause = pacing.pause
	store.pauseUntil = pacing.pacedBytes

	transport, client := pinnedTransport(pinnedBuffer)
	t.Cleanup(transport.CloseIdleConnections)
	client.watch(t, pacedPause)
	store.pin.watch(t, pacedPause)

	ctx, cancel := context.WithTimeout(context.Background(), probeArmBudget)
	defer cancel()

	var worst time.Duration
	gaps, completed := 0, 0
	started := time.Now()
	for run := 0; run < probeRuns; run++ {
		gap, n, err := oneUploadRun(ctx, store.url, path, pacing.bodySize, transport)
		transport.CloseIdleConnections()
		gaps += n
		if gap > worst {
			worst = gap
		}
		if err != nil {
			if ctx.Err() == nil {
				t.Fatalf("%s failed on run %d of %d: %v", name, run+1, probeRuns, err)
			}
			break
		}
		completed++
	}
	elapsed := time.Since(started)

	// THE POSITIVE CONTROLS, PER ARM, because an arm that measured
	// nothing reports the same silence as an arm that measured
	// agreement. They run on an arm that ran out of budget too: the
	// partial number it brings back is only worth reading if the
	// condition it was taken under was the one this arm is named for.
	if gaps == 0 {
		t.Fatalf("%s sampled no gap at all, so its number is about an instrument "+
			"that stopped working rather than about a receive buffer", name)
	}
	send, sendUses := client.record()
	if sendUses == 0 {
		t.Fatalf("%s never pinned a send buffer on any connection, so the end this "+
			"control HOLDS STILL was not held at all", name)
	}
	if send.Err != "" || !send.Held() {
		t.Fatalf("%s could not hold the send pin (asked %d, read back %d, %q). Every "+
			"arm here varies the receive end against a fixed send end, and an arm "+
			"whose fixed end moved is comparing two things at once.",
			name, send.Requested, send.ReadBack, send.Err)
	}
	got, receiveUses := store.pin.record()
	if receive == 0 {
		// THE UNPINNED ARM'S OWN CONTROL, and the one an implementation
		// is most likely to get wrong: "unpinned" has to mean the
		// listener pinned NOTHING, not that it pinned something nobody
		// looked at.
		if receiveUses != 0 {
			t.Fatalf("the unpinned arm's listener pinned %d connection(s), so this arm "+
				"is not the one it is named for", receiveUses)
		}
	} else {
		if receiveUses == 0 {
			t.Fatalf("%s never pinned a receive buffer on any connection, so this arm "+
				"measured the same autotuning as the unpinned one", name)
		}
		if got.Err != "" || !got.Held() {
			t.Fatalf("%s could not hold its receive pin (asked %d, read back %d, %q) "+
				"— this is a leg that holds one, so an arm that could not is a "+
				"measurement of something else",
				name, got.Requested, got.ReadBack, got.Err)
		}
	}

	var row string
	if completed < probeRuns {
		row = fmt.Sprintf("%-28s did not complete on %s within %ds: %d of %d runs, "+
			"worst so far %v", name, probeLeg(), int(elapsed.Seconds()), completed,
			probeRuns, worst)
	} else {
		row = fmt.Sprintf("%-28s worst gap %-14v (%d gaps, %d runs in %v)%s",
			name, worst, gaps, completed, elapsed.Round(time.Second),
			pinNote(observedPin(client, store.pin)))
	}
	// REPORTED HERE AND NOT ONLY AT THE END. The version of this control
	// that reported only at the end brought back nothing when one arm
	// ran long, including the arms that had already finished.
	t.Errorf("%s", row)
	return row
}
