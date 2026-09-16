package guard

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

// THE README, HELD TO FOUR MORE PIECES OF DATA it does not otherwise carry
// a live source for: the exit codes, whether a release tag is reachable,
// the full set of catalog headlines (the direction
// TestNoReadmeHeadlineIsOneTheCatalogDoesNotCarry does not check), and the
// catalog's own id set. Each row reads the artefact it is about rather
// than a second, hand-typed copy of it.

func readReadmeFile(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	return string(data)
}

// ---------------------------------------------------------------------
// The exit-code table.
// ---------------------------------------------------------------------

// unexportedIntConst reads a single, unexported, untyped integer constant
// straight out of a file's own source, the way the catalog guard already
// reaches unexported values it cannot import: by parsing rather than
// linking. interruptExitCode is deliberately unexported (see its own
// comment), so this is the only way to hold the README's table to the
// real number without exporting a name that exists to be unexported.
func unexportedIntConst(t *testing.T, path, name string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 || vs.Names[0].Name != name {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				continue
			}
			n, err := strconv.Atoi(lit.Value)
			if err != nil {
				t.Fatalf("%s: %s does not parse as an integer literal: %v", path, name, err)
			}
			return n
		}
	}
	t.Fatalf("%s: no integer constant named %s found", path, name)
	return 0
}

