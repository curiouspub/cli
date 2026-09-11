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
	"sort"
	"strings"
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

// probeRuns is how many consecutive VALID runs stand behind a probe's
// number. Twenty, because one run is an outcome and a margin is a
// distribution: the window that failed one run in six passed the run
// that chose it.
//
// VALID is the word that moved. It used to be twenty runs, of which the
// starved ones were dropped — so a noisy machine produced a maximum over
// fifteen readings while the record said twenty, and past a fifth the
// leg stopped altogether. Now a starved run is retaken and twenty means
// twenty. See attemptCap for what bounds the retaking.
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
//
// THE FOUR LINES ABOVE ARE WHAT THAT RUN PRINTED, and a paced arm's line
// now carries one thing more: the attempts it spent. The two counts
// stopped being the same number when a starved pass began to be retaken
// — an arm cut at its budget can have made twenty-six attempts and hold
// nineteen readings — and a line reporting only the second would be
// describing a cheaper run than the one that happened.
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
func report(t *testing.T, entry *timing.Entry, runs int, measured time.Duration, pin *timing.PinnedPair, integrity *timing.PaceIntegrity, extra string) {
	t.Helper()
	leg := probeLeg()
	recorded := entry.Measurements[leg]
	// COMPLETE, NOT MERELY MEASURED. The paste hint below is the only
	// place a record's literal is ever printed, and it used to fire on
	// Measured() alone — so a record that was missing a field this probe
	// could have supplied got no hint, and the person filling it in had
	// to reconstruct the literal from a prose log line. A record is
	// incomplete when the fixture had a pace and the admitted passes'
	// spread is absent: that is the distribution the threshold is judged
	// against, and it exists here and nowhere else.
	known := recorded.Measured() &&
		(recorded.Integrity.StatedPace == 0 || recorded.Integrity.ValidGapMax > 0)

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
		t.Errorf("%s has no complete record on %s, and this run measured a "+
			"worst gap of %v over %d runs on %s%s.\nRecord it in internal/timing: "+
			"{WorstGap: %v, Runs: %d, Date: \"%s\"%s%s%s} — or run this again and "+
			"record the worst across the passes, with the run count to match, "+
			"which is how the darwin entry was taken.\nTWO THINGS THIS LINE ALONE "+
			"WILL NOT TELL YOU. make ci runs this package twice, plainly and under "+
			"the race detector, and WHICH OF THE TWO IS WORSE IS NOT THE SAME "+
			"ANSWER ON EVERY LEG OR EVERY ROW — measured, the detector is the "+
			"FRIENDLIER condition on two of the three legs of one read-side entry "+
			"(darwin 24.4 ms under it against 104.4 ms without) and the harsher one "+
			"elsewhere. So run BOTH and keep the worse of what you get; this run "+
			"was %s. And nothing here may be seeded from another leg's "+
			"number: the buffers and the scheduler belong to the kernel and the "+
			"runner.",
			entry.Name, leg, measured, runs, time.Now().Format("2006-01-02"),
			detectorNote(),
			measured.Round(time.Microsecond), runs,
			time.Now().Format("2006-01-02"),
			blockPointHint(entry), pinLiteral(pin), integrityLiteral(integrity),
			detectorPhrase())
		return
	}

	// A GAP AND THE CONDITION IT WAS TAKEN UNDER ARE ONE FACT. A record
	// whose pin this run could not reproduce is a margin over a quantity
	// this machine does not have, and comparing the two numbers while
	// ignoring that would be the whole defect this round removed.
	confirmPin(t, entry, leg, pin)
	confirmIntegrity(t, entry, leg, recorded.Integrity)

	// # A RUN MAY NOT QUIETLY SPEND THE MARGIN THE RULE PROMISES
	//
	// There used to be one band here and it was a note. A run past a
	// tenth of the record logged a line asking for the record to be
	// retaken, and anything short of the window itself was green — so
	// the whole range between "the record drifted" and "the row is about
	// to flake" printed a sentence into a -v log nobody reads and the
	// gate went on agreeing.
	//
	// It is not hypothetical. Measured 2026-09-11, on a developer Mac
	// running the ordinary package under the detector:
	// StreamPartialLineIsNotAStall reported 128.757833ms against a 230ms
	// window and a 45.707917ms record. The row's margin was 1.79× where
	// this task's whole rule is five, the record it was measured against
	// was wrong by a factor of nearly three, and the run PASSED with a
	// log line. A timing row whose margin is smaller than the thing it
	// did not measure is a flake with a schedule — which is the rule
	// this whole package exists to keep, happening inside the instrument
	// built to keep it.
	//
	// So the bands are graded by what the reading COSTS rather than by
	// how far it is from a number somebody wrote down:
	//
	//	measured >= window          the row could not have passed. Errors
	//	                            above; the environment and the window
	//	                            disagree.
	//	measured * marginFloor      the record is not stale, it is wrong
	//	    > window                by a factor, and the row is inside the
	//	                            band where it flakes. ERRORS.
	//	measured > record + 10%     the record drifted. A note.
	//
	// WHERE THE MIDDLE LINE GOES IS A CHOICE and it is named rather than
	// buried: half the ruled five. A floor at the full five would red on
	// any pass at all above the record, because the windows in this
	// registry are rounded to within a per cent of five times their
	// measurement — every leg would sit one noisy pass from a red, which
	// is a gate somebody switches off. Half is far enough above the
	// rounding to be about the record rather than about the noise, and
	// far enough below five to fire while the row still passes. Moving
	// it is a one-constant ruling.
	if measured*marginFloorNum > entry.Window*marginFloorDen {
		// WHICH OF THE TWO IS WRONG IS ANSWERABLE, SO IT IS ANSWERED.
		// The band fires on the relation between a window and a gap, and
		// there are two ways to reach it: a record this run has left
		// behind, or a window that never cleared the rule against the
		// record it was set from. Naming the record in both cases sends
		// half the readers to retake a measurement that is fine. The
		// second case is the registry's own guard's business — it says
		// so and points there.
		culprit := fmt.Sprintf("The recorded worst is %v (taken over %d runs on "+
			"%s), so this run is %.1f× the record: the RECORD is what is stale, "+
			"not the row, and the row is passing on a margin it does not have.\n"+
			"Retake the measurement on this leg and re-rule the window from it. "+
			"Do not raise the window to quiet this line without the measurement "+
			"behind it.",
			recorded.WorstGap, recorded.Runs, recorded.Date,
			float64(measured)/float64(recorded.WorstGap))
		if recorded.WorstGap*marginRule > entry.Window {
			culprit = fmt.Sprintf("The record is not what is wrong here: %v over "+
				"%d runs on %s does not clear five times over against this "+
				"window either. The WINDOW is under the rule, this run merely "+
				"walked into it, and the registry's own guard says the same "+
				"thing from the other side. Fix it there.",
				recorded.WorstGap, recorded.Runs, recorded.Date)
		}
		t.Errorf("%s measured %v on %s, and a %v window is %.2f× that — the rule "+
			"this registry keeps is five, and anything under %.1f× is reported "+
			"here rather than left to a log line.\n%s",
			entry.Name, measured, leg, entry.Window,
			float64(entry.Window)/float64(measured),
			float64(marginFloorNum)/float64(marginFloorDen), culprit)
		return
	}

	// A TENTH PAST THE RECORD, not a nanosecond past it. The record is a
	// maximum over many runs, so an ordinary run beats it by a hair
	// fairly often; a note that fires on a microsecond is a note nobody
	// reads by the second week. A tenth is past the noise and inside the
	// band the error above owns.
	if measured > recorded.WorstGap+recorded.WorstGap/10 {
		t.Logf("%s: this run's %v is past the recorded worst of %v on %s (taken "+
			"over %d runs on %s). The record is the one that is stale, not this "+
			"run: retake it before trusting the margin.",
			entry.Name, measured, recorded.WorstGap, leg, recorded.Runs, recorded.Date)
	}
}

