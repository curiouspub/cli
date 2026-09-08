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
		m = append(m, Status{CheckID: id})
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
	m := append(everyDeclaredID(), Status{CheckID: "lockfle"})

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
			{CheckID: IDAstroDep},
			{CheckID: IDLockfile},
			{CheckID: IDPagesDir},
			{CheckID: IDBuildFormat},
		},
		Findings: []Finding{{CheckID: IDAstroDep, Severity: SeverityWarning, Message: "from the engine"}},
	}
	late := Results{
		Manifest: Manifest{
			{CheckID: IDLocalhost},
			{CheckID: IDSymlinks},
			{CheckID: IDCaseCollision},
			{CheckID: IDPathCharset},
			{CheckID: IDLimitFiles},
			{CheckID: IDLimitFileSize},
			{CheckID: IDLimitTotal},
			{CheckID: IDLimitPacked},
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
	rows[0] = Status{CheckID: "clobbered"}
	if report.Manifest()[0].CheckID != IDAstroDep {
		t.Errorf("a caller overwriting its copy changed the report: %v", ids(report.Manifest()))
	}
}

// TestCombineRefusesAFindingUnderADeclinedID is the fifth enforcement,
// and it closes a contradiction the other four permit by construction.
//
//	Skipped lockfile: couldn't read package.json
//	lockfile is stale
//
// Two adjacent lines about one id, disagreeing about whether anybody
// looked. Claimed-ids allows exactly this, because the id IS claimed —
// the row is there, it simply says the check declined.
//
// A check that looked enough to find something ANSWERED. A check that
// declined has nothing to report. There is no third case, and this is
// where that becomes true rather than hoped.
//
// MUTATION: remove the enforcement. Reds here.
// MUST NOT MOVE: the four rows above — a duplicate, a coverage gap, an
// unclaimed finding and an undeclared severity each still red on their
// own enforcement and on nothing else.
func TestCombineRefusesAFindingUnderADeclinedID(t *testing.T) {
	m := everyDeclaredID()
	for i := range m {
		if m[i].CheckID == IDLockfile {
			m[i] = Status{
				CheckID: IDLockfile,
				Outcome: Declined,
				Kind:    Environmental,
				Reason:  "couldn't read package.json",
			}
		}
	}

	_, err := Combine(Results{
		Manifest: m,
		Findings: []Finding{
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "answered, fine"},
			{CheckID: IDLockfile, Severity: SeverityWarning, Message: "lockfile is stale"},
		},
	})

	var contradicted *ContradictedFindingError
	if !errors.As(err, &contradicted) {
		t.Fatalf("error = %#v, want a contradiction failure", err)
	}
	if !reflect.DeepEqual(contradicted.CheckIDs, []string{IDLockfile}) {
		t.Errorf("CheckIDs = %v, want [%s]", contradicted.CheckIDs, IDLockfile)
	}
	if !strings.Contains(err.Error(), IDLockfile) {
		t.Errorf("message = %q, want it to name the id", err.Error())
	}
}

// TestCombineAllowsAFindingUnderAnAnsweredIDBesideOtherDeclines is the
// floor. The rule is about the id a finding is ABOUT, not about whether
// the report contains declines at all — without this the enforcement
// could be "refuse any report with both findings and declines", which
// would refuse most real reports.
func TestCombineAllowsAFindingUnderAnAnsweredIDBesideOtherDeclines(t *testing.T) {
	m := everyDeclaredID()
	for i := range m {
		if m[i].CheckID == IDBuildFormat {
			m[i] = Status{
				CheckID: IDBuildFormat,
				Outcome: Declined,
				Kind:    ByDesign,
				Reason:  "the config builds this value at run time",
			}
		}
	}

	report, err := Combine(Results{
		Manifest: m,
		Findings: []Finding{
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "a development URL"},
		},
	})
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}
	if len(report.Findings()) != 1 {
		t.Errorf("findings = %+v, want the one under an answered id", report.Findings())
	}
}

