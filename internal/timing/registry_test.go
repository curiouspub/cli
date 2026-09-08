package timing_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
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
	fully := &timing.Entry{
		Name: "PositiveControl",
		Measurements: map[timing.Leg]timing.Measurement{
			timing.Linux:   {WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-09"},
			timing.Darwin:  {WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-09"},
			timing.Windows: {WorstGap: time.Millisecond, Runs: 20, Date: "2026-09-09"},
		},
	}
	if missing := fully.UnmeasuredLegs(); len(missing) != 0 {
		t.Errorf("an entry measured on all three legs was reported unmeasured on "+
			"%v, so this row refuses everything and its reds mean nothing", missing)
	}

	// AND IT MUST STILL SAY NO. A "measured" test satisfied by any value
	// at all would pass the control above and never red on a real gap.
	for _, tc := range []struct {
		name string
		m    timing.Measurement
	}{
		{"no gap", timing.Measurement{Runs: 20, Date: "2026-09-09"}},
		{"no run count", timing.Measurement{WorstGap: time.Millisecond, Date: "2026-09-09"}},
		{"no date", timing.Measurement{WorstGap: time.Millisecond, Runs: 20}},
		{"nothing at all", timing.Measurement{}},
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
	return ""
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
	sound := &timing.Entry{
		Name: "Carrier", Side: timing.Write, Instrument: "the same reader",
		Carried: &timing.Carried{From: "Source", Reason: "same side, same reader, one mechanism"},
	}
	source := &timing.Entry{Name: "Source", Side: timing.Write, Instrument: "the same reader"}
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
