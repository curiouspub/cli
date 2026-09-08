package preflight

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// engineChecks is every check this package registers, over the walked
// file list the localhost row scans.
//
// It is written out here rather than exported from production because
// assembling the set is the DEPLOY FLOW's decision, not this package's:
// the file list comes from a traversal that happens outside the engine,
// and a package-level constructor would have to either take that list or
// invent one. What this package owes is that each registration claims
// the right ids, and the row below asserts exactly that.
func engineChecks(files []string) []Check {
	return []Check{
		AstroDepCheck(),
		LockfileCheck(),
		AstroConfigCheck(),
		LocalhostCheck(files),
	}
}

// TestTheRegisteredChecksClaimExactlyThisPackagesIDs is the wiring row.
// Four registrations, five ids, in the declared order — and the two ends
// are asserted separately because they fail differently: an id nobody
// claims is a silent hole in the report, and an id claimed twice is two
// producers answering one question.
//
// THE REMAINING THREE ARE ASSERTED AS MISSING, on purpose. They belong
// to the file walk, which reports from a package that must never import
// this one, so "this package covers exactly its own five" is only
// checkable by naming what it does NOT cover. A row asserting no gaps at
// all would either be false or be quietly asking the wrong question.
//
// REQUIRED MUTATION: drop LockfileCheck() from engineChecks above. The
// manifest loses a row and the missing set gains lockfile; both halves
// red.
func TestTheRegisteredChecksClaimExactlyThisPackagesIDs(t *testing.T) {
	res := Run(engineChecks(nil), OSFileSystem{}, projectFixture(t, "valid"))

	want := []string{
		check.IDAstroDep,
		check.IDLockfile,
		check.IDPagesDir,
		check.IDBuildFormat,
		check.IDLocalhost,
	}
	if got := manifestIDs(res.Manifest); !equalStrings(got, want) {
		t.Errorf("manifest = %v, want one row per id this package owns, in the "+
			"declared order %v", got, want)
	}

	missing, unexpected := check.CoverageGaps(res.Manifest)
	wantMissing := []string{check.IDSymlinks, check.IDCaseCollision, check.IDPathCharset}
	if !equalStrings(missing, wantMissing) {
		t.Errorf("missing = %v, want exactly the ids the file walk owns %v",
			missing, wantMissing)
	}
	if len(unexpected) != 0 {
		t.Errorf("unexpected = %v, want none — a row for an id nobody declared "+
			"reaches the user as the name of a check they have never heard of",
			unexpected)
	}
}

// TestACleanProjectHasNothingToSay runs every registration over a
// complete, ordinary project and expects silence: no findings, and no
// row that could not answer.
//
// IT IS THE POSITIVE CONTROL FOR THE WHOLE FILE. Every other row here
// asserts that something is reported, and each of those would be
// satisfied by a set of checks that complained about everything. This is
// the one that says they discriminate.
//
// Nothing here asserts what a surface DOES with that silence, because
// nothing here renders. With no findings and no environmental decline
// there is nothing for a renderer to raise and nothing to ask about, and
// that mapping is held by the renderer's own suite where it belongs.
//
// REQUIRED MUTATION: make any one check emit a finding unconditionally —
// returning the standing no-lockfile hard stop from CheckLockfile does
// it. Reds naming the check.
func TestACleanProjectHasNothingToSay(t *testing.T) {
	root := projectFixture(t, "valid")
	walked := []string{"package-lock.json", "package.json", "src/pages/index.astro"}

	res := Run(engineChecks(walked), OSFileSystem{}, root)

	if len(res.Findings) != 0 {
		t.Errorf("findings on a clean project = %+v, want none", res.Findings)
	}
	for _, row := range res.Manifest {
		if row.Outcome != check.Answered {
			t.Errorf("%s did not answer on a clean project: %q", row.CheckID, row.Reason)
		}
	}
}