// marginFloorNum and marginFloorDen are the least multiple of a measured
// gap a window may be before this probe refuses rather than notes: five
// halves, which is half the ruled five. See report above for why the
// floor is not the rule itself.
//
// A FRACTION IN TWO CONSTANTS RATHER THAN ONE, for the reason pacingFor
// spells the same shape out: 5/2 written as one untyped constant is
// integer division, and the compiler would have taken it as two without
// a word. The halves here are real.
const (
	marginRule     = 5
	marginFloorNum = 5
	marginFloorDen = 2
)

// gathered is what a probe's run loop brings back: one client gap per
// ATTEMPT in the order they were attempted, every progress event
// sampled across all of them, and how many of those attempts measured
// the client.
//
// THE GAPS ARE PER ATTEMPT AND NOT PER VALID PASS, because the starved
// ones are what say how noisy this runner was, and a slice that dropped
// them on the way out would make the rate below unmeasurable at exactly
// the moment it becomes interesting.
type gathered struct {
	gaps    []time.Duration
	samples int
	valid   int
}

// worst is the largest client gap seen across every attempt, starved
// ones included. It is what an arm that ran out of budget reports — at
// that point nothing has been classified, so "the worst so far" is the
// only honest thing to say about it.
func (g gathered) worst() time.Duration {
	var worst time.Duration
	for _, gap := range g.gaps {
		if gap > worst {
			worst = gap
		}
	}
	return worst
}

// gatherPasses runs attempts until it holds want VALID passes or spends
// the attempt cap, and that is the whole of the change a starved pass
// brought with it.
//
// # A SHORT SAMPLE IS ANSWERED BY ANOTHER READING
//
// The loop this replaced ran exactly twenty times and let the classifier
// sort the results out afterwards. A starved attempt therefore cost the
// sample a reading it never got back: nineteen where twenty were asked
// for, or fifteen, and past a fifth the leg stopped and reported
// nothing at all. Every other instrument answers a spoiled reading by
// taking another one, and this one now does too.
//
// THE DECISION HAS TO BE MADE INSIDE THE LOOP, which is why this exists
// as a function rather than as a second pass over two slices: whether to
// attempt again depends on the attempt that has just finished, so the
// classification cannot all wait until the runs are over. What still
// waits is the MAXIMUM — see classifyPasses, which takes it over a
// subset decided after the fact, and a running maximum cannot be
// un-taken.
//
// fixtureGaps is the fixture's own record, one entry per attempt it has
// served, in the order it served them. It is read after every attempt
// and the last entry is the one just finished; a count that has stopped
// agreeing with the attempts made is refused rather than guessed at,
// for the same reason classifyPasses refuses it — the pairing is BY
// POSITION, and where it fails there is no attempt for this gap to
// belong to.
func gatherPasses(want int, pace, budget time.Duration,
	attempt func() (time.Duration, int, error),
	fixtureGaps func() []time.Duration) (gathered, error) {

	threshold, err := starvationThreshold(pace, budget)
	if err != nil {
		return gathered{}, err
	}
	cap := attemptCap(want)
	g := gathered{gaps: make([]time.Duration, 0, cap)}
	for g.valid < want && len(g.gaps) < cap {
		gap, samples, err := attempt()
		g.samples += samples
		g.gaps = append(g.gaps, gap)
		if err != nil {
			return g, err
		}
		fixture := fixtureGaps()
		if len(fixture) != len(g.gaps) {
			return g, fmt.Errorf("the fixture has recorded %d gaps after %d attempts, "+
				"so there is no record of the attempt this loop is about to judge. "+
				"Whether to take another reading depends on the one just finished, "+
				"and the pairing that answers it is BY POSITION",
				len(fixture), len(g.gaps))
		}
		if !starvedPass(fixture[len(fixture)-1], threshold) {
			g.valid++
		}
	}
	return g, nil
}

// starvedPass reports whether one attempt measured the MACHINE rather
// than the client: its fixture missed its own stated pace by more than
// the threshold.
//
// ONE HOME FOR THE RULE AND TWO PLACES THAT APPLY IT. The run loop asks
// after every attempt, because it has to decide whether to take another;
// the classifier asks again across the whole series, because the maximum
// is a maximum over the passes this rule admitted and the record has to
// say how many there were. Two applications of one rule is fine; two
// spellings of it would drift, so the comparison is written once.
//
// A FIXTURE WITH NO PACE HAS NOTHING TO MISS, and this says so rather
// than letting a zero through the arithmetic. It writes its frames and
// goes silent: every attempt measured the client, nothing is retaken,
// and a fixture gap of any size there is the quantity being measured
// rather than a spoiled reading.
func starvedPass(fixtureGap, threshold time.Duration) bool {
	// NO ZERO-THRESHOLD BRANCH, AND ITS ABSENCE IS THE RULING. This
	// function used to answer "false" for a threshold of nought, which
	// read as leniency and was an EXEMPTION: a fixture that states no
	// interval could not starve, however long it actually paused. It was
	// not saying that fixture cannot starve — it was saying its
	// starvation had no declared threshold to be measured against, and
	// the two are only the same thing while nobody looks.
	//
	// A zero reaching here now is a caller that did not resolve one, and
	// it is a programming error rather than a reading: taken literally a
	// zero threshold marks EVERY pass starved. starvationThreshold is
	// where the resolution lives and where the refusal is raised.
	return fixtureGap*time.Duration(paceThresholdDen) >
		threshold*time.Duration(paceThresholdNum)
}

