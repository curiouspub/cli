package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// THE HEADLINE ROWS. A headline is filed under (id, Stage): the id names
// the cause, and the stage is what the person was doing when it met them,
// declared by the site that builds the failure. These rows hold the two
// halves of that key, the one-What-per-key rule that makes a headline
// quotable, and the README's side of the bargain.

// TestEveryFailureSiteDeclaresAStage holds the key's second half at every
// construction.
//
// The compiler already makes every call to a constructor pass a stage. It
// does not make that stage a declared one, and it says nothing about a
// composite literal, so this row reads what each site actually passes.
func TestEveryFailureSiteDeclaresAStage(t *testing.T) {
	obs := contractObligations(t)
	constructed := map[string]bool{}
	stages := map[string][]obligation{}
	for _, o := range obs {
		if o.problem != "" {
			continue
		}
		switch o.field {
		case "ID":
			constructed[o.where] = true
		case "Stage":
			stages[o.where] = append(stages[o.where], o)
		}
	}
	if len(constructed) == 0 {
		t.Fatal("the checker reported no construction site, so this row compared nothing")
	}
	for _, where := range headlineKeys(constructed) {
		declared := stages[where]
		if len(declared) == 0 {
			t.Errorf("%s constructs a failure and declares no Stage.\n"+
				"The stage is half of the key a headline is filed under, and a site that does "+
				"not say where the person was cannot be filed at all.", where)
			continue
		}
		for _, o := range declared {
			if len(o.values) != 1 || !declaredStage(o.values[0]) {
				t.Errorf("%s declares its stage as %q, which is not a declared ui.Stage constant.\n"+
					"A stage is something the site says in the vocabulary internal/ui publishes; "+
					"a conversion or a variable that happens to spell one is not.", where, o.expr)
				break
			}
		}
	}
	if !t.Failed() {
		t.Logf("%d construction sites, every one declaring a Stage", len(constructed))
	}
}

// TestOneWhatPerIdAndStage is the rule that makes a headline quotable, on
// the live tree.
//
// Two sites sharing an id and a stage and writing different sentences
// leave the catalog nothing to quote. The row names every site of every
// disagreement, because the fix needs the pair: either one site declares
// the wrong stage, or one sentence changed. NO COPY MOVES to make it pass —
// which sentence is right is a product decision, recorded as one.
func TestOneWhatPerIdAndStage(t *testing.T) {
	census := liveCatalogCensus(t)
	for _, problem := range headlineDisagreements(census.whats) {
		t.Error(problem)
	}
	pairs := 0
	for _, stages := range census.whats {
		pairs += len(stages)
	}
	if pairs == 0 {
		t.Fatal("the census holds no (id, Stage) pair, so this row compared nothing")
	}
	if !t.Failed() {
		t.Logf("%d (id, Stage) pairs, one What each", pairs)
	}
}

// TestTwoSitesSharingAnIdAndStageWithDifferentWhatsRedNamingBoth is the
// named fixture for the row above: the disagreement it exists to catch,
// and the two agreements it must not.
func TestTwoSitesSharingAnIdAndStageWithDifferentWhatsRedNamingBoth(t *testing.T) {
	disagree := map[string]map[string]map[string]map[string]bool{
		"rate-limited": {"deploys": {
			"literal:Too many requests from here.":      {"internal/flow/create.go:231": true},
			"literal:The server is asking for a pause.": {"internal/flow/publish.go:501": true},
		}},
	}
	problems := headlineDisagreements(disagree)
	if len(problems) != 1 {
		t.Fatalf("one disagreement produced %d findings, want 1: %v", len(problems), problems)
	}
	for _, site := range []string{"internal/flow/create.go:231", "internal/flow/publish.go:501"} {
		if !strings.Contains(problems[0], site) {
			t.Errorf("the finding does not name %s, and the fix needs both sites:\n%s", site, problems[0])
		}
	}

	// CONTROLS. The same sentence at two sites is agreement, and different
	// sentences at different stages are what the key exists to allow.
	agree := map[string]map[string]map[string]map[string]bool{
		"service-unavailable": {"deploys": {
			"literal:curious.pub is not taking deploys right now.": {
				"internal/flow/capacity.go:160": true, "internal/flow/create.go:214": true},
		}},
	}
	if got := headlineDisagreements(agree); len(got) != 0 {
		t.Errorf("one sentence written at two sites was reported as a disagreement: %v", got)
	}
	acrossStages := map[string]map[string]map[string]map[string]bool{
		"rate-limited": {
			"deploys":   {"literal:Too many requests from here.": {"internal/flow/create.go:231": true}},
			"addresses": {"literal:The server is asking for a pause.": {"internal/flow/publish.go:501": true}},
		},
	}
	if got := headlineDisagreements(acrossStages); len(got) != 0 {
		t.Errorf("different sentences at different stages were reported as a disagreement: %v", got)
	}
}

