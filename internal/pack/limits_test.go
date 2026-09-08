package pack

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/units"
	"github.com/curiouspub/cli/pkg/wire"
)

// ---------------------------------------------------------------------
// The numbers themselves
// ---------------------------------------------------------------------

// TestTheLimitsAreTheDecimalSIValuesAndNotTheirBinaryNeighbours.
//
// THE EXPECTATIONS ARE LITERALS, not references to the constants they
// check. A row written as `wire.MaxSourceFileBytes == wire.MaxSourceFileBytes`
// is an identity that passes every edit, and the whole point of pinning
// these is that a unit change has to be made twice to go unnoticed.
//
// The binary neighbours are named because the difference between the two
// readings of "5 MB" is 5%, and 5% shows up nowhere except at the
// boundary — as a refusal whose numbers a user cannot make agree, on the
// one measurement the client and the server must not disagree about.
//
// REQUIRED MUTATION, run 2026-09-08: set MaxSourceFileBytes in the wire
// contract to 5 << 20. Reds here naming both values, and reds again in
// the boundary rows below.
func TestTheLimitsAreTheDecimalSIValuesAndNotTheirBinaryNeighbours(t *testing.T) {
	for _, row := range []struct {
		name   string
		got    int64
		want   int64
		binary int64
	}{
		{"files", wire.MaxSourceFiles, 3_000, 0},
		{"file size", wire.MaxSourceFileBytes, 5_000_000, 5 * 1024 * 1024},
		{"total size", wire.MaxSourceTotalBytes, 30_000_000, 30 * 1024 * 1024},
		{"packed size", wire.MaxPackedBytes, 30_000_000, 30 * 1024 * 1024},
	} {
		if row.got != row.want {
			t.Errorf("the %s limit is %d, want exactly %d", row.name, row.got, row.want)
		}
		if row.binary != 0 && row.got == row.binary {
			t.Errorf("the %s limit is %d, which is the BINARY reading — client and server "+
				"must not each pick their own reading of the same number", row.name, row.got)
		}
	}
}

// ---------------------------------------------------------------------
// How many files
// ---------------------------------------------------------------------

// TestTheFileCountBoundaryIsInclusive. The limit is stated as "at most",
// so the project sitting exactly on it deploys.
//
// THE PASSING ROWS ARE THE POSITIVE CONTROL. Every refusal row below
// would be satisfied by a check that refused everything, and these two
// are what say it discriminates.
//
// REQUIRED MUTATION, run 2026-09-08: change the comparison in
// fileCountFinding from `<=` to `<`. Reds on the 3,000 row, which is the
// only one that can see an off-by-one at the boundary.
func TestTheFileCountBoundaryIsInclusive(t *testing.T) {
	for _, row := range []struct {
		count   int
		refused bool
	}{
		{2_999, false},
		{3_000, false},
		{3_001, true},
	} {
		res := Limits(generatedFiles("src", row.count, 1))
		found := findingsFor(res, check.IDLimitFiles)
		if refused := len(found) > 0; refused != row.refused {
			t.Errorf("%d files: refused = %v, want %v (%v)", row.count, refused, row.refused, found)
		}
	}
}

// TestTooManyFilesNamesTheCountTheLimitAndWhereTheyAre.
//
// "3,412 files (limit 3,000)" leaves a reader to go hunting; "1,900 of
// them under a gallery directory" ends the conversation. The numbers are
// asserted as the reader sees them — grouped — because a message
// carrying an ungrouped five-digit count is one nobody can read at a
// glance.
//
// REQUIRED MUTATION, run 2026-09-08: return an empty slice from
// topDirectories. Reds on the directory and the sub-count, and leaves
// the count-and-limit assertions green — which is the split the row is
// written for.
func TestTooManyFilesNamesTheCountTheLimitAndWhereTheyAre(t *testing.T) {
	files := append(generatedFiles("public/gallery", 3_400, 1), generatedFiles("src/pages", 12, 1)...)

	res := Limits(files)
	found := findingsFor(res, check.IDLimitFiles)
	if len(found) != 1 {
		t.Fatalf("findings = %v, want exactly one", found)
	}
	if found[0].Severity != check.SeverityHardStop {
		t.Errorf("Severity = %q, want a hard stop", found[0].Severity)
	}

	text := wholeFinding(found[0])
	for _, want := range []string{"3,412", "3,000", "public/gallery/", "3,400", "src/pages/"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message does not contain %q:\n%s", want, text)
		}
	}
}

// TestTheTopDirectoriesAreTheBusiestFiveAndTiesBreakByPath.
//
// TIES ARE THE ORDINARY CASE, not an edge one: a generated tree gives
// four directories forty files each, and with no tie-break the message
// differs between two runs over one unchanged project — in a package
// whose entire promise is that it answers the same way twice.
//
// THE FIXTURE IS BUILT SO THAT MAP ORDER CANNOT AGREE WITH THE ANSWER.
// Six directories tie at the cut, so any run that read the map's order
// has five ways to be wrong and one to be right, and Go randomises that
// order on every run.
//
// REQUIRED MUTATION, run 2026-09-08: delete the path comparison from
// topDirectories' tie-break, leaving only the count. MEASURED over
// twenty consecutive runs: red on twenty of twenty. That number is
// reported rather than "it reds", because a row whose failure depends on
// map iteration could have been the kind that reds one run in six, and
// the difference decides whether it can be trusted in a single CI leg.
func TestTheTopDirectoriesAreTheBusiestFiveAndTiesBreakByPath(t *testing.T) {
	var files []File
	files = append(files, generatedFiles("public/huge", 100, 1)...)
	for _, dir := range []string{"f", "e", "d", "c", "b", "a"} {
		files = append(files, generatedFiles("public/"+dir, 40, 1)...)
	}

	want := []dirCount{
		{dir: "public/huge/", count: 100},
		{dir: "public/a/", count: 40},
		{dir: "public/b/", count: 40},
		{dir: "public/c/", count: 40},
		{dir: "public/d/", count: 40},
	}
	if got := topDirectories(files); !reflect.DeepEqual(got, want) {
		t.Errorf("topDirectories = %v, want %v — busiest first, ties by path", got, want)
	}
}

// TestAFileAtTheTopLevelIsNamedInWordsRatherThanAsADot. path.Dir returns
// "." for a file with no directory, and a message telling somebody that
// "." holds 40 of their files names nothing they can put in an ignore
// rule.
//
// REQUIRED MUTATION, run 2026-09-08: return path.Dir unchanged from
// displayParent. Reds here with ".".
func TestAFileAtTheTopLevelIsNamedInWordsRatherThanAsADot(t *testing.T) {
	got := topDirectories([]File{{Path: "index.html"}, {Path: "about.html"}})
	if len(got) != 1 || got[0].dir != "the project root" {
		t.Errorf("topDirectories = %v, want the top level named in words", got)
	}
}

// ---------------------------------------------------------------------
// How big one file may be
// ---------------------------------------------------------------------