// TestCombineRefusesAFindingThatSaysNothing is the sixth enforcement,
// and it closes a hole the other five could not see by construction.
//
// EVERY ONE OF THEM IS ABOUT A FINDING'S ADDRESS — who claimed the id,
// whether the universe covers it, whether the severity is one that
// exists, whether the id was answered. None is about its CONTENT. So a
// warning carrying no message at all passed the gate, rendered as a
// blank line, and still asked "Continue anyway?" — the user deciding
// about nothing, and a "no" stopping their deploy.
//
// It is the criterion this project minted and has ruled on four times
// since, finally asked at the gate: AN ADVISORY NAMES SOMETHING THE USER
// CAN ACT ON OR OBSERVE, OR IT DOES NOT FIRE. A finding with no message
// names nothing by construction.
//
// WHITESPACE COUNTS AS NOTHING, which is a decision rather than a
// detail: " " renders as the same blank line "" does, and a rule that
// refused one and passed the other would be a rule about the bytes
// rather than about what a person can read.
//
// IT APPLIES TO NOTES TOO, and that is the other decision. A note is
// shown by no surface, so it looks like the safe exception — but a note
// is a fact kept for a caller that asks, and a note with no message
// keeps nothing. A conditional gate is no gate.
//
// MUTATION: remove the enforcement. Reds here; the five existing
// enforcement rows do not move, since none of their fixtures is silent.
func TestCombineRefusesAFindingThatSaysNothing(t *testing.T) {
	cases := []struct {
		name     string
		severity Severity
		message  string
	}{
		{"a warning with no message", SeverityWarning, ""},
		{"a hard stop with no message", SeverityHardStop, ""},
		{"a note with no message", SeverityNote, ""},
		{"a message of spaces", SeverityWarning, "   "},
		{"a message of a newline", SeverityWarning, "\n"},
		{"a message of a tab", SeverityWarning, "\t"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Combine(Results{
				Manifest: everyDeclaredID(),
				Findings: []Finding{{
					CheckID:  IDLocalhost,
					Severity: tc.severity,
					Message:  tc.message,
				}},
			})

			var silent *EmptyMessageError
			if !errors.As(err, &silent) {
				t.Fatalf("error = %#v, want a refusal: a finding that says nothing renders "+
					"as a blank line and still asks the user to decide about it", err)
			}
			if !reflect.DeepEqual(silent.CheckIDs, []string{IDLocalhost}) {
				t.Errorf("CheckIDs = %v, want [%s]", silent.CheckIDs, IDLocalhost)
			}
			if !strings.Contains(err.Error(), IDLocalhost) {
				t.Errorf("message = %q, want it to name the check", err.Error())
			}
		})
	}
}

// TestCombineAcceptsAFindingThatSaysSomething is the floor, and without
// it the enforcement above could be satisfied by refusing everything —
// which would pass every row in this file that only asserts a refusal.
//
// The message is deliberately unremarkable: one ordinary sentence, no
// paths, no copy. The rule is that there IS something to read, not that
// it is well written.
func TestCombineAcceptsAFindingThatSaysSomething(t *testing.T) {
	report, err := Combine(Results{
		Manifest: everyDeclaredID(),
		Findings: []Finding{
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "A development URL is hard-coded."},
			{CheckID: IDBuildFormat, Severity: SeverityNote, Message: "recorded, shown to nobody"},
		},
	})
	if err != nil {
		t.Fatalf("Combine refused a report whose findings both say something: %v", err)
	}
	if len(report.Findings()) != 2 {
		t.Errorf("findings = %+v, want both", report.Findings())
	}
}

// TestTheReportKeepsItsOwnCopyWhenACallerMutatesWhatItHandsOut is the
// outbound half of the same symmetry.
//
// Combine deep-copies on the way IN, so a producer cannot reach into a
// validated report afterwards. Findings copied only the outer slice on
// the way OUT, so a caller could reach into the report itself — and
// mutate Sizes and Paths into exactly the length disagreement the gate
// exists to refuse. A validated article that its readers can invalidate
// is not one.
//
// THE EXISTING ROW CANNOT SEE THIS. It mutates through the accessor and
// asserts the PRODUCER is untouched, which the inbound copy already
// guarantees; it says nothing about the report. Same call, different
// subject, and the difference is the whole finding.
//
// REQUIRED MUTATION: restore the shallow copy in Findings — copy the
// outer slice and return it. Reds on both fields.
func TestTheReportKeepsItsOwnCopyWhenACallerMutatesWhatItHandsOut(t *testing.T) {
	report, err := Combine(Results{
		Manifest: everyDeclaredID(),
		Findings: []Finding{{
			CheckID:  IDLimitFileSize,
			Severity: SeverityHardStop,
			Message:  "two files are too large",
			Paths:    []string{"a.bin", "b.bin"},
			Sizes:    []int64{6_000_000, 7_000_000},
		}},
	})
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}

	handed := report.Findings()
	handed[0].Paths[0] = "CLOBBERED"
	handed[0].Sizes[0] = -1

	again := report.Findings()
	if again[0].Paths[0] != "a.bin" {
		t.Errorf("Paths inside the report were changed through what it handed out: %v",
			again[0].Paths)
	}
	if again[0].Sizes[0] != 6_000_000 {
		t.Errorf("Sizes inside the report were changed through what it handed out: %v",
			again[0].Sizes)
	}
}

