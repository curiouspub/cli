package check

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func ids(m Manifest) []string {
	var out []string
	for _, row := range m {
		out = append(out, row.CheckID)
	}
	return out
}

func messages(findings []Finding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.Message)
	}
	return out
}

// TestCombineMergesProducersIntoOneOrderedResult is the whole point of
// the function. Findings do not all come from one place — the engine
// runs the checks that read named files, and the file walk reports on
// the tree it walks — and a person reading the result should see ONE
// report in the declared order, not two reports concatenated in whatever
// order the caller happened to assemble them.
//
// The producers are passed in the wrong order on purpose: an order that
// mirrored the argument list would be the caller's rule, not this one's.
func TestCombineMergesProducersIntoOneOrderedResult(t *testing.T) {
	walk := Results{
		Findings: []Finding{
			{CheckID: IDCaseCollision, Severity: SeverityWarning, Message: "two names that collide"},
		},
		Manifest: Manifest{
			{CheckID: IDSymlinks},
			{CheckID: IDCaseCollision},
			{CheckID: IDPathCharset},
		},
	}
	limits := Results{
		Manifest: Manifest{
			{CheckID: IDLimitFiles},
			{CheckID: IDLimitFileSize},
			{CheckID: IDLimitTotal},
			{CheckID: IDLimitPacked, Outcome: Declined, Kind: ByDesign, Reason: "nothing packed yet"},
		},
	}
	engine := Results{
		Findings: []Finding{
			{CheckID: IDAstroDep, Severity: SeverityHardStop, Message: "not an Astro project"},
			{CheckID: IDPagesDir, Severity: SeverityWarning, Message: "no pages directory"},
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "a development URL"},
		},
		Manifest: Manifest{
			{CheckID: IDAstroDep},
			{CheckID: IDLockfile, Outcome: Declined, Kind: Environmental, Reason: "couldn't read package.json"},
			{CheckID: IDPagesDir},
			{CheckID: IDBuildFormat},
			{CheckID: IDLocalhost},
		},
	}

	got, err := Combine(walk, engine, limits)
	if err != nil {
		t.Fatalf("Combine: %v", err)
	}

	wantFindings := []string{
		"not an Astro project", "no pages directory", "a development URL", "two names that collide",
	}
	if msgs := messages(got.Findings()); !reflect.DeepEqual(msgs, wantFindings) {
		t.Errorf("findings = %v, want %v — hard stops first, then the declared order",
			msgs, wantFindings)
	}
	wantManifest := []string{
		IDAstroDep, IDLockfile, IDPagesDir, IDBuildFormat, IDLocalhost,
		IDSymlinks, IDCaseCollision, IDPathCharset,
		IDLimitFiles, IDLimitFileSize, IDLimitTotal, IDLimitPacked,
	}
	if rows := ids(got.Manifest()); !reflect.DeepEqual(rows, wantManifest) {
		t.Errorf("manifest = %v, want the declared order %v", rows, wantManifest)
	}
}

// TestCombineRefusesTwoProducersClaimingOneCheck. A manifest row is a
// CLAIM OF OWNERSHIP — "I was asked to look at this, and here is whether
// I did" — so two rows for one id are two producers answering the same
// question, and the answers can disagree. Silently keeping one is the
// failure with no symptom: the report looks complete and one producer's
// verdict has vanished.
//
// MUTATION: keep the first row and drop the rest. Reds here; every other
// row in this file passes, because none of them has a duplicate.
func TestCombineRefusesTwoProducersClaimingOneCheck(t *testing.T) {
	a := Results{Manifest: Manifest{{CheckID: IDAstroDep}}}
	b := Results{Manifest: Manifest{
		{CheckID: IDLockfile},
		{CheckID: IDAstroDep, Outcome: Declined, Kind: Environmental, Reason: "couldn't read package.json"},
	}}

	_, err := Combine(a, b)
	if err == nil {
		t.Fatal("Combine accepted two producers claiming the same check")
	}

	var dup *DuplicateCoverageError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %#v, want one a caller can inspect", err)
	}
	if !reflect.DeepEqual(dup.CheckIDs, []string{IDAstroDep}) {
		t.Errorf("CheckIDs = %v, want [%s]", dup.CheckIDs, IDAstroDep)
	}
	if !strings.Contains(err.Error(), IDAstroDep) {
		t.Errorf("message = %q, want it to name the check", err.Error())
	}
}

// TestCombineReportsEveryDuplicateAtOnce, in the declared order. Fixing
// one and rediscovering the next is the same round-trip the engine
// refuses to make a user do, and this error is read by whoever is wiring
// the producers together.
//
// MUTATION: return on the first duplicate found. Reds on the length.
func TestCombineReportsEveryDuplicateAtOnce(t *testing.T) {
	a := Results{Manifest: Manifest{
		{CheckID: IDLocalhost},
		{CheckID: IDAstroDep},
	}}
	b := Results{Manifest: Manifest{
		{CheckID: IDAstroDep},
		{CheckID: IDLocalhost},
	}}

	_, err := Combine(a, b)

	var dup *DuplicateCoverageError
	if !errors.As(err, &dup) {
		t.Fatalf("error = %#v, want a duplicate report", err)
	}
	want := []string{IDAstroDep, IDLocalhost}
	if !reflect.DeepEqual(dup.CheckIDs, want) {
		t.Errorf("CheckIDs = %v, want %v — every duplicate, in the declared order", dup.CheckIDs, want)
	}
}

