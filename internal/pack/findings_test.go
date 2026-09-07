package pack

import (
	"reflect"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// The four detectors are PURE FUNCTIONS OF A PATH LIST, and this file
// drives them from injected lists on every platform.
//
// That is not a convenience. One of the states a detector has to
// recognise — two names differing only in case — is a state two of the
// three filesystems this ships to refuse to hold, so a row that built it
// on disk would red on those platforms for a reason that has nothing to
// do with the detector, and a reader would conclude the detector was
// broken. The one row that does put colliding files on a real disk is
// tagged for the platform that can hold them and says why in the file.

// ---------------------------------------------------------------------
// Skipped symlinks
// ---------------------------------------------------------------------

// TestSymlinkFindingNamesEveryLinkOnce. A skipped link is one fact about
// the packing — these were not included — so it is one finding carrying
// the list, which the renderer prints under the headline. Splitting it
// per file would print the same sentence once per link.
func TestSymlinkFindingNamesEveryLinkOnce(t *testing.T) {
	got := symlinkFindings([]string{"content", "src/data"})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want one carrying both links", len(got))
	}
	if got[0].CheckID != check.IDSymlinks {
		t.Errorf("CheckID = %q, want %q", got[0].CheckID, check.IDSymlinks)
	}
	if got[0].Severity != check.SeverityWarning {
		t.Errorf("Severity = %q, want a warning", got[0].Severity)
	}
	if !reflect.DeepEqual(got[0].Paths, []string{"content", "src/data"}) {
		t.Errorf("Paths = %v, want both links", got[0].Paths)
	}
}

// TestSymlinkFindingIsSilentWithNoLinks. The state everything else is
// measured against: a check that fires on a clean project is a check
// people learn to ignore.
func TestSymlinkFindingIsSilentWithNoLinks(t *testing.T) {
	if got := symlinkFindings(nil); len(got) != 0 {
		t.Errorf("findings = %v, want none", got)
	}
}

// ---------------------------------------------------------------------
// Case collisions
// ---------------------------------------------------------------------

// TestCaseCollisionGroupsTheNamesThatCollide, on an injected list, on
// every platform.
//
// The unit is the GROUP rather than the file, because what a reader has
// to act on is "these two are the same name" — a per-file finding would
// name one half of a pair and leave the reader to find the other.
//
// MUTATION: compare the full paths without lowercasing them. The
// collision half reds and the distinct half does not move.
func TestCaseCollisionGroupsTheNamesThatCollide(t *testing.T) {
	got := collisionFindings([]string{
		"README.md",
		"docs/GUIDE.md",
		"docs/guide.md",
		"readme.md",
		"src/index.astro",
		"Readme.MD",
	})

	if len(got) != 2 {
		t.Fatalf("findings = %d, want one per colliding group: %v", len(got), got)
	}
	for _, f := range got {
		if f.CheckID != check.IDCaseCollision {
			t.Errorf("CheckID = %q, want %q", f.CheckID, check.IDCaseCollision)
		}
		if f.Severity != check.SeverityWarning {
			t.Errorf("Severity = %q, want a warning", f.Severity)
		}
	}

	want := [][]string{
		{"README.md", "Readme.MD", "readme.md"},
		{"docs/GUIDE.md", "docs/guide.md"},
	}
	gotPaths := [][]string{got[0].Paths, got[1].Paths}
	if !reflect.DeepEqual(gotPaths, want) {
		t.Errorf("Paths = %v, want %v", gotPaths, want)
	}
}

// TestCaseCollisionIsSilentOnNamesThatMerelyLookAlike is the
// discrimination half, and it is the half a detector can pass trivially
// without. A function that reported every pair as colliding would
// satisfy the row above in full.
func TestCaseCollisionIsSilentOnNamesThatMerelyLookAlike(t *testing.T) {
	got := collisionFindings([]string{
		"README.md",
		"README.mdx",
		"docs/README.md",
		"src/readme/index.astro",
	})
	if len(got) != 0 {
		t.Errorf("findings = %v, want none — these lowercase to four different paths", got)
	}
}

// TestCaseCollisionComparesTheWholePath. Two files with the same name in
// two directories do not collide; two files whose DIRECTORIES differ
// only in case do. The key a site is served by is the whole path, so
// that is what the comparison has to be over.
func TestCaseCollisionComparesTheWholePath(t *testing.T) {
	got := collisionFindings([]string{"Src/index.astro", "src/index.astro"})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want one — the directory names collide: %v", len(got), got)
	}
	if !reflect.DeepEqual(got[0].Paths, []string{"Src/index.astro", "src/index.astro"}) {
		t.Errorf("Paths = %v", got[0].Paths)
	}
}

