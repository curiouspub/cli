package check

import (
	"reflect"
	"testing"
)

// TestFindingRoundTrips is the shape test: a Finding constructed with
// any severity carries its check id, severity and message back out
// unchanged, and every severity constructs.
func TestFindingRoundTrips(t *testing.T) {
	cases := []struct {
		name     string
		severity Severity
	}{
		{"hard stop", SeverityHardStop},
		{"warning", SeverityWarning},
		{"note", SeverityNote},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := Finding{
				CheckID:  "astro-in-package-json",
				Severity: tc.severity,
				Message:  "this does not look like an Astro project",
			}
			if f.CheckID != "astro-in-package-json" {
				t.Errorf("CheckID = %q, want %q", f.CheckID, "astro-in-package-json")
			}
			if f.Severity != tc.severity {
				t.Errorf("Severity = %q, want %q", f.Severity, tc.severity)
			}
			if f.Message != "this does not look like an Astro project" {
				t.Errorf("Message = %q, want %q", f.Message, "this does not look like an Astro project")
			}
		})
	}
}

// TestFindingCarriesPaths pins the field that exists so a machine
// surface never has to parse English back out of Message. Two states
// matter and they are different: a finding ABOUT particular files names
// them in order, and a finding about the project as a whole carries
// none.
func TestFindingCarriesPaths(t *testing.T) {
	f := Finding{
		CheckID:  IDLocalhost,
		Severity: SeverityWarning,
		Message:  "a development URL is hard-coded in these files",
		Paths:    []string{"src/pages/index.astro", "src/lib/api.ts"},
	}
	want := []string{"src/pages/index.astro", "src/lib/api.ts"}
	if !reflect.DeepEqual(f.Paths, want) {
		t.Errorf("Paths = %#v, want %#v", f.Paths, want)
	}

	whole := Finding{
		CheckID:  IDLockfile,
		Severity: SeverityHardStop,
		Message:  "no lockfile found",
	}
	if len(whole.Paths) != 0 {
		t.Errorf("Paths on a project-wide finding = %#v, want empty", whole.Paths)
	}
}

// TestAdvisoryClassifiesEverySeverity asserts the note/not-note split at
// the single place it is spelled. Asserted over the whole severity set
// rather than over the two interesting members, so a fourth severity
// added without a decision here reds instead of defaulting silently.
func TestAdvisoryClassifiesEverySeverity(t *testing.T) {
	cases := []struct {
		severity Severity
		advisory bool
	}{
		{SeverityHardStop, true},
		{SeverityWarning, true},
		{SeverityNote, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.severity), func(t *testing.T) {
			got := Finding{Severity: tc.severity}.Advisory()
			if got != tc.advisory {
				t.Errorf("Advisory() = %v, want %v", got, tc.advisory)
			}
		})
	}
}