// starvationThreshold is the quantity one leg's passes are judged
// against: a fixture's stated interval where it has one, and its
// MEASURED FLUSH BUDGET where it does not.
//
// WHY A WRITE-THEN-SILENT FIXTURE NEEDED ONE. StreamGoesQuiet writes two
// frames and holds the connection open, so there is no interval to
// state — and for as long as that meant "no threshold", every pass it
// took counted whatever the machine did underneath it. On 2026-09-11 a
// merge-group runner paused that fixture for 35.113375 ms, against a
// routine 56 µs to 11 ms on the same leg. The pass counted, the client's
// arrival gap came back 54.251708 ms against a 55 ms window, and the
// margin floor reported the ROW as passing thin on a reading that was
// about the machine. It ejected a green pull request from the queue.
//
// The budget is measured rather than chosen, per leg, and lives on the
// Measurement for the reason set out there: an interval is this
// repository's constant, a budget is what a kind of machine does.
func starvationThreshold(pace, budget time.Duration) (time.Duration, error) {
	if pace > 0 {
		return pace, nil
	}
	if budget > 0 {
		return budget, nil
	}
	return 0, fmt.Errorf("this fixture states no pace and its leg records no flush " +
		"budget, so there is no quantity its starvation could be measured against. " +
		"That is nobody having looked rather than a fixture that cannot starve, and " +
		"it is refused here rather than passed on as a threshold of nought — which " +
		"would not be the lenient reading but the strictest possible one, marking " +
		"every pass starved and stopping the leg at its attempt cap")
}

// budgetRecorded is the budget as a RECORD rather than as a threshold:
// zero when the fixture states a pace, because then the pace is what its
// passes were judged against and a budget beside it would be a second
// number nobody used.
func budgetRecorded(pace, budget time.Duration) time.Duration {
	if pace > 0 {
		return 0
	}
	return budget
}

// thresholdPhrase names WHICH quantity a message is talking about, so a
// reader of a starvation line is never left to infer whether the number
// beside it is a fixture's own constant or a leg's measured budget.
func thresholdPhrase(pace, budget time.Duration) string {
	if pace > 0 {
		return fmt.Sprintf("stated %v pace", pace)
	}
	return fmt.Sprintf("%v flush budget for this leg", budget)
}

// attemptCap is the total attempts a probe may spend reaching want valid
// passes, and spending it without reaching them is now the ONLY way
// starvation stops a leg.
//
// # WHAT IT REPLACED, AND WHY THAT HAD TO GO
//
// The rule before it was "more than one pass in five starved is a STOP".
// It fired three times in one working day with no client-margin breach
// under any of them — 32 ms against a 255 ms window, 1.2 ms against
// 55 ms — and the same command on the same machine gave five of twenty
// on one branch, one of twenty on the main line, and zero of twenty on
// every probe when the worst branch was run a second time. A line drawn
// at one in five through a distribution that looks like that reds
// without a defect underneath it, and a gate that reds without a defect
// is a gate somebody switches off.
//
// The cap asks a different question, and it is the question worth
// asking: not "was this runner noisy" — that is a number now, recorded
// per leg — but "did it produce the readings this margin needs at all".
//
// TWICE WHAT WAS ASKED FOR, so forty against the twenty every paced
// probe wants. The figure is a budget rather than a measured threshold
// and it is worth saying so: a runner starving half its attempts still
// delivers a full sample here, and one starving more than half is
// spending most of its time being something other than an instrument.
// Unbounded retaking would be the worse failure — a busy machine turning
// a bounded arm into one that never finishes, which is the shape the arm
// budget above exists against, one level down.
//
// EVERY FIXTURE GETS THE BUDGET NOW. This function used to hand an
// unpaced fixture a cap equal to what it asked for, on the reasoning
// that with no pace there is no starved pass and a retake budget would
// be machinery with no input. The reasoning was sound and its premise
// was the exemption starvationThreshold removed: a write-then-silent
// fixture has a threshold now, so it has starved passes, so it needs
// somewhere to retake them from.
func attemptCap(want int) int {
	return want * retakeBudget
}

// retakeBudget is the "twice" in attemptCap, in one place so that moving
// it is one edit and the cap and the message that reports it cannot
// disagree about what was spent.
const retakeBudget = 2