// TestTheFileSizeBoundaryIsInclusive, and the binary neighbour is over.
//
// THE MEBIBYTE ROW IS NOT A CURIOSITY. It is the only assertion in this
// tree that can catch a silent return to binary units: a project of one
// 5,242,880-byte file is legal under the binary reading and refused
// under the decimal one, so the two implementations differ on exactly
// this input and agree on almost every other.
//
// REQUIRED MUTATION, run 2026-09-08: change the comparison in
// fileSizeFinding from `>` to `>=`. Reds on the 5,000,000 row alone,
// which is the only one that can see an off-by-one at the boundary.
// SECOND REQUIRED MUTATION, run 2026-09-08: compare against 5 << 20
// instead of the contract's constant. Reds on BOTH refusal rows —
// measured, and not what was predicted, which had the mebibyte row
// reding alone: under the binary reading neither 5,000,001 nor 5,242,880
// is over the limit, so a port to binary units is caught twice rather
// than once.
func TestTheFileSizeBoundaryIsInclusive(t *testing.T) {
	for _, row := range []struct {
		size    int64
		refused bool
	}{
		{4_999_999, false},
		{5_000_000, false},
		{5_000_001, true},
		{5 * 1024 * 1024, true},
	} {
		res := Limits([]File{{Path: "public/asset.bin", Size: row.size}})
		found := findingsFor(res, check.IDLimitFileSize)
		if refused := len(found) > 0; refused != row.refused {
			t.Errorf("a file of %d bytes: refused = %v, want %v", row.size, refused, row.refused)
		}
		if len(found) == 0 {
			// A row that expected a refusal and got none has already
			// been reported. Reading a finding that is not there would
			// replace that report with a panic, which names the test
			// instead of the defect.
			continue
		}
		// THE PATH IS READ FROM Paths, NOT FROM THE PROSE. The finding
		// carries its offenders as facts and the renderer formats them;
		// a row grepping the copy for a filename would go green the day
		// somebody reworded a sentence and red the day the list broke,
		// which is the wrong way round.
		if !reflect.DeepEqual(found[0].Paths, []string{"public/asset.bin"}) {
			t.Errorf("a file of %d bytes: Paths = %v, want the offender",
				row.size, found[0].Paths)
		}
		if !reflect.DeepEqual(found[0].Sizes, []int64{row.size}) {
			t.Errorf("a file of %d bytes: Sizes = %v, want the measurement beside it",
				row.size, found[0].Sizes)
		}
		text := wholeFinding(found[0])
		if !strings.Contains(text, "5.0 MB") {
			t.Errorf("a file of %d bytes: the message does not restate the limit "+
				"in the reader's units:\n%s", row.size, text)
		}
	}
}

// TestEveryOversizeFileIsReportedInOneRun, largest first.
//
// A user with four oversize files learns about four. Reporting the first
// and stopping is the round-trip this whole surface exists to prevent,
// and it is the failure that looks most like working software: the
// message is correct, the exit code is right, and the user runs the tool
// four times.
//
// THE PATHS ARE ASSERTED IN THE STRUCTURED FIELD AS WELL as in the
// prose, because one of the surfaces reading this is a machine and an
// agent that had to parse English back out of a message to learn which
// file to open is the defect that field exists to prevent.
//
// REQUIRED MUTATION, run 2026-09-08: return after appending the first
// oversize file in fileSizeFinding. Reds on the count, the order and the
// path list at once.
func TestEveryOversizeFileIsReportedInOneRun(t *testing.T) {
	res := Limits([]File{
		{Path: "public/small.png", Size: 10},
		{Path: "public/c.bin", Size: 6_000_000},
		{Path: "public/a.bin", Size: 18_900_000},
		{Path: "public/d.bin", Size: 5_200_000},
		{Path: "public/b.bin", Size: 12_400_000},
	})

	found := findingsFor(res, check.IDLimitFileSize)
	if len(found) != 1 {
		t.Fatalf("findings = %v, want one finding carrying all four", found)
	}

	want := []string{"public/a.bin", "public/b.bin", "public/c.bin", "public/d.bin"}
	if !reflect.DeepEqual(found[0].Paths, want) {
		t.Errorf("Paths = %v, want %v — largest first", found[0].Paths, want)
	}
	// Sizes travel with the paths, in the same order, so a reader can
	// pair them by index — which is the contract the gate enforces.
	wantSizes := []int64{18_900_000, 12_400_000, 6_000_000, 5_200_000}
	if !reflect.DeepEqual(found[0].Sizes, wantSizes) {
		t.Errorf("Sizes = %v, want %v — largest first, beside their paths",
			found[0].Sizes, wantSizes)
	}
	if strings.Contains(wholeFinding(found[0]), "public/small.png") {
		t.Errorf("the message names a file that is within the limit:\n%s", found[0].What)
	}
}

// TestTheOversizeListStopsAtTenAndSaysHowManyMore. Forty paths is a wall
// somebody scrolls past, and the fortieth is no more actionable than the
// first — but "and 30 more" and a list of ten are different situations,
// and a reader who cannot tell them apart fixes ten files and runs
// again.
//
// REQUIRED MUTATION, run 2026-09-08: drop the "and N more" line from
// andMore. Reds on the count sentence while the ten-path assertion stays
// green, which is the half of the property that has no other row.
//
// THE COUNT STAYS IN THE COPY WHILE THE LIST MOVED OUT, and that split
// is deliberate: the renderer knows how many paths it was handed, and
// only this check knows how many there were.
func TestTheOversizeListStopsAtTenAndSaysHowManyMore(t *testing.T) {
	var files []File
	for i := 0; i < 42; i++ {
		files = append(files, File{
			Path: fmt.Sprintf("public/blob-%02d.bin", i),
			Size: int64(6_000_000 + i),
		})
	}

	found := findingsFor(Limits(files), check.IDLimitFileSize)
	if len(found) != 1 {
		t.Fatalf("findings = %v, want one", found)
	}
	if n := len(found[0].Paths); n != listedOffenders {
		t.Errorf("the finding carries %d paths, want %d", n, listedOffenders)
	}
	if n := len(found[0].Sizes); n != listedOffenders {
		t.Errorf("the finding carries %d sizes, want %d — the gate refuses a "+
			"finding whose two lists disagree", n, listedOffenders)
	}
	if !strings.Contains(found[0].What, "and 32 more") {
		t.Errorf("the message does not say how many were left out:\n%s", found[0].What)
	}
	if !strings.Contains(found[0].Message, "42 files are larger") {
		t.Errorf("the one-line summary does not carry the real count: %q", found[0].Message)
	}
}

// ---------------------------------------------------------------------
// How big they are together
// ---------------------------------------------------------------------

// TestTheTotalBoundaryIsInclusive, and the total is the sum of the
// UNCOMPRESSED sizes.
//
// That makes this client the stricter of the two ends by construction: a
// tree at this limit always compresses to something the server accepts,
// so anything refused here would have been accepted there. Being wrong
// in that direction costs a project that would have deployed; being
// wrong in the other costs an opaque failure at the far end of an upload
// the user already waited for.
//
// REQUIRED MUTATION, run 2026-09-08: change the comparison in
// totalSizeFinding from `<=` to `<`. Reds on the exact-limit row alone.
func TestTheTotalBoundaryIsInclusive(t *testing.T) {
	for _, row := range []struct {
		total   int64
		refused bool
	}{
		{29_999_999, false},
		{30_000_000, false},
		{30_000_001, true},
	} {
		// Three files rather than one, so the total is a SUM and not a
		// single size wearing the total's name — a check that read the
		// largest file would agree with a one-file fixture on every row.
		files := []File{
			{Path: "a.bin", Size: row.total - 2_000_000},
			{Path: "b.bin", Size: 1_000_000},
			{Path: "c.bin", Size: 1_000_000},
		}
		found := findingsFor(Limits(files), check.IDLimitTotal)
		if refused := len(found) > 0; refused != row.refused {
			t.Errorf("a total of %d bytes: refused = %v, want %v", row.total, refused, row.refused)
		}
		if !row.refused {
			continue
		}
		text := wholeFinding(found[0])
		if !strings.Contains(text, "30.0 MB") {
			t.Errorf("the message does not restate the limit in the reader's units:\n%s", text)
		}
	}
}