// TestAdvisoriesKeepsOrderAndDropsNotes covers both halves of the
// filter: what survives, and in what order. Order is asserted because a
// surface renders this slice directly and two runs over one project must
// produce identical output.
func TestAdvisoriesKeepsOrderAndDropsNotes(t *testing.T) {
	in := []Finding{
		{CheckID: "one", Severity: SeverityNote, Message: "kept, not shown"},
		{CheckID: "two", Severity: SeverityHardStop, Message: "first shown"},
		{CheckID: "three", Severity: SeverityNote, Message: "also kept, also not shown"},
		{CheckID: "four", Severity: SeverityWarning, Message: "second shown"},
	}

	got := Advisories(in)
	want := []Finding{
		{CheckID: "two", Severity: SeverityHardStop, Message: "first shown"},
		{CheckID: "four", Severity: SeverityWarning, Message: "second shown"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Advisories() = %#v, want %#v", got, want)
	}

	if got := Advisories(nil); got != nil {
		t.Errorf("Advisories(nil) = %#v, want nil", got)
	}
}

// TestRanRecordsWhyNot pins the row a manifest exists to carry. A check
// that ran carries no reason; one that did not carries the reason, and
// the reason is the only thing standing between a reader and the
// assumption that silence means a clean result.
func TestRanRecordsWhyNot(t *testing.T) {
	ran := Ran{CheckID: IDPagesDir, Ran: true}
	if !ran.Ran {
		t.Error("Ran = false on a row that ran")
	}
	if ran.Reason != "" {
		t.Errorf("Reason = %q on a row that ran, want empty", ran.Reason)
	}

	skipped := Ran{CheckID: IDLockfile, Ran: false, Reason: "couldn't read package.json"}
	if skipped.Ran {
		t.Error("Ran = true on a row that did not run")
	}
	if skipped.Reason != "couldn't read package.json" {
		t.Errorf("Reason = %q, want %q", skipped.Reason, "couldn't read package.json")
	}
}

// TestManifestKeepsDeclaredOrder asserts the property the slice was
// chosen for. A map would answer every lookup this type is asked and
// would render in a different order on every run, which is the one
// thing a deterministic report cannot have.
func TestManifestKeepsDeclaredOrder(t *testing.T) {
	m := Manifest{
		{CheckID: IDAstroDep, Ran: true},
		{CheckID: IDLockfile, Ran: false, Reason: "couldn't read package.json"},
		{CheckID: IDPagesDir, Ran: true},
	}

	want := []string{IDAstroDep, IDLockfile, IDPagesDir}
	var got []string
	for _, row := range m {
		got = append(got, row.CheckID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manifest order = %v, want %v", got, want)
	}
}

// TestCheckIDsAreStableAndDistinct asserts the literal strings rather
// than that the constants exist. They are read by a machine surface and
// quoted in a support answer, so renaming one is a change to something
// somebody outside this program is keying on — and the exact spelling
// is the whole of what they get.
func TestCheckIDsAreStableAndDistinct(t *testing.T) {
	ids := map[string]string{
		"IDAstroDep":    IDAstroDep,
		"IDLockfile":    IDLockfile,
		"IDPagesDir":    IDPagesDir,
		"IDBuildFormat": IDBuildFormat,
		"IDLocalhost":   IDLocalhost,
	}
	want := map[string]string{
		"IDAstroDep":    "astro-dep",
		"IDLockfile":    "lockfile",
		"IDPagesDir":    "pages-dir",
		"IDBuildFormat": "build-format",
		"IDLocalhost":   "localhost",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("check ids = %#v, want %#v", ids, want)
	}

	seen := make(map[string]string, len(ids))
	for name, id := range ids {
		if other, clash := seen[id]; clash {
			t.Errorf("%s and %s share the id %q", name, other, id)
		}
		seen[id] = name
	}
}

// TestManifestNotRunSelectsOnlyTheSkipped asserts the accessor a
// renderer is meant to use, including that it reports nothing when every
// check ran — the state a findings-only stream renders identically to a
// project nobody looked at.
func TestManifestNotRunSelectsOnlyTheSkipped(t *testing.T) {
	m := Manifest{
		{CheckID: IDAstroDep, Ran: true},
		{CheckID: IDLockfile, Ran: false, Reason: "couldn't read package.json"},
		{CheckID: IDPagesDir, Ran: true},
		{CheckID: IDBuildFormat, Ran: false, Reason: "couldn't read astro.config"},
	}

	want := []Ran{
		{CheckID: IDLockfile, Ran: false, Reason: "couldn't read package.json"},
		{CheckID: IDBuildFormat, Ran: false, Reason: "couldn't read astro.config"},
	}
	if got := m.NotRun(); !reflect.DeepEqual(got, want) {
		t.Errorf("NotRun() = %#v, want %#v", got, want)
	}

	full := Manifest{{CheckID: IDAstroDep, Ran: true}, {CheckID: IDLockfile, Ran: true}}
	if got := full.NotRun(); got != nil {
		t.Errorf("NotRun() on a full manifest = %#v, want nil", got)
	}
}

// ---------------------------------------------------------------------
// The declared universe
// ---------------------------------------------------------------------

// TestDeclaredOrderIsTheCompleteUniverseInReportOrder pins both things
// this list is: the SET of check ids anything is expected to cover, and
// the ORDER results are reported in. Cheapest and most fundamental
// first, so somebody standing in the wrong directory reads "this isn't
// an Astro project" as the first line and not as the fourth.
//
// build-format sits immediately after pages-dir because one check
// produces both off one parse of one file, and splitting them would put
// two facts about one file in two places.
func TestDeclaredOrderIsTheCompleteUniverseInReportOrder(t *testing.T) {
	want := []string{IDAstroDep, IDLockfile, IDPagesDir, IDBuildFormat, IDLocalhost}
	if got := DeclaredOrder(); !reflect.DeepEqual(got, want) {
		t.Errorf("DeclaredOrder() = %v, want %v", got, want)
	}
}

// TestDeclaredOrderHandsBackACopy. The list is the single place the
// universe is written down, and a caller that ranged over it and sorted
// it in place would silently reorder every later run in the process.
// One shared mutable list is the same failure as two lists, arriving
// from the other direction.
//
// MUTATION: return the backing slice instead of a copy. Reds here.
func TestDeclaredOrderHandsBackACopy(t *testing.T) {
	first := DeclaredOrder()
	for i := range first {
		first[i] = "clobbered"
	}
	want := []string{IDAstroDep, IDLockfile, IDPagesDir, IDBuildFormat, IDLocalhost}
	if got := DeclaredOrder(); !reflect.DeepEqual(got, want) {
		t.Errorf("after a caller overwrote what it was given, DeclaredOrder() = %v, want %v",
			got, want)
	}
}

// TestRankFollowsTheDeclaredOrder, and an id nobody declared ranks after
// every id that was — never dropped, never a panic. A producer reporting
// something unexpected is still reporting something, and the failure
// mode with no symptom is the one to avoid.
func TestRankFollowsTheDeclaredOrder(t *testing.T) {
	for i, id := range DeclaredOrder() {
		if got := Rank(id); got != i {
			t.Errorf("Rank(%q) = %d, want %d", id, got, i)
		}
	}
	if got, floor := Rank("nobody-declared-this"), len(DeclaredOrder()); got != floor {
		t.Errorf("Rank of an undeclared id = %d, want %d — after everything declared", got, floor)
	}
}

// TestSortFindingsGroupsBySeverityThenDeclaredOrder. Every hard stop,
// then every warning, then the notes no surface shows; within one
// severity the declared order; within one check, arrival.
//
// It lives here rather than in the engine because the engine is not the
// only producer of findings, and two sorters would disagree the first
// time one of them was changed.
//
// MUTATION: drop the severity key. Reds on the warning arriving before
// the second hard stop.
func TestSortFindingsGroupsBySeverityThenDeclaredOrder(t *testing.T) {
	findings := []Finding{
		{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "second warning"},
		{CheckID: IDBuildFormat, Severity: SeverityNote, Message: "a note"},
		{CheckID: IDPagesDir, Severity: SeverityHardStop, Message: "second hard stop"},
		{CheckID: IDLockfile, Severity: SeverityWarning, Message: "first warning"},
		{CheckID: IDAstroDep, Severity: SeverityHardStop, Message: "first hard stop"},
	}

	SortFindings(findings)

	want := []string{"first hard stop", "second hard stop", "first warning", "second warning", "a note"}
	var got []string
	for _, f := range findings {
		got = append(got, f.Message)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestSortFindingsKeepsArrivalOrderWithinOneCheck. A check that reports
// three files in the order it walked them must not have them shuffled,
// and the sort has to be total or two runs over one project stop
// producing the same bytes.
//
// MUTATION: swap the stable sort for an unstable one. Reds
// intermittently, which is the reason the comparison is total.
func TestSortFindingsKeepsArrivalOrderWithinOneCheck(t *testing.T) {
	findings := []Finding{
		{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "first"},
		{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "second"},
		{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "third"},
	}

	SortFindings(findings)

	want := []string{"first", "second", "third"}
	var got []string
	for _, f := range findings {
		got = append(got, f.Message)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestSortManifestFollowsTheDeclaredOrder. The manifest is rendered, so
// its order is as load-bearing as the findings' — a report that reorders
// between two runs over one project cannot be diffed.
func TestSortManifestFollowsTheDeclaredOrder(t *testing.T) {
	m := Manifest{
		{CheckID: IDLocalhost, Ran: true},
		{CheckID: IDAstroDep, Ran: true},
		{CheckID: IDBuildFormat, Ran: true},
		{CheckID: IDLockfile, Ran: false, Reason: "couldn't read package.json"},
	}

	SortManifest(m)

	want := []string{IDAstroDep, IDLockfile, IDBuildFormat, IDLocalhost}
	var got []string
	for _, row := range m {
		got = append(got, row.CheckID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}
