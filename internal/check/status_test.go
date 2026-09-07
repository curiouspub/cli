package check

import (
	"reflect"
	"testing"
)

// TestRanCarriesAStatusAndADeclineKind. A row used to say only whether a
// check ran, which could not express the state the tree has actually had
// all along: one check covering two ids, ANSWERING one and giving up on
// the other. The row is per id, so the status is too.
//
// The KIND is the part that is not decoration. Two declines cost a user
// different things — one is something outside the check that they may be
// able to fix, the other is the check declining to guess — and a row
// that could not tell them apart would have to charge the same for both.
func TestRanCarriesAStatusAndADeclineKind(t *testing.T) {
	answered := Ran{CheckID: IDPagesDir}
	if answered.Status != Answered {
		t.Errorf("Status = %v, want Answered", answered.Status)
	}
	if answered.Reason != "" {
		t.Errorf("Reason = %q on an answered row, want empty", answered.Reason)
	}

	environmental := Ran{
		CheckID: IDLockfile,
		Status:  Declined,
		Kind:    Environmental,
		Reason:  "couldn't read package.json",
	}
	if environmental.Status != Declined || environmental.Kind != Environmental {
		t.Errorf("row = %+v, want an environmental decline", environmental)
	}

	byDesign := Ran{
		CheckID: IDBuildFormat,
		Status:  Declined,
		Kind:    ByDesign,
		Reason:  "the config builds this value at run time",
	}
	if byDesign.Status != Declined || byDesign.Kind != ByDesign {
		t.Errorf("row = %+v, want a by-design decline", byDesign)
	}
}

// TestAnsweredIsTheZeroStatus, and the zero DECLINE KIND is
// environmental. Both defaults are chosen rather than inherited, so this
// row exists to make a later change to either of them visible.
//
// Answered-as-zero means a row built without thinking about status reads
// as a tick, which is the shape this program calls a lie when it is
// wrong. It is tolerable HERE and nowhere else because rows are built in
// exactly two places in this program — the engine, from a check's own
// per-id declines, and the walk, at a single guarded site — where a
// finding's severity is set at every emission point in every check.
func TestAnsweredIsTheZeroStatus(t *testing.T) {
	var row Ran
	if row.Status != Answered {
		t.Errorf("the zero Status is %v, want Answered", row.Status)
	}
	if row.Kind != Environmental {
		t.Errorf("the zero DeclineKind is %v, want Environmental — the costlier of the "+
			"two, so an unset kind is read in the safe direction", row.Kind)
	}
}

// TestManifestDeclinesSelectsByKind is the accessor the renderer decides
// on. Reading not-run from the manifest was always the rule; reading the
// KIND from it is what stops one sentence being charged at two prices.
//
// MUTATION: have DeclinesOfKind ignore the kind. The by-design row and
// the environmental row both red.
func TestManifestDeclinesSelectsByKind(t *testing.T) {
	m := Manifest{
		{CheckID: IDAstroDep},
		{CheckID: IDLockfile, Status: Declined, Kind: Environmental, Reason: "couldn't read package.json"},
		{CheckID: IDPagesDir},
		{CheckID: IDBuildFormat, Status: Declined, Kind: ByDesign, Reason: "the value is built at run time"},
	}

	if got := ids(m.Declines()); !reflect.DeepEqual(got, []string{IDLockfile, IDBuildFormat}) {
		t.Errorf("Declines() = %v, want both declines in manifest order", got)
	}
	if got := ids(m.DeclinesOfKind(Environmental)); !reflect.DeepEqual(got, []string{IDLockfile}) {
		t.Errorf("DeclinesOfKind(Environmental) = %v, want [%s]", got, IDLockfile)
	}
	if got := ids(m.DeclinesOfKind(ByDesign)); !reflect.DeepEqual(got, []string{IDBuildFormat}) {
		t.Errorf("DeclinesOfKind(ByDesign) = %v, want [%s]", got, IDBuildFormat)
	}
}

// TestManifestDeclinesIsSilentOnAFullyAnsweredManifest — the state
// everything else is measured against, and the one an accessor has to
// get right or every clean project grows a line.
func TestManifestDeclinesIsSilentOnAFullyAnsweredManifest(t *testing.T) {
	var m Manifest
	for _, id := range DeclaredOrder() {
		m = append(m, Ran{CheckID: id})
	}

	if got := m.Declines(); got != nil {
		t.Errorf("Declines() = %+v, want nothing", got)
	}
	for _, kind := range []DeclineKind{Environmental, ByDesign} {
		if got := m.DeclinesOfKind(kind); got != nil {
			t.Errorf("DeclinesOfKind(%v) = %+v, want nothing", kind, got)
		}
	}
}

// TestDeclineCarriesWhatARowNeeds. It is the shape a check hands back
// per id, and it exists so the kind and the reason are defined once
// rather than once for the producer and once for the row — two homes for
// one fact diverge.
func TestDeclineCarriesWhatARowNeeds(t *testing.T) {
	d := Decline{Kind: ByDesign, Reason: "the value is built at run time"}
	if d.Kind != ByDesign || d.Reason != "the value is built at run time" {
		t.Errorf("Decline = %+v, want the kind and the reason back unchanged", d)
	}
}