// ---------------------------------------------------------------------
// Combining marks
// ---------------------------------------------------------------------

// TestCombiningMarkDetectionUsesTheDecomposedBytes.
//
// THE ESCAPE IS THE ASSERTION. Typing an accented character into this
// file would leave the byte sequence to whatever the editor chose, and
// the two spellings of that name are exactly what the row is about — so
// the composed and decomposed forms are both written as escapes, and the
// row can then state which one fires and which does not.
//
// MUTATION: test the composed rune for the mark category instead of
// iterating the name's runes. Both halves red.
func TestCombiningMarkDetectionUsesTheDecomposedBytes(t *testing.T) {
	decomposed := "public/cafe\u0301.png" // "e" then U+0301 COMBINING ACUTE ACCENT
	composed := "public/caf\u00e9.png"    // U+00E9 LATIN SMALL LETTER E WITH ACUTE

	got := markFindings([]string{decomposed, composed, "public/plain.png"})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want one — only the decomposed spelling carries a mark: %v",
			len(got), got)
	}
	if got[0].CheckID != check.IDUnicodeMarks {
		t.Errorf("CheckID = %q, want %q", got[0].CheckID, check.IDUnicodeMarks)
	}
	if got[0].Severity != check.SeverityWarning {
		t.Errorf("Severity = %q, want a warning", got[0].Severity)
	}
	if !reflect.DeepEqual(got[0].Paths, []string{decomposed}) {
		t.Errorf("Paths = %v, want only the decomposed name", got[0].Paths)
	}
}

// TestCombiningMarkDetectionLooksAtEverySegment. A mark in a directory
// component is the same hazard as one in a file name: the key the site
// is served by carries both.
func TestCombiningMarkDetectionLooksAtEverySegment(t *testing.T) {
	if got := markFindings([]string{"cafe\u0301/index.astro"}); len(got) != 1 {
		t.Errorf("findings = %v, want one for a mark in a directory name", got)
	}
}

// ---------------------------------------------------------------------
// The key charset — the hard stop
// ---------------------------------------------------------------------

// TestCharsetRejectsWhatTheServerWouldRefuse, one finding per file,
// because the REASON differs per file and one message cannot name two of
// them. A space and a non-ASCII character are different things to tell
// somebody about.
//
// MUTATION: accept any byte below 0x80. The space row reds and the
// legal-sibling row does not move.
func TestCharsetRejectsWhatTheServerWouldRefuse(t *testing.T) {
	space := "public/my photo.png"
	accented := "public/caf\u00e9.png"
	legal := "public/a-b_c.1~2.png"

	got := charsetFindings([]string{space, accented, legal})
	if len(got) != 2 {
		t.Fatalf("findings = %d, want one per offending file: %v", len(got), got)
	}
	for i, f := range got {
		if f.CheckID != check.IDPathCharset {
			t.Errorf("[%d] CheckID = %q, want %q", i, f.CheckID, check.IDPathCharset)
		}
		if f.Severity != check.SeverityHardStop {
			t.Errorf("[%d] Severity = %q, want a hard stop", i, f.Severity)
		}
		if len(f.Paths) != 1 {
			t.Errorf("[%d] Paths = %v, want exactly the one file", i, f.Paths)
		}
		if f.Next == "" {
			t.Errorf("[%d] a hard stop with no action: %+v", i, f)
		}
	}

	if got[0].Paths[0] != space || got[1].Paths[0] != accented {
		t.Fatalf("Paths = %v, %v, want %q then %q", got[0].Paths, got[1].Paths, space, accented)
	}
	if !strings.Contains(headline(got[0]), space) {
		t.Errorf("the message %q does not name the file it is about", headline(got[0]))
	}
	if !strings.Contains(headline(got[1]), accented) {
		t.Errorf("the message %q does not name the file it is about", headline(got[1]))
	}
	for _, f := range got {
		if strings.Contains(headline(f)+f.Why+f.Next, legal) {
			t.Errorf("a finding names the legal sibling %q", legal)
		}
	}
}

// TestCharsetAllowsTheWholeAllowedSet is the discrimination half: the
// charset has to be an allowlist rather than a mood, so every character
// in it is exercised and none of them fires.
func TestCharsetAllowsTheWholeAllowedSet(t *testing.T) {
	legal := []string{
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"abcdefghijklmnopqrstuvwxyz",
		"0123456789",
		"._~-",
		"src/pages/blog/a-b_c.1~2.astro",
	}
	if got := charsetFindings(legal); len(got) != 0 {
		t.Errorf("findings = %v, want none — every one of these is inside the allowed set", got)
	}
}

