package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentSurfacePackage is the package this guard watches, written as a
// slash path and joined for the host below. It is the local MCP server —
// the half of this binary an agent talks to — and it is named here once
// so the failure message and the filter cannot disagree about which
// package the rule is about.
const agentSurfacePackage = "internal/mcp"

// siteDomainWord is the distinctive word of the domain a published site
// answers under, and it is the WORD rather than the whole domain on
// purpose.
//
// A second copy does not have to arrive spelled the way the original is.
// The two obvious ways to write one are a full host in a literal and a
// concatenation of a label with a suffix, and only the first contains
// the whole domain; the word is in both. Searching for the narrower
// string would pass over exactly the spelling somebody reaches for when
// they are avoiding an obvious duplicate.
//
// It is folded to lower case before comparison for the same reason: a
// capitalised spelling inside an identifier is still a second copy, and
// there is no legitimate use of this word in that package for the
// folding to get wrong.
const siteDomainWord = "curiously"

// TestTheAgentSurfaceSpellsNoSiteDomain is the row that keeps one
// composer from quietly becoming two.
//
// THE FAILURE IT PREVENTS IS NOT A WRONG ADDRESS TODAY. A hand-built
// address is right the first time it is written; what it costs is the
// day the domain moves, when one of the two sites is updated and the
// other goes on composing the old one — and an address is the last thing
// a deploy prints, so the wrong half is discovered by a user rather than
// by a suite. The composer in the deploy package is exported precisely
// so this package never needs the domain, and this row is the part of
// that decision a later change can actually trip over.
//
// IT SCANS TESTS AS WELL AS SOURCE, and that is the same reasoning the
// citation guard settled one file over. A fixture is where a second copy
// lands first: somebody writes the expected address out by hand in an
// assertion, the row passes, and the hand-written string is now the
// thing the package agrees with. Scoping this to non-test files would
// leave the likeliest home for the defect outside the guard.
//
// THE ENUMERATION IS GIT'S, not a walk. The reason belongs to
// publishedTextFiles and is argued there at length; what matters here is
// that a worktree lane under this checkout is another branch's copy of
// this package, and a guard that counted one would be reporting on files
// that are not the ones being changed.
//
// REQUIRED MUTATION, run 2026-09-11: see the commit message. Both
// directions were run — a second copy of the domain planted in the
// package, and the filter pointed at a package that does not exist.
func TestTheAgentSurfaceSpellsNoSiteDomain(t *testing.T) {
	root := moduleRoot(t)
	prefix := filepath.Join(root, filepath.FromSlash(agentSurfacePackage)) + string(filepath.Separator)

	var scanned int
	for _, path := range publishedTextFiles(t, root) {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		scanned++

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(strings.ToLower(string(data)), siteDomainWord) {
			continue
		}
		t.Errorf("%s spells %q.\n"+
			"The address a published deploy answers at is composed in exactly one place — "+
			"flow.PublishedURL — out of the label the server sends. A second spelling here "+
			"is a second composer, and the day the domain moves only one of them will be "+
			"updated. Call the composer with the label instead.",
			displayPath(root, path), siteDomainWord)
	}

	// THE GUARD FAILS LOUDLY RATHER THAN PASSING OVER AN EMPTY SET, like
	// every other guard in this package. A filter naming a package that
	// has been renamed, moved or removed finds nothing and reports a
	// clean scan, which is indistinguishable from a clean package — and
	// this one is a NEGATIVE assertion, so it is the shape most able to
	// pass by never looking.
	if scanned == 0 {
		t.Fatalf("no published file under %s was scanned, so this guard measured nothing",
			agentSurfacePackage)
	}
}