// TestTheTotalMessageNamesTheLargestFiles. "You are 2 MB over" leaves a
// reader with nothing to do; the largest files are the only actionable
// thing the measurement knows.
//
// REQUIRED MUTATION, run 2026-09-08: pass the caller's slice to
// sortBySizeThenPath in totalSizeFinding instead of a copy. This row
// stays green and the row below it reds, which is why both exist.
func TestTheTotalMessageNamesTheLargestFiles(t *testing.T) {
	var files []File
	for i := 0; i < 14; i++ {
		files = append(files, File{Path: fmt.Sprintf("assets/%02d.bin", i), Size: int64(3_000_000 - i)})
	}

	found := findingsFor(Limits(files), check.IDLimitTotal)
	if len(found) != 1 {
		t.Fatalf("findings = %v, want one", found)
	}
	if n := len(found[0].Paths); n != listedOffenders {
		t.Errorf("the finding carries %d paths, want the %d largest", n, listedOffenders)
	}
	if len(found[0].Paths) > 0 && found[0].Paths[0] != "assets/00.bin" {
		t.Errorf("Paths[0] = %q, want the largest first", found[0].Paths[0])
	}
	if !reflect.DeepEqual(found[0].Sizes[:1], []int64{3_000_000}) {
		t.Errorf("Sizes[0] = %v, want the largest file's measurement", found[0].Sizes[:1])
	}
	for _, p := range found[0].Paths {
		if p == "assets/13.bin" {
			t.Errorf("the finding names the smallest file: %v", found[0].Paths)
		}
	}
}

// TestMeasuringTheLimitsDoesNotReorderTheCallersList.
//
// The walk's list is canonical and three other things read it. A check
// that sorted it in place to find its largest files would reorder the
// archive's input as a side effect of producing a message — and the
// archive sorts again, so nothing would look wrong until something
// downstream trusted the order it was handed.
//
// REQUIRED MUTATION, run 2026-09-08: sort the caller's slice directly in
// totalSizeFinding rather than a copy of it. Reds here.
func TestMeasuringTheLimitsDoesNotReorderTheCallersList(t *testing.T) {
	files := []File{
		{Path: "a.bin", Size: 1_000_000},
		{Path: "b.bin", Size: 29_000_001},
		{Path: "c.bin", Size: 1_000_000},
	}
	before := pathsOf(files)

	Limits(files)

	if got := pathsOf(files); !reflect.DeepEqual(got, before) {
		t.Errorf("the caller's list was reordered: %v, was %v", got, before)
	}
}

// ---------------------------------------------------------------------
// What is counted, and what is not
// ---------------------------------------------------------------------

// TestFilesTheWalkExcludedCountTowardNothing is the assertion that
// proves the ORDER OF OPERATIONS: the limits read the post-exclusion
// list, so a project with an enormous dependency directory and a small
// source tree deploys.
//
// THE HOSTILE HALF HAS TO BE BIG ENOUGH TO REACH THE PROPERTY. A
// fixture whose excluded half was a kilobyte could not tell a check that
// respects the exclusions from one that ignores them — both pass — so
// the dependency directory here is four hundred megabytes, past every
// limit on its own, written as a sparse file so it costs no disk.
//
// THE POSITIVE CONTROL IS IN THE SAME FUNCTION: the source file really
// is in the walk's list and really is measured. Without it, a walk that
// returned nothing at all would satisfy every assertion here.
//
// REQUIRED MUTATION, run 2026-09-08: delete the node_modules entry from
// forcedExcludes. Reds — and the failure it produces is the fixture
// guard rather than a limit, naming the list with the dependency file in
// it. That is the positive control doing its job: it is asked before
// anything is measured, so the row reports the cause instead of the
// symptom.
func TestFilesTheWalkExcludedCountTowardNothing(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "index.html", body: "<html>"},
		{path: "src/pages/index.astro", body: "page"},
	})
	sparseFile(t, filepath.Join(root, "node_modules", "big.pack"), 400<<20)

	tree := mustWalk(t, OSFileSystem{}, root)
	if got := pathsOf(tree.Files); !reflect.DeepEqual(got, []string{"index.html", "src/pages/index.astro"}) {
		t.Fatalf("the walk listed %v — this row cannot measure anything from that list", got)
	}
	if total := totalSize(tree.Files); total == 0 {
		t.Fatal("the walked files measure zero bytes — the limits would pass over an " +
			"empty measurement and this row would prove nothing")
	}

	res := Limits(tree.Files)
	if len(check.Advisories(res.Findings)) != 0 {
		t.Errorf("findings = %v, want none — an excluded directory counts toward nothing",
			res.Findings)
	}
}

// TestSkippedLinksAreNotCounted, and the SUBJECT is the counting rather
// than the packer.
//
// The packer refuses a non-regular entry outright, so a row that handed
// one to Pack would be measuring a different property in a different
// function. What is asserted here is that the list the count runs over
// never held the links in the first place — which is the walk's decision
// and the count's input.
//
// THE FIXTURE REACHES THE PROPERTY. Three thousand regular files is
// exactly the limit, so five links counted as files would be 3,005 and a
// refusal; a fixture of ten files and five links could not tell the two
// implementations apart.
//
// It runs on every platform because the entry kinds come from a
// synthetic directory listing rather than from a filesystem with an
// opinion about whether this account may create a link.
//
// REQUIRED MUTATION, run 2026-09-08: append each symlink to w.files as
// a File in the walk's symlink branch. Reds here — 3,005 files against a
// 3,000 limit.
func TestSkippedLinksAreNotCounted(t *testing.T) {
	entries := make([]fakeEntry, 0, wire.MaxSourceFiles+5)
	for i := 0; i < wire.MaxSourceFiles; i++ {
		entries = append(entries, fakeEntry{name: fmt.Sprintf("page-%04d.html", i), size: 1})
	}
	for i := 0; i < 5; i++ {
		entries = append(entries, fakeEntry{name: fmt.Sprintf("link-%d", i), mode: fs.ModeSymlink})
	}

	tree := mustWalk(t, fakeFS{dirs: map[string][]fakeEntry{"": entries}}, "root")
	if len(tree.Symlinks) != 5 {
		t.Fatalf("the walk recorded %d links, want 5 — this row's fixture never reached "+
			"the branch it is about", len(tree.Symlinks))
	}
	if len(tree.Files) != wire.MaxSourceFiles {
		t.Fatalf("the walk listed %d files, want exactly the limit", len(tree.Files))
	}

	if found := findingsFor(Limits(tree.Files), check.IDLimitFiles); len(found) != 0 {
		t.Errorf("findings = %v, want none — a skipped link is not a file that will be "+
			"packed, so it is not a file that counts", found)
	}
}

// ---------------------------------------------------------------------
// What every message has to say
// ---------------------------------------------------------------------

// TestEveryLimitMessageNamesTheIgnoreFileAndWhatWasAlreadyExcluded.
//
// The first thing anybody told their project has too many files does is
// wonder whether the dependency directory was counted, and a message
// that leaves them to find out has cost the round-trip it existed to
// save. The action is the other half: a hard stop that names no action
// leaves a first-time reader guessing.
//
// REQUIRED MUTATION, run 2026-09-08: blank the Why field of any one of
// the four findings. Reds naming that check.
func TestEveryLimitMessageNamesTheIgnoreFileAndWhatWasAlreadyExcluded(t *testing.T) {
	for id, finding := range everyLimitFinding(t) {
		text := wholeFinding(finding)
		if !strings.Contains(text, ".gitignore") {
			t.Errorf("%s: the message never names the fix:\n%s", id, text)
		}
		if finding.Next == "" {
			t.Errorf("%s: the finding carries no action", id)
		}
		for _, name := range forcedExcludeNames() {
			if !strings.Contains(text, name) {
				t.Errorf("%s: the message does not say that %q was already excluded:\n%s",
					id, name, text)
			}
		}
	}
}