// TestCombineDeepCopiesSizes mirrors TestCombineDeepCopiesPaths, and it
// is about the producer rather than the report.
//
// Combine copies a finding's slices on the way in so that a producer
// still holding its own Results cannot reach into a validated report
// afterwards — and the report cannot reach back into the producer. The
// Paths half has had that row since the field existed. The Sizes half was
// written with the same care and no row at all: deleting its branch from
// copyFinding left every package green.
//
// WHY THE OUTBOUND ROW IS NOT THIS ROW. Findings now deep-copies too, so
// mutating through the accessor reds without this — but it reds on the
// REPORT being changed, not on the producer being reached. Same
// mechanism, two subjects, and only a row per subject can tell which one
// broke.
//
// REQUIRED MUTATION, RUN: delete the Sizes branch from copyFinding. Reds
// here, on the producer's own slice.
func TestCombineDeepCopiesSizes(t *testing.T) {
	producer := Results{
		Manifest: everyDeclaredID(),
		Findings: []Finding{{
			CheckID:  IDLimitFileSize,
			Severity: SeverityHardStop,
			Message:  "one file is too large",
			Paths:    []string{"a.bin", "b.bin"},
			Sizes:    []int64{6_000_000, 7_000_000},
		}},
	}

	report, err := Combine(producer)
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}
	report.Findings()[0].Sizes[0] = -1

	if got := producer.Findings[0].Sizes; !reflect.DeepEqual(got, []int64{6_000_000, 7_000_000}) {
		t.Errorf("the producer's own Sizes were reached through the report: %v", got)
	}
}

// TestSupersedeReplacesADeclineWithTheAnswerThatArrivedLater is the
// primitive's whole reason: a check that could not answer when the report
// was built, answering once the thing it measures exists.
//
// The packed-size limit is the case. Before anything is packed there is
// no archive, so the honest row declines; the archive is then written,
// the check runs for real, and without this the validated report still
// says nothing was ever packed — a human reading the refusal sees the
// real numbers while an agent reading the manifest is told the opposite.
// **The human surface being right is what makes it easy to miss.**
//
// REQUIRED MUTATIONS, BOTH RUN:
//   - drop the same-id check so any id may be superseded: the
//     unknown-id subtest reds;
//   - drop the declined check so an answered row may be overwritten: the
//     already-answered subtest reds.
func TestSupersedeReplacesADeclineWithTheAnswerThatArrivedLater(t *testing.T) {
	base := func(t *testing.T) Report {
		t.Helper()
		manifest := Manifest{}
		for _, id := range DeclaredOrder() {
			row := Status{CheckID: id}
			if id == IDLimitPacked {
				row = Status{
					CheckID: id, Outcome: Declined, Kind: ByDesign,
					Reason: "nothing has been packed yet",
				}
			}
			manifest = append(manifest, row)
		}
		report, err := Combine(Results{Manifest: manifest})
		if err != nil {
			t.Fatalf("Combine: %v", err)
		}
		return report
	}

	t.Run("the decline becomes the measurement", func(t *testing.T) {
		report := base(t)

		// The control: before superseding, the report says the opposite
		// of what the run went on to learn.
		for _, row := range report.Manifest() {
			if row.CheckID == IDLimitPacked && row.Outcome != Declined {
				t.Fatalf("the fixture does not start from a decline: %+v", row)
			}
		}

		got, err := report.Supersede(Results{
			Manifest: Manifest{{CheckID: IDLimitPacked}},
			Findings: []Finding{{
				CheckID:  IDLimitPacked,
				Severity: SeverityHardStop,
				Message:  "the packed archive is over the cap",
			}},
		})
		if err != nil {
			t.Fatalf("Supersede: %v", err)
		}

		var packed Status
		for _, row := range got.Manifest() {
			if row.CheckID == IDLimitPacked {
				packed = row
			}
		}
		if packed.Outcome != Answered {
			t.Errorf("%s = %+v, want the answer that arrived later — an agent "+
				"reading this is otherwise told nothing was ever packed",
				IDLimitPacked, packed)
		}
		if len(got.Findings()) != 1 {
			t.Errorf("findings = %v, want the later producer's own", got.Findings())
		}
	})

	t.Run("an id the report does not claim is refused", func(t *testing.T) {
		_, err := base(t).Supersede(Results{
			Manifest: Manifest{{CheckID: "invented-id"}},
		})
		var refused *SupersedeError
		if !errors.As(err, &refused) {
			t.Fatalf("err = %v, want a refusal", err)
		}
		if !reflect.DeepEqual(refused.Unknown, []string{"invented-id"}) {
			t.Errorf("Unknown = %v, want the id nobody claimed", refused.Unknown)
		}
	})

	t.Run("a row that already answered is refused", func(t *testing.T) {
		_, err := base(t).Supersede(Results{
			Manifest: Manifest{{CheckID: IDAstroDep}},
		})
		var refused *SupersedeError
		if !errors.As(err, &refused) {
			t.Fatalf("err = %v, want a refusal", err)
		}
		if !reflect.DeepEqual(refused.NotDeclined, []string{IDAstroDep}) {
			t.Errorf("NotDeclined = %v, want the id that had already answered — an "+
				"answer is not a later producer's to overwrite", refused.NotDeclined)
		}
	})
}