// classifyPasses separates the runs that measured the CLIENT from the runs in
// which the fixture was the thing that stopped, and returns the maximum
// over the first kind.
//
// # THE PROBE CARRIES THE SAME INTEGRITY TEST AS THE ROW
//
// Both read-side rows already read the fixture's own widest gap and
// refuse when the fixture paused past the window — "this row measured
// the machine rather than the client". The probe printed the same number
// and used none of it, so a starved run's gap became the leg's worst,
// the window became five times it, and the fixture grew to span three
// and a half of that. An instrument that records its own starvation as
// the subject's margin performs the widening it was built to prevent.
//
// THE THRESHOLD IS THREE TIMES THE STATED PACE, and both numbers that
// set it are here rather than in a commit message. Ordinary passes sit
// at about 1.25× — a 20 ms pace delivering with a widest gap near 25 ms,
// a 15 ms pace near 17 ms. The pass this rule was written for sat at
// 15.8×: 315.881875 ms against a stated 20 ms. Three is well above the
// ordinary spread and far below the event, which is what a threshold
// between two measured populations should be, and it is one constant to
// move.
//
// THE THRESHOLD MOVES ON THE DISTRIBUTION, NOT ON ONE ENTRY, and the
// distribution has now been looked at. Twenty-one darwin passes of
// twenty runs, three paced entries, 2026-09-11 — the admitted passes'
// ratio of fixture gap to stated pace:
//
//	entry          median across passes   largest admitted
//	upload         1.06-1.08              2.29
//	keep-alive     1.09-1.11              2.53
//	partial line   1.08-1.09              2.42
//
// It is a TIGHT BODY WITH A THIN TAIL, not a spread running evenly from
// one to three. Every median across those 420 runs sits between 1.06
// and 1.11, and nothing admitted anywhere reached 2.6. So the line at
// three sits in open space above the tail rather than through the
// middle of one population, which is the shape a threshold wants and
// the reason this one stays where it is.
//
// WHAT THE SAME DATA SAYS THAT IS LESS COMFORTABLE: each entry's
// recorded maximum comes from its HIGHEST-RATIO admitted pass. Upload's
// 179.409167 ms came from the pass that ran 2.29×, the keep-alives'
// 38.055 ms from 2.53×, the partial line's 48.391958 ms from 2.42×. The
// tail is not a curiosity beside the measurement — it is where every
// window comes from. That is an observation for a person rather than a
// number to act on: tightening the line would cut the passes that
// produce the maxima, which would lower every window on an argument
// about the instrument rather than about the client.
//
// A ZERO PACE IS NOT A PASSING GRADE. A fixture that writes its frames
// and goes silent has no pace to miss, so there is no test to apply and
// every run counts — recorded as StatedPace zero, so a reader can tell
// "held its pace" from "had none".
//
// WHAT A STARVED PASS COSTS NOW, since this function used to decide it.
// It is retaken rather than counted — see gatherPasses, which is the
// loop that hands this one its attempts — so the population here is
// every ATTEMPT, want is the valid passes that were asked for, and the
// only starvation stop left is having spent the attempts without
// reaching them. "More than one in five starved" was a rule about a
// sample this instrument had no reason to keep, and it is gone rather
// than loosened.
func classifyPasses(name string, leg timing.Leg, want int, perRun, fixture []time.Duration,
	pace, budget time.Duration, flushes int) (time.Duration, *timing.PaceIntegrity, error) {

	threshold, err := starvationThreshold(pace, budget)
	if err != nil {
		return 0, nil, fmt.Errorf("%s on %s: %w", name, leg, err)
	}

	// PAIRING BY POSITION IS AN ASSUMPTION, SO IT IS CHECKED. Runs are
	// sequential and each opens one connection, so run i is connection
	// i — and if the counts ever disagree that reasoning has stopped
	// holding and every classification below it is arbitrary.
	if len(fixture) != len(perRun) {
		return 0, nil, fmt.Errorf("%s on %s: %d runs against %d fixture records, so "+
			"there is no run this fixture gap belongs to. The classification below "+
			"pairs them BY POSITION and that pairing has stopped being true",
			name, leg, len(perRun), len(fixture))
	}

	integrity := &timing.PaceIntegrity{
		// ATTEMPTS IS THE POPULATION, and it is set from the slice
		// rather than from want because the two stop being the same
		// number the moment anything is retaken. It is the starvation
		// rate's denominator, and a rate over the passes that survived
		// would report a fraction of the wrong thing.
		Attempts:     len(perRun),
		ThresholdNum: paceThresholdNum,
		ThresholdDen: paceThresholdDen,
		StatedPace:   pace,
		// RECORDED BESIDE THE PACE, not instead of it, so the record says
		// which of the two the threshold came from. Exactly one is
		// non-zero.
		FlushBudget: budgetRecorded(pace, budget),
		Flushes:     flushes,
	}
	var worst time.Duration
	// THE ADMITTED PASSES' OWN GAPS ARE KEPT, not just the largest. A
	// threshold is a line through a distribution, and a record carrying
	// only the top of that distribution cannot say whether the line sits
	// in open space or through the middle of one population. See
	// PaceIntegrity's ValidGap fields.
	admitted := make([]time.Duration, 0, len(perRun))
	for i, gap := range perRun {
		if fixture[i] > integrity.WorstFixtureGap {
			integrity.WorstFixtureGap = fixture[i]
		}
		if starvedPass(fixture[i], threshold) {
			integrity.Starved++
			continue
		}
		integrity.Valid++
		admitted = append(admitted, fixture[i])
		if gap > worst {
			worst = gap
		}
	}
	if len(admitted) > 0 {
		sorted := append([]time.Duration(nil), admitted...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		integrity.ValidGapMin = sorted[0]
		integrity.ValidGapMax = sorted[len(sorted)-1]
		// THE LOWER MIDDLE ON AN EVEN COUNT, stated rather than left to
		// a reader to guess: this is a spread's shape, not a statistic
		// anybody averages, and interpolating between two observations
		// would put a number in the record that nothing measured.
		integrity.ValidGapMedian = sorted[(len(sorted)-1)/2]
	}
	if integrity.Valid == 0 {
		return 0, nil, fmt.Errorf("%s on %s: every one of %d attempts starved — the "+
			"fixture never came within %d/%d of its %s, worst %v. There is no "+
			"measurement here to report",
			name, leg, len(perRun), paceThresholdNum, paceThresholdDen,
			thresholdPhrase(pace, budget), integrity.WorstFixtureGap)
	}
	// THE ONE STARVATION STOP LEFT, and it fires at the cap rather than
	// at a ratio. Reaching it means this runner spent every attempt it
	// was given and still could not produce the readings the margin
	// needs — which is a statement about whether there is a measurement
	// here, not about how much work it took to get one. A leg that took
	// thirty-nine attempts to reach twenty valid passes is noisy and is
	// recorded as noisy; it is not stopped.
	//
	// IT NAMES BOTH NUMBERS RATHER THAN A RATIO. "A third of the
	// attempts starved" sends a reader to argue with a threshold; "10
	// valid in 40" says what was asked for, what arrived, and what it
	// cost, and the next question — is this machine busy or is the
	// fixture wrong — is answerable from the two.
	if integrity.Valid < want {
		return 0, nil, fmt.Errorf("%s on %s: this runner cannot hold the fixture's "+
			"pace: %d valid in %d. It spent every attempt it is given and did not "+
			"reach the %d valid passes this margin stands on, worst fixture gap %v "+
			"against a %s.\nA starved pass is retaken rather than counted, so "+
			"this is not a noisy runner being refused — it is a runner that could "+
			"not produce the readings at all, and a maximum over the few passes it "+
			"managed is a number about the quiet moments of a busy machine",
			name, leg, integrity.Valid, len(perRun), want,
			integrity.WorstFixtureGap, thresholdPhrase(pace, budget))
	}
	return worst, integrity, nil
}

// confirmIntegrity refuses when the record was admitted under a
// different rule from the one this run is applying.
//
// IT IS confirmPin AND confirmPace ONE MORE TIME. A maximum means
// nothing without the rule that decided which passes could contribute to
// it, so the threshold travels with the number; a record taken at a
// looser threshold is a maximum over a wider population and is not
// comparable with this run.
func confirmIntegrity(t *testing.T, entry *timing.Entry, leg timing.Leg, p *timing.PaceIntegrity) {
	t.Helper()
	if p == nil {
		// The registry guard reds on this alone and with a better
		// message. Two reports of one absence read as two problems.
		return
	}
	if p.ThresholdNum != paceThresholdNum || p.ThresholdDen != paceThresholdDen {
		t.Errorf("timing.%s's %s measurement was admitted at a %d/%d pace threshold "+
			"and this probe applies %d/%d. A maximum is a maximum over the passes "+
			"a rule let in, so the record and the rule are one fact: retake the "+
			"measurement under this threshold, or say why the record's stands.",
			entry.Name, leg, p.ThresholdNum, p.ThresholdDen,
			paceThresholdNum, paceThresholdDen)
	}
}

// integrityLiteral is the Integrity field of the record a paste hint
// asks for, so the person pasting it does not have to invent one.
func integrityLiteral(p *timing.PaceIntegrity) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(", Integrity: &timing.PaceIntegrity{Attempts: %d, Valid: %d, "+
		"Starved: %d, ThresholdNum: %d, ThresholdDen: %d, StatedPace: %d, Flushes: %d, "+
		"WorstFixtureGap: %d, ValidGapMin: %d, ValidGapMedian: %d, ValidGapMax: %d}",
		p.Attempts, p.Valid, p.Starved, p.ThresholdNum, p.ThresholdDen,
		p.StatedPace, p.Flushes, p.WorstFixtureGap,
		p.ValidGapMin, p.ValidGapMedian, p.ValidGapMax)
}