// TestTheExcludedSetIsRENDEREDFromTheWalksOwnListAndNotRetyped.
//
// A copy of that list inside a message is a second list, and the day
// somebody adds a directory to the forced set the message goes on naming
// the old ones — with nothing able to see the difference, because both
// lists would still be internally consistent. So the property is not
// "the message happens to contain the right names" but "the message
// changes when the set does", and only adding to the real set can show
// that.
//
// It is the positive control for the row above: that one asserts the
// names are present, which a retyped copy also satisfies.
//
// REQUIRED MUTATION, run 2026-09-08: replace the strings.Join over
// forcedExcludeNames in alreadyExcluded with the same names written out
// as a literal. Reds here and nowhere else — every other row in this
// file stays green, which is exactly the invisibility this one exists
// for.
func TestTheExcludedSetIsRENDEREDFromTheWalksOwnListAndNotRetyped(t *testing.T) {
	const invented = "a-directory-nobody-has-ever-had"

	before := alreadyExcluded()
	if strings.Contains(before, invented) {
		t.Fatalf("the sentence already mentions %q, so this row can prove nothing", invented)
	}

	original := forcedExcludes
	t.Cleanup(func() { forcedExcludes = original })
	forcedExcludes = append(append([]forcedRule{}, original...), forcedRule{name: invented})

	if after := alreadyExcluded(); !strings.Contains(after, invented) {
		t.Errorf("the excluded set gained %q and the sentence did not:\n%s", invented, after)
	}
}

// TestThePrefixRuleIsShownAsAPrefix. The environment-file rule is the
// whole prefix, sample files included, and a message naming the bare
// stem tells a reader their suffixed variants were kept.
//
// REQUIRED MUTATION, run 2026-09-08: return r.name unchanged from
// forcedRule.display. Reds here.
func TestThePrefixRuleIsShownAsAPrefix(t *testing.T) {
	names := forcedExcludeNames()
	if len(names) != len(forcedExcludes) {
		t.Fatalf("names = %v, want one per rule (%d)", names, len(forcedExcludes))
	}
	var prefixed []string
	for _, n := range names {
		if strings.HasSuffix(n, "*") {
			prefixed = append(prefixed, n)
		}
	}
	if len(prefixed) == 0 {
		t.Fatal("no rule renders as a prefix — the fixture cannot see the property")
	}
	for i, rule := range forcedExcludes {
		if rule.prefix != strings.HasSuffix(names[i], "*") {
			t.Errorf("rule %q renders as %q", rule.name, names[i])
		}
	}
}

// TestTheForcedExcludeRulesStillMatchWhatTheyDidAsASwitch is the guard
// on turning the set from code into data.
//
// UNIFYING TWO THINGS AND SEEING NOTHING GO RED IS NOT EVIDENCE THE
// CHANGE WAS SAFE, and the walk's own suite could not have seen a
// depth rule inverted: it covers the ordinary spellings and none of the
// pairs where the two rules disagree. This row is written AFTER the
// change, on the property the change created — that a rule's depth and
// its prefix-ness survived the move.
//
// REQUIRED MUTATION, run 2026-09-08: drop the rootOnly guard from
// forcedRule.matches. Reds on the nested build-output rows.
// SECOND REQUIRED MUTATION, run 2026-09-08: make matches compare by
// prefix for every rule. Reds on the row for a name that merely starts
// with a forced one.
func TestTheForcedExcludeRulesStillMatchWhatTheyDidAsASwitch(t *testing.T) {
	for _, row := range []struct {
		name    string
		atRoot  bool
		want    bool
		because string
	}{
		{"node_modules", true, true, "dependencies go at any depth"},
		{"node_modules", false, true, "a workspace has several of them"},
		{".git", false, true, "a linked checkout puts one anywhere"},
		{".DS_Store", false, true, ""},
		{"Thumbs.db", false, true, ""},
		{"dist", true, true, "build output at the root"},
		{"dist", false, false, "a dist directory further down is somebody's own source"},
		{".astro", true, true, ""},
		{".astro", false, false, "the same reasoning as dist"},
		{".env", true, true, ""},
		{".env.local", false, true, "the rule is the whole prefix"},
		{".environment", false, true, "the prefix rule has no exception, by design"},
		{"node_modules_backup", false, false, "an exact-name rule is not a prefix rule"},
		{"src", true, false, ""},
		{"index.html", false, false, ""},
	} {
		if got := forcedExclude(row.name, row.atRoot); got != row.want {
			t.Errorf("forcedExclude(%q, atRoot=%v) = %v, want %v (%s)",
				row.name, row.atRoot, got, row.want, row.because)
		}
	}
}

// ---------------------------------------------------------------------
// The manifest, and the report the whole thing has to fit into
// ---------------------------------------------------------------------

// TestTheLimitsClaimExactlyTheirOwnIDs, in both directions, because they
// fail differently: an id nobody claims is a silent hole in the report,
// and an id claimed twice is two producers answering one question.
//
// REQUIRED MUTATION, run 2026-09-08: drop the limit-total row from
// Limits' manifest. The claimed set reds and the coverage half reds with
// it.
func TestTheLimitsClaimExactlyTheirOwnIDs(t *testing.T) {
	res := Limits(nil)

	var got []string
	for _, row := range res.Manifest {
		got = append(got, row.CheckID)
	}
	if !reflect.DeepEqual(got, limitIDs) {
		t.Errorf("manifest = %v, want one row per id these limits own, in order %v",
			got, limitIDs)
	}

	missing, unexpected := check.CoverageGaps(res.Manifest)
	if len(unexpected) != 0 {
		t.Errorf("unexpected = %v, want none — a row for an id nobody declared reaches "+
			"the user as the name of a check they have never heard of", unexpected)
	}
	for _, id := range limitIDs {
		for _, m := range missing {
			if m == id {
				t.Errorf("%s is declared and this producer did not claim it", id)
			}
		}
	}
}

// TestTheUnpackedRunSaysTheArchiveWasNotMeasuredRatherThanAnsweringZero.
//
// "Found nothing" and "never looked" arrive at a surface as the same
// silence, and a check reported as having run when it did not is a lie
// the reader acts on. The packed size is a fact about an artefact that
// does not exist before the pack, so the row says so.
//
// IT IS A BY-DESIGN DECLINE rather than an environmental one, which
// decides what it costs. Nothing outside the check went wrong and there
// is nothing here anybody can fix, so a surface that showed it would be
// asking somebody to decide about the passage of time.
//
// REQUIRED MUTATION, run 2026-09-08: build the packed row with answered
// instead of declined in Limits. Reds here on the outcome.
func TestTheUnpackedRunSaysTheArchiveWasNotMeasuredRatherThanAnsweringZero(t *testing.T) {
	row := rowFor(t, Limits(nil), check.IDLimitPacked)
	if row.Outcome != check.Declined {
		t.Errorf("Outcome = %v, want a decline — nothing has been packed", row.Outcome)
	}
	if row.Kind != check.ByDesign {
		t.Errorf("Kind = %v, want by design — nothing outside the check went wrong", row.Kind)
	}
	if row.Reason == "" {
		t.Error("the decline records no reason, which renders as a bare trailing colon")
	}
}

