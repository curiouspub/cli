package check

import "sort"

// declaredOrder is the COMPLETE SET of pre-flight check ids and the
// order results are reported in — one list doing both jobs, because they
// are the same question asked twice.
//
// IT LIVES IN THE LEAF, and that is a consequence rather than a
// preference. Findings do not all come from one producer: the file walk
// reports its own ids from a package that must never import the engine,
// and the row that asserts the two producers between them cover
// everything has to run somewhere both can be seen. A private copy
// inside the engine would be a second list, and two lists that are
// uniformly wrong pass every check that compares one against itself.
//
// The order is cheapest and most fundamental first, so somebody standing
// in the wrong directory reads "this isn't an Astro project" as the
// first line and not as the fourth. build-format sits immediately after
// pages-dir because one check produces both off one parse of one file,
// and splitting them would put two facts about one file in two places.
var declaredOrder = []string{
	IDAstroDep,
	IDLockfile,
	IDPagesDir,
	IDBuildFormat,
	IDLocalhost,
}

// DeclaredOrder returns the declared universe, in report order.
//
// A COPY, every time. This is the single place the universe is written
// down, so a caller that ranged over it and sorted it in place would
// quietly reorder every later run in the process. One shared mutable
// list is the same failure as two lists, arriving from the other side.
func DeclaredOrder() []string {
	out := make([]string, len(declaredOrder))
	copy(out, declaredOrder)
	return out
}

// Rank places a check id in the declared order.
//
// An id nobody declared ranks after every id that was, rather than being
// dropped or panicking: a producer reporting something unexpected is
// still reporting something, and the failure mode with no symptom is the
// one worth engineering against.
func Rank(id string) int {
	for i, known := range declaredOrder {
		if known == id {
			return i
		}
	}
	return len(declaredOrder)
}

// severityRank orders the report's sections: every hard stop, then every
// warning, then the notes no surface shows by default.
func severityRank(s Severity) int {
	switch s {
	case SeverityHardStop:
		return 0
	case SeverityWarning:
		return 1
	case SeverityNote:
		return 2
	default:
		return 3
	}
}

// SortFindings puts findings into report order, in place: severity
// first, then the declared check order, then the order they arrived in.
//
// THE COMPARISON IS TOTAL rather than merely sorted, and the sort is
// stable, because two runs over one project have to produce
// byte-identical output — a comparison that leaves any pair unordered
// hands that guarantee to whatever the sort happens to do with them.
//
// It lives beside the list rather than inside a producer because the
// engine is not the only producer, and two sorters would disagree the
// first time one of them was changed.
func SortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if sa, sb := severityRank(a.Severity), severityRank(b.Severity); sa != sb {
			return sa < sb
		}
		return Rank(a.CheckID) < Rank(b.CheckID)
	})
}

// SortManifest puts manifest rows into the declared order, in place. The
// manifest is rendered, so its order is exactly as load-bearing as the
// findings': a report that reorders between two runs over one project
// cannot be diffed.
func SortManifest(m Manifest) {
	sort.SliceStable(m, func(i, j int) bool {
		return Rank(m[i].CheckID) < Rank(m[j].CheckID)
	})
}