// TestTheFiveIdsWhoseSentencesVaryPassUnderTheirDeclaredStages is the
// row that would have caught both earlier rules: "same id, same What"
// reds on every one of these, and a stage taken from the package directory
// put five of one id's six sentences under one stage.
//
// It also refuses to go on passing when an id STOPS varying, because then
// it would no longer be testing the key at all.
func TestTheFiveIdsWhoseSentencesVaryPassUnderTheirDeclaredStages(t *testing.T) {
	census := liveCatalogCensus(t)
	for _, id := range []string{"service-unavailable", "server-answer-unrecognised",
		"server-unanswered", "rate-limited", "client-request-rejected"} {
		stages := census.whats[id]
		if len(stages) < 2 {
			t.Errorf("%s is declared at %d stage(s); this row is about the ids whose sentence "+
				"varies by stage, and it no longer measures this one", id, len(stages))
			continue
		}
		distinct := map[string]bool{}
		for _, values := range stages {
			for value := range values {
				distinct[value] = true
			}
		}
		if len(distinct) < 2 {
			t.Errorf("%s writes one sentence at every stage, so it no longer varies and this row "+
				"no longer tests the key", id)
		}
		for _, problem := range headlineDisagreements(
			map[string]map[string]map[string]map[string]bool{id: stages}) {
			t.Error(problem)
		}
		t.Logf("%s: %d stages, %d sentences", id, len(stages), len(distinct))
	}
}

// headlineDisagreements names every (id, Stage) written with more than one
// What, with the sites writing each.
func headlineDisagreements(whats map[string]map[string]map[string]map[string]bool) []string {
	var out []string
	for _, id := range headlineKeys(whats) {
		for _, stage := range headlineKeys(whats[id]) {
			values := whats[id][stage]
			if len(values) < 2 {
				continue
			}
			var described []string
			for _, value := range headlineKeys(values) {
				described = append(described, fmt.Sprintf("  %s at %s",
					describedWhat(value), strings.Join(headlineKeys(values[value]), ", ")))
			}
			out = append(out, fmt.Sprintf("id %q at stage %q is written with %d different Whats:\n%s\n"+
				"A headline is one What per (id, Stage). Either one of these sites declares the wrong "+
				"stage, or one sentence changed; no copy moves to make this pass, because which "+
				"sentence is right is a product decision.", id, stage, len(values), strings.Join(described, "\n")))
		}
	}
	return out
}

func describedWhat(value string) string {
	kind, text, _ := strings.Cut(value, ":")
	if kind == "literal" || kind == "format" {
		return strconv.Quote(text)
	}
	return "a What composed at run time"
}

func liveCatalogCensus(t *testing.T) catalogCensus {
	t.Helper()
	obs := contractObligations(t)
	constants := exportedNamedStringConstants(t,
		filepath.Join(moduleRoot(t), "internal", "ui"), "FailureID")
	return catalogCensusFrom(t, obs, productionCheckFamilies(), constants)
}

func headlineKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// THE README'S SIDE. A README quotes headlines from the catalog, inside a
// marked block, one headline per blockquote line, each rendered by
// renderedHeadline. Outside the block the README is prose this row does not
// read.
const (
	readmeHeadlinesBegin = "<!-- failure-headlines -->"
	readmeHeadlinesEnd   = "<!-- /failure-headlines -->"
)

var goVerb = regexp.MustCompile(`%(?:%|[-+# 0]*(?:\d+|\*)?(?:\.(?:\d+|\*))?[a-zA-Z])`)