// TestAProjectThatFailsPreFlightStillCarriesAllFourLimitRows.
//
// THIS IS THE ROW THAT MAKES THE ALWAYS-RUN RULE A PROPERTY rather than
// a sentence. The combiner refuses a report that does not cover every
// declared check, so a run that skipped the limits because pre-flight
// had already stopped it would be holding a legitimate hard stop it
// could not render — a completeness gate refusing its own caller's
// honest report, reaching a user who typed nothing wrong.
//
// Both facts have to be in the one report: a project that is both
// unbuildable and too large should tell its owner both things once, not
// one per attempt.
//
// REQUIRED MUTATION, run 2026-09-08: in Limits, append a manifest row
// only for the checks that produced a finding — the shape a report that
// says only what it found would take, and the shape a run that skipped
// the limits would produce. Reds here with a coverage error naming the
// rows that went missing.
func TestAProjectThatFailsPreFlightStillCarriesAllFourLimitRows(t *testing.T) {
	// The engine's own five, one of them a hard stop, as they would
	// arrive from a project with no lockfile.
	engine := check.Results{
		Findings: []check.Finding{{
			CheckID:  check.IDLockfile,
			Severity: check.SeverityHardStop,
			Message:  "no lockfile found",
		}},
		Manifest: check.Manifest{
			{CheckID: check.IDAstroDep},
			{CheckID: check.IDLockfile},
			{CheckID: check.IDPagesDir},
			{CheckID: check.IDBuildFormat},
			{CheckID: check.IDLocalhost},
		},
	}

	tree := mustWalk(t, OSFileSystem{}, t.TempDir())
	oversize := generatedFiles("public/gallery", 3_400, 1)

	report, err := check.Combine(engine, tree.Results, Limits(oversize))
	if err != nil {
		t.Fatalf("combining a hard-stopped run with the limits: %v", err)
	}

	var ids []string
	for _, row := range report.Manifest() {
		ids = append(ids, row.CheckID)
	}
	if !reflect.DeepEqual(ids, check.DeclaredOrder()) {
		t.Errorf("manifest = %v, want the whole declared universe %v", ids, check.DeclaredOrder())
	}

	var said []string
	for _, f := range report.Findings() {
		said = append(said, f.CheckID)
	}
	for _, want := range []string{check.IDLockfile, check.IDLimitFiles} {
		if !containsString(said, want) {
			t.Errorf("the report says nothing about %s; it names %v — a project that is "+
				"both unbuildable and too large should learn both facts in one run",
				want, said)
		}
	}
}

// ---------------------------------------------------------------------
// Around the pack
// ---------------------------------------------------------------------

// TestARefusedRunPacksNothingAndUploadsNothing.
//
// THE SENTINEL AND ITS POSITIVE CONTROL ARE IN ONE FUNCTION, and that is
// the whole shape of the row. An instrument that reads zero is only
// evidence when the same instrument has been seen to read one — a
// sentinel wired to nothing reports "no upload was attempted" on every
// run, including the runs that uploaded.
//
// THE TEMPORARY DIRECTORY IS THE TEST'S OWN and is read back whole,
// rather than a guessed path being stat-ed: "nothing was left behind" is
// a claim about a directory, and asking about one file it might have
// been called cannot see the one it was.
//
// REQUIRED MUTATION, run 2026-09-08: delete the findings refusal from
// Prepare. The refused half reds on the leftover archive — "the
// temporary directory holds [curious-….tar.gz], want nothing" — while
// the positive control stays green. The handed-back archive stays empty
// under that mutation, which is why the directory is read rather than
// the return value trusted.
func TestARefusedRunPacksNothingAndUploadsNothing(t *testing.T) {
	var uploads uploadSentinel

	// The positive control FIRST, so nothing below is trusted reading a
	// zero from an instrument that has never read anything else.
	root := writeTree(t, []entry{{path: "index.html", body: "<html>"}})
	dir := t.TempDir()
	tree := mustWalk(t, OSFileSystem{}, root)

	prepared, res, err := Prepare(OSFileSystem{}, root, dir, tree.Files, Limits(tree.Files))
	if err != nil {
		t.Fatalf("Prepare on an ordinary project: %v", err)
	}
	uploads.after(prepared)
	if uploads.n != 1 {
		t.Fatalf("the sentinel read %d after a project that passed every limit — it is "+
			"not wired to anything, and every zero below would be meaningless", uploads.n)
	}
	if err := prepared.Archive.Remove(); err != nil {
		t.Fatalf("removing the archive the control produced: %v", err)
	}
	if len(check.Advisories(res.Findings)) != 0 {
		t.Fatalf("an ordinary project produced %v", res.Findings)
	}

	// Now the refusal, into a directory of its own.
	//
	// THE OVER-LIMIT FIXTURE IS REAL ENOUGH TO PACK, served from memory
	// rather than named on disk. That is what makes the mutation
	// meaningful: a fixture of paths nothing can open fails Prepare with
	// an I/O error the moment anything tries to pack it, so a packer that
	// ignored the verdict would red for the wrong reason and the leftover
	// archive — the thing this row is about — would never exist.
	// Measured: written the other way, the mutation reds on "no such file
	// or directory".
	refusedFS, refusedFiles := manyTinyFiles(wire.MaxSourceFiles + 1)
	refusedDir := t.TempDir()
	refusedLimits := Limits(refusedFiles)
	if len(findingsFor(refusedLimits, check.IDLimitFiles)) == 0 {
		t.Fatalf("the fixture is not over the count limit, so this half measures nothing: %v",
			refusedLimits.Findings)
	}

	prepared, _, err = Prepare(refusedFS, "root", refusedDir, refusedFiles, refusedLimits)
	uploads.after(prepared)

	// THE REFUSAL IS NAMED, not merely counted. A negative row satisfied
	// by any old failure is satisfied by an I/O error, a missing fixture
	// or a typo'd root — every one of which also packs nothing.
	if err == nil {
		t.Fatalf("Prepare packed a project the limits had already refused")
	}
	if !strings.Contains(err.Error(), check.IDLimitFiles) {
		t.Errorf("the refusal %q does not name the limit that refused the project", err)
	}
	if uploads.n != 1 {
		t.Errorf("the sentinel read %d, want 1 — a refused run must not upload", uploads.n)
	}
	if prepared.Archive.Path != "" {
		t.Errorf("a refused run produced an archive at %q", prepared.Archive.Path)
	}
	if prepared.Receipt != "" {
		t.Errorf("a refused run printed a receipt: %q", prepared.Receipt)
	}
	if left := readDir(t, refusedDir); len(left) != 0 {
		t.Errorf("the temporary directory holds %v, want nothing", left)
	}
}

// TestAVerdictThatNeverMeasuredAnythingIsRefused is the other half of
// the argument-shaped gate, and it is the half an obvious reading omits.
//
// "The report carries no finding" is true of a zero check.Results, so a
// caller that measured nothing at all would pack — the order-by-memory
// the argument replaced, wearing a parameter. Prepare therefore asks
// whether the three source limits were ANSWERED, and a declined row is
// not an answer.
//
// The clean report packs in the same function, because every assertion
// here is about a refusal and a Prepare that refused everything would
// satisfy all of them.
//
// REQUIRED MUTATION, run 2026-09-08: delete the unansweredSourceLimits
// refusal from Prepare. The first two subtests red; the control stays
// green.
func TestAVerdictThatNeverMeasuredAnythingIsRefused(t *testing.T) {
	root := writeTree(t, []entry{{path: "index.html", body: "<html>"}})
	tree := mustWalk(t, OSFileSystem{}, root)

	declinedEverything := check.Results{Manifest: check.Manifest{
		declined(check.IDLimitFiles, check.Environmental, "the file list could not be read"),
		declined(check.IDLimitFileSize, check.Environmental, "the file list could not be read"),
		declined(check.IDLimitTotal, check.Environmental, "the file list could not be read"),
	}}

	for _, tc := range []struct {
		name    string
		limits  check.Results
		wantErr bool
	}{
		{"nothing was measured at all", check.Results{}, true},
		{"every source limit declined", declinedEverything, true},
		{"control: a real verdict packs", Limits(tree.Files), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prepared, _, err := Prepare(OSFileSystem{}, root, dir, tree.Files, tc.limits)
			t.Cleanup(func() { _ = prepared.Archive.Remove() })

			switch {
			case tc.wantErr && err == nil:
				t.Fatal("Prepare packed against a report that answered nothing")
			case tc.wantErr:
				for _, id := range []string{
					check.IDLimitFiles, check.IDLimitFileSize, check.IDLimitTotal,
				} {
					if !strings.Contains(err.Error(), id) {
						t.Errorf("the refusal %q does not name the unanswered limit %s", err, id)
					}
				}
				if left := readDir(t, dir); len(left) != 0 {
					t.Errorf("the temporary directory holds %v, want nothing", left)
				}
			case err != nil:
				t.Fatalf("Prepare refused a measured, clean verdict: %v", err)
			case prepared.Archive.Path == "":
				t.Error("the control packed nothing, so every refusal above may be a " +
					"Prepare that refuses everything")
			}
		})
	}
}

