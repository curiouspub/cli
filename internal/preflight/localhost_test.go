package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/curiouspub/cli/internal/check"
)

// hitFixtureFiles is the walked list for the committed scan fixture, in
// the order the walk emits: sorted byte-wise on the slash-separated
// path, on every platform.
var hitFixtureFiles = []string{
	"README.md",
	"package-lock.json",
	"package.json",
	"src/lib/api.ts",
	"src/near-misses.ts",
	"src/pages/index.astro",
}

// runLocalhost runs the check the way the engine will: through the
// registration, so a row exercises the wiring and not only the scan.
func runLocalhost(t *testing.T, fsys FS, root string, files []string) Result {
	t.Helper()
	c := LocalhostCheck(files)
	if !reflect.DeepEqual(c.IDs, []string{check.IDLocalhost}) {
		t.Fatalf("the registration claims %v, want only %s", c.IDs, check.IDLocalhost)
	}
	return c.Run(fsys, root)
}

// TestLocalhostNamesTheFileAndTheLine is the shape of the warning, and
// the line number is the part a reader acts on — a project with a
// hard-coded development URL has one line to open, not a file to search.
//
// The path assertion is not decoration. Paths is rendered into a result
// an agent may act on, so it has to be project-relative and
// slash-separated whatever the host's own separator is; this row runs on
// all three platforms and the Windows leg is the one that means it.
//
// REQUIRED MUTATION: report `number` instead of `number+1` in
// scanForDevURLs. Reds on the line number.
//
// SECOND REQUIRED MUTATION: build Paths from the joined host path
// instead of rel. Reds on the path, on every platform.
func TestLocalhostNamesTheFileAndTheLine(t *testing.T) {
	res := runLocalhost(t, OSFileSystem{}, projectFixture(t, "localhost-hits"), hitFixtureFiles)

	var found *check.Finding
	for i, f := range res.Findings {
		if strings.HasPrefix(f.Message, "src/pages/index.astro:") {
			found = &res.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("no finding for src/pages/index.astro; findings = %+v", res.Findings)
	}
	if found.Severity != check.SeverityWarning {
		t.Errorf("severity = %q, want a warning — this is something to be told about, "+
			"not stopped for", found.Severity)
	}
	if !strings.HasPrefix(found.Message, "src/pages/index.astro:2:") {
		t.Errorf("message = %q, want it to name line 2", found.Message)
	}
	if !strings.Contains(found.Message, "const endpoint") {
		t.Errorf("message = %q, want the matching line quoted back", found.Message)
	}
	if !reflect.DeepEqual(found.Paths, []string{"src/pages/index.astro"}) {
		t.Errorf("Paths = %v, want the project-relative slash-separated path",
			found.Paths)
	}
}

// TestLocalhostScansBeyondTheTwoNamedExtensions is the widening, with
// its own name because it is a decision. Taken literally the pre-flight
// table's ".astro/.js sources" misses TypeScript, JSX and every
// framework component — most of a modern project — and this check only
// ever produces a warning, so the cost of being wrong is a question
// rather than a refusal.
//
// REQUIRED MUTATION: remove ".ts" from scannedExtensions. Reds.
func TestLocalhostScansBeyondTheTwoNamedExtensions(t *testing.T) {
	res := runLocalhost(t, OSFileSystem{}, projectFixture(t, "localhost-hits"), hitFixtureFiles)

	var scanned []string
	for _, f := range res.Findings {
		scanned = append(scanned, f.Message)
	}
	joined := strings.Join(scanned, "\n")
	if !strings.Contains(joined, "src/lib/api.ts:1:") {
		t.Errorf("the TypeScript file was not scanned:\n%s", joined)
	}
}

// TestLocalhostIgnoresProse holds the markdown exclusion. Prose about
// running something locally is the single biggest source of false hits,
// and a link in a blog post is not a production hazard worth stopping
// anybody for.
//
// The two rows above are this one's control: the same run, over the same
// list, finds the hits in the source files — so the silence about the
// prose file is a decision the scan made rather than a scan that never
// ran.
//
// REQUIRED MUTATION: add ".md" to scannedExtensions. Reds.
func TestLocalhostIgnoresProse(t *testing.T) {
	root := projectFixture(t, "localhost-hits")

	// The control, first and explicitly: the prose file really does
	// contain the string, so its absence below is about the extension.
	prose, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	if !strings.Contains(string(prose), devURLNeedle) {
		t.Fatalf("the prose fixture no longer contains the string this row is about")
	}

	res := runLocalhost(t, OSFileSystem{}, root, hitFixtureFiles)
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "README.md") {
			t.Errorf("a hit in prose was reported: %q", f.Message)
		}
	}
	if len(res.Findings) == 0 {
		t.Error("nothing at all was reported, so this row cannot tell an excluded " +
			"extension from a scan that never ran")
	}
}