// integrityNote renders what a pass says about its own instrument.
//
// THE STARVATION RATE IS PRINTED HERE BECAUSE IT IS NO LONGER A RED.
// While starvation stopped a leg, the only way anybody saw the number
// was a failure, so the quantity existed in the memory of whoever was
// interrupted and nowhere else. It is a column now — every run of every
// paced probe says how noisy the machine was while it produced its
// number, whether or not anybody is looking that day.
func integrityNote(p *timing.PaceIntegrity) string {
	if p.StatedPace == 0 {
		// A WRITE-THEN-SILENT FIXTURE, JUDGED AGAINST ITS BUDGET. This
		// branch used to say the fixture had no pace to hold and that
		// every attempt therefore counted; it now reports the same
		// arithmetic every other row gets, against the measured budget
		// instead of a stated interval.
		return fmt.Sprintf(" the fixture stayed inside its %v flush budget on %d "+
			"passes out of %d attempts (%d went past %d/%d of it and were retaken, "+
			"a starvation rate of %.2f); its own widest gap was %v",
			p.FlushBudget, p.Valid, p.Attempts, p.Starved,
			paceThresholdNum, paceThresholdDen, p.StarvationRate(),
			p.WorstFixtureGap)
	}
	lo, mid, hi := p.Ratios()
	return fmt.Sprintf(" the fixture held its %v pace on %d passes out of %d attempts "+
		"(%d starved past %d/%d of it and were retaken, a starvation rate of %.2f); "+
		"its own widest gap was %v, and the admitted passes ran %.2f/%.2f/%.2f times "+
		"the pace (min/median/max)",
		p.StatedPace, p.Valid, p.Attempts, p.Starved,
		paceThresholdNum, paceThresholdDen, p.StarvationRate(),
		p.WorstFixtureGap, lo, mid, hi)
}

// paceThresholdNum over paceThresholdDen is the multiple of its stated
// pace a fixture may miss by and still have measured the client. See
// classify for the two measured populations it sits between.
const (
	paceThresholdNum = 3
	paceThresholdDen = 1
)

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

	// EVERY ATTEMPT'S GAP IS KEPT, the retaken ones included. The
	// maximum is taken over a subset and which subset is decided after
	// the runs are over; a running maximum cannot be un-taken. See
	// gatherPasses for the loop and classifyPasses for the subset.
	started := time.Now()
	taken, err := gatherPasses(probeRuns, pacing.pause, 0,
		func() (time.Duration, int, error) {
			gap, n, err := oneUploadRun(ctx, store.url, path, pacing.bodySize, transport)
			transport.CloseIdleConnections()
			return gap, n, err
		},
		store.widestDrainPerRequest)
	// THE BUDGET EXPIRING IS THE RESULT; anything else is a broken
	// instrument, and the two are told apart rather than folded
	// together.
	if err != nil && ctx.Err() == nil {
		t.Fatalf("the probe upload failed on attempt %d: %v", len(taken.gaps), err)
	}
	if taken.samples == 0 {
		t.Fatal("the probe recorded no gap at all, so its silence is about an " +
			"instrument that stopped working rather than about a fast machine")
	}
	pinsWereApplied(t, client, store.pin)
	if err != nil {
		reportIncompleteArm(t, entry.Name, taken.valid, probeRuns,
			fmt.Sprintf("worst gap so far %v over %d attempts", taken.worst(), len(taken.gaps)),
			time.Since(started))
		return
	}

	// THE SAME INTEGRITY TEST, ON THE SIDE IT WAS MISSING FROM. The gap
	// this row governs is the time for the client's send buffer to free
	// space, and space frees at exactly the rate this store drains — so
	// a run in which the STORE missed its own pace measured the store,
	// not the client, for the same reason a starved stream fixture
	// measures the runner.
	worst, integrity, err := classifyPasses(entry.Name, probeLeg(), probeRuns,
		taken.gaps, store.widestDrainPerRequest(), pacing.pause, 0, 0)
	if err != nil {
		t.Fatalf("%v", err)
	}

	report(t, entry, integrity.Valid, worst, observedPin(client, store.pin), integrity,
		fmt.Sprintf(" (%d gaps sampled, store pacing %d KiB every %v over a "+
			"%d-byte body, %d of it paced;%s)",
			taken.samples, pacing.chunk>>10, pacing.pause, pacing.bodySize,
			pacing.pacedBytes, integrityNote(integrity)))
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

			// EVERY ATTEMPT'S GAP IS KEPT, not just the running maximum,
			// because the maximum is taken over a SUBSET and which
			// subset is decided after the runs are over. A running
			// maximum cannot be un-taken.
			//
			// THE WRITE-THEN-SILENT CASE PASSES THROUGH THIS UNCHANGED,
			// and it is the same call rather than a branch — but for the
			// opposite reason it used to be. It was "its fixture has no
			// pace to miss, so no attempt is ever starved"; that was the
			// exemption. Its fixture is now judged against the measured
			// flush budget this leg records, so its attempts starve and
			// retake like every other row's, through this one path.
			started := time.Now()
			budget := tc.entry.Measurements[probeLeg()].FlushBudget
			taken, err := gatherPasses(tc.runs, tc.script.pace, budget,
				func() (time.Duration, int, error) { return oneStreamRun(ctx, srv.URL) },
				script.widestPerConnection)
			if err != nil && ctx.Err() == nil {
				t.Fatalf("the probe stream failed on attempt %d: %v", len(taken.gaps), err)
			}
			if taken.samples == 0 {
				t.Fatal("the probe recorded no arrival at all, so its silence is " +
					"about an instrument that stopped working")
			}
			if err != nil {
				reportIncompleteArm(t, tc.entry.Name, taken.valid, tc.runs,
					fmt.Sprintf("worst gap so far %v over %d attempts",
						taken.worst(), len(taken.gaps)),
					time.Since(started))
				return
			}

			// THE FIXTURE'S OWN WIDEST FLUSH GAP, PER RUN, and now acted
			// on rather than only printed. The two rows this probe
			// stands behind each refuse when their fixture paused past
			// the window, saying they measured the machine rather than
			// the client; this is the same test, applied by the
			// instrument to itself.
			worst, integrity, err := classifyPasses(tc.entry.Name, probeLeg(), tc.runs,
				taken.gaps, script.widestPerConnection(), tc.script.pace, budget,
				len(tc.script.frames))
			if err != nil {
				t.Fatalf("%v", err)
			}
			extra := fmt.Sprintf(" (%d arrivals sampled;%s)", taken.samples,
				integrityNote(integrity))
			// NIL PIN, and that is the two mechanisms held apart rather
			// than an omission. Nothing buffers on this client's behalf
			// while it reads: the gap is the far end's pacing plus the
			// scheduler, and no socket buffer this client can set
			// governs it. A pin recorded here would be a condition that
			// had no bearing on the number beside it.
			report(t, tc.entry, integrity.Valid, worst, nil, integrity, extra)
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

