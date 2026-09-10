package timing_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/timing"
)

// sortedEntries is the registry in a stable order, so a run's output
// reads the same way twice and a red is diffable against the last one.
func sortedEntries() []*timing.Entry {
	names := make([]string, 0, len(timing.Registry))
	for name := range timing.Registry {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]*timing.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, timing.Registry[name])
	}
	return entries
}

// TestEveryRegisteredWindowIsMeasuredOnEveryLeg is the measurement half
// of the guard.
//
// IT IS A ROW OF ITS OWN, separate from the absence row next door,
// because they are two failures: a window nobody registered, and a
// registered window nobody measured. An implementation catching only the
// first would pass a single combined row, and a number that has never
// been measured on a leg would go on quietly passing there.
//
// A SINGLE MACHINE'S CLEAN RUN IS ONE LEG'S EVIDENCE and is recorded as
// that. Socket buffer sizes, their autotuning and scheduling granularity
// belong to the kernel and the runner; the three legs this project gates
// on are three kernels and three runners, so a number from one of them
// says nothing about the other two. This row is what stops the silence
// on the other two reading like a pass.
//
// REQUIRED MUTATION, RUN 2026-09-09, and it needed a step the brief did
// not name. While a leg is genuinely pending this row is red anyway, so
// blanking a measurement changes nothing visible — the mutation was run
// against a registry temporarily filled on all three legs, which first
// established that this row CAN be green. Blanking darwin on one entry
// then reds it alone:
//
//	timing.StreamGoesQuiet (a 100ms window on the read side) has no
//	measurement on: darwin.
//
// The absence row and every other row here stayed green, because the
// entry is still registered and every site still resolves. Both the fill
// and the blanking were reverted by preserved copy and checksum.
//
// REQUIRED MUTATIONS FOR THE FLOOR AND THE DATE, RUN 2026-09-10, each
// reverted. Both of these counted as evidence before this round:
//
//  1. Set StreamGoesQuiet's darwin run count to 19, one short of
//     MinimumRuns. This row reds, naming darwin alongside the two legs
//     that are genuinely pending.
//  2. Set StreamKeepAlivesAreProofOfLife's darwin date to "recently".
//     Same red, same entry — a string that is merely not empty passes an
//     emptiness test and cannot be compared with anything.
//
// TestEveryMeasurementSaysWhetherItsFixtureHeldItsPace is the registry
// half of the probe's integrity test.
//
// A MAXIMUM IS A MAXIMUM OVER THE PASSES A RULE LET IN, so the rule
// travels with the number. Three things are asserted and each has a way
// of being silently wrong:
//
//  1. A measured leg carries an Integrity record. A nil one is not "the
//     fixture was fine", it is nobody having looked — and every number
//     recorded before this rule existed was taken by an instrument that
//     could not tell a starved pass from a measurement.
//  2. Its Valid count is the run count behind WorstGap. A record whose
//     Runs disagree with the passes that were allowed to contribute is
//     a maximum over one population labelled with another's size.
//  3. It did not starve more often than the rule allows. Past one pass
//     in five the leg is a STOP with a reason, not a number.
//
// REQUIRED MUTATIONS, RUN ON THE TIP:
//
//  1. Drop the Integrity from one measured leg. Reds here alone, naming
//     the entry and the leg.
//  2. Set a leg's Starved past a fifth of its passes. Reds here with the
//     runner-cannot-hold-the-pace wording, and nothing else moves.
//  3. Remove the threshold from the probe so a starved pass enters the
//     maximum. Reds on the margin row instead, because the darwin window
//     inflates past what its own record supports.
func TestEveryMeasurementSaysWhetherItsFixtureHeldItsPace(t *testing.T) {
	for _, entry := range sortedEntries() {
		for _, leg := range timing.Legs {
			m := entry.Measurements[leg]
			// A LEG WITH NOTHING RECORDED IS THE OTHER ROW'S BUSINESS.
			// This one is about a number that EXISTS and does not say
			// what admitted it — which is not the same absence, and
			// naming it here is what tells a reader why an entry they
			// can see a figure for is reported as unmeasured.
			if m.WorstGap <= 0 || m.Runs < timing.MinimumRuns {
				continue
			}
			p := m.Integrity
			if p == nil {
				t.Errorf("timing.%s's %s measurement does not say whether its "+
					"fixture held its pace.\nA worst gap is a maximum over the "+
					"passes some rule admitted, and with no record of that rule "+
					"this number could be the runner's own starvation wearing the "+
					"client's name — which is exactly what it was on darwin at "+
					"315.9ms against a stated 20ms pace. Retake it with the probe "+
					"on this tip, which reports the record to paste.",
					entry.Name, leg)
				continue
			}
			if p.Valid != m.Runs {
				t.Errorf("timing.%s's %s measurement stands on %d runs and its "+
					"integrity record admitted %d passes. Those are the same "+
					"number or the record is a maximum over one population "+
					"labelled with the size of another.",
					entry.Name, leg, m.Runs, p.Valid)
			}
			if p.Starves() {
				t.Errorf("timing.%s's %s runner cannot hold the fixture's pace: %d "+
					"of %d passes starved, worst fixture gap %v against a stated "+
					"%v.\nThat is a STOP with a reason rather than a measurement. "+
					"A maximum over the few passes a busy machine did not starve "+
					"is a number about its quiet moments.",
					entry.Name, leg, p.Starved, p.Valid+p.Starved,
					p.WorstFixtureGap, p.StatedPace)
			}
			if p.ThresholdNum <= 0 || p.ThresholdDen <= 0 {
				t.Errorf("timing.%s's %s integrity record has no threshold (%d/%d), "+
					"so it says passes were excluded without saying by what rule",
					entry.Name, leg, p.ThresholdNum, p.ThresholdDen)
			}
		}
	}
}

