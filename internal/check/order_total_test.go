package check

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// insertionSortThreshold is where Go's unstable sort stops being an
// insertion sort and can start reordering equal elements.
//
// MEASURED on this toolchain, and the measurement has TWO conditions,
// which is the part that is easy to get wrong:
//
//	elements   distinct groups   arrival preserved
//	3, 6, 12   any               yes
//	13+        1                 yes  — nothing to swap them past
//	13+        3                 NO
//
// So a fixture needs BOTH size and interleaving before it can observe
// the loss of a total comparison. A row of fifteen findings all under
// one id is the third-from-last cell and goes green under exactly the
// mutation it was written to catch — which is what happened here, and
// is why the table is written down rather than remembered.
//
// Thirteen is not a synthetic number for findings: the localhost check
// emits one per hit, so a project with thirteen hard-coded development
// URLs across a few files crosses it.
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
	var placed []placedStatus
	for _, id := range []string{IDAstroDep, IDLockfile, "one", "two", "three"} {
		placed = append(placed, placedStatus{row: Status{CheckID: id}, arrival: len(placed)})
	}

	for i := range placed {
		for j := range placed {
			if i == j {
				continue
			}
			forward, backward := lessPlacedStatus(placed[i], placed[j]), lessPlacedStatus(placed[j], placed[i])
			if forward == backward {
				t.Errorf("less(%d,%d)=%v and less(%d,%d)=%v: %q and %q are unordered",
					i, j, forward, j, i, backward, placed[i].row.CheckID, placed[j].row.CheckID)
			}
		}
	}
}

// TestSortFindingsKeepsArrivalOrderPastTheThreshold is the behavioural
// half, and BOTH of its fixture's properties were measured rather than
// assumed. An unstable sort reorders tied elements only when they are
// INTERLEAVED with other groups and only past a size — with one group,
// or below the size, it never moves anything:
//
//	elements   groups   arrival preserved under an unstable sort
//	3, 6, 12   any      yes
//	13+        1        yes  — nothing to swap them past
//	13+        3        NO
//
// The first version of this row had fifteen findings under ONE id, which
// is the "13+, one group" cell: it went green under the very mutation it
// was written to catch. Three ids at one severity is also the realistic
// shape — several checks each reporting a few files.
//
// MUTATION: drop the arrival key from lessPlacedFinding. Reds here.
// MUST NOT MOVE: the three-row arrival row, which passes either way —
// that is why this fixture exists separately from it.
func TestSortFindingsKeepsArrivalOrderPastTheThreshold(t *testing.T) {
	interleaved := []string{IDAstroDep, IDLockfile, IDLocalhost}
	n := (insertionSortThreshold + 2) * len(interleaved)

	var findings []Finding
	want := map[string][]string{}
	for i := 0; i < n; i++ {
		id := interleaved[i%len(interleaved)]
		message := fmt.Sprintf("hit-%02d", i)
		findings = append(findings, Finding{
			CheckID: id, Severity: SeverityWarning, Message: message,
		})
		want[id] = append(want[id], message)
	}

	SortFindings(findings)

	got := map[string][]string{}
	for _, f := range findings {
		got[f.CheckID] = append(got[f.CheckID], f.Message)
	}
	for id, messages := range want {
		if !reflect.DeepEqual(got[id], messages) {
			t.Errorf("%s: findings were reordered within one check:\ngot  %v\nwant %v",
				id, got[id], messages)
		}
	}
}

// TestSortManifestKeepsArrivalOrderPastTheThreshold. Ids nobody declared
// all share one rank, so they tie — and they are interleaved here with
// declared ids, which is the condition an unstable sort actually needs
// before it will disturb them.
//
// A real manifest will not hold rows like these. The function is
// exported and its contract is an order, so the contract is asserted at
// a shape that can observe it, and the comparator row above covers the
// sizes a real manifest reaches.
//
// MUTATION: drop the arrival key from lessPlacedStatus. Reds here.
func TestSortManifestKeepsArrivalOrderPastTheThreshold(t *testing.T) {
	n := insertionSortThreshold + 2

	var m Manifest
	var want []string
	for i := 0; i < n; i++ {
		for _, id := range []string{
			IDAstroDep,
			fmt.Sprintf("undeclared-%02d", i),
			IDLocalhost,
		} {
			m = append(m, Status{CheckID: id})
		}
		want = append(want, fmt.Sprintf("undeclared-%02d", i))
	}

	SortManifest(m)

	var got []string
	for _, row := range m {
		if strings.HasPrefix(row.CheckID, "undeclared-") {
			got = append(got, row.CheckID)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tied rows were reordered:\ngot  %v\nwant %v", got, want)
	}
}
