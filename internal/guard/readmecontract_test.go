package guard

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
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

var exitCodeRowPattern = regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|([^|]+)\|\s*$`)

// dispatchExitLiterals reads the exit codes main.go RETURNS, out of its
// own source.
//
// THE NUMBERS USED TO BE TYPED HERE. Three of the five — a plain success,
// a wrong invocation, a stop with copy — are literals rather than named
// constants, so the row that held the README to "the real values" held
// three of them to a list a person maintained in this file. Changing one
// in the dispatch moved the program and nothing went red: the table and
// the test agreed with each other, and both had stopped agreeing with the
// binary.
//
// EVERY RETURNED INTEGER LITERAL IN THAT FILE IS AN EXIT CODE, which is
// what makes the walk this simple and is a property of the file rather
// than an assumption about it: that package is dispatch and holds no
// logic, so an integer it returns is a code the process exits with.
func dispatchExitLiterals(t *testing.T, path string) map[int]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	out := map[int]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, result := range ret.Results {
			lit, ok := result.(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				continue
			}
			code, err := strconv.Atoi(lit.Value)
			if err != nil {
				continue
			}
			out[code] = true
		}
		return true
	})
	if len(out) == 0 {
		t.Fatalf("%s returns no integer literal at all, so this row read nothing and the "+
			"table below would be held to the two named constants alone", path)
	}
	return out
}

// exitMeaningStopWords are the words that carry no evidence of a binding.
//
// THEY ARE FUNCTION WORDS AND ONE CONTENT WORD, and the content word is
// the reason this list exists at all rather than a distinctiveness
// calculation. "run" appears in one constant's documentation and in BOTH
// of the sentences the table writes, because both of these codes are
// things that happen to a run — so left in, it binds each cell to the
// wrong constant as readily as to the right one, which is precisely the
// confusion the row is built to detect.
var exitMeaningStopWords = map[string]bool{
	"and": true, "are": true, "the": true, "that": true, "this": true, "with": true,
	"for": true, "from": true, "was": true, "were": true, "has": true, "have": true,
	"not": true, "but": true, "you": true, "your": true, "its": true, "it": true,
	"what": true, "which": true, "when": true, "where": true, "how": true, "why": true,
	"every": true, "all": true, "any": true, "some": true, "each": true, "other": true,
	"rather": true, "because": true, "while": true, "into": true, "out": true,
	"only": true, "also": true, "both": true, "than": true, "then": true, "there": true,
	"run": true, "number": true, "code": true, "exit": true, "reports": true,
}

// meaningWords is the content vocabulary of a sentence.
func meaningWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r < 'a' || r > 'z'
	}) {
		if len(word) > 2 && !exitMeaningStopWords[word] {
			out[word] = true
		}
	}
	return out
}

// documentedConstantWords is the vocabulary of the doc comments naming a
// constant, read out of the source rather than restated here.
//
// IT READS MORE THAN ONE NAME PER CODE, and that is forced by where the
// meaning actually lives. A number's own doc comment says what the
// process reports; what the number MEANS to a person — the door being
// shut — is documented on the sentinel beside it, which that comment
// names as the scope behind the number. Reading only the constant leaves
// this row comparing a first-timer's sentence against a maintainer's, and
// those two share no content word at all. Measured before it was written:
// against the constant alone the overlap is empty, and the row would have
// been unbuildable rather than merely weak.
func documentedConstantWords(t *testing.T, root string, names map[string][]string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for file, wanted := range names {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(file)), nil,
			parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		found := map[string]bool{}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) == 0 {
					continue
				}
				for _, want := range wanted {
					if vs.Names[0].Name != want {
						continue
					}
					found[want] = true
					// A single-spec declaration carries its doc on the
					// declaration; one inside a parenthesised group carries
					// it on the spec. Both spellings are in this tree.
					for _, doc := range []*ast.CommentGroup{vs.Doc, gen.Doc} {
						if doc == nil {
							continue
						}
						for word := range meaningWords(doc.Text()) {
							out[word] = true
						}
					}
				}
			}
		}
		for _, want := range wanted {
			if !found[want] {
				t.Fatalf("%s declares no constant or variable named %s, so this row read no "+
					"documentation for it", file, want)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("the named declarations carry no documentation, so the comparison below " +
			"would pass against an empty vocabulary")
	}
	return out
}

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
	meaning := map[int]string{}
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("exit-code table row %q does not parse: %v", m[0], err)
		}
		if _, seen := meaning[n]; seen {
			t.Errorf("exit-code table names %d more than once", n)
		}
		meaning[n] = strings.TrimSpace(m[2])
	}

	interruptCode := unexportedIntConst(t, filepath.Join(root, "internal", "ui", "interrupt.go"),
		"interruptExitCode")
	want := dispatchExitLiterals(t, filepath.Join(root, "cmd", "curious", "main.go"))
	want[ui.ExitServerClosed] = true
	want[interruptCode] = true

	for n := range want {
		if _, named := meaning[n]; !named {
			t.Errorf("README's exit-code table does not name %d", n)
		}
	}
	for n := range meaning {
		if !want[n] {
			t.Errorf("README's exit-code table names %d, which the dispatch never returns and "+
				"which is neither ui.ExitServerClosed (%d) nor interruptExitCode (%d)",
				n, ui.ExitServerClosed, interruptCode)
		}
	}

	// THE MEANING COLUMN, AND NOT ONLY THE NUMBERS. A table naming exactly
	// the right set of codes says nothing about which sentence sits beside
	// which one, and the two that have a constant behind them are the two a
	// reader is least able to check for themselves: a script author reading
	// "3" wants to know it is the closed door rather than the interrupt.
	// Swapping those two cells left every assertion above satisfied.
	//
	// The comparison is against the documentation those constants carry,
	// so the table is held to the program rather than to a second sentence
	// in this file. Sharing a word with your own is what makes the binding
	// evidence; sharing one with the OTHER — and not with your own — is
	// what a swap looks like from here.
	docs := map[int]map[string]bool{
		ui.ExitServerClosed: documentedConstantWords(t, root, map[string][]string{
			"internal/ui/errors.go": {"ExitServerClosed", "ErrServerClosed"},
		}),
		interruptCode: documentedConstantWords(t, root, map[string][]string{
			"internal/ui/interrupt.go": {"interruptExitCode"},
		}),
	}
	for code, own := range docs {
		cell, named := meaning[code]
		if !named {
			continue // already reported above
		}
		words := meaningWords(cell)
		shared := false
		for word := range words {
			if own[word] {
				shared = true
			}
		}
		if !shared {
			t.Errorf("the table's meaning for %d shares no word with the documentation of the "+
				"constant behind it: %q\nThe column is what a reader acts on, and nothing tied "+
				"it to the program. If the wording is right, the documentation is where the "+
				"meaning is stated and the two should agree somewhere.", code, cell)
		}
		for other, otherDocs := range docs {
			if other == code {
				continue
			}
			for word := range words {
				if otherDocs[word] && !own[word] {
					t.Errorf("the table's meaning for %d uses %q, which belongs to the "+
						"documentation of %d and not to its own: %q\nThese two rows have most "+
						"likely exchanged their sentences, which every check on the numbers "+
						"alone is satisfied by.", code, word, other, cell)
				}
			}
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

// htmlComment matches a comment in the markdown source, which a reader
// never sees.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// renderedProse is what a person actually reads: the source with every
// HTML comment removed and the whitespace collapsed.
//
// A COMMENT IS NOT A DISCLAIMER. The sentence this row requires is there
// to be READ, by somebody deciding whether they can install this today,
// and markdown renders a comment to nothing at all. Searching the raw
// source is satisfied by a sentence nobody can see — which is the same
// class of defect as a check satisfied by an empty set, arriving through
// the one row whose whole subject is what the page tells a stranger.
func renderedProse(markdown string) string {
	return normalizeSpace(htmlComment.ReplaceAllString(markdown, " "))
}

// readmeTopSection is the title block and the first section under it.
//
// THE PLACE MATTERS AND NOT ONLY THE PRESENCE. A reader deciding whether
// this is installable today decides it in the first screen; the same
// sentence four sections down is true and arrives after the decision.
func readmeTopSection(readme string) string {
	const heading = "\n## "
	first := strings.Index(readme, heading)
	if first < 0 {
		return readme
	}
	rest := first + len(heading)
	next := strings.Index(readme[rest:], heading)
	if next < 0 {
		return readme
	}
	return readme[:rest+next]
}

// reachableReleaseTags is every tag this checkout can reach from HEAD.
//
// IT REPLACED A STAND-IN. The row used to key on the wrapper's checksum
// file still being an empty object, which is a FILE THIS REPOSITORY
// WRITES rather than evidence about what has been released: it says a
// release has not been built here, not that none exists, and somebody
// filling it in by hand would have retired the row without publishing
// anything. A tag reachable from the commit under test is the thing the
// sentence is actually about.
func reachableReleaseTags(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "tag", "--merged", "HEAD")
	cmd.Dir = root
	cmd.Env = gitSafeEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("asking git which tags this commit reaches: %v", err)
	}
	var tags []string
	for _, line := range strings.Split(string(out), "\n") {
		if tag := strings.TrimSpace(line); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// TestReadmePublicationStateRow holds the README to the one-directional
// rule: with no release tag reachable from this commit, the page must
// say so where a reader will meet it, and must describe installing in
// the future tense. The row says nothing about the other direction — a
// tagged tree may drop either — because nothing requires present tense
// once a release exists. An earlier version asserted both directions at
// once; the converse was never true, since a tree that has a tag is free
// to go on saying what it said before one existed.
func TestReadmePublicationStateRow(t *testing.T) {
	root := moduleRoot(t)
	if tags := reachableReleaseTags(t, root); len(tags) > 0 {
		t.Skipf("this commit reaches %d release tag(s) (%s), and the publication-state rule "+
			"requires nothing of the README once a release exists",
			len(tags), strings.Join(tags, ", "))
	}

	readme := readReadmeFile(t, root)
	if top := renderedProse(readmeTopSection(readme)); !strings.Contains(top, noReleaseDisclaimer) {
		t.Errorf("no release tag is reachable from this commit, and the README's opening does "+
			"not carry the sentence %q as prose a reader sees.\nIt is the first thing somebody "+
			"deciding whether they can install this today needs to know, so it belongs above "+
			"the fold rather than in the section they reach afterwards — and a sentence inside "+
			"an HTML comment is rendered to nothing.", noReleaseDisclaimer)
	}
	if body := renderedProse(readme); !strings.Contains(body, futureTenseInstall) {
		t.Errorf("no release tag is reachable from this commit, and README.md does not carry "+
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
// The composed entries say what their message NAMES.
// ---------------------------------------------------------------------

// composedDisclaimerMarker is the phrase every composed entry's shared
// line carries. It is matched rather than quoted whole so that rewording
// the disclaimer is an ordinary copy change; what this row is about is
// whether anything stands BESIDE it.
const composedDisclaimerMarker = "assembled at run time"

// readmeTroubleshootingEntries splits the failure-headlines block into
// one body per id, keyed by the id its heading declares.
//
// Scoped to the marked block for the reason readmeTroubleshootingIDs is:
// a level-3 heading used for prose elsewhere in the README — the note
// about composed messages, which sits deliberately outside the block — is
// not an entry and must not be read as one.
func readmeTroubleshootingEntries(t *testing.T, readme string) map[string][]string {
	t.Helper()
	begin := strings.Index(readme, readmeHeadlinesBegin)
	if begin < 0 {
		t.Fatal("README.md carries no failure-headlines block, so it declares no entry")
	}
	end := strings.Index(readme[begin:], readmeHeadlinesEnd)
	if end < 0 {
		t.Fatalf("README.md opens a %s block and never closes it", readmeHeadlinesBegin)
	}

	entries := map[string][]string{}
	current := ""
	for _, line := range strings.Split(readme[begin:begin+end], "\n") {
		if m := readmeTroubleshootingHeadingPattern.FindStringSubmatch(line); m != nil {
			current = m[1]
			entries[current] = nil
			continue
		}
		if current != "" {
			entries[current] = append(entries[current], line)
		}
	}
	return entries
}

// shapeProse is the lines of an entry that describe what its message
// names: everything that is not the stage line, the disclaimer, or a
// quoted headline.
func shapeProse(body []string) []string {
	var out []string
	for _, line := range body {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "**"): // the stage and its sentence
		case strings.HasPrefix(trimmed, ">"): // a quoted headline
		case strings.Contains(trimmed, composedDisclaimerMarker):
		default:
			out = append(out, trimmed)
		}
	}
	return out
}

// TestEveryComposedEntryDescribesWhatItsMessageNames holds the half of
// the troubleshooting section the catalog cannot fill in.
//
// A composed What has no fixed sentence, so there is nothing to quote and
// the block quotes nothing for it. That left eighteen entries saying only
// that they had nothing to say, which tells a reader with a failure id in
// front of them precisely nothing about the message they are looking at.
//
// WHAT IS REQUIRED IS A DESCRIPTION, NEVER A TEMPLATE. The shapes are
// readable at the sites that build them — the host an upload was going
// to, the limit a project broke and the files that broke it, the path
// that cannot be published — and prose describing those is a claim about
// the program that stays true when the wording moves. A hand-typed
// rendering would be wording nothing checks, which is the exact fragility
// the catalog exists to end, arriving through the section written to
// explain it. So the marking a quoted headline uses for an interpolated
// value is refused here as well.
func TestEveryComposedEntryDescribesWhatItsMessageNames(t *testing.T) {
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
	entries := readmeTroubleshootingEntries(t, readme)

	composed := 0
	for _, f := range catalog.Failures {
		hasComposed := false
		for _, headline := range f.Headline {
			if headline == nil {
				hasComposed = true
			}
		}
		if !hasComposed {
			continue
		}
		composed++

		body, declared := entries[f.ID]
		if !declared {
			t.Errorf("catalog.json composes %s at run time and README.md declares no "+
				"troubleshooting entry for it", f.ID)
			continue
		}
		prose := shapeProse(body)
		if len(prose) == 0 {
			t.Errorf("%s composes its message at run time and its README entry carries the "+
				"disclaimer and nothing else.\nAn entry that says only that it has no sentence "+
				"to quote tells a reader holding that failure id nothing at all. Say what the "+
				"message NAMES — the address, the limit and the files that broke it, the path "+
				"— which is readable at the site that builds it and stays true when the "+
				"wording moves.", f.ID)
			continue
		}
		for _, line := range prose {
			if strings.Contains(line, "<…>") {
				t.Errorf("%s's README entry carries the marking a quoted headline uses for an "+
					"interpolated value: %q\nThese entries describe what a message names; they "+
					"do not template it. A hand-typed rendering is wording nothing checks.",
					f.ID, line)
			}
		}
	}

	if composed == 0 {
		t.Fatal("catalog.json carries no composed headline, so this row compared nothing")
	}
	if !t.Failed() {
		t.Logf("%d composed ids, each describing what its message names", composed)
	}
}

// ---------------------------------------------------------------------
// The block as TRIPLES, not as three global sets.
// ---------------------------------------------------------------------

// readmeTriple is one thing the block states: this id, at this stage,
// quotes this headline.
type readmeTriple struct {
	id       string
	stage    string
	headline string
}

// readmeStageHeading matches the bold stage line that opens each of an
// entry's stages, capturing the stage's own value.
var readmeStageHeading = regexp.MustCompile(`^\*\*(.+?)\.\*\*`)

// readmeHeadlineTriples reads the block as what it actually claims.
//
// THE TWO ROWS EITHER SIDE OF THIS ONE COMPARE GLOBAL SETS, and a global
// set cannot see the arrangement. Every headline the catalog carries
// appears somewhere in the block, and every line the block quotes is a
// headline the catalog carries — both true, and both still true after two
// ids have swapped bodies entirely. What a reader uses the section for is
// the pairing, and the pairing was the one thing nothing checked.
//
// A headline is filed under (id, Stage), so that is the unit read here:
// the id from its heading, the stage from the bold line above the
// quotation, and the quotation itself.
func readmeHeadlineTriples(t *testing.T, readme string) []readmeTriple {
	t.Helper()
	begin := strings.Index(readme, readmeHeadlinesBegin)
	if begin < 0 {
		t.Fatal("README.md carries no failure-headlines block")
	}
	end := strings.Index(readme[begin:], readmeHeadlinesEnd)
	if end < 0 {
		t.Fatalf("README.md opens a %s block and never closes it", readmeHeadlinesBegin)
	}

	var out []readmeTriple
	var id, stage string
	for _, line := range strings.Split(readme[begin:begin+end], "\n") {
		trimmed := strings.TrimSpace(line)
		if m := readmeTroubleshootingHeadingPattern.FindStringSubmatch(line); m != nil {
			id, stage = m[1], ""
			continue
		}
		if m := readmeStageHeading.FindStringSubmatch(trimmed); m != nil {
			stage = m[1]
			continue
		}
		if text, ok := strings.CutPrefix(trimmed, "> "); ok {
			out = append(out, readmeTriple{id: id, stage: stage,
				headline: strings.TrimSpace(text)})
		}
	}
	return out
}

// catalogHeadlineTriples is the same claim, from the artefact the block
// is quoting.
func catalogHeadlineTriples(catalog publicCatalog) []readmeTriple {
	var out []readmeTriple
	for _, f := range catalog.Failures {
		for stage, headline := range f.Headline {
			if headline == nil {
				continue
			}
			out = append(out, readmeTriple{id: f.ID, stage: stage,
				headline: renderedHeadline(*headline)})
		}
	}
	return out
}

// TestTheReadmeBlockAndTheCatalogAgreeTripleForTriple compares the two
// sets of triples in both directions.
//
// IT IS ONE COMPARISON RATHER THAN THREE, and that is the point. Holding
// the ids, the stages and the headlines to the catalog as three separate
// sets is satisfied by any arrangement that uses each of them the right
// number of times — including one where two entries have exchanged their
// contents, and one where a stage's line is deleted while an identical
// line under some other id keeps the global set complete.
func TestTheReadmeBlockAndTheCatalogAgreeTripleForTriple(t *testing.T) {
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

	inReadme := map[readmeTriple]int{}
	for _, tr := range readmeHeadlineTriples(t, readme) {
		inReadme[tr]++
	}
	inCatalog := map[readmeTriple]int{}
	for _, tr := range catalogHeadlineTriples(catalog) {
		inCatalog[tr]++
	}
	if len(inCatalog) == 0 {
		t.Fatal("catalog.json carries no quotable headline, so this row compared nothing")
	}
	if len(inReadme) == 0 {
		t.Fatal("README.md's block quotes no headline, so this row compared nothing")
	}

	for tr, n := range inCatalog {
		switch got := inReadme[tr]; {
		case got == 0:
			t.Errorf("the catalog files %q at stage %q under the headline %q, and README.md "+
				"does not quote that line under that id and stage.\nA headline belongs to an "+
				"(id, stage) pair; quoting it somewhere else in the block is not the same "+
				"claim.", tr.id, tr.stage, tr.headline)
		case got != n:
			t.Errorf("README.md quotes %q under %q/%q %d time(s) and the catalog files it %d",
				tr.headline, tr.id, tr.stage, got, n)
		}
	}
	for tr := range inReadme {
		if inCatalog[tr] == 0 {
			t.Errorf("README.md quotes %q under the id %q at stage %q, and the catalog files "+
				"no such headline there.\nEither the line is wording typed by hand, or it "+
				"belongs to another entry and has been filed under this one.",
				tr.headline, tr.id, tr.stage)
		}
	}
	if !t.Failed() {
		t.Logf("%d (id, stage, headline) triples, agreeing in both directions", len(inCatalog))
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
