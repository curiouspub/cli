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
// TODAY THERE IS ONE PRODUCER. The walk is not written yet, and this row
// is deliberately shaped for two: it combines rather than reading one
// manifest, so the walk arrives as one more argument and one more entry
// in the declared list, and nothing here is rewritten to notice it.
//
// MUTATION: leave one check out of the registered set — the missing half
// reds. MUTATION: give a stand-in an id nobody declared — the unexpected
// half reds.
func TestCombinedCoverageEqualsTheDeclaredUniverse(t *testing.T) {
	engine := preflight.Run([]preflight.Check{
		standIn(check.IDAstroDep),
		standIn(check.IDLockfile),
		standIn(check.IDPagesDir, check.IDBuildFormat),
		standIn(check.IDLocalhost),
	}, preflight.OSFileSystem{}, "irrelevant")

	combined, err := check.Combine(engine)
	if err != nil {
		t.Fatalf("combining the producers: %v", err)
	}

	missing, unexpected := check.CoverageGaps(combined.Manifest)
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
