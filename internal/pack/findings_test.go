package pack

import (
	"reflect"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// The three detectors are PURE FUNCTIONS OF A PATH LIST, and this file
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

// TestCharsetTellsACombiningMarkFromEveryOtherRefusal is where the
// retired warning's detection went, and the sentence is the whole of
// what it bought.
//
// THE WARNING COULD NEVER FIRE ALONE. Every combining mark is outside
// ASCII and therefore outside the allowed set, so a name that tripped it
// always tripped this hard stop on the same file — an advisory that has
// never once been the thing a reader could act on is not an advisory,
// and it was retired rather than reworded. What a reader CAN act on is
// knowing which refusal they met: a letter with an accent written as two
// characters is a different problem from a space, and the fix is a
// different fix.
//
// The wording is not asserted, because these are product sentences and
// the next person to improve them should not have to edit a test. What
// is asserted is the SHAPE the sentences have to have — and the first
// draft of this row got that wrong, which is worth recording because a
// mutation is what found it.
//
// It asserted only that the reasons DIFFER. Dropping the mark branch
// entirely leaves them differing, because the fallback names the
// offending character and the two spellings offend with different
// characters — so a row written to catch that mutation sailed past it.
// A property satisfied for the wrong reason is not weaker evidence, it
// is none.
//
// What separates the two mechanisms is that the mark case is ONE
// problem: an accent written as a separate character, whichever accent
// it is. So two different marks must give the SAME sentence, where two
// different ordinary characters must not — and no fallback that names
// the character can satisfy both halves.
//
// MUTATION: drop the mark branch and let a mark fall through to the
// generic wording. Reds here. MUTATION: make the action the same
// whatever the refusal was. Reds here. MUST NOT MOVE: the allowed-set
// row, the over-length rows.
func TestCharsetTellsACombiningMarkFromEveryOtherRefusal(t *testing.T) {
	decomposed := "public/cafe\u0301.png"   // "e" then U+0301 COMBINING ACUTE ACCENT
	otherMark := "public/u\u0308ber.png"    // "u" then U+0308 COMBINING DIAERESIS
	composed := "public/caf\u00e9.png"      // U+00E9 LATIN SMALL LETTER E WITH ACUTE
	otherComposed := "public/\u00fcber.png" // U+00FC LATIN SMALL LETTER U WITH DIAERESIS
	space := "public/my photo.png"

	got := charsetFindings([]string{
		decomposed, otherMark, composed, otherComposed, space, "public/plain.png",
	})
	if len(got) != 5 {
		t.Fatalf("findings = %d, want one for each of the five bad names: %v", len(got), got)
	}

	reasons := map[string]string{}
	for i, name := range []string{decomposed, otherMark, composed, otherComposed, space} {
		if got[i].Severity != check.SeverityHardStop {
			t.Errorf("[%d] Severity = %q, want a hard stop", i, got[i].Severity)
		}
		if !reflect.DeepEqual(got[i].Paths, []string{name}) {
			t.Errorf("[%d] Paths = %v, want [%s]", i, got[i].Paths, name)
		}
		if !strings.Contains(headline(got[i]), name) {
			t.Errorf("[%d] the message %q does not name the file", i, headline(got[i]))
		}
		reasons[name] = strings.Replace(headline(got[i]), name, "", 1)
	}

	// One problem, one sentence: which accent it is changes nothing about
	// what happened or what to do. A fallback that names the offending
	// character cannot satisfy this.
	if reasons[decomposed] != reasons[otherMark] {
		t.Errorf("two names carrying different combining marks are refused with different "+
			"sentences (%q vs %q) — the problem is the same one either way, and a message "+
			"that varies with the accent is naming the character rather than the problem",
			reasons[decomposed], reasons[otherMark])
	}
	// And the other way, so the row cannot be satisfied by one sentence
	// covering everything: a message naming the character is right where
	// the character IS the problem.
	if reasons[composed] == reasons[otherComposed] {
		t.Errorf("two different characters outside the allowed set are refused with the "+
			"same sentence (%q) — the reader is not told which one they have",
			reasons[composed])
	}
	if reasons[decomposed] == reasons[composed] {
		t.Errorf("the decomposed and precomposed spellings are refused with the same "+
			"sentence (%q) — the two-character spelling is the one a reader has to be "+
			"told about", reasons[decomposed])
	}
	if reasons[decomposed] == reasons[space] || reasons[composed] == reasons[space] {
		t.Errorf("a space and a character outside ASCII are refused with the same "+
			"sentence: %q / %q / %q", reasons[decomposed], reasons[composed], reasons[space])
	}
	if got[0].Next == got[4].Next {
		t.Errorf("a name carrying a combining mark offers the same action as one with a "+
			"space (%q) — retyping the accented letter gives its other spelling, which "+
			"this platform cannot serve either, and that is the sentence being bought",
			got[0].Next)
	}
}

// TestCharsetNamesTheMarkEvenWhenSomethingElseIsAlsoWrong. A name can
// break more than one rule, and the mark is the one the reader cannot
// SEE — two spellings of the same visible name is not a thing a person
// notices in a file listing, and a space is. So the mark takes
// precedence over every other refusal in the wording.
func TestCharsetNamesTheMarkEvenWhenSomethingElseIsAlsoWrong(t *testing.T) {
	both := "public/my photo\u0301.png"
	spaceOnly := "public/my photo.png"

	got := charsetFindings([]string{both, spaceOnly})
	if len(got) != 2 {
		t.Fatalf("findings = %v, want one each", got)
	}
	withMark := strings.Replace(headline(got[0]), both, "", 1)
	withoutMark := strings.Replace(headline(got[1]), spaceOnly, "", 1)
	if withMark == withoutMark {
		t.Errorf("a name with a space AND a combining mark is refused as though it only "+
			"had the space: %q", withMark)
	}
}

// TestCharsetLooksAtEverySegment. A mark in a directory component is the
// same hazard as one in a file name: the address a site is served at
// carries both.
func TestCharsetLooksAtEverySegment(t *testing.T) {
	if got := charsetFindings([]string{"cafe\u0301/index.astro"}); len(got) != 1 {
		t.Errorf("findings = %v, want one for a mark in a directory name", got)
	}
}

// ---------------------------------------------------------------------
// The walk's coverage claim
// ---------------------------------------------------------------------

// TestWalkClaimsExactlyItsThreeChecks. Where a package answers a set of
// questions, the set is asserted: a fifth id added without a detector,
// or a detector added without an id, is a gap nothing else in this
// package can see.
func TestWalkClaimsExactlyItsThreeChecks(t *testing.T) {
	want := []string{
		check.IDSymlinks,
		check.IDCaseCollision,
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
		if row.Outcome != check.Answered {
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
// with it produces no findings and three rows, which is the distinction
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

// TestCharsetNamesTheMarkForEveryCombiningCategory. The mark detection
// asked unicode.Mn alone, which is NONSPACING marks — one of the three
// categories Unicode calls a mark. A spacing mark (Mc) or an enclosing
// one (Me) fell through to the fallback and was refused by naming the
// character: "its name contains "ः" (U+0903)".
//
// NEITHER PUBLISHES EITHER WAY, so this is not a missed refusal. What it
// misses is the SENTENCE, and the sentence is the whole reason the
// detection survived the warning that used to call it: the warning was
// retired because it could never fire alone, on the argument that the
// wording was the actionable half. A rule kept for its wording that
// covers two thirds of its subject is that argument holding for two
// thirds of its subject.
//
// Asserted structurally, like the row above it: all three categories
// must give the SAME sentence as each other, and a character that is not
// a mark must still give a different one — so the fix cannot be "call
// everything a mark".
//
// MUTATION: drop Mc and Me from hasCombiningMark. The first comparison
// reds. MUTATION: make hasCombiningMark return true always. The second
// reds, and so does the row above.
func TestCharsetNamesTheMarkForEveryCombiningCategory(t *testing.T) {
	var (
		nonspacing = "public/café.png" // Mn — a combining acute
		spacing    = "public/kaः.png"   // Mc — Devanagari visarga
		enclosing  = "public/x⃝.png"    // Me — combining enclosing circle
		notAMark   = "public/naïve.png" // an ordinary non-ASCII letter
	)

	got := charsetFindings([]string{nonspacing, spacing, enclosing, notAMark})
	if len(got) != 4 {
		t.Fatalf("findings = %v, want one each — every one of these is outside the "+
			"allowed set and must be refused", got)
	}

	reason := map[string]string{}
	action := map[string]string{}
	for i, name := range []string{nonspacing, spacing, enclosing, notAMark} {
		if got[i].Severity != check.SeverityHardStop {
			t.Errorf("[%d] Severity = %q, want a hard stop", i, got[i].Severity)
		}
		reason[name] = strings.Replace(headline(got[i]), name, "", 1)
		action[name] = got[i].Next
	}

	for _, other := range []string{spacing, enclosing} {
		if reason[other] != reason[nonspacing] {
			t.Errorf("a spacing or enclosing mark is refused with a different sentence "+
				"from a nonspacing one:\n  %q\n  %q\nall three are one character combining "+
				"with its neighbour, which is the thing the reader cannot see",
				reason[other], reason[nonspacing])
		}
		if action[other] != action[nonspacing] {
			t.Errorf("a spacing or enclosing mark offers a different action from a "+
				"nonspacing one:\n  %q\n  %q", action[other], action[nonspacing])
		}
	}

	if reason[notAMark] == reason[nonspacing] {
		t.Errorf("an ordinary non-ASCII letter is refused as though it carried a mark "+
			"(%q) — it does not, and naming the character is right where the character "+
			"IS the problem", reason[notAMark])
	}
}