// TestCharsetRejectsAnOverlongSegmentAndAnOverlongKey. Same check, two
// more ways for a name to be refused after the upload has started, and
// each gets a row because the lengths are separate limits: a path can
// break either without breaking the other.
//
// The over-length cases are driven from injected paths rather than from
// a real tree deliberately — a 1 KB path is not a thing every platform
// in the matrix will create, and the limit belongs to the key rather
// than to the local filesystem.
func TestCharsetRejectsAnOverlongSegmentAndAnOverlongKey(t *testing.T) {
	longSegment := "public/" + strings.Repeat("a", 256) + ".png"
	longKey := strings.Repeat("dir/", 300) + "index.html"
	fine := "public/" + strings.Repeat("a", 255)

	if got := charsetFindings([]string{longSegment}); len(got) != 1 {
		t.Errorf("a 256-byte segment produced %v, want one hard stop", got)
	}
	if got := charsetFindings([]string{longKey}); len(got) != 1 {
		t.Errorf("a %d-byte key produced %v, want one hard stop", len(longKey), got)
	}
	if got := charsetFindings([]string{fine}); len(got) != 0 {
		t.Errorf("a 255-byte segment produced %v, want nothing — the limit is inclusive", got)
	}
}

// TestCharsetRejectsEveryCombiningMarkToo records a fact about the two
// checks rather than about one of them: a combining mark is by
// definition outside ASCII, so a name that fires the mark warning always
// fires this hard stop as well. Nothing in the tree can trip one without
// the other, and a row that assumed otherwise would be asserting an
// impossible state.
func TestCharsetRejectsEveryCombiningMarkToo(t *testing.T) {
	name := "public/cafe\u0301.png"
	if got := markFindings([]string{name}); len(got) != 1 {
		t.Fatalf("mark findings = %v, want one", got)
	}
	if got := charsetFindings([]string{name}); len(got) != 1 {
		t.Errorf("charset findings = %v, want one — a combining mark is never inside the allowed set", got)
	}
}

// ---------------------------------------------------------------------
// The walk's coverage claim
// ---------------------------------------------------------------------

// TestWalkClaimsExactlyItsFourChecks. Where a package answers a set of
// questions, the set is asserted: a fifth id added without a detector,
// or a detector added without an id, is a gap nothing else in this
// package can see.
func TestWalkClaimsExactlyItsFourChecks(t *testing.T) {
	want := []string{
		check.IDSymlinks,
		check.IDCaseCollision,
		check.IDUnicodeMarks,
		check.IDPathCharset,
	}
	if !reflect.DeepEqual(walkIDs, want) {
		t.Errorf("walkIDs = %v, want %v", walkIDs, want)
	}
}

// TestWalkManifestAnswersEveryIDItClaims. The row is the only thing
// separating "found nothing" from "never looked", and that is as true of
// a producer that always runs as of one that sometimes cannot — a report
// with no row for a check cannot say the check was covered, however
// certainly it was.
func TestWalkManifestAnswersEveryIDItClaims(t *testing.T) {
	m := walkManifest()

	var ids []string
	for _, row := range m {
		ids = append(ids, row.CheckID)
		if !row.Ran {
			t.Errorf("%s reports as not run: %+v", row.CheckID, row)
		}
		if row.Reason != "" {
			t.Errorf("%s carries a reason %q, and it did run", row.CheckID, row.Reason)
		}
	}
	if !reflect.DeepEqual(ids, walkIDs) {
		t.Errorf("manifest ids = %v, want %v", ids, walkIDs)
	}
}

// TestCleanTreeStillProducesAFullManifest. A project with nothing wrong
// with it produces no findings and four rows, which is the distinction
// the manifest exists for: silence from a check that looked and silence
// from a check nobody wired up are the same silence without it.
func TestCleanTreeStillProducesAFullManifest(t *testing.T) {
	res := results(nil, nil)
	if len(res.Findings) != 0 {
		t.Errorf("findings = %v, want none", res.Findings)
	}
	if len(res.Manifest) != len(walkIDs) {
		t.Errorf("manifest = %v, want a row for each of %v", res.Manifest, walkIDs)
	}
}

// headline is what a surface prints first for a finding: its own
// one-line summary if it wrote one, and the message otherwise.
func headline(f check.Finding) string {
	if f.What != "" {
		return f.What
	}
	return f.Message
}