// TestPagesDirOutcomesSurfaceThroughTheEngine asserts the registration
// rather than the check. The check itself is covered in full next door;
// what this file adds is that its answers are not swallowed on the way
// through the engine — a finding dropped in aggregation looks exactly
// like a project with nothing wrong.
//
// NO HARD-STOP ROW, and its absence is deliberate rather than an
// omission. A missing pages directory does not fail an Astro build, and
// route-injecting integrations ship projects without one, so this check
// never hard-stops — a row asserting one would be asserting a severity
// the code deliberately does not have. Instead every row below asserts
// that NOTHING it produced is a hard stop.
//
// REQUIRED MUTATION: in Run (engine.go), stop appending a check's
// findings when that check also declined an id. The unresolved row loses
// its warning and reds; the other two do not move.
//
// SECOND REQUIRED MUTATION: have AstroConfigCheck claim only
// check.IDPagesDir. The build-format decline becomes an unclaimed id and
// the manifest assertions red.
func TestPagesDirOutcomesSurfaceThroughTheEngine(t *testing.T) {
	for _, tc := range []struct {
		name        string
		root        string
		wantPages   int
		wantMessage string
		wantDecline check.Outcome
		wantKind    check.DeclineKind
	}{
		{
			name:        "a project whose pages are where Astro looks",
			root:        filepath.Join("testdata", "engine", "clean"),
			wantPages:   0,
			wantDecline: check.Answered,
		},
		{
			name:        "no pages directory and no config to explain it",
			root:        filepath.Join("testdata", "engine", "no-pages-dir"),
			wantPages:   1,
			wantMessage: "no astro.config file was found",
			wantDecline: check.Answered,
		},
		{
			// The downgrade. The scan met a construct it cannot bound,
			// so it does not claim the pages directory is missing — it
			// says it could not confirm one, and gives up on the second
			// question rather than guessing at it.
			name:        "a config the scan cannot finish reading",
			root:        filepath.Join("testdata", "astroconfig", "srcdir-template-interp"),
			wantPages:   1,
			wantMessage: "couldn't be fully read",
			wantDecline: check.Declined,
			wantKind:    check.ByDesign,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := Run([]Check{AstroConfigCheck()}, OSFileSystem{}, tc.root)

			pages := findingsFor(Result{Findings: res.Findings}, check.IDPagesDir)
			if len(pages) != tc.wantPages {
				t.Fatalf("pages-dir findings = %+v, want %d", pages, tc.wantPages)
			}
			for _, f := range pages {
				if f.Severity != check.SeverityWarning {
					t.Errorf("severity = %q, want a warning — this check never hard "+
						"stops, and a missing pages directory does not fail a build",
						f.Severity)
				}
				if !strings.Contains(f.Message, tc.wantMessage) {
					t.Errorf("message = %q, want it to contain %q", f.Message, tc.wantMessage)
				}
			}

			for _, f := range res.Findings {
				if f.Severity == check.SeverityHardStop {
					t.Errorf("a hard stop reached the report: %+v", f)
				}
			}

			var buildFormat check.Status
			var found bool
			for _, row := range res.Manifest {
				if row.CheckID == check.IDBuildFormat {
					buildFormat, found = row, true
				}
			}
			if !found {
				t.Fatalf("manifest = %v, want a row for %s",
					manifestIDs(res.Manifest), check.IDBuildFormat)
			}
			if buildFormat.Outcome != tc.wantDecline {
				t.Errorf("%s outcome = %v, want %v",
					check.IDBuildFormat, buildFormat.Outcome, tc.wantDecline)
			}
			if tc.wantDecline == check.Declined && buildFormat.Kind != tc.wantKind {
				t.Errorf("%s kind = %v, want %v — it looked and chose not to guess, "+
					"which costs the reader no question",
					check.IDBuildFormat, buildFormat.Kind, tc.wantKind)
			}
		})
	}
}

// TestAstroConfigCheckClaimsBothIDsInOnePass pins the registration's own
// shape: one check, two ids, one parse of one file. Splitting it would
// put two facts about one file in two places and read the file twice.
//
// REQUIRED MUTATION: return only check.IDPagesDir from AstroConfigCheck.
// Reds.
func TestAstroConfigCheckClaimsBothIDsInOnePass(t *testing.T) {
	c := AstroConfigCheck()
	want := []string{check.IDPagesDir, check.IDBuildFormat}
	if !reflect.DeepEqual(c.IDs, want) {
		t.Errorf("IDs = %v, want %v", c.IDs, want)
	}
	if c.Run == nil {
		t.Error("the registration has no function, which the engine reports as a " +
			"check that never looked")
	}
}