// TestCombineRefusesADuplicateInsideOneProducer. The rule is about the
// combined manifest, not about who produced which row: one producer
// claiming a check twice is the same broken report as two producers
// claiming it once each, and a rule that only looked across producers
// would let the sloppier case through.
func TestCombineRefusesADuplicateInsideOneProducer(t *testing.T) {
	one := Results{Manifest: Manifest{
		{CheckID: IDAstroDep},
		{CheckID: IDAstroDep},
	}}

	if _, err := Combine(one); err == nil {
		t.Fatal("Combine accepted one producer claiming the same check twice")
	}
}

// TestCombineOfNothingIsRefused, and this row's assertion is the REVERSE
// of what it used to make. It read that zero producers was a legitimate
// question with a legitimate answer, and that erroring would make every
// caller special-case a state where nothing went wrong.
//
// Nothing HAS gone wrong in the project, and everything has gone wrong
// in the wiring: an engine registered with no checks produced an empty
// manifest, no findings and no error, and a renderer read that as a
// clean project and let the deploy proceed. The empty answer is
// indistinguishable from a passing one at exactly the layer that
// decides, which is what the coverage rule exists to prevent.
func TestCombineOfNothingIsRefused(t *testing.T) {
	got, err := Combine()
	if err == nil {
		t.Fatalf("Combine() of nothing produced a usable report: %+v", got)
	}
	if got.Valid() {
		t.Error("a refused Combine still returned a validated report")
	}
}

// TestCombineDoesNotDisturbTheProducersItWasGiven. A caller may hold on
// to its own result — to render it separately, or to report on its own
// producer — and a merge that sorted its argument in place would reorder
// something it does not own. The bug would surface far from here.
//
// MUTATION: sort the incoming manifest in place instead of a copy. Reds
// here and nowhere else.
func TestCombineDoesNotDisturbTheProducersItWasGiven(t *testing.T) {
	producer := Results{
		Findings: []Finding{
			{CheckID: IDLocalhost, Severity: SeverityWarning, Message: "second"},
			{CheckID: IDAstroDep, Severity: SeverityHardStop, Message: "first"},
		},
		// Deliberately out of declared order, and complete: the row is
		// about what Combine does to its argument, and a manifest that
		// did not pass the gate would never reach the sort at all.
		Manifest: Manifest{
			{CheckID: IDLocalhost},
			{CheckID: IDAstroDep},
			{CheckID: IDLockfile},
			{CheckID: IDPagesDir},
			{CheckID: IDBuildFormat},
			{CheckID: IDSymlinks},
			{CheckID: IDCaseCollision},
			{CheckID: IDPathCharset},
			{CheckID: IDLimitFiles},
			{CheckID: IDLimitFileSize},
			{CheckID: IDLimitTotal},
			{CheckID: IDLimitPacked},
		},
	}

	if _, err := Combine(producer); err != nil {
		t.Fatalf("Combine: %v", err)
	}

	if msgs := messages(producer.Findings); !reflect.DeepEqual(msgs, []string{"second", "first"}) {
		t.Errorf("the caller's findings were reordered: %v", msgs)
	}
	if rows := ids(producer.Manifest); !reflect.DeepEqual(rows, []string{
		IDLocalhost, IDAstroDep, IDLockfile, IDPagesDir, IDBuildFormat,
		IDSymlinks, IDCaseCollision, IDPathCharset,
		IDLimitFiles, IDLimitFileSize, IDLimitTotal, IDLimitPacked,
	}) {
		t.Errorf("the caller's manifest was reordered: %v", rows)
	}
}

// TestCoverageGapsReportsBothDirections. Two different failures, and
// naming only one of them is how the other ships: a declared check
// nobody ran is a silent hole in the report, and a row for an id nobody
// declared means a producer is answering a question that is not on the
// list — usually a typo in an id, which renders as a check the user has
// never heard of.
//
// MUTATION: return only the missing half. Reds on the unexpected half.
func TestCoverageGapsReportsBothDirections(t *testing.T) {
	m := Manifest{
		{CheckID: IDAstroDep},
		{CheckID: IDLocalhost},
		{CheckID: "typo-in-an-id"},
	}

	missing, unexpected := CoverageGaps(m)

	wantMissing := []string{
		IDLockfile, IDPagesDir, IDBuildFormat,
		IDSymlinks, IDCaseCollision, IDPathCharset,
		IDLimitFiles, IDLimitFileSize, IDLimitTotal, IDLimitPacked,
	}
	if !reflect.DeepEqual(missing, wantMissing) {
		t.Errorf("missing = %v, want %v in the declared order", missing, wantMissing)
	}
	if !reflect.DeepEqual(unexpected, []string{"typo-in-an-id"}) {
		t.Errorf("unexpected = %v, want [typo-in-an-id]", unexpected)
	}
}

// TestCoverageGapsIsSilentOnAFullManifest — the state everything else is
// measured against, and the one a gap report has to get right or it will
// be ignored.
func TestCoverageGapsIsSilentOnAFullManifest(t *testing.T) {
	var m Manifest
	for _, id := range DeclaredOrder() {
		m = append(m, Status{CheckID: id})
	}

	missing, unexpected := CoverageGaps(m)
	if missing != nil || unexpected != nil {
		t.Errorf("CoverageGaps on a full manifest = (%v, %v), want nothing", missing, unexpected)
	}
}
