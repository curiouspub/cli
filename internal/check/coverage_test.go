// This file is the LEAF'S EXTERNAL TEST PACKAGE, and the package clause
// is the point of it.
//
// The row below asserts a whole-program property — that between them the
// producers cover every declared check and invent none — so it has to see
// every producer. A leaf cannot import a producer, but `package
// check_test` is compiled separately from `package check` and may import
// packages that import it, which is what makes the assertion possible
// where the concept lives instead of inside one producer's suite. A
// program-wide guarantee kept in one producer's test file disappears the
// day that file is reorganised.
//
// Probed before it was relied on, rather than reasoned about.
package check_test

import (
	"testing"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/pack"
	"github.com/curiouspub/cli/internal/preflight"
)

// standIn is a producer's check that reports nothing. These rows are
// about WHICH QUESTIONS GET ANSWERED, not about the answers, and a check
// that reads a real file would only add a fixture to a row that does not
// depend on one.
func standIn(ids ...string) preflight.Check {
	return preflight.Check{
		IDs: ids,
		Run: func(preflight.FS, string) preflight.Result { return preflight.Result{} },
	}
}

// TestCombinedCoverageEqualsTheDeclaredUniverse is the row that makes
// several producers safe, and it is the reason the declared list left the
// engine.
//
// Each producer can only see its own coverage. Only the UNION can be
// compared against what was supposed to be covered — so a check nobody
// was wired up to run is invisible to every producer separately and
// obvious here. Both directions are asserted: a declared check with no
// row is a silent hole in the report, and a row for an id nobody declared
// is a producer answering a question that is not on the list, which
// reaches a user as the name of a check they have never heard of.
//
// THERE ARE NOW THREE PRODUCERS, which is what this row was shaped for.
// The file walk arrived as one more argument and three more entries in
// the declared list; the local limits arrived the same way with four
// more. The shape held both times: the row combines rather than reading
// one manifest, so nothing here had to learn that another producer
// exists beyond being handed it.
//
// THE LIMITS ARE MEASURED OVER THE WALK'S OWN LIST, which for an empty
// directory is empty — and they still claim all four of their rows. That
// is the property this row is in the best place to see: a producer whose
// verdicts are all "nothing to say" still has to answer for every
// question it owns, or the report it belongs to cannot be built.
//
// The walk is asked to scan A REAL DIRECTORY THAT IS EMPTY — the test's
// own scratch directory — because this row is about WHICH QUESTIONS GET
// ANSWERED and not about the answers. Its manifest is complete whatever
// it finds, which is the property that makes the row cheap; a fixture
// with something wrong in it would only add findings nobody here reads.
//
// MUTATION: leave one check out of the registered set — the missing half
// reds. MUTATION: give a stand-in an id nobody declared — the unexpected
// half reds. MUTATION: drop one id from the walk's own manifest rows —
// the missing half reds and no finding row moves.
func TestCombinedCoverageEqualsTheDeclaredUniverse(t *testing.T) {
	engine := preflight.Run([]preflight.Check{
		standIn(check.IDAstroDep),
		standIn(check.IDLockfile),
		standIn(check.IDPagesDir, check.IDBuildFormat),
		standIn(check.IDLocalhost),
	}, preflight.OSFileSystem{}, "irrelevant")

	tree, err := pack.Walk(pack.OSFileSystem{}, t.TempDir())
	if err != nil {
		t.Fatalf("walking an empty directory: %v", err)
	}

	combined, err := check.Combine(engine, tree.Results, pack.Limits(tree.Files))
	if err != nil {
		t.Fatalf("combining the producers: %v", err)
	}

	missing, unexpected := check.CoverageGaps(combined.Manifest())
	if len(missing) != 0 {
		t.Errorf("declared checks nobody covered: %v — every id in the declared "+
			"universe needs a producer, or the report has a hole nothing reports", missing)
	}
	if len(unexpected) != 0 {
		t.Errorf("covered ids nobody declared: %v — a producer is answering a question "+
			"that is not on the list, and a user would meet it as a check they have "+
			"never heard of", unexpected)
	}
}