// TestLocalhostMatchesOneStringOnly holds the deliberate narrowness. A
// secure scheme on the same host and the loopback address written as
// numbers are both left out: the table names one string, this check is
// the noisiest of the four, and every extra pattern adds false positives
// to a prompt people are learning to dismiss.
//
// REQUIRED MUTATION: search for the secure scheme as well — scan each
// line for `"https:" + "//" + devURLHost` too. Reds on the near-miss
// file.
func TestLocalhostMatchesOneStringOnly(t *testing.T) {
	res := runLocalhost(t, OSFileSystem{}, projectFixture(t, "localhost-hits"), hitFixtureFiles)

	for _, f := range res.Findings {
		if strings.Contains(f.Message, "near-misses.ts") {
			t.Errorf("a near miss was reported as a hit: %q", f.Message)
		}
	}
	if len(findingsFor(res, check.IDLocalhost)) == 0 {
		t.Error("nothing at all was reported, so the near-miss file's absence proves " +
			"nothing")
	}
}

// TestLocalhostScansOnlyTheFilesItIsGiven is the property that keeps the
// dependency directory out of the scan, and it is asserted as the
// property rather than as a fact about one directory name.
//
// The check never enumerates anything: it reads the walk's canonical
// list, which has already applied the forced exclusions and the
// project's own ignore rules. So the row drives it both ways over ONE
// tree — the list the walk would produce, and the same list with the
// excluded file added — and only the second reports it. Without the
// second half this row would pass just as happily against a fixture with
// no such file in it at all, which is the failure mode a "not found" row
// is one step away from.
//
// REQUIRED MUTATION: ignore the files parameter in LocalhostCheck and
// walk the tree from root. The first half reds.
func TestLocalhostScansOnlyTheFilesItIsGiven(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"src/pages/index.astro":      "const a = \"" + devURLNeedle + ":4321\";\n",
		"node_modules/dep/bundle.js": "var b=\"" + devURLNeedle + ":9000\";\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
	}

	walked := []string{"src/pages/index.astro"}
	res := runLocalhost(t, OSFileSystem{}, root, walked)
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly the one in the walked list", res.Findings)
	}
	if strings.Contains(res.Findings[0].Message, "node_modules") {
		t.Errorf("a file outside the walked list was scanned: %q", res.Findings[0].Message)
	}

	// The control. The excluded file exists and does contain a hit, so
	// its absence above is the LIST and not the fixture.
	everything := append(walked, "node_modules/dep/bundle.js")
	if got := len(runLocalhost(t, OSFileSystem{}, root, everything).Findings); got != 2 {
		t.Errorf("naming the excluded file in the list produced %d findings, want 2 — "+
			"the file the row above relies on is not there or has no hit", got)
	}
}

