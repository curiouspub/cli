package check

import (
	"reflect"
	"slices"
	"testing"
)

// A NEGATIVE FIXTURE'S PREMISE IS AN ASSERTION, NOT A NAME.
//
// Several rows here need an id that NOTHING claims — the row proving a
// finding under an unclaimed id is refused, the row proving a manifest
// row for an undeclared id is reported. Written as a plausible-looking
// literal, that premise is a bet on what gets built next, and the space
// of ids nobody will claim shrinks every time the program grows.
//
// The bet was lost inside this repository. The unclaimed-finding row
// invented "symlinks", correctly, at a time when no check answered to
// it — and the file walk then claimed exactly that id. The row would
// have gone on passing right up to the moment the id became real, and
// then reported that the combiner had ACCEPTED an unclaimed finding,
// which is the opposite of what had happened. Nothing about the failure
// would have pointed at the fixture.
//
// So the id is reserved, once, here, and the guard below turns the
// premise into something that fails when it stops being true. The
// spelling is deliberately not a shape any check id takes — check ids
// are lowercase words joined by hyphens, and this one carries a dot —
// but the shape is a courtesy and the guard is the guarantee.

const reservedUnclaimedID = "reserved.never-a-check"

// TestTheReservedIDIsClaimedByNothing is that guarantee.
//
// It asks the question three ways because they are three different
// promises, and a row can lose any one of them separately: the id is not
// in the declared list, it does not rank as though it were, and the
// coverage check reports it as something nobody declared. A fixture
// resting on the first two would still mislead if the third changed.
//
// MUTATION: set reservedUnclaimedID to a declared id. Reds here, and in
// every row that rests on it — which is the point: the failure names the
// fixture rather than the code the fixture was testing.
func TestTheReservedIDIsClaimedByNothing(t *testing.T) {
	declared := DeclaredOrder()
	if len(declared) == 0 {
		t.Fatal("the declared universe is empty — this guard would pass by comparing " +
			"against nothing")
	}

	if slices.Contains(declared, reservedUnclaimedID) {
		t.Fatalf("%q is in the declared universe %v — every negative fixture resting on "+
			"it is now asserting the opposite of what it was written for",
			reservedUnclaimedID, declared)
	}

	if got, after := Rank(reservedUnclaimedID), len(declared); got != after {
		t.Errorf("Rank(%q) = %d, want %d — an id nothing declared ranks after everything "+
			"that was", reservedUnclaimedID, got, after)
	}

	missing, unexpected := CoverageGaps(append(everyDeclaredID(),
		Status{CheckID: reservedUnclaimedID}))
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none — the manifest claims the whole universe", missing)
	}
	if !reflect.DeepEqual(unexpected, []string{reservedUnclaimedID}) {
		t.Errorf("unexpected = %v, want [%s]", unexpected, reservedUnclaimedID)
	}
}