var exitCodeRowPattern = regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|[^|]+\|\s*$`)

// TestReadmeExitCodeTableMatchesConstants holds the README's exit-code
// table to the real values AS DATA: ui.ExitServerClosed, the unexported
// interruptExitCode read out of its own source, and the two literal
// returns (0 and 2) main.go uses for a plain success and a wrong
// invocation. It does not re-prove what the binary returns for each —
// three behavioural rows already do that — it proves the table names
// exactly this set of numbers and none other.
func TestReadmeExitCodeTableMatchesConstants(t *testing.T) {
	root := moduleRoot(t)
	readme := readReadmeFile(t, root)

	matches := exitCodeRowPattern.FindAllStringSubmatch(readme, -1)
	if len(matches) == 0 {
		t.Fatal("README.md carries no exit-code table row in the `| <code> | ... |` shape, " +
			"so this row compared nothing")
	}
	got := map[int]bool{}
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("exit-code table row %q does not parse: %v", m[0], err)
		}
		if got[n] {
			t.Errorf("exit-code table names %d more than once", n)
		}
		got[n] = true
	}

	interruptCode := unexportedIntConst(t, filepath.Join(root, "internal", "ui", "interrupt.go"), "interruptExitCode")
	want := map[int]bool{0: true, 1: true, 2: true, ui.ExitServerClosed: true, interruptCode: true}

	for n := range want {
		if !got[n] {
			t.Errorf("README's exit-code table does not name %d", n)
		}
	}
	for n := range got {
		if !want[n] {
			t.Errorf("README's exit-code table names %d, which is none of 0, 1, 2, "+
				"ui.ExitServerClosed (%d) or interruptExitCode (%d)", n, ui.ExitServerClosed, interruptCode)
		}
	}
}

// ---------------------------------------------------------------------
// The publication-state row.
// ---------------------------------------------------------------------

// These two sentences are the checkable half of the one-directional
// rule: present-tense install copy, and the absence of the
// disclaimer, both require a reachable release tag. The row does not
// parse tense generally — that is not a mechanical question — it holds
// the README to carrying THESE TWO SENTENCES, verbatim, whenever the
// checkable proxy for "no reachable tag" holds. Written here rather than
// inferred from the README so a change to either sentence is a decision
// made in this file, not a silent pass produced by loosening the row.
const (
	noReleaseDisclaimer = "Nothing is published yet."
	futureTenseInstall  = "the commands below are what installing will look like rather than what works today"
)

// TestReadmePublicationStateRow holds the README to the one-directional
// row: when npm/checksums.json is the placeholder {}, present-tense
// install copy and the absent disclaimer both require a reachable release
// tag that this tree does not have, so the disclaimer and the
// future-tense framing must both be present. The row says nothing about
// the other direction — a tagged tree may drop either — because nothing
// requires present tense once a tag exists. An earlier version of this
// row asserted both directions at once; the converse was never true,
// since a tree that has a tag is free to go on saying what it said
// before one existed.
func TestReadmePublicationStateRow(t *testing.T) {
	root := moduleRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "npm", "checksums.json"))
	if err != nil {
		t.Fatalf("reading npm/checksums.json: %v", err)
	}
	var checksums map[string]any
	if err := json.Unmarshal(raw, &checksums); err != nil {
		t.Fatalf("npm/checksums.json does not decode as an object: %v", err)
	}
	if len(checksums) != 0 {
		// The checkable proxy says a release has happened. Nothing is
		// required of the README in that direction, so this row has
		// nothing to check — the day this tree carries real checksums is
		// the day a person decides what the README now says.
		t.Skip("npm/checksums.json is no longer the {} placeholder; the publication-state row " +
			"requires nothing of the README once a release exists")
	}

	readme := normalizeSpace(readReadmeFile(t, root))
	if !strings.Contains(readme, noReleaseDisclaimer) {
		t.Errorf("npm/checksums.json is {} (no reachable release), and README.md does not carry "+
			"the disclaimer sentence %q", noReleaseDisclaimer)
	}
	if !strings.Contains(readme, futureTenseInstall) {
		t.Errorf("npm/checksums.json is {} (no reachable release), and README.md does not carry "+
			"the future-tense framing %q", futureTenseInstall)
	}
}

// normalizeSpace collapses every run of whitespace — including the line
// break a hard-wrapped paragraph carries in its own source — to one
// space, so a sentence this file asserts verbatim can be found regardless
// of where the prose around it happens to wrap. Markdown treats a single
// line break inside a paragraph as a space; this check must too, or every
// wrap-width edit to an unrelated sentence becomes a reason to rewrite
// the marker it happens to fall across.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ---------------------------------------------------------------------
// The other half of the headline-equality row.
// ---------------------------------------------------------------------

// TestEveryNonNullCatalogHeadlineIsQuotedInReadme is the direction
// TestNoReadmeHeadlineIsOneTheCatalogDoesNotCarry does not check: that one
// holds every README headline to a catalog headline; this one holds every
// catalog headline the catalog is entitled to quote (every non-null one)
// to a line inside the README's block. Together the two make the block's
// quoted set and the catalog's non-null-headline set the same set.
//
// It does not touch readmeHeadlineLines, renderedHeadline or the block
// markers — it reads them, unchanged, from failureheadline_test.go.
func TestEveryNonNullCatalogHeadlineIsQuotedInReadme(t *testing.T) {
	root := moduleRoot(t)
	readme := readReadmeFile(t, root)
	raw, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatalf("reading catalog.json: %v", err)
	}
	var catalog publicCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("catalog.json does not decode as the published shape: %v", err)
	}
	lines, marked, err := readmeHeadlineLines(readme)
	if err != nil {
		t.Fatalf("README.md %v", err)
	}
	if !marked {
		t.Fatal("README.md carries no failure-headlines block, so no catalog headline can be quoted in it")
	}
	quoted := map[string]bool{}
	for _, line := range lines {
		quoted[line] = true
	}

	total := 0
	for _, f := range catalog.Failures {
		for stage, headline := range f.Headline {
			if headline == nil {
				continue
			}
			total++
			rendered := renderedHeadline(*headline)
			if !quoted[rendered] {
				t.Errorf("catalog.json's %s at stage %q has the headline %q, and README.md's "+
					"failure-headlines block quotes no such line", f.ID, stage, rendered)
			}
		}
	}
	if total == 0 {
		t.Fatal("catalog.json carries no non-null headline, so this row compared nothing")
	}
}

// TestNonNullCatalogHeadlineMissingFromReadmeReds is the named fixture:
// a catalog headline with no matching README line is exactly what the row
// above must catch, and a catalog headline the README does carry must not
// trip it.
func TestNonNullCatalogHeadlineMissingFromReadmeReds(t *testing.T) {
	format := "curious.pub is not taking %s right now."
	entries := []publicCatalogEntry{{ID: "fixture",
		Headline: map[string]*string{"deploys": &format}}}
	rendered := renderedHeadline(format)

	readmeMissing := "Some prose.\n\n" + readmeHeadlinesBegin + "\n" +
		"> a headline the catalog does not carry at all.\n" + readmeHeadlinesEnd + "\n"
	if quotesAll(t, readmeMissing, entries) {
		t.Fatal("a README with none of the catalog's headline was reported as quoting all of it")
	}

	readmePresent := "Some prose.\n\n" + readmeHeadlinesBegin + "\n" +
		"> " + rendered + "\n" + readmeHeadlinesEnd + "\n"
	if !quotesAll(t, readmePresent, entries) {
		t.Fatal("a README quoting the catalog's one headline was reported as missing it")
	}
}

// quotesAll is the property TestEveryNonNullCatalogHeadlineIsQuotedInReadme
// asserts of the real tree, extracted so the fixture above can drive it
// against values it controls instead of against files on disk.
func quotesAll(t *testing.T, readme string, entries []publicCatalogEntry) bool {
	t.Helper()
	lines, marked, err := readmeHeadlineLines(readme)
	if err != nil || !marked {
		t.Fatalf("fixture README did not parse: marked=%v err=%v", marked, err)
	}
	quoted := map[string]bool{}
	for _, line := range lines {
		quoted[line] = true
	}
	for _, f := range entries {
		for _, headline := range f.Headline {
			if headline == nil {
				continue
			}
			if !quoted[renderedHeadline(*headline)] {
				return false
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------
// The troubleshooting entry set.
// ---------------------------------------------------------------------

var readmeTroubleshootingHeadingPattern = regexp.MustCompile(`(?m)^### ([a-z0-9][a-z0-9-]*)\s*$`)