// TestAStarvedPassIsNotAMeasurement drives classifyPasses directly,
// because the thing it decides — which passes are allowed to set a
// window — cannot be exercised by waiting for a runner to starve.
//
// THE PROBE'S OWN BEHAVIOUR IS STOCHASTIC AND THIS IS NOT. A starved
// pass happens when a machine happens to pause, so a row that ran the
// real probe and hoped would be green on the days it proved nothing.
// The classifier is a function of two slices and a pace; driven
// directly, every branch it has is reachable on purpose.
//
// REQUIRED MUTATIONS, RUN ON THE TIP 2026-09-11:
//
//  1. Remove the threshold comparison so no pass is ever excluded. The
//     "starved pass does not set the maximum" row reds, reporting the
//     starved run's 900ms where it wants the valid runs' 40ms — which
//     is the registry's darwin window inflating, in miniature and
//     without waiting for a runner.
//  2. Drop the length check. The "pairing" row reds; without it a
//     shorter fixture slice silently classifies the wrong runs.
//
// A THIRD ONE USED TO LIVE HERE, and what happened to it is worth more
// than what it proved. It mutated the one-in-five bound, and the rows it
// reddened asserted that boundary from both sides — a leg starving one
// pass in five passed, one in four was refused. Both rows are gone with
// the rule: a starved pass is retaken now, so a leg starving one in four
// reaches its twenty readings and is recorded as having taken
// twenty-six attempts to do it. See TestAStarvedPassIsRetakenRatherThanCounted,
// which is where that boundary's replacement is asserted.
func TestAStarvedPassIsNotAMeasurement(t *testing.T) {
	const pace = 20 * time.Millisecond
	ms := func(n ...int) []time.Duration {
		out := make([]time.Duration, 0, len(n))
		for _, v := range n {
			out = append(out, time.Duration(v)*time.Millisecond)
		}
		return out
	}

	t.Run("a fixture that held its pace excludes nothing", func(t *testing.T) {
		worst, p, err := classifyPasses("Entry", timing.Darwin, 3,
			ms(30, 40, 25), ms(22, 25, 21), pace, 0, 0)
		if err != nil {
			t.Fatalf("three ordinary passes were refused: %v", err)
		}
		if worst != 40*time.Millisecond {
			t.Errorf("worst = %v, want 40ms — the maximum over every pass", worst)
		}
		if p.Attempts != 3 || p.Valid != 3 || p.Starved != 0 {
			t.Errorf("attempts/valid/starved = %d/%d/%d, want 3/3/0",
				p.Attempts, p.Valid, p.Starved)
		}
		if p.WorstFixtureGap != 25*time.Millisecond {
			t.Errorf("worst fixture gap = %v, want 25ms", p.WorstFixtureGap)
		}
	})

	t.Run("a starved pass does not set the maximum", func(t *testing.T) {
		// The starved run carries the LARGEST client gap, which is the
		// only arrangement that proves anything: if the excluded pass
		// were not the maximum, dropping it would change nothing and
		// the row would pass with the rule removed.
		worst, p, err := classifyPasses("Entry", timing.Darwin, 4,
			ms(30, 40, 900, 25, 35), ms(22, 25, 890, 21, 24), pace, 0, 0)
		if err != nil {
			t.Fatalf("five attempts producing the four valid passes asked for "+
				"were refused: %v", err)
		}
		if worst != 40*time.Millisecond {
			t.Errorf("worst = %v, want 40ms — the 900ms pass measured a fixture "+
				"that had stopped, and a window five times it would be a margin "+
				"over this machine rather than over the client", worst)
		}
		if p.Attempts != 5 || p.Valid != 4 || p.Starved != 1 {
			t.Errorf("attempts/valid/starved = %d/%d/%d, want 5/4/1",
				p.Attempts, p.Valid, p.Starved)
		}
		// The excluded pass is still VISIBLE. Dropping it quietly would
		// hide how far this runner was from being an instrument.
		if p.WorstFixtureGap != 890*time.Millisecond {
			t.Errorf("worst fixture gap = %v, want 890ms — the starved pass is "+
				"excluded from the maximum and recorded anyway", p.WorstFixtureGap)
		}
	})

	t.Run("attempts spent without the passes asked for is the one stop left",
		func(t *testing.T) {
			// Four attempts, one starved, and four valid passes wanted:
			// the attempts are spent and the sample is short.
			_, _, err := classifyPasses("Entry", timing.Darwin, 4,
				ms(30, 900, 25, 35), ms(22, 890, 21, 24), pace, 0, 0)
			if err == nil {
				t.Fatal("three valid passes out of four attempts answered a demand " +
					"for four")
			}
			// BOTH NUMBERS, not a ratio. What the refusal has to hand a
			// reader is what was asked for and what arrived.
			if !strings.Contains(err.Error(), "cannot hold the fixture's pace: 3 valid in 4") {
				t.Errorf("the refusal does not name what arrived and what it cost:\n%v", err)
			}
		})

	t.Run("every pass starved is not a thinner sample", func(t *testing.T) {
		_, _, err := classifyPasses("Entry", timing.Darwin, 2,
			ms(900, 800), ms(890, 790), pace, 0, 0)
		if err == nil {
			t.Fatal("a pass set in which the fixture never held its pace produced a " +
				"measurement")
		}
		if !strings.Contains(err.Error(), "no measurement here to report") {
			t.Errorf("the refusal reads like a bound rather than an absence:\n%v", err)
		}
	})

	t.Run("a write-then-silent fixture is judged against its budget", func(t *testing.T) {
		// THE EXEMPTION THIS ROW USED TO ASSERT. It read "a fixture with
		// no pace has nothing to miss" and wanted 900ms back — the
		// starved run's own gap, admitted as the leg's maximum because
		// no threshold existed to exclude it. That is the defect in
		// miniature: the same shape that let a 35.113375 ms machine
		// pause become a 54.251708 ms client margin on the gate.
		const budget = 11 * time.Millisecond
		worst, p, err := classifyPasses("Entry", timing.Darwin, 2,
			ms(30, 900, 25), ms(22, 890, 21), 0, budget, 0)
		if err != nil {
			t.Fatalf("a write-then-silent fixture was refused: %v", err)
		}
		if worst != 30*time.Millisecond {
			t.Errorf("worst = %v, want 30ms — the maximum over the passes whose "+
				"fixture stayed inside three times its budget, with the 890ms "+
				"pass excluded", worst)
		}
		if p.Starved != 1 || p.Valid != 2 {
			t.Errorf("starved/valid = %d/%d, want 1/2", p.Starved, p.Valid)
		}
		if p.StatedPace != 0 || p.FlushBudget != budget {
			t.Errorf("pace/budget = %v/%v, want 0/%v — the record says which of "+
				"the two the threshold came from", p.StatedPace, p.FlushBudget, budget)
		}
	})

	t.Run("a fixture with neither a pace nor a budget is refused", func(t *testing.T) {
		// NOT A LENIENT READING, THE STRICTEST POSSIBLE ONE. A threshold
		// of nought marks every pass starved, so passing a zero through
		// would not restore the old exemption — it would stop the leg at
		// its cap with no readings at all. The refusal says so rather
		// than letting either behaviour happen by arithmetic.
		_, _, err := classifyPasses("Entry", timing.Darwin, 2,
			ms(30, 900, 25), ms(22, 890, 21), 0, 0, 0)
		if err == nil {
			t.Fatal("a fixture stating no pace, on a leg recording no flush " +
				"budget, was classified anyway — against a threshold of nought")
		}
		if !strings.Contains(err.Error(), "nobody having looked") {
			t.Errorf("the refusal does not say that this is an absent measurement "+
				"rather than a fixture that cannot starve:\n%v", err)
		}
	})

	t.Run("the pairing is checked rather than assumed", func(t *testing.T) {
		_, _, err := classifyPasses("Entry", timing.Darwin, 3,
			ms(30, 40, 25), ms(22, 25), pace, 0, 0)
		if err == nil {
			t.Fatal("three runs were classified against two fixture records")
		}
		if !strings.Contains(err.Error(), "BY POSITION") {
			t.Errorf("the refusal does not name the assumption that failed:\n%v", err)
		}
	})
}

