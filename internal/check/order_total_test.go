package check

import (
	"fmt"
	"reflect"
	"testing"
)

// insertionSortThreshold is where Go's unstable sort stops being an
// insertion sort and starts reordering equal elements. MEASURED on this
// toolchain rather than assumed: ties are preserved at n of 3, 6 and 12
// and reordered from 13 upward.
//
// It is named here because it is the reason the fixtures below are the
// size they are. A three-row fixture cannot tell a total comparison from
// a stable sort, so a row built at that size passes whether or not the
// property it claims to test exists.
//
// Thirteen is not a synthetic number for findings: the localhost check
// emits one per hit, so a project with thirteen hard-coded development
// URLs crosses it.
const insertionSortThreshold = 13

// TestFindingComparatorIsTotal is the assertion the doc comment has been
// making in prose. For every ordered pair of distinct positions exactly
// one of less(a,b) and less(b,a) must hold — otherwise the pair is
// unordered, the comparison is not total, and the output order is
// whatever the sort call happens to do with it.
//
// Asserted on the COMPARATOR rather than on sorted output, because a
// stable sort hides the defect completely: it supplies an order the
// comparison does not, and the result looks correct until somebody
// changes the sort call or the input grows past the threshold above.
func TestFindingComparatorIsTotal(t *testing.T) {
	// Deliberately full of ties: same severity, same check, different
	// messages. That is one check reporting several files, which is the
	// commonest shape a finding slice takes.
	var placed []placedFinding
	for i := 0; i < 4; i++ {
		placed = append(placed, placedFinding{
			finding: Finding{CheckID: IDLocalhost, Severity: SeverityWarning,
				Message: fmt.Sprintf("hit %d", i)},
			arrival: len(placed),
		})
	}
	placed = append(placed, placedFinding{
		finding: Finding{CheckID: IDAstroDep, Severity: SeverityHardStop, Message: "hard"},
		arrival: len(placed),
	})

	for i := range placed {
		for j := range placed {
			if i == j {
				continue
			}
			forward, backward := lessPlacedFinding(placed[i], placed[j]), lessPlacedFinding(placed[j], placed[i])
			if forward == backward {
				t.Errorf("less(%d,%d)=%v and less(%d,%d)=%v: the pair %q/%q is unordered, "+
					"so their order comes from the sort call rather than from the comparison",
					i, j, forward, j, i, backward,
					placed[i].finding.Message, placed[j].finding.Message)
			}
		}
	}
}

// TestManifestComparatorIsTotal is the same assertion for the other
// sorter, and it matters more there rather than less: a realistic
// manifest is smaller than the threshold, so no amount of fixture
// growth would ever expose the gap through sorted output alone.
func TestManifestComparatorIsTotal(t *testing.T) {
	var placed []placedRan
	for _, id := range []string{IDAstroDep, IDLockfile, "one", "two", "three"} {
		placed = append(placed, placedRan{row: Ran{CheckID: id, Ran: true}, arrival: len(placed)})
	}

	for i := range placed {
		for j := range placed {
			if i == j {
				continue
			}
			forward, backward := lessPlacedRan(placed[i], placed[j]), lessPlacedRan(placed[j], placed[i])
			if forward == backward {
				t.Errorf("less(%d,%d)=%v and less(%d,%d)=%v: %q and %q are unordered",
					i, j, forward, j, i, backward, placed[i].row.CheckID, placed[j].row.CheckID)
			}
		}
	}
}

// TestSortFindingsKeepsArrivalOrderPastTheThreshold is the behavioural
// half, built one element past the size at which an unstable sort starts
// reordering ties. The three-row fixture beside it cannot catch the loss
// of a total comparison; this one can.
//
// MUTATION: drop the arrival key from lessPlacedFinding. Reds here.
// MUST NOT MOVE: the three-row arrival row, which passes either way —
// that is the whole point of this fixture existing separately.
func TestSortFindingsKeepsArrivalOrderPastTheThreshold(t *testing.T) {
	n := insertionSortThreshold + 2

	var findings []Finding
	var want []string
	for i := 0; i < n; i++ {
		message := fmt.Sprintf("src/pages/page-%02d.astro has a development URL", i)
		findings = append(findings, Finding{
			CheckID: IDLocalhost, Severity: SeverityWarning, Message: message,
		})
		want = append(want, message)
	}

	SortFindings(findings)

	var got []string
	for _, f := range findings {
		got = append(got, f.Message)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %d tied findings the order changed:\ngot  %v\nwant %v", n, got, want)
	}
}

// TestSortManifestKeepsArrivalOrderPastTheThreshold. The ids are all
// undeclared, which is what makes them tie: Rank gives every undeclared
// id the same position, so their order among themselves is decided by
// the comparison or by nothing.
//
// A real manifest will not hold fifteen undeclared rows. The function is
// exported and its contract is an order, so the contract is asserted at
// a size that can actually observe it.
//
// MUTATION: drop the arrival key from lessPlacedRan. Reds here.
func TestSortManifestKeepsArrivalOrderPastTheThreshold(t *testing.T) {
	n := insertionSortThreshold + 2

	var m Manifest
	var want []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("undeclared-%02d", i)
		m = append(m, Ran{CheckID: id, Ran: true})
		want = append(want, id)
	}

	SortManifest(m)

	var got []string
	for _, row := range m {
		got = append(got, row.CheckID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with %d tied rows the order changed:\ngot  %v\nwant %v", n, got, want)
	}
}