// TestLocalhostSkipsAFileTooLargeToBeSource holds the size gate. A
// minified vendor bundle checked into a repository is not hand-written
// source, and reading it would cost more than the rest of this program
// put together.
//
// THE BIG FILE IS GENERATED, NEVER COMMITTED. This repository is public,
// and a committed two-megabyte blob is repository weight for ever —
// while the property under test is a size comparison, which a generated
// file measures exactly as well.
//
// REQUIRED MUTATION: raise maxScannedFileBytes past the size of the
// generated file — 8 << 20. The first half reds.
//
// Deleting the gate outright does NOT compile, because the stat result
// is then unused; and with the stat kept, the capped reader refuses the
// file anyway and the check records a note. Naming the mutation that was
// actually run matters more than naming the tidiest-sounding one.
func TestLocalhostSkipsAFileTooLargeToBeSource(t *testing.T) {
	root := t.TempDir()
	line := "var endpoint = \"" + devURLNeedle + ":4321\";\n"

	big := []byte(line)
	big = append(big, []byte(strings.Repeat("// filler\n", 250_000))...)
	if int64(len(big)) <= maxScannedFileBytes {
		t.Fatalf("the generated file is %d bytes, which is inside the gate this row "+
			"is about", len(big))
	}
	if err := os.WriteFile(filepath.Join(root, "bundle.js"), big, 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.js"), []byte(line), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	res := runLocalhost(t, OSFileSystem{}, root, []string{"bundle.js"})
	if len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none — a file this size is not hand-written "+
			"source", res.Findings)
	}

	// The control: the same line in a small file IS found, so the
	// silence above is the size and not the content.
	if got := len(runLocalhost(t, OSFileSystem{}, root, []string{"small.js"}).Findings); got != 1 {
		t.Errorf("the same line in a small file produced %d findings, want 1 — the "+
			"row above is not about the size gate", got)
	}
}

// TestLocalhostListsTenHitsAndCountsTheRest holds the cap. A project
// that hard-coded a development URL in two hundred places needs the
// first ten and the number, not two hundred lines of scrollback.
//
// The determinism half is asserted by running twice: two runs over one
// project have to produce the same report, and a scan whose order came
// from a map would satisfy every other assertion here.
//
// REQUIRED MUTATION: raise maxReportedHits to 25, or delete the cap.
// Reds on the count and on the summary line.
func TestLocalhostListsTenHitsAndCountsTheRest(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, "const endpoint%d = \"%s:%d\";\n", i, devURLNeedle, 4000+i)
	}
	if err := os.WriteFile(filepath.Join(root, "config.ts"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	res := runLocalhost(t, OSFileSystem{}, root, []string{"config.ts"})
	if len(res.Findings) != maxReportedHits+1 {
		t.Fatalf("findings = %d, want %d listed hits plus one summary: %+v",
			len(res.Findings), maxReportedHits, res.Findings)
	}
	summary := res.Findings[len(res.Findings)-1]
	if !strings.Contains(summary.Message, "and 15 more") {
		t.Errorf("summary = %q, want it to count the 15 that were not listed",
			summary.Message)
	}
	if len(summary.Paths) != 0 {
		t.Errorf("the summary names paths (%v); it is about a count, not a file",
			summary.Paths)
	}

	second := runLocalhost(t, OSFileSystem{}, root, []string{"config.ts"})
	if !reflect.DeepEqual(res.Findings, second.Findings) {
		t.Errorf("two runs over one project disagreed:\n%+v\n%+v",
			res.Findings, second.Findings)
	}
}

// TestLocalhostRecordsAFileItCouldNotRead keeps "I scanned everything
// and found nothing" apart from "one file defeated me". Silently merging
// them makes a clean verdict partly unearned.
//
// IT IS A NOTE RATHER THAN A WARNING, and the suppression is asserted
// here: an advisory names something the reader can act on or observe,
// and a file that vanished between the walk and the scan is neither. The
// fact is kept for a caller that asks and shown by no surface.
//
// REQUIRED MUTATION: return nil, nil instead of the note in
// readScannable. Reds on the missing row.
func TestLocalhostRecordsAFileItCouldNotRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.ts"),
		[]byte("const a = \""+devURLNeedle+":4321\";\n"), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	res := runLocalhost(t, OSFileSystem{}, root, []string{"real.ts", "vanished.ts"})

	var notes, advisories int
	for _, f := range res.Findings {
		if f.Severity == check.SeverityNote {
			notes++
			if !strings.Contains(f.Message, "vanished.ts") {
				t.Errorf("note = %q, want it to name the file it could not read",
					f.Message)
			}
		}
	}
	advisories = len(check.Advisories(res.Findings))

	if notes != 1 {
		t.Errorf("notes = %d, want one for the file that could not be read: %+v",
			notes, res.Findings)
	}
	if advisories != 1 {
		t.Errorf("advisories = %d, want only the real hit — a note is recorded, "+
			"never raised", advisories)
	}
}