// TestARunThatNeverPacksStillReportsAllFourRows. The report a refused
// run produces has to pass the same gate as any other, and the packed
// row nothing can answer yet says so rather than being absent. That is
// Limits's row to carry, and it carries it on every run — including the
// ones that stop before a packer is ever reached.
//
// REQUIRED MUTATION, run 2026-09-08: drop the declined packed row from
// Limits. Reds here on the row count.
func TestARunThatNeverPacksStillReportsAllFourRows(t *testing.T) {
	res := Limits(generatedFiles("public", 3_400, 1))

	var got []string
	for _, row := range res.Manifest {
		got = append(got, row.CheckID)
	}
	if !reflect.DeepEqual(got, limitIDs) {
		t.Errorf("manifest = %v, want %v", got, limitIDs)
	}
	if row := rowFor(t, res, check.IDLimitPacked); row.Outcome != check.Declined {
		t.Errorf("the packed row says it answered, and nothing was packed")
	}
	if len(findingsFor(res, check.IDLimitFiles)) == 0 {
		t.Errorf("the refused run said nothing about the limit it broke: %v", res.Findings)
	}
}

// TestTheSuccessReceiptNamesFilesSourceAndArchive. It is the receipt for
// what is about to leave the machine, and it makes a packed state
// visible rather than inferred from a progress line stopping.
//
// REQUIRED MUTATION, run 2026-09-08: make receipt render the archive
// size in place of the source total — the two numbers swapped, which is
// how the source total goes missing without the format string and its
// arguments falling out of step. Reds on the middle assertion alone.
func TestTheSuccessReceiptNamesFilesSourceAndArchive(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "index.html", body: strings.Repeat("<html>", 1_000)},
		{path: "src/pages/index.astro", body: "page"},
	})
	tree := mustWalk(t, OSFileSystem{}, root)

	prepared, _, err := Prepare(OSFileSystem{}, root, t.TempDir(), tree.Files, Limits(tree.Files))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Archive.Remove() })

	for _, want := range []string{
		"2 files",
		"6.0 kB",
		units.Bytes(prepared.Archive.Size),
	} {
		if !strings.Contains(prepared.Receipt, want) {
			t.Errorf("the receipt %q does not name %q", prepared.Receipt, want)
		}
	}
}

// TestAnArchiveOverTheCapIsRefusedWithNoReceiptAboveIt.
//
// THIS IS THE ONE REFUSAL A USER CAN REACH HAVING PASSED EVERY OTHER
// CHECK HERE, so the fixture is the real thing rather than an archive
// size handed in: three thousand incompressible files of ten thousand
// bytes each is exactly thirty million bytes of content — inside every
// source limit — and the tar records alone carry it past the cap before
// compression has contributed anything.
//
// A FIXTURE THAT COULD ONLY PRODUCE ORDINARY VALUES CANNOT SEE THIS. Any
// compressible fixture packs to a fraction of its size and passes, so
// the bytes are pseudo-random and each file's are different: three
// thousand copies of one random block would compress to almost nothing
// after the first.
//
// THE ORDER IS THE PROPERTY. Both the check and the receipt happen after
// packing, and written the other way round a refused run prints a
// receipt for an upload that will not happen, directly above the
// refusal.
//
// REQUIRED MUTATION, run 2026-09-08: in Prepare, build the receipt
// before the packed-size comparison and return it whatever the verdict.
// Reds on the receipt assertion alone, with the finding still correct —
// which is exactly the transcript that contradicts itself.
// SECOND REQUIRED MUTATION, run 2026-09-08: drop the archive.Remove call
// from the over-cap branch. Reds on the empty-directory assertion.
func TestAnArchiveOverTheCapIsRefusedWithNoReceiptAboveIt(t *testing.T) {
	const files, size = 3_000, 10_000

	fsys, list := incompressibleTree(t, files, size)
	if total := totalSize(list); total != wire.MaxSourceTotalBytes {
		t.Fatalf("the fixture is %d bytes of source, want exactly the limit %d — this row "+
			"is only about a project that passed every source limit",
			total, wire.MaxSourceTotalBytes)
	}
	if found := findingsFor(Limits(list), check.IDLimitFiles); len(found) != 0 {
		t.Fatalf("the fixture is already refused by an earlier limit: %v", found)
	}

	dir := t.TempDir()
	prepared, res, err := Prepare(fsys, "root", dir, list, Limits(list))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	found := findingsFor(res, check.IDLimitPacked)
	if len(found) != 1 {
		t.Fatalf("findings = %v, want the packed-size refusal — if this is empty the "+
			"fixture compressed to something the cap accepts and the row measured nothing",
			res.Findings)
	}
	if found[0].Severity != check.SeverityHardStop {
		t.Errorf("Severity = %q, want a hard stop", found[0].Severity)
	}
	if prepared.Receipt != "" {
		t.Errorf("a refused run printed a receipt above its refusal: %q", prepared.Receipt)
	}
	if prepared.Archive.Path != "" {
		t.Errorf("a refused run handed back an archive at %q", prepared.Archive.Path)
	}
	if left := readDir(t, dir); len(left) != 0 {
		t.Errorf("the temporary directory holds %v, want nothing", left)
	}

	// The message's own job: name the overhead rather than the content,
	// give both numbers, and point at the files worth acting on.
	text := wholeFinding(found[0])
	if !strings.Contains(text, "overhead") {
		t.Errorf("the message does not say the overflow is the archive's own overhead — "+
			"the numbers then read as a contradiction:\n%s", text)
	}
	if !strings.Contains(text, "30.0 MB") {
		t.Errorf("the message does not state the cap:\n%s", text)
	}
	// THE FILES ARE READ FROM Paths, NOT FROM THE COPY. They used to be
	// in both, which is what made the renderer print each one twice.
	if len(found[0].Paths) == 0 {
		t.Errorf("the finding carries no paths, so it names nothing anybody can act " +
			"on, and an agent has to read English to learn which file to open")
	}
	if len(found[0].Sizes) != len(found[0].Paths) {
		t.Errorf("Sizes = %v, Paths = %v — the gate refuses a finding whose two lists "+
			"disagree", found[0].Sizes, found[0].Paths)
	}
	for _, p := range found[0].Paths {
		if !strings.HasPrefix(p, "blob-") {
			t.Errorf("Paths names %q, which is not one of the fixture's files", p)
		}
	}
}

