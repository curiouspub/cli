package check

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// everyDeclaredID is a manifest claiming the whole universe, which is
// what a valid report needs before anything else about it can be
// asserted. Rows below start from it and break exactly one thing.
func everyDeclaredID() Manifest {
	var m Manifest
	for _, id := range DeclaredOrder() {
		m = append(m, Ran{CheckID: id, Ran: true})
	}
	return m
}

// TestCombineIsTheOnlyDoorToAReport is the structural claim the rest of
// this file rests on. A Report cannot be built by a caller: its fields
// are unexported, so the zero value is the only one anybody outside can
// name, and the zero value is not valid.
//
// THIS IS WHY THE TYPE EXISTS. Every promise the combiner makes was
// previously a promise about a call site — the duplicates are refused IF
// somebody calls Combine, the coverage is checked IF somebody calls
// CoverageGaps. A renderer that takes a Report cannot be handed anything
// that skipped the gate, and there is no second door to keep in step.
//
// MUTATION: have Combine return a Report without setting the marker.
// Reds here and in the renderer's zero-report row.
// MUST NOT MOVE: the rows that assert what Combine refuses — they never
// get a Report at all.
func TestCombineIsTheOnlyDoorToAReport(t *testing.T) {
	var zero Report
	if zero.Valid() {
		t.Error("the zero Report reports itself as validated")
	}
	if zero.Findings() != nil || zero.Manifest() != nil {
		t.Errorf("the zero Report carries content: %+v / %+v", zero.Findings(), zero.Manifest())
	}

	got, err := Combine(Results{Manifest: everyDeclaredID()})
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}
	if !got.Valid() {
		t.Error("Combine returned a Report that does not report itself as validated")
	}
}

// TestCombineRefusesAnIncompleteUniverse. An engine wired up with no
// checks at all produced an empty manifest, no findings and no error —
// and a renderer read that as a clean project and let the deploy
// proceed. The coverage question had an answer the whole time; nothing
// was obliged to ask it.
//
// MUTATION: skip the coverage enforcement. Reds here and in the
// engine-side row for an empty registration.
// MUST NOT MOVE: every row whose manifest claims the full universe.
func TestCombineRefusesAnIncompleteUniverse(t *testing.T) {
	if _, err := Combine(); err == nil {
		t.Fatal("Combine() of nothing produced a usable report")
	}

	short := everyDeclaredID()
	dropped := short[len(short)-1].CheckID
	short = short[:len(short)-1]

	_, err := Combine(Results{Manifest: short})
	var gap *CoverageError
	if !errors.As(err, &gap) {
		t.Fatalf("error = %#v, want a coverage failure", err)
	}
	// The dropped id is taken from the list rather than written out.
	// This row's subject is the REFUSAL, and naming a particular id here
	// would make it red every time the universe grows — which the
	// universe's own row already covers, and covers better.
	if !reflect.DeepEqual(gap.Missing, []string{dropped}) {
		t.Errorf("Missing = %v, want [%s]", gap.Missing, dropped)
	}
	if len(gap.Unexpected) != 0 {
		t.Errorf("Unexpected = %v, want none", gap.Unexpected)
	}
	if !strings.Contains(err.Error(), dropped) {
		t.Errorf("message = %q, want it to name the check nobody covered", err.Error())
	}
}

// TestCombineRefusesAnIDNobodyDeclared. The other direction, and it is a
// different failure: a producer answering a question that is not on the
// list, which in practice is a mistyped id and reaches the user as the
// name of a check they have never heard of.
func TestCombineRefusesAnIDNobodyDeclared(t *testing.T) {
	m := append(everyDeclaredID(), Ran{CheckID: "lockfle", Ran: true})

	_, err := Combine(Results{Manifest: m})
	var gap *CoverageError
	if !errors.As(err, &gap) {
		t.Fatalf("error = %#v, want a coverage failure", err)
	}
	if !reflect.DeepEqual(gap.Unexpected, []string{"lockfle"}) {
		t.Errorf("Unexpected = %v, want [lockfle]", gap.Unexpected)
	}
}

// TestCombineRefusesAFindingUnderAnIDNobodyClaimed. Manifest rows were
// refused and findings were not, so a producer could report on a
// question it never claimed — and where that finding then sorted was
// argument order, which is the caller's rule. The engine refuses to let
// a caller decide that for checks; the combiner should not hand it back.
//
// MUTATION: skip the claimed-id enforcement. Reds here.
// MUST NOT MOVE: rows whose findings all sit under claimed ids.
//
// THE UNCLAIMED ID IS RESERVED AND GUARDED, in reserved_test.go. This
// row used to invent one inline, which is how it came to rest on a name
// the file walk later claimed; the premise is now an assertion that
// fails when it stops being true, rather than a fact about the day the
// row was written.
func TestCombineRefusesAFindingUnderAnIDNobodyClaimed(t *testing.T) {
	_, err := Combine(Results{
		Manifest: everyDeclaredID(),
		Findings: []Finding{
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "claimed, fine"},
			{CheckID: reservedUnclaimedID, Severity: SeverityWarning, Message: "never claimed"},
		},
	})

	var unclaimed *UnclaimedFindingError
	if !errors.As(err, &unclaimed) {
		t.Fatalf("error = %#v, want an unclaimed-finding failure", err)
	}
	if !reflect.DeepEqual(unclaimed.CheckIDs, []string{reservedUnclaimedID}) {
		t.Errorf("CheckIDs = %v, want [%s]", unclaimed.CheckIDs, reservedUnclaimedID)
	}
}