// pacedRunner is a probe's two halves faked: an attempt that always
// succeeds, and a fixture that starves on a FIXED CADENCE.
//
// # A ROW WHOSE OUTCOME DEPENDS ON THE HOST'S SCHEDULER IS NOT A ROW
//
// The behaviour under test is what happens when a machine starves the
// fixture, and a machine starves it when it feels like it. A row that
// ran the real probe and hoped would be green on the days it proved
// nothing, and the whole subject of this change is a suite that reds on
// a distribution rather than on a defect — proving it with a stochastic
// row would be the same mistake one level up. So the cadence is a
// function of the attempt number: three valid then one starved is
// exactly three valid then one starved, every run, on every leg.
//
// THE STARVED ATTEMPTS CARRY THE LARGEST CLIENT GAP, which is the only
// arrangement that proves anything about the maximum. If the thrown-away
// attempts were not the biggest numbers in the set, dropping them would
// change nothing and the rows below would pass with the rule removed.
type pacedRunner struct {
	pace time.Duration
	// starves decides, from the one-based attempt number, whether this
	// attempt's fixture missed its pace. It is a predicate rather than a
	// count so that one in four and three in four are the same fake.
	starves func(attempt int) bool

	attempts int
	fixture  []time.Duration
}

// attempt is what gatherPasses calls, in the shape the real per-run
// functions have: a client gap, the progress events behind it, and what
// went wrong.
func (r *pacedRunner) attempt() (time.Duration, int, error) {
	r.attempts++
	if r.starves != nil && r.starves(r.attempts) {
		// Far past the threshold on the fixture side, and the largest
		// client gap in the set on the other.
		r.fixture = append(r.fixture, 45*r.pace)
		return 900 * time.Millisecond, 1, nil
	}
	// Comfortably inside the threshold: the ordinary passes on real
	// hardware sit near 1.1 times the stated pace.
	r.fixture = append(r.fixture, r.pace+r.pace/10)
	return 40 * time.Millisecond, 1, nil
}

// gaps is the fixture's own record, in the shape the real stores and
// scripts hand back: a copy, one entry per attempt served.
func (r *pacedRunner) gaps() []time.Duration {
	return append([]time.Duration(nil), r.fixture...)
}

