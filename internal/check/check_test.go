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

// TestStatusRecordsWhyNot pins the row a manifest exists to carry. A check
// that ran carries no reason; one that did not carries the reason, and
// the reason is the only thing standing between a reader and the
// assumption that silence means a clean result.
func TestStatusRecordsWhyNot(t *testing.T) {
	ran := Status{CheckID: IDPagesDir}
	if ran.Outcome != Answered {
		t.Error("outcome is not answered on a row that answered")
	}
	if ran.Reason != "" {
		t.Errorf("Reason = %q on a row that ran, want empty", ran.Reason)
	}

	skipped := Status{CheckID: IDLockfile, Outcome: Declined, Kind: Environmental, Reason: "couldn't read package.json"}
	if skipped.Outcome != Declined {
		t.Error("outcome is not declined on a row that declined")
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
		{CheckID: IDAstroDep},
		{CheckID: IDLockfile, Outcome: Declined, Kind: Environmental, Reason: "couldn't read package.json"},
		{CheckID: IDPagesDir},
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

// THE CHECK-ID SET IS ASSERTED IN internal/guard, not here.
//
// A row stood in this place claiming to pin the ids. It compared a
// hand-written map of the constants against a hand-written map of their
// values — two transcriptions of one author's belief, which agree with
// each other by construction. It could see a value change and nothing
// else: a constant added and left out of both maps was invisible, and
// neither map was ever compared against the declared universe, which is
// the list the program actually uses. When a proposed check was retired
// it caught nothing, and the declared-order rows did.
//
// Its replacement reads the constants out of the SOURCE with a type
// checker and compares them against the compiled program's own declared
// order — two mechanisms, so one edit cannot satisfy both by agreeing
// with itself. It also asks a type question rather than a spelling one,
// so an id named without the usual prefix is still seen.
//
// This note is here because a row removed with no pointer is
// indistinguishable from a row nobody replaced.

// TestManifestNotRunSelectsOnlyTheSkipped asserts the accessor a
// renderer is meant to use, including that it reports nothing when every
// check ran — the state a findings-only stream renders identically to a
// project nobody looked at.
func TestManifestNotRunSelectsOnlyTheSkipped(t *testing.T) {
	m := Manifest{
		{CheckID: IDAstroDep},
		{CheckID: IDLockfile, Outcome: Declined, Kind: Environmental, Reason: "couldn't read package.json"},
		{CheckID: IDPagesDir},
		{CheckID: IDBuildFormat, Outcome: Declined, Kind: Environmental, Reason: "couldn't read astro.config"},
	}

	want := []Status{
		{CheckID: IDLockfile, Outcome: Declined, Kind: Environmental, Reason: "couldn't read package.json"},
		{CheckID: IDBuildFormat, Outcome: Declined, Kind: Environmental, Reason: "couldn't read astro.config"},
	}
	if got := m.Declines(); !reflect.DeepEqual(got, want) {
		t.Errorf("Declines() = %#v, want %#v", got, want)
	}

	full := Manifest{{CheckID: IDAstroDep}, {CheckID: IDLockfile}}
	if got := full.Declines(); got != nil {
		t.Errorf("Declines() on a full manifest = %#v, want nil", got)
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
	want := []string{
		IDAstroDep, IDLockfile, IDPagesDir, IDBuildFormat, IDLocalhost,
		IDSymlinks, IDCaseCollision, IDPathCharset,
	}
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
	want := []string{
		IDAstroDep, IDLockfile, IDPagesDir, IDBuildFormat, IDLocalhost,
		IDSymlinks, IDCaseCollision, IDPathCharset,
	}
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
		{CheckID: IDLocalhost},
		{CheckID: IDAstroDep},
		{CheckID: IDBuildFormat},
		{CheckID: IDLockfile, Outcome: Declined, Kind: Environmental, Reason: "couldn't read package.json"},
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

// ---------------------------------------------------------------------
// The optional three-part copy
// ---------------------------------------------------------------------

// TestFindingCarriesOptionalCopy. Message stays the required one-line
// summary; What/Why/Next are what a check that has worked its copy out
// puts beside it. The three parts exist because a hard stop that names
// no action leaves the reader guessing, and the reader is usually
// somebody deploying their first site.
func TestFindingCarriesOptionalCopy(t *testing.T) {
	f := Finding{
		CheckID:  IDLockfile,
		Severity: SeverityHardStop,
		Message:  "no lockfile found",
		What:     "No lockfile found.",
		Why:      "curious installs your dependencies from a lockfile.",
		Next:     "Run `npm install`, commit the lockfile, and try again.",
	}
	if f.Message != "no lockfile found" {
		t.Errorf("Message = %q, want the summary to survive alongside the copy", f.Message)
	}
	for _, part := range []struct{ name, got, want string }{
		{"What", f.What, "No lockfile found."},
		{"Why", f.Why, "curious installs your dependencies from a lockfile."},
		{"Next", f.Next, "Run `npm install`, commit the lockfile, and try again."},
	} {
		if part.got != part.want {
			t.Errorf("%s = %q, want %q", part.name, part.got, part.want)
		}
	}

	bare := Finding{CheckID: IDAstroDep, Severity: SeverityHardStop, Message: "summary only"}
	if bare.What != "" || bare.Why != "" || bare.Next != "" {
		t.Errorf("a finding that carries no copy has non-empty parts: %+v", bare)
	}
}

// TestHasCopyAsksAboutAnyPart, not about all three. A check that wrote
// only the action — the part a reader can act on, and the one most often
// missing — has worked its copy out as far as it needed to, and
// answering "no" there would throw that sentence away.
//
// It is asked HERE rather than at each surface so the terminal and the
// machine-readable result cannot disagree about whether a finding has
// copy, which is the sort of divergence nobody notices until the two
// render the same finding differently.
//
// MUTATION: require all three parts. The three single-part rows red.
func TestHasCopyAsksAboutAnyPart(t *testing.T) {
	cases := []struct {
		name    string
		finding Finding
		want    bool
	}{
		{"nothing", Finding{Message: "m"}, false},
		{"what only", Finding{Message: "m", What: "w"}, true},
		{"why only", Finding{Message: "m", Why: "y"}, true},
		{"next only", Finding{Message: "m", Next: "n"}, true},
		{"all three", Finding{Message: "m", What: "w", Why: "y", Next: "n"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.finding.HasCopy(); got != tc.want {
				t.Errorf("HasCopy() = %v, want %v", got, tc.want)
			}
		})
	}
}