// renderedHeadline is THE ONE DOCUMENTED TRANSFORM from a catalog headline
// to the text a README quotes: every Go formatting verb becomes <…>, and %%
// becomes %. A headline carries its verbs because that is the sentence as
// written in code; a reader needs to see where a value goes, not how Go
// spells it.
func renderedHeadline(headline string) string {
	return goVerb.ReplaceAllStringFunc(headline, func(verb string) string {
		if verb == "%%" {
			return "%"
		}
		return "<…>"
	})
}

// readmeHeadlineLines is every headline a README quotes: each blockquote
// line inside the marked block.
func readmeHeadlineLines(readme string) (lines []string, marked bool, err error) {
	begin := strings.Index(readme, readmeHeadlinesBegin)
	if begin < 0 {
		return nil, false, nil
	}
	end := strings.Index(readme[begin:], readmeHeadlinesEnd)
	if end < 0 {
		return nil, true, fmt.Errorf("opens a %s block and never closes it", readmeHeadlinesBegin)
	}
	for _, line := range strings.Split(readme[begin:begin+end], "\n") {
		if text, ok := strings.CutPrefix(strings.TrimSpace(line), "> "); ok {
			lines = append(lines, strings.TrimSpace(text))
		}
	}
	return lines, true, nil
}

// headlinesTheCatalogDoesNotCarry is every README headline that is not a
// rendered catalog headline.
func headlinesTheCatalogDoesNotCarry(readmeLines []string, entries []publicCatalogEntry) []string {
	carried := map[string]bool{}
	for _, entry := range entries {
		for _, headline := range entry.Headline {
			if headline != nil {
				carried[renderedHeadline(*headline)] = true
			}
		}
	}
	var out []string
	for _, line := range readmeLines {
		if !carried[line] {
			out = append(out, line)
		}
	}
	return out
}

// TestNoReadmeHeadlineIsOneTheCatalogDoesNotCarry holds the README to the
// catalog: a headline quoted there is a catalog headline, rendered, or it
// is wording typed by hand — the fragility the catalog exists to end.
func TestNoReadmeHeadlineIsOneTheCatalogDoesNotCarry(t *testing.T) {
	root := moduleRoot(t)
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatalf("reading catalog.json: %v", err)
	}
	var catalog publicCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("catalog.json does not decode as the published shape: %v", err)
	}
	lines, marked, err := readmeHeadlineLines(string(readme))
	if err != nil {
		t.Fatalf("README.md %v", err)
	}
	if !marked {
		t.Logf("README.md carries no %s block yet, so it quotes no headline to disagree with; "+
			"TestAReadmeHeadlineTheCatalogDoesNotCarryReds proves this row reds when one does",
			readmeHeadlinesBegin)
		return
	}
	if len(lines) == 0 {
		t.Errorf("README.md's %s block quotes no headline, and a block that quotes nothing is a "+
			"check that passes by being empty", readmeHeadlinesBegin)
	}
	for _, line := range headlinesTheCatalogDoesNotCarry(lines, catalog.Failures) {
		t.Errorf("README.md quotes the headline %q, and the catalog carries no such headline.\n"+
			"A README headline is a catalog headline rendered by renderedHeadline, every Go verb "+
			"as <…>; anything else is wording typed by hand.", line)
	}
}

// TestAReadmeHeadlineTheCatalogDoesNotCarryReds is the named fixture for
// the README row, and for the transform it depends on.
func TestAReadmeHeadlineTheCatalogDoesNotCarryReds(t *testing.T) {
	format := "curious.pub is not taking %s right now (%d%%)."
	if got, want := renderedHeadline(format), "curious.pub is not taking <…> right now (<…>%)."; got != want {
		t.Fatalf("renderedHeadline(%q) = %q, want %q", format, got, want)
	}
	entries := []publicCatalogEntry{{ID: "fixture",
		Headline: map[string]*string{"deploys": &format, "uploads": nil}}}
	readme := "Some prose.\n\n" + readmeHeadlinesBegin + "\n" +
		"> curious.pub is not taking <…> right now (<…>%).\n" +
		"> curious.pub is not taking logins today.\n" +
		readmeHeadlinesEnd + "\n\n> A blockquote outside the block is prose.\n"
	lines, marked, err := readmeHeadlineLines(readme)
	if err != nil || !marked || len(lines) != 2 {
		t.Fatalf("read %d headline lines (marked %t, err %v), want 2 from inside the block", len(lines), marked, err)
	}
	missing := headlinesTheCatalogDoesNotCarry(lines, entries)
	if len(missing) != 1 || missing[0] != "curious.pub is not taking logins today." {
		t.Fatalf("headlines the catalog does not carry = %q, want exactly the hand-typed one", missing)
	}
	if _, _, err := readmeHeadlineLines(readmeHeadlinesBegin + "\n> something\n"); err == nil {
		t.Error("a block that never closes was read as a closed one, which would pass by ending early")
	}
}