func TestEveryRegisteredWindowIsMeasuredOnEveryLeg(t *testing.T) {
	if len(timing.Registry) == 0 {
		t.Fatal("the registry is empty, so this row's silence is about nothing " +
			"rather than about a measured tree")
	}

	for _, entry := range sortedEntries() {
		missing := entry.UnmeasuredLegs()
		if len(missing) == 0 {
			continue
		}
		legs := make([]string, 0, len(missing))
		for _, leg := range missing {
			legs = append(legs, string(leg))
		}
		t.Errorf("timing.%s (a %v window on the %s side) has no measurement on: %s.\n"+
			"That window is unproved on those legs. It is measured by running "+
			"this repository's suite there — the probe beside %s reports the worst "+
			"gap it saw — and recording the number, the run count and the date in "+
			"the entry.",
			entry.Name, entry.Window, entry.Side, strings.Join(legs, ", "), entry.Row)
	}

	// THE POSITIVE CONTROL, and this row needs one badly: while two legs
	// are pending it fails, and a check that has never been seen to pass
	// is a check nobody can tell apart from one that always fails.
	held := &timing.PaceIntegrity{Valid: 20, ThresholdNum: 3, ThresholdDen: 1}
	fully := &timing.Entry{
		Name: "PositiveControl",
		Measurements: map[timing.Leg]timing.Measurement{
			timing.Linux:   {WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-09", Integrity: held},
			timing.Darwin:  {WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-09", Integrity: held},
			timing.Windows: {WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-09", Integrity: held},
		},
	}
	if missing := fully.UnmeasuredLegs(); len(missing) != 0 {
		t.Errorf("an entry measured on all three legs was reported unmeasured on "+
			"%v, so this row refuses everything and its reds mean nothing", missing)
	}

	// AND IT MUST STILL SAY NO. A "measured" test satisfied by any value
	// at all would pass the control above and never red on a real gap.
	//
	// EVERY CASE BELOW CARRIES A SOUND INTEGRITY RECORD EXCEPT THE ONE
	// ABOUT INTEGRITY, and that is not decoration. When the pace record
	// became part of what a measurement IS, every case here started
	// failing for the new reason instead of the one it names — nine rows
	// still green, none of them testing what its name says any more.
	// Handing each one a held integrity puts the defect it names back to
	// being the only defect it has.
	for _, tc := range []struct {
		name string
		m    timing.Measurement
	}{
		{"no gap", timing.Measurement{Runs: 20, Date: "2026-09-09", Integrity: held}},
		{"no run count", timing.Measurement{WorstGap: time.Millisecond, Date: "2026-09-09", Integrity: held}},
		{"no date", timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Integrity: held}},
		{"nothing at all", timing.Measurement{}},
		// The floor and the date. One run under the floor is not a
		// distribution, and a date that does not parse cannot be
		// compared with anything — which is the only thing a date is
		// for. Both of these were evidence before this round.
		{"one run short of the floor",
			timing.Measurement{WorstGap: time.Millisecond, Runs: timing.MinimumRuns - 1, Date: "2026-09-09", Integrity: held}},
		{"a single run", timing.Measurement{WorstGap: time.Millisecond, Runs: 1, Date: "2026-09-09", Integrity: held}},
		{"a date that is a word", timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "recently", Integrity: held}},
		{"a date that is not a date", timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-13-45", Integrity: held}},
		{"a date in another format", timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "09/09/2026", Integrity: held}},
		// The new one. A number with everything else right and no record
		// of which passes were allowed to set it is a maximum over an
		// unknown population.
		{"no record of whether its fixture held its pace",
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-09"}},
	} {
		if tc.m.Measured() {
			t.Errorf("a measurement with %s counted as evidence", tc.name)
		}
	}
}

// carryProblem reports what is wrong with an entry's carried
// measurement, or "" when the carry is sound. It takes the registry it
// should resolve the source against, so the row below can run it over
// the real one and over a bench of its own.
//
// It is a FUNCTION rather than a loop body because the real registry is
// expected to be clean: run only over that, this check can show that it
// says yes and never that it can say no.
func carryProblem(entry *timing.Entry, in map[string]*timing.Entry) string {
	if entry.Carried == nil {
		return ""
	}
	if entry.Carried.From == "" {
		return "carries a measurement from nowhere"
	}
	source := in[entry.Carried.From]
	if source == nil {
		return "carries from " + entry.Carried.From + ", which is not in the registry"
	}
	if strings.TrimSpace(entry.Carried.Reason) == "" {
		return "carries its measurement from " + source.Name + " and gives no reason. " +
			"A constant carried between two places carries its number, not the reason " +
			"the number was chosen: say what makes the two rows one mechanism — the " +
			"same side, the same reader, the same quantity being bounded — or measure " +
			"this row on its own"
	}
	// A CARRIED MEASUREMENT IS CARRIED ALONG ONE MECHANISM. The two this
	// repository has are read and write; they are measured by different
	// methods through different readers, so a number crossing between
	// them is a number about something else.
	if source.Side != entry.Side {
		return "is a " + string(entry.Side) + "-side window carrying from " +
			source.Name + ", which is " + string(source.Side) + " side: the two are " +
			"different quantities measured by different methods"
	}
	if source.Instrument != entry.Instrument {
		return "carries from " + source.Name + " through a different reader (" +
			entry.Instrument + " against " + source.Instrument + ")"
	}
	// CARRY MEANS EQUAL, and for a while nothing here checked that it
	// did. A carried entry named a source, gave a reason, matched its
	// side and its reader, and was then free to hold any window and any
	// measurement it liked: a source at 2s beside a carrier at 1s passed
	// every guard in this package, measured. That is not a carry, it is
	// two windows with a note attached — and the note is the part a
	// reader trusts.
	//
	// The WINDOW first, because it is the thing rows actually use.
	if source.Window != entry.Window {
		return "carries from " + source.Name + " and does not share its window (" +
			entry.Window.String() + " against " + source.Window.String() + "). A carried " +
			"measurement is the same measurement, so the margin over it is the same " +
			"margin: two windows citing one gap are two claims, and only one of them " +
			"can be five times it"
	}
	// Then the EVIDENCE, leg by leg. A carrier holding a number its
	// source does not hold has measured something, somewhere, and
	// labelled it a carry — which is the defect this package exists
	// against wearing the one name that is allowed through.
	for _, leg := range timing.Legs {
		from, mine := source.Measurements[leg], entry.Measurements[leg]
		if reflect.DeepEqual(from, mine) {
			continue
		}
		return "carries from " + source.Name + " and records a different measurement on " +
			string(leg) + " (" + measurementText(mine) + " against " + source.Name + "'s " +
			measurementText(from) + "). Carrying is sharing one run's evidence, not " +
			"agreeing to have some of one's own"
	}
	return ""
}

// measurementText renders a leg's evidence for a refusal a reader can
// act on without opening the registry.
func measurementText(m timing.Measurement) string {
	if !m.Measured() {
		return "nothing measured"
	}
	text := m.WorstGap.String() + " over " + strconv.Itoa(m.Runs) + " runs on " + m.Date
	if m.BlockPoint > 0 {
		text += ", block point " + strconv.FormatInt(m.BlockPoint, 10)
	}
	if m.Pin != nil {
		text += ", pinned " + strconv.Itoa(m.Pin.Send.ReadBack) + "/" +
			strconv.Itoa(m.Pin.Receive.ReadBack)
	}
	return text
}

// TestACarriedMeasurementRecordsItsReason.
//
// Carrying is allowed WITH ITS REASON. Rows sharing a mechanism may
// share a measurement, and the argument for why it transfers is how a
// measurement is meant to be used — a value that is right for one window
// can be impossible for another, and nothing about the copy will say so.
// Carrying the number alone is the thing this package exists against.
//
// REQUIRED MUTATION, RUN 2026-09-09: delete the Reason from the one
// carried entry. Reds, alone:
//
//	timing.UploadWedgedStops carries its measurement from
//	UploadSlowIsNotStalled and gives no reason. A constant carried
//	between two places carries its number, not the reason the number was
//	chosen …
//
// The entry it carries FROM stays green, because it carries nothing.
//
// REQUIRED MUTATIONS FOR THE TWO CLAUSES BELOW, RUN 2026-09-10, each
// reverted. Before them, a carrier could name a source, give a reason,
// match its side and its reader, and then hold any window and any
// evidence it liked — which is two rows agreeing to look like one.
//
//  1. Halve UploadWedgedStops's window. Reds here:
//     "carries from UploadSlowIsNotStalled and does not share its window
//     (400ms against 800ms)". It ALSO reds
//     TestEveryWindowClearsTheMinimumMarginOverItsSlowestLeg, which the
//     prediction did not say and which is correct: 400 ms is under five
//     times the gap it cites. Two guards seeing one edit from two sides.
//  2. Give UploadWedgedStops a worst gap of its own. Reds here alone,
//     printing both measurements so a reader can see which half moved.
//
// And a third, from the pin row next door, arrived here unpredicted:
// blanking a read-back on ONE of the two entries makes their
// measurements differ, so this row reds too. That is the carry rule
// doing exactly what it says — a carried measurement includes the
// condition it was taken under.
func TestACarriedMeasurementRecordsItsReason(t *testing.T) {
	for _, entry := range sortedEntries() {
		if problem := carryProblem(entry, timing.Registry); problem != "" {
			t.Errorf("timing.%s %s", entry.Name, problem)
		}
	}

	// THE BENCH. Every row asserting an absence also asserts a presence:
	// without the accepted case below, a check that refused every carry
	// would pass the loop above by making the registry red, and a check
	// that accepted everything would pass it by saying nothing.
	evidence := map[timing.Leg]timing.Measurement{
		timing.Linux: {WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10", BlockPoint: 4096,
			Pin: &timing.PinnedPair{
				Send:    timing.Pin{Requested: 16384, ReadBack: 32768},
				Receive: timing.Pin{Requested: 16384, ReadBack: 32768},
			}},
	}
	sound := &timing.Entry{
		Name: "Carrier", Side: timing.Write, Instrument: "the same reader",
		Window: time.Second, Measurements: evidence,
		Carried: &timing.Carried{From: "Source", Reason: "same side, same reader, one mechanism"},
	}
	source := &timing.Entry{
		Name: "Source", Side: timing.Write, Instrument: "the same reader",
		Window: time.Second, Measurements: evidence,
	}
	bench := map[string]*timing.Entry{"Source": source, "Carrier": sound}

	if problem := carryProblem(sound, bench); problem != "" {
		t.Errorf("a carry with a reason, on one side, through one reader, was "+
			"refused: %s — this check refuses everything and its reds mean nothing",
			problem)
	}
	if problem := carryProblem(source, bench); problem != "" {
		t.Errorf("an entry that carries nothing was refused: %s", problem)
	}

	for _, tc := range []struct {
		name  string
		entry *timing.Entry
	}{
		{"no reason", &timing.Entry{
			Name: "C", Side: timing.Write, Instrument: "the same reader",
			Carried: &timing.Carried{From: "Source"}}},
		{"a reason of whitespace", &timing.Entry{
			Name: "C", Side: timing.Write, Instrument: "the same reader",
			Carried: &timing.Carried{From: "Source", Reason: "   \n\t"}}},
		{"a source that is not registered", &timing.Entry{
			Name: "C", Side: timing.Write, Instrument: "the same reader",
			Carried: &timing.Carried{From: "Nowhere", Reason: "because"}}},
		{"across the two sides", &timing.Entry{
			Name: "C", Side: timing.Read, Instrument: "the same reader",
			Carried: &timing.Carried{From: "Source", Reason: "because"}}},
		{"through a different reader", &timing.Entry{
			Name: "C", Side: timing.Write, Instrument: "another reader",
			Window: time.Second, Measurements: evidence,
			Carried: &timing.Carried{From: "Source", Reason: "because"}}},
		// THE TWO ADDED WITH THE EQUALITY RULE, and both passed
		// everything before it.
		{"half the source's window", &timing.Entry{
			Name: "C", Side: timing.Write, Instrument: "the same reader",
			Window: 500 * time.Millisecond, Measurements: evidence,
			Carried: &timing.Carried{From: "Source", Reason: "because"}}},
		{"a measurement of its own", &timing.Entry{
			Name: "C", Side: timing.Write, Instrument: "the same reader",
			Window: time.Second,
			Measurements: map[timing.Leg]timing.Measurement{
				timing.Linux: {WorstGap: 2 * time.Millisecond, Runs: 40, Date: "2026-09-10",
					BlockPoint: 4096, Pin: evidence[timing.Linux].Pin},
			},
			Carried: &timing.Carried{From: "Source", Reason: "because"}}},
	} {
		if problem := carryProblem(tc.entry, bench); problem == "" {
			t.Errorf("a carry with %s was accepted", tc.name)
		}
	}
}

// TestEveryWindowClearsTheMinimumMarginOverItsSlowestLeg applies the
// sizing rule where the numbers are.
//
// The margin is taken against the SLOWEST measured leg — not the average
// and not the machine the author is sitting at — because a window is
// only as good as its worst environment. An entry with nothing measured
// yet is skipped here and reds in the measurement row instead; it is one
// failure, and reporting it twice would make the second report look like
// a second problem.
func TestEveryWindowClearsTheMinimumMarginOverItsSlowestLeg(t *testing.T) {
	checked := 0
	for _, entry := range sortedEntries() {
		leg, ok := entry.SlowestMeasuredLeg()
		if !ok {
			continue
		}
		checked++
		worst := entry.Measurements[leg].WorstGap
		if need := timing.MinimumMargin * worst; entry.Window < need {
			t.Errorf("timing.%s has a %v window against a worst gap of %v on %s, "+
				"which is under the %d× minimum (%v). Raise the window and name the "+
				"leg that set it — do not lower the rule.",
				entry.Name, entry.Window, worst, leg, timing.MinimumMargin, need)
		}
		if entry.SetBy != leg {
			t.Errorf("timing.%s says it was set by %s, but its slowest measured leg "+
				"is %s. The value cites the leg that set it, and that is the slowest "+
				"one measured.", entry.Name, entry.SetBy, leg)
		}
	}
	if checked == 0 {
		t.Log("no entry has a measured leg yet, so the sizing rule had nothing to " +
			"apply — the measurement row is where that is reported")
	}
}

// TestEveryEntryNamesItsSideQuantityInstrumentAndRow. An entry with a
// number and no account of what the number is about is the state this
// package was built to end.
func TestEveryEntryNamesItsSideQuantityInstrumentAndRow(t *testing.T) {
	for _, entry := range sortedEntries() {
		if entry.Window <= 0 {
			t.Errorf("timing.%s has no window", entry.Name)
		}
		if entry.Side != timing.Read && entry.Side != timing.Write {
			t.Errorf("timing.%s has side %q, which is neither read nor write",
				entry.Name, entry.Side)
		}
		if strings.TrimSpace(entry.Governs) == "" {
			t.Errorf("timing.%s does not name the quantity its gap is set by, which "+
				"is the one thing a window has to be sized against", entry.Name)
		}
		if strings.TrimSpace(entry.Instrument) == "" {
			t.Errorf("timing.%s does not name the reader it is measured through",
				entry.Name)
		}
		if strings.TrimSpace(entry.Row) == "" {
			t.Errorf("timing.%s does not name the row it governs", entry.Name)
		}
	}
}

// TestABlockPointIsRecordedOnTheWriteSideAndNowhereElse holds the two
// mechanisms apart in the data.
//
// The write side blocks: there is a number of bytes the client hands
// over before it stops making progress at all, and a fixture smaller
// than it measures nothing. The read side does not: nothing is buffering
// on this client's behalf, so there is no such number and one recorded
// here would be an invention dressed as evidence.
func TestABlockPointIsRecordedOnTheWriteSideAndNowhereElse(t *testing.T) {
	for _, entry := range sortedEntries() {
		for _, leg := range timing.Legs {
			m := entry.Measurements[leg]
			if !m.Measured() {
				continue
			}
			switch entry.Side {
			case timing.Write:
				if m.BlockPoint <= 0 {
					t.Errorf("timing.%s is a write-side window measured on %s with no "+
						"block point. Without it nobody can tell whether the fixture "+
						"was ever large enough to make the client block, and a row "+
						"that never blocked measured nothing at all.",
						entry.Name, leg)
				}
			case timing.Read:
				if m.BlockPoint != 0 {
					t.Errorf("timing.%s is a read-side window and records a block "+
						"point of %d on %s. Nothing buffers on this client's behalf "+
						"while it reads, so that number is about something else.",
						entry.Name, m.BlockPoint, leg)
				}
			}
		}
	}
}

// paceProblem reports what is wrong with one entry's recorded fixture
// pace, or "" when there is nothing wrong with it.
//
// It is a FUNCTION rather than a loop body for the reason carryProblem
// and pinProblem are: the real registry is expected to be clean, so run
// only over that, this check can be seen to say yes and never to say no.
func paceProblem(entry *timing.Entry) string {
	// THE TWO MECHANISMS ARE HELD APART IN THE DATA, in both directions,
	// exactly as the block point and the socket-buffer pin are. On the
	// write side the gap is a send buffer draining; no pace this
	// repository states governs it, and one recorded there would be a
	// fact about something else sitting where a reader will take it for
	// evidence.
	if entry.Side == timing.Write {
		if entry.Pace != nil {
			return "is a write-side window recording a fixture pace. The gap it bounds is " +
				"the time for a send buffer to free space, which is set by the far end's " +
				"drain rate and by socket buffers — a pace written here is a condition " +
				"with no bearing on the number beside it"
		}
		return ""
	}
	// A READ-SIDE ENTRY WITH NO PACE AT ALL IS "NOBODY SAID", and that
	// is a different thing from a fixture that does not pace. The gap a
	// read-side window bounds IS the fixture's own pause between
	// flushes, measured to within a millisecond across twenty-one
	// passes, so an entry recording the gap and not the pace has
	// recorded half of one fact.
	if entry.Pace == nil {
		return "is a read-side window with no record of the pace its gap was measured " +
			"under. That is not 'unpaced', it is nobody saying: the gap a read-side " +
			"window bounds is the fixture's own pause between flushes, so a number " +
			"recorded without it is half a fact"
	}
	// A FIXTURE THAT NEVER FLUSHES PRODUCES NO ARRIVAL, so there is no
	// interval for anything to be a margin over. Zero here is the shape
	// a struct literal takes when somebody filled in the interval and
	// stopped.
	if entry.Pace.Flushes <= 0 {
		return "records a fixture that flushes " + strconv.Itoa(entry.Pace.Flushes) +
			" time(s), which delivers nothing — there is no interval between arrivals " +
			"for this window to be a margin over"
	}
	if entry.Pace.Interval < 0 {
		return "records a negative fixture pace, which is not a pause any fixture can take"
	}
	// AN UNPACED FIXTURE FLUSHES AND STOPS. A zero interval says the
	// frames go out back to back, which is only a coherent description
	// of a fixture small enough to have nothing to pace: a hundred
	// frames at no interval is a fixture whose gaps are the scheduler
	// alone, and calling that a stated pace would be recording a
	// condition nobody chose.
	if entry.Pace.Interval == 0 && entry.Pace.Flushes > unpacedFlushCeiling {
		return "records a fixture with no pace at all and " + strconv.Itoa(entry.Pace.Flushes) +
			" flushes. With no interval stated, every gap between those flushes is " +
			"whatever the scheduler gave — which is a condition nobody chose rather " +
			"than a pace anybody stated"
	}
	return ""
}

// unpacedFlushCeiling is how many flushes a fixture may make with no
// stated interval before the zero stops being a statement and starts
// being an omission. Small: an unpaced fixture in this suite writes
// what it has and goes silent, which is two or three frames.
const unpacedFlushCeiling = 4

// TestAPaceIsRecordedOnTheReadSideAndNowhereElse, and it is the read
// side's half of the condition rule.
//
// A window is five times a gap, and a gap is that number only under the
// condition it was taken in. On the write side that condition is a pair
// of socket buffers. On the read side it is the fixture's own pacing —
// measured, across twenty-one passes of three probes, the client's worst
// gap and the fixture's own widest pause between flushes agreed to
// within a millisecond every time. So the pace is not context for the
// number, it very nearly IS the number, and an entry that records one
// without the other has recorded half a fact.
//
// WHAT IS REFUSED HERE IS SILENCE, in both directions: a read-side entry
// with no pace cannot be told apart from one nobody thought about, and a
// write-side entry carrying one is a condition with no bearing on the
// figure beside it.
//
// REQUIRED MUTATIONS, RUN 2026-09-10:
//
//  1. Drop the Pace from StreamPartialLineIsNotAStall. Reds here alone,
//     with the wording about nobody saying; the other two read entries
//     stay green, and so does every write-side row.
//  2. Give UploadSlowIsNotStalled a Pace. Reds here alone, on the write
//     side. Both of these add exactly one failing row to this package's
//     baseline, which is the "alone" being a measurement rather than a
//     claim — the baseline itself is red on the legs still pending, and
//     an extra red in a suite that already has one is easy to assert
//     and easy to get wrong.
//  3. Set StreamKeepAlivesAreProofOfLife's Flushes to one less than its
//     fixture's. Reds in internal/flow rather than here, at the probe,
//     which is the tie that makes this record checked rather than
//     transcribed: "the registry records 40 flushes and this fixture
//     makes 41".
func TestAPaceIsRecordedOnTheReadSideAndNowhereElse(t *testing.T) {
	for _, entry := range sortedEntries() {
		if problem := paceProblem(entry); problem != "" {
			t.Errorf("timing.%s %s", entry.Name, problem)
		}
	}

	// THE BENCH, because a check run only over a clean registry can be
	// seen to say yes and never to say no.
	paced := &timing.FixturePace{Interval: 20 * time.Millisecond, Flushes: 76}
	for _, tc := range []struct {
		name  string
		entry timing.Entry
	}{
		{"a read-side entry with a stated pace", timing.Entry{Side: timing.Read, Pace: paced}},
		// The unpaced case is accepted ON PURPOSE: one fixture here
		// writes two frames and then goes silent, so it has no interval
		// to state, and a check that quietly required a positive one
		// would refuse the only entry whose gap is establishment rather
		// than pacing.
		{"a read-side entry whose fixture does not pace", timing.Entry{Side: timing.Read,
			Pace: &timing.FixturePace{Interval: 0, Flushes: 2}}},
		{"a write-side entry with no pace", timing.Entry{Side: timing.Write}},
	} {
		if problem := paceProblem(&tc.entry); problem != "" {
			t.Errorf("%s was refused: %s — this check refuses everything and its reds "+
				"mean nothing", tc.name, problem)
		}
	}

	for _, tc := range []struct {
		name  string
		entry timing.Entry
	}{
		{"a read-side entry with no pace recorded at all", timing.Entry{Side: timing.Read}},
		{"a write-side entry carrying a pace", timing.Entry{Side: timing.Write, Pace: paced}},
		{"a fixture that flushes nothing", timing.Entry{Side: timing.Read,
			Pace: &timing.FixturePace{Interval: 20 * time.Millisecond}}},
		{"a negative pace", timing.Entry{Side: timing.Read,
			Pace: &timing.FixturePace{Interval: -time.Millisecond, Flushes: 10}}},
		{"a long fixture with no pace stated", timing.Entry{Side: timing.Read,
			Pace: &timing.FixturePace{Interval: 0, Flushes: 40}}},
	} {
		if paceProblem(&tc.entry) == "" {
			t.Errorf("%s was accepted, so this check says yes to everything", tc.name)
		}
	}
}

// TestEveryDeclaredEntryIsInTheRegistry reads this package's own source.
//
// The registry map is written out rather than assembled by reflection,
// so that adding an entry is a deliberate, readable act — and a written
// list can be short. A declared entry that never reaches the map is
// invisible to every row above it: it would carry no measurement, cite
// no leg and red nowhere, while a test using it resolved happily,
// because the absence row asks the map and the map is where it is not.
func TestEveryDeclaredEntryIsInTheRegistry(t *testing.T) {
	dir := filepath.Join(moduleRoot(t), "internal", "timing")
	names, err := declaredEntries(dir)
	if err != nil {
		t.Fatalf("reading the registry's own source: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no entry is declared in this package's source at all, so this row " +
			"is comparing two empty things")
	}
	for _, name := range names {
		entry, ok := timing.Registry[name]
		if !ok {
			t.Errorf("timing.%s is declared and is not in the Registry map, so every "+
				"rule this package enforces passes over it in silence", name)
			continue
		}
		if entry.Name != name {
			t.Errorf("the entry declared as %s calls itself %q; the identifier, the "+
				"map key and the Name field are one name and a guard resolves a test's "+
				"use through all three", name, entry.Name)
		}
	}
	if len(names) != len(timing.Registry) {
		t.Errorf("%d entries are declared and %d are registered", len(names), len(timing.Registry))
	}
}

// declaredEntries is every package-level variable in dir whose value is
// an Entry composite literal.
func declaredEntries(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				lit, ok := value.Values[0].(*ast.CompositeLit)
				if !ok {
					continue
				}
				if ident, ok := lit.Type.(*ast.Ident); ok && ident.Name == "Entry" {
					names = append(names, value.Names[0].Name)
				}
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// pinProblem reports what is wrong with one leg's recorded pin, or ""
// when there is nothing wrong with it.
//
// It is a FUNCTION rather than a loop body for the reason carryProblem
// is: the real registry is expected to be clean, so run only over that,
// this check can show that it says yes and never that it can say no.
func pinProblem(side timing.Side, leg timing.Leg, m timing.Measurement) string {
	// THE TWO MECHANISMS ARE HELD APART IN THE DATA, exactly as the
	// block point is. On the read side the gap is the far end's pacing
	// plus the scheduler; nothing buffers on this client's behalf and no
	// socket option it can set governs the number, so a pin recorded
	// there is a condition with no bearing on the figure beside it.
	if side == timing.Read {
		if m.Pin != nil {
			return "is a read-side window recording a socket-buffer pin on " + string(leg) +
				". Nothing buffers on this client's behalf while it reads, so that " +
				"condition has no bearing on the gap it sits beside"
		}
		return ""
	}
	// A WRITE-SIDE MEASUREMENT WITH NO PIN AT ALL IS "NOBODY SAID",
	// which is a different thing from "this leg could not pin" and has
	// to be refused rather than read as either. What HAPPENS on this leg
	// follows from the answer: a pinned leg gets a narrow window and a
	// cheap fixture, and a leg that cannot pin STOPS — its row reds
	// naming the end that failed, because the fixture it would need to
	// carry a margin over an autotuned buffer does not fit inside this
	// client's own input limit. See the Pin field in internal/timing.
	if m.Pin == nil {
		return "is a write-side window measured on " + string(leg) + " with no record " +
			"of the socket buffers it was measured under. That is not 'unpinned', it " +
			"is nobody saying: record the pin the run held, or record the failure " +
			"that stopped it holding one"
	}
	for _, end := range []struct {
		name string
		pin  timing.Pin
	}{{"send", m.Pin.Send}, {"receive", m.Pin.Receive}} {
		if end.pin.Requested <= 0 {
			return "records a " + end.name + " pin on " + string(leg) +
				" that asked for nothing, so there is no request for the read-back to " +
				"be an answer to"
		}
		if end.pin.Err != "" {
			continue
		}
		if end.pin.ReadBack <= 0 {
			return "records a " + end.name + " pin on " + string(leg) + " with no error " +
				"and no read-back. Setting a socket option proves only that the request " +
				"was made — a kernel may clamp it, round it, double it or ignore it and " +
				"report none of that — so a pin nobody read back is a claim rather than " +
				"a condition"
		}
		// THE COHERENCE BAND. A read-back is evidence only if it
		// stands in a known relation to the request. Linux stores twice
		// what was asked for and hands the doubled number back, so
		// [Requested, 2*Requested] is the honest band and nothing
		// outside it is: a kernel reporting half the request clamped it,
		// and one reporting thirty times it was answering about a buffer
		// nobody chose. Before this existed, ReadBack: 1 against
		// Requested: 131072 passed every guard here — measured, not
		// imagined, and one byte is not a socket buffer anywhere.
		if !end.pin.Held() {
			return "records a " + end.name + " pin on " + string(leg) + " that asked for " +
				strconv.Itoa(end.pin.Requested) + " bytes and read back " +
				strconv.Itoa(end.pin.ReadBack) + ", which is outside [" +
				strconv.Itoa(end.pin.Requested) + ", " + strconv.Itoa(2*end.pin.Requested) +
				"]. A kernel that doubles a request is honouring it and a kernel that " +
				"answers with anything else is describing a buffer this record did not " +
				"ask for, so the pin is not claimed"
		}
	}
	return ""
}

// TestAPinIsRecordedOnTheWriteSideAndNowhereElse, and it says which mode
// the leg is in.
//
// A window is five times a gap, and a gap is that number only under the
// condition it was taken in. On the write side that condition is a pair
// of socket buffers, neither of which any kernel holds still on its own;
// on the read side there is no such condition, and one written down
// would be a fact about something else sitting where a reader will take
// it for evidence.
//
// THE ERR CASE IS DATA AND NOT A DEFECT — but it is data about a leg
// that has STOPPED, not one running more expensively. A record carrying
// an Err says this leg tried to pin and could not; its row reds there,
// naming the end, because the fixture a margin over an autotuned buffer
// would need does not fit inside this client's own input limit. What is
// refused HERE is silence: a write-side measurement with no pin at all
// cannot be told apart from one nobody thought about.
//
// REQUIRED MUTATION, RUN 2026-09-10, three of them, each reverted:
//
//  1. Drop the Pin from UploadSlowIsNotStalled's darwin measurement.
//     Reds alone, on that entry, with the wording about nobody saying;
//     UploadWedgedStops stays green, because it carries its own copy.
//  2. Give StreamGoesQuiet's darwin measurement a Pin. Reds alone, on
//     the read side, and no write-side row moves.
//  3. Blank the ReadBack on the send end of UploadSlowIsNotStalled's
//     pin. Reds here — and ALSO, unpredicted, in internal/flow, where
//     TestASlowUploadIsNotAStalledOne and TestProbeTheUploadStallGap
//     both refuse: the live socket reads back 131072 and the record now
//     says 0, so confirmPin reports a condition the record does not
//     describe. That second red is the more valuable one, because it is
//     the row saying it is running a margin over a number that was
//     measured somewhere else.
//
// REQUIRED MUTATION FOR THE COHERENCE BAND, RUN 2026-09-10: set the
// send end's ReadBack to 1 against a Requested of 16384. Reds here —
// "asked for 16384 bytes and read back 1, which is outside
// [16384, 32768]" — and, unpredicted, in the carry row above, because
// the two upload entries hold one measurement by construction and this
// moved one of them. Before the band existed this mutation was green
// everywhere: one byte is not a socket buffer on any operating system,
// and the check it passed was asking whether a number was positive.
func TestAPinIsRecordedOnTheWriteSideAndNowhereElse(t *testing.T) {
	for _, entry := range sortedEntries() {
		for _, leg := range timing.Legs {
			m := entry.Measurements[leg]
			if !m.Measured() {
				continue
			}
			if problem := pinProblem(entry.Side, leg, m); problem != "" {
				t.Errorf("timing.%s %s", entry.Name, problem)
			}
		}
	}

	// THE BENCH, because a check run only over a clean registry can be
	// seen to say yes and never to say no — and one that refused
	// everything would make the loop above red rather than silent, which
	// is a failure mode a reader would blame on the registry.
	held := &timing.PinnedPair{
		Send:    timing.Pin{Requested: 131072, ReadBack: 262144},
		Receive: timing.Pin{Requested: 131072, ReadBack: 262144},
	}
	for _, tc := range []struct {
		name string
		side timing.Side
		m    timing.Measurement
	}{
		// The doubled read-back is the accepted case ON PURPOSE: it is
		// what a Linux kernel reports for an honoured request, and a
		// check that quietly required ReadBack == Requested would refuse
		// the one leg where the pin is most certainly applied.
		{"a held pin whose read-back is doubled", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10", Pin: held}},
		// The other end of the band: a kernel that hands back exactly
		// what it was asked for, which is what darwin and Windows do.
		{"a held pin whose read-back is the request", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10",
				Pin: &timing.PinnedPair{
					Send:    timing.Pin{Requested: 16384, ReadBack: 16384},
					Receive: timing.Pin{Requested: 16384, ReadBack: 16384},
				}}},
		{"a write-side leg that could not pin", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10",
				Pin: &timing.PinnedPair{
					Send:    timing.Pin{Requested: 131072, Err: "operation not permitted"},
					Receive: timing.Pin{Requested: 131072, Err: "operation not permitted"},
				}}},
		{"a read-side leg with no pin", timing.Read,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10"}},
	} {
		if problem := pinProblem(tc.side, timing.Linux, tc.m); problem != "" {
			t.Errorf("%s was refused: %s — this check refuses everything and its reds "+
				"mean nothing", tc.name, problem)
		}
	}

	for _, tc := range []struct {
		name string
		side timing.Side
		m    timing.Measurement
	}{
		{"a write-side leg with no pin recorded at all", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10"}},
		{"a read-side leg carrying a pin", timing.Read,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10", Pin: held}},
		{"a pin that asked for nothing", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10",
				Pin: &timing.PinnedPair{Receive: timing.Pin{Requested: 131072, ReadBack: 131072}}}},
		{"a pin with no error and no read-back", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10",
				Pin: &timing.PinnedPair{
					Send:    timing.Pin{Requested: 131072},
					Receive: timing.Pin{Requested: 131072, ReadBack: 131072},
				}}},
		// THE BAND, from both sides. The first is the literal case
		// that passed before it existed.
		{"a read-back of one byte against a 128 KiB request", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10",
				Pin: &timing.PinnedPair{
					Send:    timing.Pin{Requested: 131072, ReadBack: 1},
					Receive: timing.Pin{Requested: 131072, ReadBack: 131072},
				}}},
		{"a read-back a kernel clamped to half the request", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10",
				Pin: &timing.PinnedPair{
					Send:    timing.Pin{Requested: 131072, ReadBack: 131072},
					Receive: timing.Pin{Requested: 131072, ReadBack: 65536},
				}}},
		{"a read-back past the doubling Linux does", timing.Write,
			timing.Measurement{WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-10",
				Pin: &timing.PinnedPair{
					Send:    timing.Pin{Requested: 16384, ReadBack: 16384},
					Receive: timing.Pin{Requested: 16384, ReadBack: 539008},
				}}},
	} {
		if pinProblem(tc.side, timing.Linux, tc.m) == "" {
			t.Errorf("%s was accepted", tc.name)
		}
	}

	// AND Pinned MUST STILL BE ABLE TO SAY NO, because everything the
	// rows do with the mode keys on it. A predicate satisfied by any
	// non-nil pin would report a leg that failed to pin as one that
	// succeeded, and that leg would then run the narrow window it has no
	// evidence for.
	for _, tc := range []struct {
		name   string
		m      timing.Measurement
		pinned bool
	}{
		{"a pin held at both ends", timing.Measurement{Pin: held}, true},
		{"no pin at all", timing.Measurement{}, false},
		{"a pin whose send end errored", timing.Measurement{Pin: &timing.PinnedPair{
			Send: timing.Pin{Requested: 1, Err: "no"}, Receive: held.Receive}}, false},
		{"a pin whose receive end errored", timing.Measurement{Pin: &timing.PinnedPair{
			Send: held.Send, Receive: timing.Pin{Requested: 1, Err: "no"}}}, false},
		{"a pin with only one end at all", timing.Measurement{Pin: &timing.PinnedPair{
			Send: held.Send}}, false},
		{"a pin whose send end read back one byte", timing.Measurement{Pin: &timing.PinnedPair{
			Send: timing.Pin{Requested: 131072, ReadBack: 1}, Receive: held.Receive}}, false},
		{"a pin whose receive end read back thirty times the request",
			timing.Measurement{Pin: &timing.PinnedPair{
				Send:    held.Send,
				Receive: timing.Pin{Requested: 16384, ReadBack: 539008}}}, false},
	} {
		if got := tc.m.Pinned(); got != tc.pinned {
			t.Errorf("%s reported Pinned() as %v, want %v", tc.name, got, tc.pinned)
		}
	}
}
