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
// THE COMPARISON IS TOTAL, and the arrival key is what makes that
// sentence true rather than merely written down. Two keys leave every
// pair from one check at one severity unordered — the commonest shape a
// finding slice takes, since a check reports one finding per file — and
// their order then comes from whatever the sort call happens to do.
//
// A stable sort HIDES that completely, which is why the key was worth
// restoring rather than relying on. Measured on this toolchain, Go's
// unstable sort disturbs ties only past about a dozen elements AND only
// when the tied ones are interleaved with other groups — so a small
// project, or one where every finding comes from a single check, agrees
// with a broken comparison, and a project with a dozen-odd hard-coded
// development URLs across a few checks does not. Two runs over one
// project have to produce byte-identical output, and that guarantee
// should not depend on how many findings there happen to be.
//
// Because the comparison is now total, the sort need not be stable, and
// an unstable one is used deliberately: it means the property is carried
// by the comparison a test can read, not by the call underneath it.
//
// It lives beside the list rather than inside a producer because the
// engine is not the only producer, and two sorters would disagree the
// first time one of them was changed.
type placedFinding struct {
	finding Finding
	arrival int
}

func lessPlacedFinding(a, b placedFinding) bool {
	if sa, sb := severityRank(a.finding.Severity), severityRank(b.finding.Severity); sa != sb {
		return sa < sb
	}
	if ra, rb := Rank(a.finding.CheckID), Rank(b.finding.CheckID); ra != rb {
		return ra < rb
	}
	return a.arrival < b.arrival
}

func SortFindings(findings []Finding) {
	placed := make([]placedFinding, len(findings))
	for i, f := range findings {
		placed[i] = placedFinding{finding: f, arrival: i}
	}
	sort.Slice(placed, func(i, j int) bool { return lessPlacedFinding(placed[i], placed[j]) })
	for i, p := range placed {
		findings[i] = p.finding
	}
}

// SortManifest puts manifest rows into the declared order, in place, and
// then by arrival. The manifest is rendered, so its order is exactly as
// load-bearing as the findings': a report that reorders between two runs
// over one project cannot be diffed.
//
// The arrival key matters MORE here than in the findings, not less. A
// realistic manifest holds fewer rows than the size at which an unstable
// sort starts reordering ties, so a gap here could never be observed
// through sorted output however the fixture was written — the comparison
// has to be asserted directly.
type placedRan struct {
	row     Ran
	arrival int
}

func lessPlacedRan(a, b placedRan) bool {
	if ra, rb := Rank(a.row.CheckID), Rank(b.row.CheckID); ra != rb {
		return ra < rb
	}
	return a.arrival < b.arrival
}

func SortManifest(m Manifest) {
	placed := make([]placedRan, len(m))
	for i, row := range m {
		placed[i] = placedRan{row: row, arrival: i}
	}
	sort.Slice(placed, func(i, j int) bool { return lessPlacedRan(placed[i], placed[j]) })
	for i, p := range placed {
		m[i] = p.row
	}
}
