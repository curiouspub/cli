package check

import (
	"fmt"
	"sort"
	"strings"
)

// Results is one producer's whole answer: what it found, and which
// checks it was asked to cover.
//
// There is more than one producer by design. The engine runs the checks
// that read named files; the file walk reports on the tree it walks, and
// lives in a package that must never import the engine. Neither can see
// the other, which is exactly why their answers meet HERE, in the leaf
// both of them already depend on.
type Results struct {
	Findings []Finding
	Manifest Manifest
}

// DuplicateCoverageError is what Combine returns when more than one
// manifest row claims the same check.
//
// It carries the ids rather than only a sentence, so the caller wiring
// the producers together can act on them — and it carries ALL of them,
// in the declared order, because fixing one and rediscovering the next
// is the same round-trip the engine refuses to make a user do.
type DuplicateCoverageError struct {
	CheckIDs []string
}

func (e *DuplicateCoverageError) Error() string {
	return fmt.Sprintf("more than one producer claims %s; each check belongs to exactly one",
		strings.Join(e.CheckIDs, ", "))
}

// Combine merges independent producers' answers into one report.
//
// IT REFUSES DUPLICATES, and that is the reason it is a function rather
// than two appends at a call site. A manifest row is a CLAIM OF
// OWNERSHIP — "I was asked to look at this, and here is whether I did" —
// so two rows for one id are two producers answering the same question,
// and the answers can disagree. Silently keeping one is the failure with
// no symptom: the report looks complete, and one producer's verdict has
// vanished on its way to the person reading it.
//
// The rule is about the COMBINED manifest rather than about who produced
// which row. One producer claiming a check twice is the same broken
// report as two producers claiming it once each, and a rule that only
// looked across producers would let the sloppier case through.
//
// Findings are not deduplicated and must not be: three hard-coded
// development URLs in three files are three findings from one check, and
// that is the shape the report wants.
//
// Nothing the caller passed in is disturbed, and that includes the
// CONTENTS of what it passed. Paths is copied rather than shared: a
// producer may keep its own result to render or report on separately,
// and a caller writing to a path in the combined report used to reach
// into the producer's own finding, with the symptom surfacing a long way
// from here.
//
// IT IS THE ONLY THING THAT RETURNS A Report, which is what turns every
// promise below from a property of a call site into a property of a
// type. See Report.
//
// FOUR ENFORCEMENTS, in this order:
//
//  1. Duplicate claims, first — a check claimed twice makes every later
//     question ill-posed, so it is worth answering before them.
//  2. Coverage, in both directions.
//  3. Findings under ids nobody claimed.
//  4. Severities that are not one of the three constants.
//
// A caller gets the first failure rather than all four. They are not
// independent — one wiring mistake usually trips several — and a reader
// fixing the first will re-run anyway.
func Combine(parts ...Results) (Report, error) {
	var findings []Finding
	var manifest Manifest
	for _, part := range parts {
		for _, f := range part.Findings {
			findings = append(findings, copyFinding(f))
		}
		manifest = append(manifest, part.Manifest...)
	}

	if duplicates := duplicateIDs(manifest); len(duplicates) > 0 {
		return Report{}, &DuplicateCoverageError{CheckIDs: duplicates}
	}

	if missing, unexpected := CoverageGaps(manifest); len(missing) > 0 || len(unexpected) > 0 {
		return Report{}, &CoverageError{Missing: missing, Unexpected: unexpected}
	}

	if unclaimed := unclaimedIDs(findings, manifest); len(unclaimed) > 0 {
		return Report{}, &UnclaimedFindingError{CheckIDs: unclaimed}
	}

	if undeclared := undeclaredSeverityIDs(findings); len(undeclared) > 0 {
		return Report{}, &UndeclaredSeverityError{CheckIDs: undeclared}
	}

	SortFindings(findings)
	SortManifest(manifest)
	return Report{findings: findings, manifest: manifest, validated: true}, nil
}

// copyFinding returns a finding that shares nothing with the one it was
// given. Only Paths needs it — every other field is a string.
func copyFinding(f Finding) Finding {
	if f.Paths == nil {
		return f
	}
	paths := make([]string, len(f.Paths))
	copy(paths, f.Paths)
	f.Paths = paths
	return f
}

// unclaimedIDs returns every check id a finding reports on that no
// manifest row claims, deterministically ordered.
func unclaimedIDs(findings []Finding, m Manifest) []string {
	claimed := make(map[string]bool, len(m))
	for _, row := range m {
		claimed[row.CheckID] = true
	}

	seen := map[string]bool{}
	var out []string
	for _, f := range findings {
		if !claimed[f.CheckID] && !seen[f.CheckID] {
			seen[f.CheckID] = true
			out = append(out, f.CheckID)
		}
	}
	sortIDs(out)
	return out
}

// undeclaredSeverityIDs returns the check ids of findings whose severity
// is not one of the three constants, deterministically ordered.
func undeclaredSeverityIDs(findings []Finding) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range findings {
		if !f.Severity.Declared() && !seen[f.CheckID] {
			seen[f.CheckID] = true
			out = append(out, f.CheckID)
		}
	}
	sortIDs(out)
	return out
}

// duplicateIDs returns every check id claimed more than once, in the
// declared order, then alphabetically for ids nobody declared — a
// deterministic list, because an error message that reorders between two
// runs is one nobody can paste into a bug report.
func duplicateIDs(m Manifest) []string {
	seen := make(map[string]int, len(m))
	for _, row := range m {
		seen[row.CheckID]++
	}

	var out []string
	for id, count := range seen {
		if count > 1 {
			out = append(out, id)
		}
	}
	sortIDs(out)
	return out
}

// CoverageGaps compares a combined manifest against the declared
// universe and reports BOTH directions, because they are two different
// failures and naming only one is how the other ships.
//
// A declared check with no row is a silent hole: nobody looked, and
// nothing in the report says so. A row for an id nobody declared is a
// producer answering a question that is not on the list — in practice a
// mistyped id, which reaches the user as the name of a check they have
// never heard of.
//
// This is the assertion that makes several producers safe. Each one can
// only see its own coverage; only the union can be compared against what
// was supposed to be covered.
func CoverageGaps(m Manifest) (missing, unexpected []string) {
	covered := make(map[string]bool, len(m))
	for _, row := range m {
		covered[row.CheckID] = true
	}

	declared := make(map[string]bool, len(declaredOrder))
	for _, id := range declaredOrder {
		declared[id] = true
		if !covered[id] {
			missing = append(missing, id)
		}
	}

	seen := make(map[string]bool, len(m))
	for _, row := range m {
		if !declared[row.CheckID] && !seen[row.CheckID] {
			seen[row.CheckID] = true
			unexpected = append(unexpected, row.CheckID)
		}
	}

	sortIDs(unexpected)
	return missing, unexpected
}

// sortIDs orders check ids the way every list of them is ordered here:
// declared order first, then alphabetically for the ones nobody
// declared, which have no declared position to sort by.
func sortIDs(ids []string) {
	sort.SliceStable(ids, func(i, j int) bool {
		ri, rj := Rank(ids[i]), Rank(ids[j])
		if ri != rj {
			return ri < rj
		}
		return ids[i] < ids[j]
	})
}