// TestCombineRefusesAnUndeclaredSeverity, and the zero value is the case
// that matters. A producer that forgets to set one produced a finding
// that Advisory() called shown, that PROMPTED a person to decide about
// it, and that sorted after the notes no surface displays. Prompting on
// a field nobody set is the never-false-positive criterion broken by an
// omission.
//
// MUTATION: skip the severity enforcement. Reds here.
// MUST NOT MOVE: every row whose findings carry one of the three
// constants.
func TestCombineRefusesAnUndeclaredSeverity(t *testing.T) {
	_, err := Combine(Results{
		Manifest: everyDeclaredID(),
		Findings: []Finding{
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "fine"},
			{CheckID: IDAstroDep, Message: "severity forgotten"},
		},
	})

	var bad *UndeclaredSeverityError
	if !errors.As(err, &bad) {
		t.Fatalf("error = %#v, want an undeclared-severity failure", err)
	}
	if !reflect.DeepEqual(bad.CheckIDs, []string{IDAstroDep}) {
		t.Errorf("CheckIDs = %v, want [%s]", bad.CheckIDs, IDAstroDep)
	}
}

// TestSeverityDeclaredKnowsTheThree, including the one nobody set. It is
// asked in the model so the gate and any later surface cannot disagree
// about what counts as a severity.
func TestSeverityDeclaredKnowsTheThree(t *testing.T) {
	for _, s := range []Severity{SeverityHardStop, SeverityWarning, SeverityNote} {
		if !s.Declared() {
			t.Errorf("Declared(%q) = false", s)
		}
	}
	for _, s := range []Severity{"", "critical", "HARD_STOP"} {
		if s.Declared() {
			t.Errorf("Declared(%q) = true", s)
		}
	}
}

// TestCombineDeepCopiesPaths. Non-disturbance was true of the slice
// headers and false of their contents: a caller writing to a path in the
// combined report reached into the producer's own finding. A producer
// may keep its result to render or report on separately, and the symptom
// of this would surface a long way from here.
//
// MUTATION: copy the findings without copying Paths. Reds here.
// MUST NOT MOVE: anything about ordering or validation.
func TestCombineDeepCopiesPaths(t *testing.T) {
	producer := Results{
		Manifest: everyDeclaredID(),
		Findings: []Finding{{
			CheckID:  IDLocalhost,
			Severity: SeverityWarning,
			Message:  "a development URL",
			Paths:    []string{"a.ts", "b.ts"},
		}},
	}

	report, err := Combine(producer)
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}
	report.Findings()[0].Paths[0] = "CLOBBERED"

	if got := producer.Findings[0].Paths; !reflect.DeepEqual(got, []string{"a.ts", "b.ts"}) {
		t.Errorf("the producer's own Paths were reached through the report: %v", got)
	}
}

// TestCombineIsIndependentOfArgumentOrder. Where a finding sits in the
// report is the declared universe's business, not the caller's — the
// same rule the engine applies to checks. With ids claimed by exactly
// one producer, swapping the arguments must change nothing at all.
//
// MUTATION: order findings by argument position instead of by rank.
// Reds here.
func TestCombineIsIndependentOfArgumentOrder(t *testing.T) {
	early := Results{
		Manifest: Manifest{
			{CheckID: IDAstroDep, Ran: true},
			{CheckID: IDLockfile, Ran: true},
			{CheckID: IDPagesDir, Ran: true},
			{CheckID: IDBuildFormat, Ran: true},
		},
		Findings: []Finding{{CheckID: IDAstroDep, Severity: SeverityWarning, Message: "from the engine"}},
	}
	late := Results{
		Manifest: Manifest{
			{CheckID: IDLocalhost, Ran: true},
			{CheckID: IDSymlinks, Ran: true},
			{CheckID: IDCaseCollision, Ran: true},
			{CheckID: IDPathCharset, Ran: true},
		},
		Findings: []Finding{{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "from the walk"}},
	}

	forward, err := Combine(early, late)
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}
	backward, err := Combine(late, early)
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}

	want := []string{"from the engine", "from the walk"}
	if got := messages(forward.Findings()); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	if got := messages(backward.Findings()); !reflect.DeepEqual(got, want) {
		t.Errorf("swapping the producers changed the report: %v, want %v", got, want)
	}
	if !reflect.DeepEqual(ids(forward.Manifest()), ids(backward.Manifest())) {
		t.Errorf("swapping the producers changed the manifest order")
	}
}

// TestReportAccessorsHandBackTheirOwnSlices. A Report is the validated
// article, and a caller reordering what it was handed must not change
// what the next reader of the same Report sees.
func TestReportAccessorsHandBackTheirOwnSlices(t *testing.T) {
	report, err := Combine(Results{
		Manifest: everyDeclaredID(),
		Findings: []Finding{
			{CheckID: IDAstroDep, Severity: SeverityHardStop, Message: "first"},
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "second"},
		},
	})
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}

	taken := report.Findings()
	taken[0], taken[1] = taken[1], taken[0]
	if got := messages(report.Findings()); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Errorf("a caller reordering its copy changed the report: %v", got)
	}

	rows := report.Manifest()
	rows[0] = Ran{CheckID: "clobbered"}
	if report.Manifest()[0].CheckID != IDAstroDep {
		t.Errorf("a caller overwriting its copy changed the report: %v", ids(report.Manifest()))
	}
}