// readmeTroubleshootingIDs is every id the README's failure-headlines
// block declares a "### <id>" heading for, in the order they appear.
// Scoped to the marked block rather than to the whole document, or to the
// whole "## Troubleshooting" section, so a level-3 heading used for prose
// elsewhere in the README (the note about composed messages, which is
// deliberately outside the block) is never mistaken for a 58th id.
func readmeTroubleshootingIDs(t *testing.T, readme string) []string {
	t.Helper()
	begin := strings.Index(readme, readmeHeadlinesBegin)
	if begin < 0 {
		t.Fatal("README.md carries no failure-headlines block, so it declares no troubleshooting id")
	}
	end := strings.Index(readme[begin:], readmeHeadlinesEnd)
	if end < 0 {
		t.Fatalf("README.md opens a %s block and never closes it", readmeHeadlinesBegin)
	}
	block := readme[begin : begin+end]
	var out []string
	for _, m := range readmeTroubleshootingHeadingPattern.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestReadmeTroubleshootingIDSetMatchesCatalog holds the README's set of
// "### <id>" headings inside the failure-headlines block to
// ui.ActiveFailureIDs, both directions: a missing id and an orphan
// heading both fail, and a duplicate heading fails too, because a
// duplicated id is not a smaller version of a missing one.
func TestReadmeTroubleshootingIDSetMatchesCatalog(t *testing.T) {
	root := moduleRoot(t)
	readme := readReadmeFile(t, root)
	headings := readmeTroubleshootingIDs(t, readme)
	if len(headings) == 0 {
		t.Fatal("README.md's failure-headlines block declares no id heading, so this row compared nothing")
	}

	seen := map[string]int{}
	for _, id := range headings {
		seen[id]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("README.md's failure-headlines block declares %q %d times", id, n)
		}
	}

	want := map[string]bool{}
	for _, id := range ui.ActiveFailureIDs {
		want[string(id)] = true
	}
	for id := range want {
		if seen[id] == 0 {
			t.Errorf("ui.ActiveFailureIDs contains %q, and README.md declares no troubleshooting entry for it", id)
		}
	}
	for id := range seen {
		if !want[id] {
			t.Errorf("README.md declares a troubleshooting entry for %q, which is not in ui.ActiveFailureIDs", id)
		}
	}
}

// ---------------------------------------------------------------------
// The spelling rule's new home.
// ---------------------------------------------------------------------

// spellingRuleSentence is the public-safe sentence this change moves
// into cli/CLAUDE.md, asserted verbatim rather than by a looser "mentions
// spelling" match — a paraphrase living in this test file and a
// paraphrase living in CLAUDE.md could each change without the other
// noticing.
const spellingRuleSentence = "One spelling per file: pick British or American and do not churn."

// TestSpellingRuleIsPresentInCLAUDEMd holds this change's own deliverable
// to account: a rule that governs the copy in this repository is written
// down IN this repository, in public-safe wording, in the file a person
// working here can actually open rather than somewhere they cannot.
func TestSpellingRuleIsPresentInCLAUDEMd(t *testing.T) {
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if !strings.Contains(normalizeSpace(string(data)), spellingRuleSentence) {
		t.Errorf("cli/CLAUDE.md does not carry the spelling rule %q", spellingRuleSentence)
	}
}