// TestEveryComposedHeadlineHasOneConstructorAndOneShape is coherence for a
// What the catalog does not quote.
//
// A composed What has no sentence to compare, so the row compares how it
// is BUILT: which constructor makes it, and the shape of the What that
// constructor is given — a concatenation's operand kinds, a helper that
// writes it in its own body, a builder's result — never its words. RULED
// 2026-09-14: one constructor and one shape per composed (id, Stage). Two
// sites may build it when they build it the same way; upload-link-expired
// is built at two sites through one helper, and that is agreement.
func TestEveryComposedHeadlineHasOneConstructorAndOneShape(t *testing.T) {
	census := liveCatalogCensus(t)
	composed, multiSite := 0, 0
	for _, id := range headlineKeys(census.builders) {
		for _, stage := range headlineKeys(census.builders[id]) {
			builders := census.builders[id][stage]
			composed++
			sites := map[string]bool{}
			for _, s := range builders {
				for site := range s {
					sites[site] = true
				}
			}
			if len(sites) > 1 {
				multiSite++
			}
		}
	}
	if composed == 0 {
		t.Fatal("the census holds no composed (id, Stage), so this row compared nothing")
	}
	for _, problem := range composedDisagreements(census.builders) {
		t.Error(problem)
	}
	if !t.Failed() {
		t.Logf("%d composed (id, Stage) pairs, each built one way; %d of them built at more than one site",
			composed, multiSite)
	}
}

// TestTwoConstructorsForOneComposedHeadlineRedNamingBoth is the named
// fixture for the row above, and its control is the case the ruling
// allows.
func TestTwoConstructorsForOneComposedHeadlineRedNamingBoth(t *testing.T) {
	disagree := map[string]map[string]map[string]map[string]bool{
		"upload-link-expired": {"uploads": {
			"flow.uploadFailed · inside uploadFailed":                {"internal/flow/upload.go:373": true},
			"ui.NewFailure · concatenation(literal, value, literal)": {"internal/flow/upload.go:341": true},
		}},
	}
	problems := composedDisagreements(disagree)
	if len(problems) != 1 {
		t.Fatalf("one disagreement produced %d findings, want 1: %v", len(problems), problems)
	}
	for _, want := range []string{"internal/flow/upload.go:341", "internal/flow/upload.go:373",
		"flow.uploadFailed", "ui.NewFailure"} {
		if !strings.Contains(problems[0], want) {
			t.Errorf("the finding does not name %s:\n%s", want, problems[0])
		}
	}
	agree := map[string]map[string]map[string]map[string]bool{
		"upload-link-expired": {"uploads": {
			"flow.uploadFailed · inside uploadFailed": {
				"internal/flow/upload.go:341": true, "internal/flow/upload.go:373": true},
		}},
	}
	if got := composedDisagreements(agree); len(got) != 0 {
		t.Errorf("two sites building one composed What the same way were reported as a disagreement: %v", got)
	}
}

// composedDisagreements names every composed (id, Stage) built more than
// one way, with the sites of each.
func composedDisagreements(builders map[string]map[string]map[string]map[string]bool) []string {
	var out []string
	for _, id := range headlineKeys(builders) {
		for _, stage := range headlineKeys(builders[id]) {
			ways := builders[id][stage]
			if len(ways) < 2 {
				continue
			}
			var described []string
			for _, way := range headlineKeys(ways) {
				described = append(described, fmt.Sprintf("  %s at %s", way, strings.Join(headlineKeys(ways[way]), ", ")))
			}
			out = append(out, fmt.Sprintf("id %q at stage %q composes its What %d different ways:\n%s\n"+
				"A composed headline is not quoted, so what holds it together is how it is built: one "+
				"constructor and one shape per (id, Stage). Two sites may build it, the same way.",
				id, stage, len(ways), strings.Join(described, "\n")))
		}
	}
	return out
}