// TestTheLeastCompressibleFilesAreTheOnesReported. The overhead
// concentrates in files that do not compress, and telling somebody "it
// is 2 MB over" leaves them with nothing to do.
//
// THE FIXTURE HOLDS BOTH KINDS, which is what makes the row able to
// fail: a tree of uniformly incompressible files would rank the same
// under any comparison, so the ordering could not be seen. Here the
// compressible half is larger on disk than the incompressible half, so a
// ranking by SIZE — the obvious wrong implementation — reports exactly
// the opposite set.
//
// REQUIRED MUTATION, run 2026-09-08: rank by file size instead of by the
// packed-to-disk ratio in leastCompressible. Reds naming the
// compressible files.
func TestTheLeastCompressibleFilesAreTheOnesReported(t *testing.T) {
	dirs := map[string][]fakeEntry{"": nil}
	var list []File
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("text-%d.txt", i)
		body := strings.Repeat("the same sentence, over and over. ", 4_000)
		dirs[""] = append(dirs[""], fakeEntry{name: name, size: int64(len(body)), body: body})
		list = append(list, File{Path: name, Size: int64(len(body))})
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("noise-%d.bin", i)
		body := string(noise(int64(i)+1, 20_000))
		dirs[""] = append(dirs[""], fakeEntry{name: name, size: int64(len(body)), body: body})
		list = append(list, File{Path: name, Size: int64(len(body))})
	}

	got, _ := leastCompressible(fakeFS{dirs: dirs}, "root", list)
	if len(got) != 3+2 {
		t.Fatalf("leastCompressible returned %d rows, want %d", len(got), listedContributors)
	}
	for i := 0; i < 3; i++ {
		if !strings.HasPrefix(got[i].file.Path, "noise-") {
			t.Errorf("[%d] = %s, want the incompressible files first", i, got[i].file.Path)
		}
	}
	if got[0].packed <= got[0].file.Size/2 {
		t.Errorf("%s packed to %d of %d, which is not incompressible — the fixture cannot "+
			"see the property", got[0].file.Path, got[0].packed, got[0].file.Size)
	}
}

// TestAnOrdinaryProjectHasNothingToSay is the positive control for this
// whole file. Every other row asserts that something is reported, and
// each of those would be satisfied by limits that refused everything.
//
// REQUIRED MUTATION, run 2026-09-08: drop the `> ` to `>= ` in any one
// of the three comparisons — or simply return the finding
// unconditionally. Reds here naming the check.
func TestAnOrdinaryProjectHasNothingToSay(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "package.json", body: `{"name":"site"}`},
		{path: "src/pages/index.astro", body: "<h1>hello</h1>"},
		{path: "public/logo.svg", body: "<svg/>"},
	})
	tree := mustWalk(t, OSFileSystem{}, root)

	res := Limits(tree.Files)
	if len(res.Findings) != 0 {
		t.Errorf("findings = %v, want none from an ordinary project", res.Findings)
	}
	// THE ROW'S OWN POSITIVE CONTROL. Every assertion here is about an
	// ABSENCE, so a Limits that had become a no-op returning nothing
	// would satisfy all of them — the row would pass hardest against the
	// subject having vanished. Naming the manifest it must carry is what
	// tells "nothing to report" apart from "nothing ran".
	if len(res.Manifest) != len(limitIDs) {
		t.Fatalf("the manifest carries %d rows, want %d — a check that reported "+
			"nothing and a check that did not run look identical from an empty "+
			"findings list", len(res.Manifest), len(limitIDs))
	}
	for _, row := range res.Manifest {
		if row.CheckID == check.IDLimitPacked {
			continue
		}
		if row.Outcome != check.Answered {
			t.Errorf("%s declined on an ordinary project: %s", row.CheckID, row.Reason)
		}
	}
}

// TestAFourFigureCountIsGroupedWhereverAMessagePrintsOne.
//
// WRITTEN AFTER THE CHANGE, ON THE PROPERTY THE CHANGE CREATED, and said
// so plainly rather than left to look like a catch. countOf was made to
// group its number on 2026-09-08 and NOTHING WENT RED — which reads like
// safety and is the opposite: until that day no message in this package
// had ever been handed a number worth grouping, so the tree could not
// have noticed the two surfaces agreeing or the day they stopped. A
// silent unification measures the suite, not the change.
//
// TWO SURFACES, because that is the whole of it. The receipt and a limit
// message are written in different functions, and one program printing
// "3,412 files" in one sentence and "3000 files" in the next has let the
// reader's confidence depend on which wall they hit.
//
// REQUIRED MUTATION, run 2026-09-08: return fmt.Sprintf("%d %s", n, many)
// from countOf. Reds on both halves.
func TestAFourFigureCountIsGroupedWhereverAMessagePrintsOne(t *testing.T) {
	fsys, files := manyTinyFiles(1_200)

	prepared, _, err := Prepare(fsys, "root", t.TempDir(), files, Limits(files))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Archive.Remove() })
	if !strings.Contains(prepared.Receipt, "1,200 files") {
		t.Errorf("the receipt does not group its count: %q", prepared.Receipt)
	}

	oversize := make([]File, 0, 1_200)
	for _, f := range files {
		oversize = append(oversize, File{Path: f.Path, Size: wire.MaxSourceFileBytes + 1})
	}
	found := findingsFor(Limits(oversize), check.IDLimitFileSize)
	if len(found) != 1 {
		t.Fatalf("findings = %v, want one", found)
	}
	if !strings.Contains(found[0].Message, "1,200 files are larger") {
		t.Errorf("the limit message does not group its count: %q", found[0].Message)
	}
}

// ---------------------------------------------------------------------
// Fixture machinery for this file
// ---------------------------------------------------------------------

// uploadSentinel counts the runs that would have sent something.
//
// THERE IS NO UPLOAD IN THIS TREE YET, so what it counts is the decision
// rather than the request: a Prepared carrying an archive is a run that
// continues, and a zero one is a run that stopped. Said plainly here
// rather than left for a reader to infer, because an instrument named
// for something it does not measure is how a row comes to prove
// something nobody meant.
type uploadSentinel struct {
	n     int
	paths []string
}

func (u *uploadSentinel) after(p Prepared) {
	if p.Archive.Path == "" {
		return
	}
	u.n++
	u.paths = append(u.paths, p.Archive.Path)
}

// manyTinyFiles is n one-byte files served from memory: a project big
// enough to break the count limit that can still really be packed.
//
// A LIST OF PATHS NOTHING CAN OPEN IS NOT THE SAME FIXTURE. A row that
// asserts nothing was packed has to be handed something that COULD have
// been, or it cannot tell a packer that declined from a packer that
// tried and failed.
func manyTinyFiles(n int) (fakeFS, []File) {
	entries := make([]fakeEntry, 0, n)
	list := make([]File, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("page-%05d.html", i)
		entries = append(entries, fakeEntry{name: name, size: 1, body: "x"})
		list = append(list, File{Path: name, Size: 1})
	}
	return fakeFS{dirs: map[string][]fakeEntry{"": entries}}, list
}

// generatedFiles is n files of one size under one directory, named so
// that their sorted order is their creation order.
func generatedFiles(dir string, n int, size int64) []File {
	out := make([]File, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, File{Path: fmt.Sprintf("%s/file-%05d.txt", dir, i), Size: size})
	}
	return out
}

// sparseFile creates a file that REPORTS a size without occupying one.
// A fixture whose excluded half has to be past every limit cannot be
// four hundred real megabytes on a test machine, and nothing here reads
// the bytes — the limits measure what the directory listing says.
func sparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating the directory for %s: %v", path, err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("sizing %s to %d: %v", path, size, err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() != size {
		t.Fatalf("%s reports %d bytes, want %d — the fixture is not the size the row "+
			"needs it to be", path, info.Size(), size)
	}
}