// TestAStarvedPassIsRetakenRatherThanCounted drives the run loop the
// probes share, because what it decides — whether a spoiled reading
// costs the sample a reading — cannot be exercised by waiting for a
// runner to starve.
//
// It is the boundary the one-in-five bound used to hold, asserted from
// both sides of its replacement: a runner starving one attempt in four
// is noisy and produces a full sample, and a runner starving three in
// four spends everything it is given and produces a stop naming both
// numbers.
//
// REQUIRED MUTATIONS, RUN ON THE TIP 2026-09-11:
//
//  1. THE RETAKE REMOVED — gatherPasses counts attempts where it counts
//     valid passes, so the loop stops at twenty attempts rather than at
//     twenty readings that measured something:
//
//     the loop spent 20 attempts, want 26
//     the loop gathered 15 valid passes, want 20
//     … cannot hold the fixture's pace: 15 valid in 20
//
//     A maximum over three quarters of the sample it asked for, and a
//     STOP on a runner the same row shows is perfectly capable of
//     producing twenty readings. That is the mutation that proves this
//     change is the RETAKE and not a looser threshold, and the threshold
//     is untouched on both sides of it. The three-in-four row reds with
//     it, unpredicted and correctly: it is a claim about the same loop.
//
//  2. The cap moved — attemptCap returning want*3. The three-in-four row
//     reds alone, with "15 valid in 60" where it wants "10 valid in 40".
//
//     AND THE ROW'S OWN CAP ASSERTION STAYED GREEN THROUGH IT, which is
//     the part worth keeping. That line compares the attempts spent
//     against attemptCap, so it moved with the mutation and could not
//     see it: a check derived from the thing it checks agrees with it by
//     construction. What caught this was the two numbers written out
//     longhand in the stop's text, ten and forty, which is the same
//     reason the refusal names both rather than a ratio.
func TestAStarvedPassIsRetakenRatherThanCounted(t *testing.T) {
	const pace = 20 * time.Millisecond

	t.Run("one attempt in four starved still reaches twenty valid passes",
		func(t *testing.T) {
			// Three valid, then one starved, repeating.
			runner := &pacedRunner{pace: pace,
				starves: func(attempt int) bool { return attempt%4 == 0 }}

			taken, err := gatherPasses(probeRuns, pace, 0, runner.attempt, runner.gaps)
			if err != nil {
				t.Fatalf("a runner starving one attempt in four was refused: %v", err)
			}
			// TWENTY-SIX, and the number is written out rather than
			// computed here. Six of the first twenty-six attempts starve
			// and twenty do not, so the twentieth valid reading lands on
			// the twenty-sixth attempt — inside a cap of forty, which is
			// the whole claim.
			if len(taken.gaps) != 26 {
				t.Errorf("the loop spent %d attempts, want 26: three valid then one "+
					"starved reaches twenty valid readings on the twenty-sixth",
					len(taken.gaps))
			}
			if taken.valid != probeRuns {
				t.Errorf("the loop gathered %d valid passes, want %d — a starved "+
					"attempt is retaken, so it does not cost the sample a reading",
					taken.valid, probeRuns)
			}

			worst, p, err := classifyPasses("Entry", timing.Darwin, probeRuns,
				taken.gaps, runner.gaps(), pace, 0, 0)
			if err != nil {
				t.Fatalf("twenty valid passes in twenty-six attempts were refused: %v", err)
			}
			if p.Attempts != 26 || p.Valid != 20 || p.Starved != 6 {
				t.Errorf("attempts/valid/starved = %d/%d/%d, want 26/20/6",
					p.Attempts, p.Valid, p.Starved)
			}
			if worst != 40*time.Millisecond {
				t.Errorf("worst = %v, want 40ms — the six thrown-away attempts each "+
					"carried 900ms, and a window five times that would be a margin "+
					"over this machine rather than over the client", worst)
			}
			// THE DENOMINATOR IS THE ATTEMPTS AND NOT THE PASSES, which
			// is the assertion this row exists for as much as the count.
			// The two agreed while nothing retook: six starved out of a
			// fixed twenty is 0.30 and six out of twenty-six attempts is
			// 0.23, and only the second is a statement about how often
			// this runner failed to be an instrument.
			if got, want := p.StarvationRate(), 6.0/26.0; got != want {
				t.Errorf("starvation rate = %.4f, want %.4f", got, want)
			}
			if p.StarvationRate() == 6.0/float64(probeRuns) {
				t.Error("the starvation rate divided by the passes asked for rather " +
					"than the attempts spent, which is a fraction of the wrong thing " +
					"the moment anything is retaken")
			}
		})

	t.Run("three attempts in four starved stops at the cap", func(t *testing.T) {
		// One valid in every four attempts.
		runner := &pacedRunner{pace: pace,
			starves: func(attempt int) bool { return attempt%4 != 1 }}

		taken, err := gatherPasses(probeRuns, pace, 0, runner.attempt, runner.gaps)
		if err != nil {
			t.Fatalf("the loop failed rather than spending its attempts: %v", err)
		}
		if len(taken.gaps) != attemptCap(probeRuns) {
			t.Errorf("the loop spent %d attempts against a cap of %d",
				len(taken.gaps), attemptCap(probeRuns))
		}
		if taken.valid != 10 {
			t.Errorf("the loop gathered %d valid passes in forty attempts, want 10",
				taken.valid)
		}

		_, _, err = classifyPasses("Entry", timing.Darwin, probeRuns,
			taken.gaps, runner.gaps(), pace, 0, 0)
		if err == nil {
			t.Fatal("ten valid readings answered a demand for twenty")
		}
		// THE STOP NAMES BOTH NUMBERS RATHER THAN A RATIO, so a reader
		// gets what was asked for, what arrived and what it cost without
		// having to know the cap.
		if !strings.Contains(err.Error(), "cannot hold the fixture's pace: 10 valid in 40") {
			t.Errorf("the stop does not say what arrived and what it cost:\n%v", err)
		}
	})

	t.Run("a write-then-silent fixture retakes and spends like any other",
		func(t *testing.T) {
			// THE OPPOSITE OF WHAT THIS ROW USED TO ASSERT, and the
			// inversion is the ruling. It read "an unpaced fixture
			// retakes nothing and spends nothing" and wanted five
			// attempts for five runs with every fixture gap far past any
			// threshold — because there was no threshold. There is one
			// now: the leg's measured flush budget, so these attempts
			// starve, are retaken, and spend the cap like every other
			// row's.
			runner := &pacedRunner{pace: pace, starves: func(int) bool { return true }}

			const want = 5
			const budget = 11 * time.Millisecond
			taken, err := gatherPasses(want, 0, budget, runner.attempt, runner.gaps)
			if err != nil {
				t.Fatalf("a write-then-silent fixture was refused: %v", err)
			}
			if len(taken.gaps) != attemptCap(want) {
				t.Errorf("%d attempts against a cap of %d — a fixture starving "+
					"every attempt must spend the budget rather than stopping at "+
					"what it asked for", len(taken.gaps), attemptCap(want))
			}
			if taken.valid != 0 {
				t.Errorf("%d valid passes, want 0 — every fixture gap here is past "+
					"three times the budget", taken.valid)
			}
			if cap := attemptCap(want); cap != want*retakeBudget {
				t.Errorf("a write-then-silent probe was given a retake budget of %d "+
					"against %d runs asked for", cap, want)
			}
		})

	t.Run("the excursion that ejected this branch is starved", func(t *testing.T) {
		// THE REAL NUMBERS, FROM THE RUN THAT CAUSED THIS RULING.
		// 2026-09-11, merge-group CI, macOS: StreamGoesQuiet's fixture
		// paused 35.113375 ms against a leg whose routine widest is
		// 56 µs to 11.010375 ms, and the client's arrival gap came back
		// 54.251708 ms against a 55 ms window. The pass counted, and the
		// margin floor reported the ROW as passing on a margin it did
		// not have.
		const (
			excursionFixture = 35113375 * time.Nanosecond
			excursionGap     = 54251708 * time.Nanosecond
			ordinaryFixture  = 10053541 * time.Nanosecond
			ordinaryGap      = 10675125 * time.Nanosecond
		)
		entry := timing.StreamGoesQuiet
		budget := entry.Measurements[timing.Darwin].FlushBudget
		if budget == 0 {
			t.Fatal("the darwin leg records no flush budget, so this row is " +
				"asserting against nothing")
		}

		worst, p, err := classifyPasses(entry.Name, timing.Darwin, 1,
			[]time.Duration{ordinaryGap, excursionGap},
			[]time.Duration{ordinaryFixture, excursionFixture},
			0, budget, 0)
		if err != nil {
			t.Fatalf("the two real passes were refused: %v", err)
		}
		if p.Starved != 1 || p.Valid != 1 {
			t.Errorf("starved/valid = %d/%d, want 1/1 — the 35.113375ms fixture "+
				"pause is %.2f× the %v budget and the threshold is %d/%d",
				p.Starved, p.Valid, float64(excursionFixture)/float64(budget),
				budget, paceThresholdNum, paceThresholdDen)
		}
		if worst != ordinaryGap {
			t.Errorf("worst = %v, want %v — the excursion's own gap must not be "+
				"the leg's maximum", worst, ordinaryGap)
		}

		// AND THE CONSEQUENCE, ASSERTED RATHER THAN DESCRIBED. The
		// margin floor errors when a measurement times marginFloorNum
		// exceeds the window times marginFloorDen. The excluded reading
		// clears it; the excursion does not, which is exactly the red
		// that ejected a green pull request from the merge queue.
		if worst*marginFloorNum > entry.Window*marginFloorDen {
			t.Errorf("the admitted maximum %v is inside the margin floor's band "+
				"against a %v window, so excluding the excursion did not buy the "+
				"row its margin back", worst, entry.Window)
		}
		if excursionGap*marginFloorNum <= entry.Window*marginFloorDen {
			t.Errorf("the excursion's %v would NOT have tripped the margin floor "+
				"against a %v window, so this row is not about the failure it "+
				"names", excursionGap, entry.Window)
		}
	})

	t.Run("a fixture record that has stopped pairing is refused", func(t *testing.T) {
		// The fixture records nothing at all, so there is no record of
		// the attempt the loop is about to judge.
		silent := func() []time.Duration { return nil }
		runner := &pacedRunner{pace: pace}
		_, err := gatherPasses(probeRuns, pace, 0, runner.attempt, silent)
		if err == nil {
			t.Fatal("the loop judged an attempt the fixture has no record of")
		}
		if !strings.Contains(err.Error(), "BY POSITION") {
			t.Errorf("the refusal does not name the assumption that failed:\n%v", err)
		}
	})
}