// TestLocalhostQuotesALongLineWithoutBreakingACharacter bounds what a
// matching line can put in a message. A single line can be the whole
// file — that is what minified means — and the bound counts RUNES, so a
// line of accented text is cut where a reader expects rather than in the
// middle of a character. Half a character renders as a replacement
// glyph, which reads as corrupted output rather than as a long line.
//
// THE LENGTH ASSERTION IS THE ONE THAT DISCRIMINATES, and it is here
// because the validity assertion alone did not. Run against a version of
// quoteLine that slices BYTES, this row was green: the fixture's prefix
// happened to be an even number of bytes from the cut, so a byte slice
// landed exactly on a character boundary and produced perfectly valid
// UTF-8. The property "does not break a character" is invisible whenever
// the arithmetic happens to agree, and a fixture cannot be relied on to
// disagree. Counting the runes cannot be satisfied by luck: a byte slice
// of a line with any multi-byte character in it is short by however many
// continuation bytes it swallowed.
//
// The fixture's prefix was then also shifted by one byte so the cut
// falls mid-character, which gives the validity assertion its teeth
// back. Both are kept: one of them is about the bound, the other is
// about the encoding, and only the second says why the bound counts
// runes at all.
//
// REQUIRED MUTATION: slice bytes instead of runes in quoteLine —
// `trimmed[:maxQuotedLineRunes]` with the rune conversion removed. Reds
// on the length, and on the validity check as the fixture now stands.
func TestLocalhostQuotesALongLineWithoutBreakingACharacter(t *testing.T) {
	root := t.TempDir()
	line := "const café = \"" + devURLNeedle + ":4321\";  // " + strings.Repeat("é", 400) + "\n"
	if err := os.WriteFile(filepath.Join(root, "long.ts"), []byte(line), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	res := runLocalhost(t, OSFileSystem{}, root, []string{"long.ts"})
	finding := soleFinding(t, res, check.IDLocalhost)

	const prefix = "long.ts:1: "
	if !strings.HasPrefix(finding.Message, prefix) {
		t.Fatalf("message = %q, want it to start with %q", finding.Message, prefix)
	}
	quoted := strings.TrimPrefix(finding.Message, prefix)

	if !utf8.ValidString(quoted) {
		t.Errorf("the quoted line is not valid UTF-8: %q", quoted)
	}
	if !strings.HasSuffix(quoted, "…") {
		t.Errorf("quoted = %q, want it marked as truncated", quoted)
	}
	if got := utf8.RuneCountInString(quoted); got != maxQuotedLineRunes+1 {
		t.Errorf("the quoted line is %d runes, want %d plus the ellipsis — a count "+
			"short of that is a byte slice wearing a rune bound", got, maxQuotedLineRunes+1)
	}

	// The control: a short line is quoted whole and carries no ellipsis,
	// so the truncation above is the bound doing something.
	if err := os.WriteFile(filepath.Join(root, "short.ts"),
		[]byte("const a = \""+devURLNeedle+":4321\";\n"), 0o644); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	short := soleFinding(t, runLocalhost(t, OSFileSystem{}, root, []string{"short.ts"}),
		check.IDLocalhost)
	if strings.HasSuffix(short.Message, "…") {
		t.Errorf("a short line was truncated: %q", short.Message)
	}
}