// incompressibleTree builds n files of pseudo-random bytes, served from
// memory so the row costs no disk beyond the archive it produces.
//
// EACH FILE'S BYTES DIFFER. n copies of one random block compress to
// almost nothing after the first, which would make the fixture
// compressible and the row vacuous.
func incompressibleTree(t *testing.T, n int, size int64) (fakeFS, []File) {
	t.Helper()
	entries := make([]fakeEntry, 0, n)
	list := make([]File, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("blob-%05d.bin", i)
		body := string(noise(int64(i), size))
		entries = append(entries, fakeEntry{name: name, size: size, body: body})
		list = append(list, File{Path: name, Size: size})
	}
	return fakeFS{dirs: map[string][]fakeEntry{"": entries}}, list
}

// noise is deterministic pseudo-random data: the same seed gives the
// same bytes on every machine, so a failure here is reproducible rather
// than a thing that happened once.
func noise(seed, size int64) []byte {
	out := make([]byte, size)
	r := rand.New(rand.NewSource(seed))
	for i := range out {
		out[i] = byte(r.Intn(256))
	}
	return out
}

// wholeFinding is every word a reader could meet, in one string. A row
// asserting that a message says something should not also be asserting
// which of the four fields the author put it in.
func wholeFinding(f check.Finding) string {
	return strings.Join([]string{f.Message, f.What, f.Why, f.Next}, "\n")
}

// listedInOrder reports the first pair of names that appear in s out of
// the order given, or the empty string when they all agree.
func listedInOrder(s string, want []string) string {
	positions := make([]int, len(want))
	for i, w := range want {
		positions[i] = strings.Index(s, w)
		if positions[i] < 0 {
			return fmt.Sprintf("%s is missing", w)
		}
	}
	if !sort.IntsAreSorted(positions) {
		return fmt.Sprintf("positions %v", positions)
	}
	return ""
}

func rowFor(t *testing.T, res check.Results, id string) check.Status {
	t.Helper()
	for _, row := range res.Manifest {
		if row.CheckID == id {
			return row
		}
	}
	t.Fatalf("no manifest row for %s in %v", id, res.Manifest)
	return check.Status{}
}

func readDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s back: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// everyLimitFinding is one finding from each of the four checks, so a
// row asserting something about all of them cannot silently cover three.
//
// It FAILS rather than skipping when a check produces nothing: a map
// with three entries would satisfy a range loop in silence, which is the
// shape of an assertion that measures less than its name says.
func everyLimitFinding(t *testing.T) map[string]check.Finding {
	t.Helper()

	out := map[string]check.Finding{}
	for _, res := range []check.Results{
		Limits(generatedFiles("public", 3_400, 1)),
		Limits([]File{{Path: "public/big.bin", Size: 6_000_000}}),
		Limits([]File{{Path: "a.bin", Size: 20_000_000}, {Path: "b.bin", Size: 20_000_000}}),
	} {
		for _, f := range res.Findings {
			out[f.CheckID] = f
		}
	}

	fsys, list := incompressibleTree(t, 40, 10_000)
	out[check.IDLimitPacked] = packedFinding(fsys, "root", list,
		Archive{Size: wire.MaxPackedBytes + 1})

	for _, id := range limitIDs {
		if _, ok := out[id]; !ok {
			t.Fatalf("no fixture produced a finding for %s — a row over this map would "+
				"pass while covering three of the four", id)
		}
	}
	return out
}

// TestThePackedFindingNamesEachFileOnce is the fourth limit's half of the
// sized-path model.
//
// It used to lay its own table into the copy — a packed size, an on-disk
// size and a path per row — AND set Paths, so the renderer appended every
// one of those paths again underneath. The one hard stop reached by a
// project that did nothing wrong listed its offenders twice.
//
// THE RATIO SURVIVES AS A SENTENCE, NOT AS A COLUMN. The list is selected
// and ordered by how little each file compressed, so the ordering already
// carries what the second column said; what a reader cannot recover from
// the ordering is the overall shape, and that is one sentence rather than
// a number per row.
//
// REQUIRED MUTATION: put the per-file packed-of-on-disk column back in
// the copy while leaving Paths set. Reds here on the duplicate name.
func TestThePackedFindingNamesEachFileOnce(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "public/a.bin", body: "not compressible enough"},
		{path: "public/b.bin", body: "also stubborn content here"},
	})
	files := []File{
		{Path: "public/a.bin", Size: 23, Mode: 0o644},
		{Path: "public/b.bin", Size: 26, Mode: 0o644},
	}

	f := packedFinding(OSFileSystem{}, root, files, Archive{Size: 32_257_024})

	if len(f.Paths) == 0 {
		t.Fatal("the finding names no files, so nothing below is about a list")
	}
	if len(f.Sizes) != len(f.Paths) {
		t.Fatalf("Sizes = %v, Paths = %v — the gate refuses a finding whose two "+
			"lists disagree", f.Sizes, f.Paths)
	}
	for _, path := range f.Paths {
		if n := strings.Count(f.What, path); n != 0 {
			t.Errorf("the copy names %q %d times as well as carrying it in Paths, "+
				"so the renderer prints it twice", path, n)
		}
	}
	// The overall ratio is still said, once, because the ordering cannot
	// say it: a reader needs to know the archive barely compressed at all.
	if !strings.Contains(f.What, "compressed least") {
		t.Errorf("the copy no longer says what the list is:\n%s", f.What)
	}
}

// TestTheCompressionDiagnosticSkipsALinkRatherThanFollowingIt.
//
// The packer refuses a non-regular entry before opening it, because Open
// resolves a symlink and a check afterwards would already have read the
// file it meant to refuse. The compression diagnostic reads the SAME
// list a second time, to say which files compressed least, and it did not
// share that refusal — so a link in the list was followed and its
// target's bytes were read, from outside the project, on a path that had
// already failed and nobody was watching.
//
// REQUIRED MUTATION: drop the packable guard from compressedSize. This
// row reds — the link is measured, so it appears in the result.
func TestTheCompressionDiagnosticSkipsALinkRatherThanFollowingIt(t *testing.T) {
	root := writeTree(t, []entry{{path: "public/real.bin", body: "ordinary content"}})
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET-CONTENT"), 0o600); err != nil {
		t.Fatalf("writing the out-of-tree fixture: %v", err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "public/link.bin")); err != nil {
		t.Skipf("this runner cannot create a symbolic link, so the input this row needs cannot exist: %v", err)
	}

	measured, left := leastCompressible(OSFileSystem{}, root, []File{
		{Path: "public/real.bin", Size: 16, Mode: 0o644},
		{Path: "public/link.bin", Size: 14, Mode: os.ModeSymlink | 0o777},
	})

	// WHICH GUARD FIRED, not merely that one did. A link left out for
	// any other reason would satisfy the list assertion below while
	// proving nothing about the refusal this row exists for.
	if len(left) != 1 || left[0].file.Path != "public/link.bin" {
		t.Fatalf("skipped = %v, want the link alone", left)
	}
	if left[0].reason != skippedUnpackable {
		t.Errorf("the link was skipped for reason %d, want the packer's own refusal — "+
			"a negative row satisfied by a different guard proves the wrong one",
			left[0].reason)
	}

	// The positive control is in the same call: the regular file IS
	// measured, so this row cannot pass against a diagnostic that has
	// quietly stopped measuring anything.
	var names []string
	for _, m := range measured {
		names = append(names, m.file.Path)
	}
	if !reflect.DeepEqual(names, []string{"public/real.bin"}) {
		t.Errorf("measured %v, want the regular file alone — a link must be skipped, "+
			"not followed to whatever it points at", names)
	}
}
